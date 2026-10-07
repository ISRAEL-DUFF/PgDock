// Package edgeapi is the contract between pgdock-server and pgdock-edge
// (V4 §2.1): the configuration feed the edge keeps its per-project cache
// from, and the reports it sends back (usage, request logs, keys in use,
// projects to wake). Every request is signed with the shared edge secret
// (the PGDock-Signature scheme over the method, path and body), so only an
// edge can read project configuration, which includes the edge login's
// password.
package edgeapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/signature"
)

// Paths on pgdock-server.
const (
	PathConfig = "/api/v1/edge/config"
	PathReport = "/api/v1/edge/report"
	PathWake   = "/api/v1/edge/wake"
)

// Key kinds.
const (
	KindPublishable = "publishable"
	KindSecret      = "secret"
)

// HashKey is how API keys are stored and looked up: hex SHA-256.
func HashKey(key string) string {
	s := sha256.Sum256([]byte(key))
	return hex.EncodeToString(s[:])
}

// Tolerance is how far a request's signature time may be from the
// receiver's clock.
const Tolerance = 5 * time.Minute

// Project states, as the edge acts on them.
const (
	StateActive    = "active"    // served
	StatePaused    = "paused"    // 503 and a wake
	StateSuspended = "suspended" // its organisation is suspended: refused
	StateDisabled  = "disabled"  // services off, deleted, or moved away: not found
)

// Key is a live API key, by its hash.
type Key struct {
	ID   uuid.UUID `json:"id"`
	Kind string    `json:"kind"` // publishable | secret
	Hash string    `json:"hash"` // hex SHA-256 of the key
}

// Settings are a project's gateway settings, with defaults filled in.
type Settings struct {
	// StatementTimeoutMs bounds each request's query.
	StatementTimeoutMs int `json:"statement_timeout_ms"`
	// RatePerIP and RatePerKey are requests per minute.
	RatePerIP  int `json:"rate_per_ip"`
	RatePerKey int `json:"rate_per_key"`
	// AllowSecretInBrowser lets a secret key be used from a page (a
	// request with an Origin header).
	AllowSecretInBrowser bool `json:"allow_secret_in_browser"`
}

// Project is one project's configuration on the edge.
type Project struct {
	Ref       string    `json:"ref"`
	ProjectID uuid.UUID `json:"project_id"`
	OrgID     uuid.UUID `json:"org_id"`
	Region    string    `json:"region"`
	State     string    `json:"state"`
	Version   int64     `json:"version"`
	Seq       int64     `json:"seq"`
	// Database is the pooler database name; the edge logs in as EdgeUser
	// and SETs ROLE to AnonRole, UserRole or ServiceRole per request.
	Database    string   `json:"database,omitempty"`
	EdgeUser    string   `json:"edge_user,omitempty"`
	Password    string   `json:"password,omitempty"`
	PoolerHost  string   `json:"pooler_host,omitempty"`
	PoolerPort  int      `json:"pooler_port,omitempty"`
	AnonRole    string   `json:"anon_role,omitempty"`
	UserRole    string   `json:"user_role,omitempty"`
	ServiceRole string   `json:"service_role,omitempty"`
	Keys        []Key    `json:"keys,omitempty"`
	CORSOrigins []string `json:"cors_origins,omitempty"`
	Settings    Settings `json:"settings"`
	// JWKs are the public keys user tokens are verified with (JWK JSON).
	JWKs []json.RawMessage `json:"jwks,omitempty"`
}

// Config is a page of the feed: projects changed after the request's
// "since", in order. Next is the position to ask from next. With Full,
// the page is a snapshot from the start and More says another page
// follows.
type Config struct {
	Projects []Project `json:"projects"`
	Next     int64     `json:"next"`
	More     bool      `json:"more"`
}

// Usage is one project's counters for one hour.
type Usage struct {
	ProjectID   uuid.UUID `json:"project_id"`
	Hour        time.Time `json:"hour"`
	Requests    int64     `json:"requests"`
	EgressBytes int64     `json:"egress_bytes"`
}

// Log is one request.
type Log struct {
	ProjectID uuid.UUID  `json:"project_id"`
	At        time.Time  `json:"at"`
	RequestID string     `json:"request_id"`
	Method    string     `json:"method"`
	Path      string     `json:"path"`
	Status    int        `json:"status"`
	LatencyMs int        `json:"latency_ms"`
	Role      string     `json:"role,omitempty"`
	UserID    *uuid.UUID `json:"user_id,omitempty"`
	KeyID     *uuid.UUID `json:"key_id,omitempty"`
	IP        string     `json:"ip,omitempty"`
	BytesOut  int64      `json:"bytes_out"`
}

// Report is what an edge sends every few seconds. BatchID makes a retried
// report count once.
type Report struct {
	BatchID  string      `json:"batch_id"`
	Edge     string      `json:"edge"`
	At       time.Time   `json:"at"`
	Usage    []Usage     `json:"usage,omitempty"`
	Logs     []Log       `json:"logs,omitempty"`
	KeysUsed []uuid.UUID `json:"keys_used,omitempty"`
}

// Wake asks for a paused project to be resumed.
type Wake struct {
	Ref string `json:"ref"`
}

// Client is pgdock-edge's side.
type Client struct {
	URL    string // pgdock-server's base URL
	Secret string
	HTTP   *http.Client
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// Signed is what the signature covers: method, path with query, body.
func Signed(method, requestURI string, body []byte) []byte {
	b := make([]byte, 0, len(method)+len(requestURI)+len(body)+2)
	b = append(b, method...)
	b = append(b, ' ')
	b = append(b, requestURI...)
	b = append(b, '\n')
	return append(b, body...)
}

// Verify checks a request's signature (the server's side).
func Verify(secret string, r *http.Request, body []byte, now time.Time) error {
	return signature.Verify(secret, r.Header.Get(signature.Header), Signed(r.Method, r.URL.RequestURI(), body), now, Tolerance)
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(signature.Header, signature.Sign(c.Secret, time.Now(), Signed(method, req.URL.RequestURI(), body)))
	res, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("pgdock-server %s %s: %s: %s", method, path, res.Status, bytes.TrimSpace(raw))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// Config asks for the feed from since, waiting up to wait for a change.
// Region limits it to one region's projects ("" for all).
func (c *Client) Config(ctx context.Context, region string, since int64, wait time.Duration) (Config, error) {
	q := url.Values{"since": {strconv.FormatInt(since, 10)}, "wait": {strconv.Itoa(int(wait / time.Second))}}
	if region != "" {
		q.Set("region", region)
	}
	var out Config
	return out, c.do(ctx, http.MethodGet, PathConfig+"?"+q.Encode(), nil, &out)
}

// Report sends usage and logs.
func (c *Client) Report(ctx context.Context, r Report) error {
	return c.do(ctx, http.MethodPost, PathReport, r, nil)
}

// Wake asks for a project to be resumed.
func (c *Client) Wake(ctx context.Context, ref string) error {
	return c.do(ctx, http.MethodPost, PathWake, Wake{Ref: ref}, nil)
}
