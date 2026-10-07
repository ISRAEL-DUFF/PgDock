package statuspage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	netmail "net/mail"
	"strings"
	"sync"
	"time"

	"github.com/israel-duff/pgdock/internal/mail"
	"github.com/israel-duff/pgdock/internal/statusapi"
)

const (
	confirmValid  = 48 * time.Hour
	resendAfter   = 10 * time.Minute
	maxAttempts   = 10
	mailerEvery   = 5 * time.Second
	mailBatchSize = 50
)

// Subscriptions are on when SMTP is configured.
func (s *Service) subscriptions() bool { return s.cfg.SMTP != nil }

func newToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

var errBadEmail = errors.New("enter a valid email address")

// Subscribe starts double opt-in for email: it sends a confirmation link,
// at most every resendAfter. It reveals nothing about whether the address
// is already subscribed.
func (s *Service) Subscribe(ctx context.Context, email string) error {
	if !s.subscriptions() {
		return errors.New("email subscriptions are not enabled")
	}
	a, err := netmail.ParseAddress(strings.TrimSpace(email))
	if err != nil || a.Name != "" || len(a.Address) > 254 || strings.ContainsAny(a.Address, "\r\n") {
		return errBadEmail
	}
	addr := strings.ToLower(a.Address)
	now := s.Now().UTC()
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var confirmed, sent sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT confirmed_at, confirm_sent_at FROM subscribers WHERE email = ?`, addr).Scan(&confirmed, &sent)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `INSERT INTO subscribers (email, unsub_token, created_at) VALUES (?, ?, ?)`, addr, newToken(), now.Unix()); err != nil {
			return err
		}
	case err != nil:
		return err
	case confirmed.Valid:
		return nil
	case sent.Valid && now.Sub(time.Unix(sent.Int64, 0)) < resendAfter:
		return nil
	}
	token := newToken()
	if _, err := tx.ExecContext(ctx, `UPDATE subscribers SET confirm_hash = ?, confirm_sent_at = ? WHERE email = ?`, hashToken(token), now.Unix(), addr); err != nil {
		return err
	}
	body := fmt.Sprintf(`Someone, hopefully you, asked to get %s updates by email.

Confirm the subscription:
%s/subscribe/confirm?token=%s

The link works for 48 hours. If you didn't ask for this, ignore this email.
`, s.cfg.Title, s.cfg.PublicURL, token)
	if err := enqueue(ctx, tx, addr, "Confirm your subscription to "+s.cfg.Title, body, now); err != nil {
		return err
	}
	return tx.Commit()
}

var errBadToken = errors.New("this link is not valid or has expired")

// Confirm finishes double opt-in.
func (s *Service) Confirm(ctx context.Context, token string) error {
	now := s.Now().UTC()
	res, err := s.st.db.ExecContext(ctx, `UPDATE subscribers SET confirmed_at = ?, confirm_hash = NULL
WHERE confirm_hash = ? AND confirmed_at IS NULL AND confirm_sent_at >= ?`, now.Unix(), hashToken(token), now.Add(-confirmValid).Unix())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errBadToken
	}
	return nil
}

// Unsubscribe removes the subscriber whose link carried token.
func (s *Service) Unsubscribe(ctx context.Context, token string) error {
	if token == "" {
		return errBadToken
	}
	res, err := s.st.db.ExecContext(ctx, `DELETE FROM subscribers WHERE unsub_token = ?`, token)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errBadToken
	}
	return nil
}

func enqueue(ctx context.Context, tx *sql.Tx, to, subject, body string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO outbox (recipient, subject, body, next_at) VALUES (?, ?, ?, ?)`, to, subject, body, now.Unix())
	return err
}

// notify queues an email about one incident update to every confirmed
// subscriber.
func (s *Service) notify(ctx context.Context, tx *sql.Tx, in statusapi.Incident, u statusapi.IncidentUpdate, now time.Time) error {
	if !s.subscriptions() {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT email, unsub_token FROM subscribers WHERE confirmed_at IS NOT NULL`)
	if err != nil {
		return err
	}
	type sub struct{ email, token string }
	var subs []sub
	for rows.Next() {
		var x sub
		if err := rows.Scan(&x.email, &x.token); err != nil {
			_ = rows.Close()
			return err
		}
		subs = append(subs, x)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	subject := fmt.Sprintf("[%s] %s: %s", s.cfg.Title, statusLabel(u.Status), in.Title)
	for _, x := range subs {
		body := fmt.Sprintf(`%s
%s — %s

%s

Details: %s/incidents/%s

You get these emails because you subscribed to %s.
Unsubscribe: %s/unsubscribe?token=%s
`, in.Title, statusLabel(u.Status), u.PostedAt.UTC().Format("2006-01-02 15:04 MST"), u.Body,
			s.cfg.PublicURL, in.ID, s.cfg.Title, s.cfg.PublicURL, x.token)
		if err := enqueue(ctx, tx, x.email, subject, body, now); err != nil {
			return err
		}
	}
	return nil
}

func statusLabel(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (s *Service) runMailer(ctx context.Context) {
	if !s.subscriptions() {
		return
	}
	t := time.NewTicker(mailerEvery)
	defer t.Stop()
	for {
		if err := s.DeliverMail(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("mail delivery", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// DeliverMail sends due emails from the outbox, retrying failures with
// backoff and giving up after maxAttempts.
func (s *Service) DeliverMail(ctx context.Context) error {
	if !s.subscriptions() {
		return nil
	}
	now := s.Now().UTC()
	rows, err := s.st.db.QueryContext(ctx, `SELECT id, recipient, subject, body, attempts FROM outbox WHERE next_at <= ? ORDER BY id LIMIT ?`, now.Unix(), mailBatchSize)
	if err != nil {
		return err
	}
	type msg struct {
		id                int64
		to, subject, body string
		attempts          int
	}
	var due []msg
	for rows.Next() {
		var m msg
		if err := rows.Scan(&m.id, &m.to, &m.subject, &m.body, &m.attempts); err != nil {
			_ = rows.Close()
			return err
		}
		due = append(due, m)
	}
	_ = rows.Close()
	cfg := s.cfg.SMTP.mail()
	for _, m := range due {
		sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := mail.Send(sctx, cfg, mail.Message{To: []string{m.to}, Subject: m.subject, Body: m.body, Headers: map[string]string{"X-PGDock-Event": "status"}})
		cancel()
		if err == nil {
			_, _ = s.st.db.ExecContext(ctx, `DELETE FROM outbox WHERE id = ?`, m.id)
			continue
		}
		if m.attempts+1 >= maxAttempts {
			s.log.Error("giving up on an email", "to", m.to, "err", err)
			_, _ = s.st.db.ExecContext(ctx, `DELETE FROM outbox WHERE id = ?`, m.id)
			continue
		}
		backoff := min(time.Duration(1<<m.attempts)*time.Minute, 6*time.Hour)
		_, _ = s.st.db.ExecContext(ctx, `UPDATE outbox SET attempts = attempts + 1, next_at = ?, last_error = ? WHERE id = ?`,
			now.Add(backoff).Unix(), shortErr(err), m.id)
	}
	return nil
}

// rateLimiter allows n events per window per key.
type rateLimiter struct {
	n      int
	window time.Duration
	mu     sync.Mutex
	seen   map[string][]time.Time
}

func newRateLimiter(n int, window time.Duration) *rateLimiter {
	return &rateLimiter{n: n, window: window, seen: map[string][]time.Time{}}
}

func (r *rateLimiter) allow(key string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.seen) > 10000 {
		for k, ts := range r.seen {
			if len(ts) == 0 || now.Sub(ts[len(ts)-1]) > r.window {
				delete(r.seen, k)
			}
		}
	}
	ts := r.seen[key][:0]
	for _, t := range r.seen[key] {
		if now.Sub(t) < r.window {
			ts = append(ts, t)
		}
	}
	if len(ts) >= r.n {
		r.seen[key] = ts
		return false
	}
	r.seen[key] = append(ts, now)
	return true
}
