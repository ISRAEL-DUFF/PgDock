package edge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Idempotency-Key on the data API's writes (Taskiem P1-G2): a repeat of a
// key within 24 hours gets the first answer instead of writing again, and
// the same key with a different request is refused. The key is claimed and
// the answer kept in the write's own transaction (pgd_auth.idempotency), so
// a key is recorded exactly when its write commits: a write that failed or
// timed out before committing left nothing, and a retry runs it.

const maxIdempotencyKey = 255

// idempotency is a write's key and the hash of what it asked.
type idempotency struct {
	key, hash string
}

// idempotencyOf reads c's Idempotency-Key, if any. body is the request's.
func idempotencyOf(c *call, req Request, body []byte) (*idempotency, *apiError) {
	k := c.r.Header.Get("Idempotency-Key")
	if k == "" {
		return nil, nil
	}
	if len(k) > maxIdempotencyKey {
		return nil, badRequest("invalid_idempotency_key", "an Idempotency-Key is 1 to %d characters", maxIdempotencyKey)
	}
	for _, r := range k {
		if r < 0x21 || r > 0x7e {
			return nil, badRequest("invalid_idempotency_key", "an Idempotency-Key is printable ASCII without spaces")
		}
	}
	// Without a user, every caller with the publishable key would share
	// the keys, and could replay each other's answers.
	if req.Role != "service" && req.Claims["sub"] == nil {
		return nil, badRequest("idempotency_needs_user", "an Idempotency-Key needs the secret key or a signed-in user")
	}
	q := c.r.URL.Query()
	q.Del("apikey")
	h := sha256.New()
	h.Write([]byte(c.r.Method + "\n" + c.r.URL.EscapedPath() + "?" + canonicalQuery(q) + "\n"))
	h.Write(body)
	return &idempotency{key: k, hash: hex.EncodeToString(h.Sum(nil))}, nil
}

func canonicalQuery(q url.Values) string { return q.Encode() } // Encode sorts by key

// replayed is the first answer to a repeated key; returned from the
// transaction, it rolls back (nothing was written) and is the response.
type replayed struct {
	status int
	body   []byte
}

func (r *replayed) Error() string { return "idempotent replay" }

// claim records the key in tx, or returns the first answer as *replayed.
func (k *idempotency) claim(ctx context.Context, tx pgx.Tx) error {
	if k == nil {
		return nil
	}
	var status *int32
	var response *string
	var reused bool
	err := tx.QueryRow(ctx, `SELECT status, response, reused FROM pgd_auth.idempotency_claim($1, $2)`, k.key, k.hash).
		Scan(&status, &response, &reused)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "42883" {
		// The project's pgd_* schemas are behind this release: the control
		// plane brings them up within minutes.
		return &apiError{Status: http.StatusServiceUnavailable, Code: "idempotency_unavailable", RetryAfter: 60,
			Message: "Idempotency-Key isn't available on this project yet; retry later, or without the key"}
	}
	if err != nil {
		return err
	}
	switch {
	case reused:
		return &apiError{Status: http.StatusUnprocessableEntity, Code: "idempotency_key_reused",
			Message: "this Idempotency-Key was used in the last 24 hours for a different request: use a new key"}
	case status != nil && response != nil:
		return &replayed{status: int(*status), body: []byte(*response)}
	}
	return nil // claimed: go ahead
}

// store keeps the answer in tx, beside the write.
func (k *idempotency) store(ctx context.Context, tx pgx.Tx, status int, body []byte) error {
	if k == nil {
		return nil
	}
	_, err := tx.Exec(ctx, `SELECT pgd_auth.idempotency_store($1, $2, $3)`, k.key, status, string(body))
	return err
}

// writeReplay answers with the first answer.
func (c *call) writeReplay(r *replayed) {
	h := c.w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("Idempotent-Replayed", "true")
	c.w.WriteHeader(r.status)
	_, _ = c.w.Write(r.body)
}
