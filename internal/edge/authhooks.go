package edge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/edgeapi"
	"github.com/israel-duff/pgdock/internal/projauth"
)

// Hooks (V4 §4.7): custom claims and before-sign-up as Postgres functions
// run as the project's hook role; before-sign-up, after-sign-up and
// after-sign-in as signed webhooks that pgdock-server sends.

const (
	pgHookTimeout = 2 * time.Second
	maxHookOut    = 16 << 10
)

// errHookFailed is a Postgres hook that errored, timed out or answered
// something other than a JSON object.
var errHookFailed = errors.New("auth hook failed")

// runPGHook calls fn ("schema.name") with event in its own transaction
// as the hook role, within pgHookTimeout; it returns the function's
// object.
func (e *Edge) runPGHook(ctx context.Context, p *project, fn string, event map[string]any) (map[string]any, error) {
	schema, name, ok := strings.Cut(fn, ".")
	if !ok || p.cfg.HookRole == "" {
		return nil, fmt.Errorf("%w: no hook role or function", errHookFailed)
	}
	pool, err := e.dbPool(ctx, p)
	if err != nil {
		return nil, err
	}
	in, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	hctx, cancel := context.WithTimeout(ctx, pgHookTimeout+time.Second)
	defer cancel()
	var out *string
	err = pgx.BeginFunc(hctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(hctx, "SET LOCAL ROLE "+pgx.Identifier{p.cfg.HookRole}.Sanitize()); err != nil {
			return err
		}
		if _, err := tx.Exec(hctx, `SELECT set_config('statement_timeout', $1, true), set_config('pgd.claims', '{"role":"auth_hook"}', true)`,
			fmt.Sprint(pgHookTimeout.Milliseconds())); err != nil {
			return err
		}
		return tx.QueryRow(hctx, `SELECT `+pgx.Identifier{schema, name}.Sanitize()+`($1::jsonb)::text`, string(in)).Scan(&out)
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", errHookFailed, fn, err)
	}
	if out == nil {
		return map[string]any{}, nil
	}
	if len(*out) > maxHookOut {
		return nil, fmt.Errorf("%w: %s answered more than %d bytes", errHookFailed, fn, maxHookOut)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(*out), &m); err != nil {
		return nil, fmt.Errorf("%w: %s must return a JSON object", errHookFailed, fn)
	}
	return m, nil
}

// protectedClaims are set by the edge; a custom-claims hook can't change
// them.
var protectedClaims = []string{"sub", "role", "aud", "iat", "exp", "session_id", "aal", "amr", "is_anonymous", "iss"}

// customClaims runs the project's custom-claims hook on claims.
func (e *Edge) customClaims(ctx context.Context, p *project, u *projauth.User, method string, claims map[string]any) (map[string]any, error) {
	fn := p.cfg.Auth.CustomClaimsHook
	if fn == "" {
		return claims, nil
	}
	out, err := e.runPGHook(ctx, p, fn, map[string]any{"user_id": u.ID, "claims": claims, "authentication_method": method})
	if err != nil {
		return nil, err
	}
	add, ok := out["claims"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf(`%w: %s must return {"claims": {...}}`, errHookFailed, fn)
	}
	merged := make(map[string]any, len(add))
	for k, v := range add {
		merged[k] = v
	}
	for _, k := range protectedClaims {
		if v, ok := claims[k]; ok {
			merged[k] = v
		} else {
			delete(merged, k)
		}
	}
	return merged, nil
}

// beforeSignup asks the project's before-sign-up hook (a Postgres function
// or a webhook) about a new user; a refusal comes back as an *apiErr.
func (e *Edge) beforeSignup(ctx context.Context, c *call, n projauth.NewUser, method string) error {
	a := c.p.cfg.Auth
	if a.BeforeSignupHook == "" && !a.BeforeSignupURL {
		return nil
	}
	user := map[string]any{"email": n.Email, "phone": n.Phone, "is_anonymous": n.Anonymous,
		"user_metadata": json.RawMessage(orObject(n.UserMetadata))}
	event := map[string]any{"user": user, "method": method, "ip": c.ip}
	var d edgeapi.HookDecision
	if a.BeforeSignupHook != "" {
		out, err := e.runPGHook(ctx, c.p, a.BeforeSignupHook, event)
		if err != nil {
			return err
		}
		if dec, _ := out["decision"].(string); strings.EqualFold(dec, "reject") {
			d.Reject = true
			d.Message, _ = out["message"].(string)
		}
	} else {
		var err error
		if d, err = e.client.SendAuthHook(ctx, edgeapi.AuthHook{Ref: c.p.cfg.Ref, Event: "before_signup", Payload: event, Wait: true}); err != nil {
			// A hook that can't be reached lets the sign-up through
			// (pgdock-server already decides that for a slow hook).
			e.cfg.Log.Warn("before-sign-up hook", "project", c.p.cfg.Ref, "err", err)
			return nil
		}
	}
	if !d.Reject {
		return nil
	}
	msg := d.Message
	if msg == "" {
		msg = "sign-up isn't allowed"
	}
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return refuse(http.StatusForbidden, "signup_rejected", msg)
}

func orObject(b json.RawMessage) []byte {
	if s := strings.TrimSpace(string(b)); s == "" || s == "null" {
		return []byte(`{}`)
	}
	return b
}

// createUser makes a user after the before-sign-up hook allows it: the
// sign-up audited and the after-sign-up webhook queued for after commit.
func (e *Edge) createUser(ctx context.Context, c *call, tx pgx.Tx, n projauth.NewUser, method string) (*projauth.User, error) {
	if err := e.beforeSignup(ctx, c, n, method); err != nil {
		var a *apiErr
		if errors.As(err, &a) {
			_ = projauth.Audit(ctx, tx, nil, projauth.ActHookRejected, c.ip, map[string]any{"method": method})
		}
		return nil, err
	}
	u, err := projauth.CreateUser(ctx, tx, n)
	if err != nil {
		return nil, err
	}
	details := map[string]any{"method": method}
	if err := projauth.Audit(ctx, tx, &u.ID, projauth.ActSignup, c.ip, details); err != nil {
		return nil, err
	}
	if c.p.cfg.Auth.AfterSignupHook {
		c.hooks = append(c.hooks, edgeapi.AuthHook{Ref: c.p.cfg.Ref, Event: "after_signup",
			Payload: map[string]any{"user": u, "method": method}})
	}
	return u, nil
}

// authTx runs fn as withAuth does, then sends the webhook events it queued.
func (e *Edge) authTx(c *call, fn func(pgx.Tx) error) error {
	c.hooks = nil
	if err := e.withAuth(c.r.Context(), c.p, fn); err != nil {
		c.hooks = nil
		return err
	}
	for _, h := range c.hooks {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(c.r.Context()), 5*time.Second)
		if _, err := e.client.SendAuthHook(ctx, h); err != nil {
			e.cfg.Log.Warn("auth hook event", "project", c.p.cfg.Ref, "event", h.Event, "err", err)
		}
		cancel()
	}
	c.hooks = nil
	return nil
}

// ---- Captcha ---------------------------------------------------------------------

// DefaultCaptchaURL is Cloudflare Turnstile's siteverify.
const DefaultCaptchaURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// security is what clients send for captcha (Supabase's clients put it in
// gotrue_meta_security).
type security struct {
	CaptchaToken string `json:"captcha_token"`
}

// captcha checks token when the project turned captcha on; false means c
// was answered.
func (e *Edge) captcha(c *call, s security, extra string) bool {
	a := c.p.cfg.Auth
	if a.CaptchaSecret == "" {
		return true
	}
	token := s.CaptchaToken
	if token == "" {
		token = extra
	}
	if token == "" || len(token) > 4096 {
		c.fail(http.StatusBadRequest, "captcha_failed", "complete the captcha (send its token as gotrue_meta_security.captcha_token)")
		return false
	}
	verify := a.CaptchaVerifyURL
	if verify == "" {
		verify = DefaultCaptchaURL
	}
	form := url.Values{"secret": {a.CaptchaSecret}, "response": {token}, "remoteip": {c.ip}}
	ctx, cancel := context.WithTimeout(c.r.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, verify, strings.NewReader(form.Encode()))
	if err != nil {
		e.dbError(c, err)
		return false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := e.httpClient().Do(req)
	if err != nil {
		e.cfg.Log.Warn("captcha", "project", c.p.cfg.Ref, "err", err)
		c.fail(http.StatusServiceUnavailable, "captcha_unavailable", "the captcha couldn't be checked; try again")
		return false
	}
	defer res.Body.Close()
	var out struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, res.Body, 1<<16)).Decode(&out); err != nil || !out.Success {
		c.fail(http.StatusBadRequest, "captcha_failed", "the captcha didn't verify; try it again")
		return false
	}
	return true
}
