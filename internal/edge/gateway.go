package edge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/israel-duff/pgdock/internal/edgeapi"
	"github.com/israel-duff/pgdock/internal/jwtes"
)

// Error is the body of every error the edge returns (V4 §3.5).
type Error struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details,omitempty"`
	RequestID string         `json:"request_id"`
}

// call is one request's state.
type call struct {
	id     string
	start  time.Time
	w      *recorder
	r      *http.Request
	p      *project
	ip     string
	keyID  *uuid.UUID
	role   string
	userID *uuid.UUID
	billed bool
	// storage marks a file download: its bytes are storage egress, and
	// transforms counts image renders.
	storage    bool
	transforms int64
	// hooks are auth webhook events to send once the auth transaction
	// commits.
	hooks []edgeapi.AuthHook
}

type recorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func newRequestID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "req_" + hex.EncodeToString(b)
}

func (c *call) json(status int, v any) {
	h := c.w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	c.w.WriteHeader(status)
	_ = json.NewEncoder(c.w).Encode(v)
}

func (c *call) fail(status int, code, msg string) {
	c.json(status, map[string]Error{"error": {Code: code, Message: msg, RequestID: c.id}})
}

// ServeHTTP is the gateway.
func (e *Edge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c := &call{id: newRequestID(), start: time.Now(), w: &recorder{ResponseWriter: w}, r: r, ip: e.clientIP(r)}
	c.w.Header().Set("X-Request-Id", c.id)
	ref, ok := e.refFromHost(r.Host)
	if !ok {
		if r.URL.Path == "/healthz" {
			if !e.Ready() {
				c.fail(http.StatusServiceUnavailable, "starting", "the edge is loading its configuration")
				return
			}
			c.json(http.StatusOK, map[string]string{"status": "ok"})
			return
		}
		c.fail(http.StatusNotFound, "unknown_host", "this hostname isn't a PGDock project")
		return
	}
	if !e.Ready() {
		c.w.Header().Set("Retry-After", "2")
		c.fail(http.StatusServiceUnavailable, "starting", "the edge is loading its configuration")
		return
	}
	p := e.lookup(ref)
	if p == nil {
		c.fail(http.StatusNotFound, "project_not_found", "no project with backend services at this hostname")
		return
	}
	c.p = p
	defer e.finish(c)
	if !e.cors(c) {
		return
	}
	switch p.cfg.State {
	case edgeapi.StateSuspended:
		c.fail(http.StatusForbidden, "project_suspended", "this project's organisation is suspended")
		return
	case edgeapi.StatePaused:
		e.wake(p.cfg.Ref)
		c.w.Header().Set("Retry-After", "10")
		c.fail(http.StatusServiceUnavailable, "project_resuming", "the project is paused and is resuming; retry shortly")
		return
	}
	if e.keylessAuth(c) || e.keylessStorage(c) {
		return
	}
	req, ok := e.authorize(c)
	if !ok {
		return
	}
	c.billed = true
	e.route(c, req)
}

// refFromHost is the project reference in host (<ref>.<domain>).
func (e *Edge) refFromHost(host string) (string, bool) {
	host = strings.ToLower(host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(host, ".")
	ref, ok := strings.CutSuffix(host, "."+e.cfg.Domain)
	if !ok || e.cfg.Domain == "" || ref == "" || strings.Contains(ref, ".") {
		return "", false
	}
	return ref, true
}

func (e *Edge) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	for _, pre := range e.cfg.TrustedProxies {
		if pre.Contains(addr) {
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				parts := strings.Split(xff, ",")
				if a, err := netip.ParseAddr(strings.TrimSpace(parts[len(parts)-1])); err == nil {
					return a.String()
				}
			}
			break
		}
	}
	return addr.String()
}

// originAllowed: an empty list allows any origin (publishable keys are made
// to be embedded); otherwise only those listed.
func (p *project) originAllowed(origin string) bool {
	if len(p.cfg.CORSOrigins) == 0 {
		return true
	}
	for _, o := range p.cfg.CORSOrigins {
		if o == "*" || strings.EqualFold(o, origin) {
			return true
		}
	}
	return false
}

// cors answers preflights and sets the response headers for browser
// requests; false means the request was answered.
func (e *Edge) cors(c *call) bool {
	origin := c.r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	h := c.w.Header()
	h.Add("Vary", "Origin")
	if !c.p.originAllowed(origin) {
		c.fail(http.StatusForbidden, "origin_not_allowed", "this origin isn't in the project's allowed origins")
		return false
	}
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Access-Control-Expose-Headers", "X-Request-Id, Retry-After, ETag, Content-Range, Content-Length, Content-Disposition, X-Cache")
	if c.r.Method == http.MethodOptions && c.r.Header.Get("Access-Control-Request-Method") != "" {
		h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "apikey, authorization, content-type, x-request-id, read-replica, prefer, x-upsert, x-metadata, range, if-none-match, cache-control")
		h.Set("Access-Control-Max-Age", "600")
		c.w.WriteHeader(http.StatusNoContent)
		return false
	}
	return true
}

// authorize checks the API key and the user's token, and the rate limits.
func (e *Edge) authorize(c *call) (Request, bool) {
	p, r := c.p, c.r
	key := r.Header.Get("apikey")
	if key == "" {
		key = r.URL.Query().Get("apikey")
	}
	if key == "" {
		c.fail(http.StatusUnauthorized, "key_required", "send the project's publishable or secret key in the apikey header")
		return Request{}, false
	}
	k, ok := p.keys[edgeapi.HashKey(key)]
	if !ok {
		c.fail(http.StatusUnauthorized, "invalid_key", "this key isn't a live key of this project")
		return Request{}, false
	}
	id := k.ID
	c.keyID = &id
	if !e.limits.allow("ip:"+p.cfg.Ref+":"+c.ip, p.cfg.Settings.RatePerIP) || !e.limits.allow("key:"+k.ID.String(), p.cfg.Settings.RatePerKey) {
		c.w.Header().Set("Retry-After", "1")
		c.fail(http.StatusTooManyRequests, "rate_limited", "too many requests; slow down")
		return Request{}, false
	}
	req := Request{Timeout: time.Duration(p.cfg.Settings.StatementTimeoutMs) * time.Millisecond}
	if k.Kind == edgeapi.KindSecret {
		if r.Header.Get("Origin") != "" && !p.cfg.Settings.AllowSecretInBrowser {
			c.fail(http.StatusForbidden, "secret_key_in_browser", "a secret key can't be used from a browser; use the publishable key")
			return Request{}, false
		}
		req.Role, req.Claims = "service", map[string]any{"role": "service"}
		c.role = req.Role
		return req, true
	}
	req.Role, req.Claims = "anon", map[string]any{"role": "anon"}
	if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && strings.TrimSpace(tok) != "" {
		claims, err := jwtes.Verify(strings.TrimSpace(tok), p.jwks, p.cfg.Ref, time.Now())
		if err != nil {
			c.fail(http.StatusUnauthorized, "invalid_token", err.Error())
			return Request{}, false
		}
		if claims["role"] != "user" {
			c.fail(http.StatusUnauthorized, "invalid_token", "the token isn't a user's access token")
			return Request{}, false
		}
		if sub, _ := claims["sub"].(string); sub != "" {
			if u, err := uuid.Parse(sub); err == nil {
				c.userID = &u
			}
		}
		req.Role, req.Claims = "user", claims
	}
	c.role = req.Role
	return req, true
}

// route sends a request to its service.
func (e *Edge) route(c *call, req Request) {
	path := c.r.URL.Path
	switch {
	case path == "/data/v1/health" && (c.r.Method == http.MethodGet || c.r.Method == http.MethodHead):
		e.health(c, req)
	case strings.HasPrefix(path, "/data/v1/"):
		if mfaRequired(c.p.cfg.Auth, req) {
			c.fail(http.StatusForbidden, "mfa_required", "this project requires a second factor: verify one to reach aal2")
			return
		}
		e.data(c, req)
	case strings.HasPrefix(path, "/auth/v1/"):
		e.auth(c, req)
	case strings.HasPrefix(path, "/storage/v1/"):
		e.storageRoute(c, req)
	case strings.HasPrefix(path, "/realtime/v1"):
		if mfaRequired(c.p.cfg.Auth, req) {
			c.fail(http.StatusForbidden, "mfa_required", "this project requires a second factor: verify one to reach aal2")
			return
		}
		e.realtime(c, req)
	default:
		c.fail(http.StatusNotFound, "no_such_endpoint", "no such endpoint")
	}
}

// health runs a request transaction and reports who it ran as: the edge,
// the pooler, the project database, and the role and claims, end to end.
func (e *Edge) health(c *call, req Request) {
	ctx, cancel := context.WithTimeout(c.r.Context(), req.Timeout+2*time.Second)
	defer cancel()
	var role, dbRole string
	var uid *string
	err := e.WithRequest(ctx, c.p, req, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT coalesce(pgd_auth.role(), ''), pgd_auth.uid()::text, current_user`).Scan(&role, &uid, &dbRole)
	})
	if err != nil {
		e.dbError(c, err)
		return
	}
	out := map[string]any{"status": "ok", "project": c.p.cfg.Ref, "role": role, "region": c.p.cfg.Region}
	if uid != nil {
		out["user_id"] = *uid
	}
	c.json(http.StatusOK, out)
}

// dbError maps a database failure to a response.
func (e *Edge) dbError(c *call, err error) {
	var pe *pgconn.PgError
	switch {
	case errors.Is(err, errConfigChanged):
		c.w.Header().Set("Retry-After", "1")
		c.fail(http.StatusServiceUnavailable, "retry", err.Error())
	case errors.As(err, &pe) && pe.Code == "57014":
		c.fail(http.StatusGatewayTimeout, "statement_timeout", "the query took longer than the project's limit")
	case errors.As(err, &pe) && unavailable(pe.Code):
		e.cfg.Log.Warn("edge database", "project", c.p.cfg.Ref, "err", err)
		c.w.Header().Set("Retry-After", "5")
		c.fail(http.StatusServiceUnavailable, "database_unavailable", "the project's database can't be reached right now")
	case errors.As(err, &pe):
		c.json(http.StatusBadRequest, map[string]Error{"error": {Code: "database_error", Message: pe.Message,
			Details: map[string]any{"pg_code": pe.Code}, RequestID: c.id}})
	default:
		e.cfg.Log.Warn("edge database", "project", c.p.cfg.Ref, "err", err)
		c.w.Header().Set("Retry-After", "5")
		c.fail(http.StatusServiceUnavailable, "database_unavailable", "the project's database can't be reached right now")
	}
}

// unavailable: connection, authentication and resource errors (and an
// administrator's shutdown) are the database not being reachable, not the
// request's fault: retry later.
func unavailable(code string) bool {
	return strings.HasPrefix(code, "08") || strings.HasPrefix(code, "28") || strings.HasPrefix(code, "53") ||
		strings.HasPrefix(code, "57P")
}

// finish meters and logs a request that reached a project.
func (e *Edge) finish(c *call) {
	status := c.w.status
	if status == 0 {
		status = http.StatusOK
	}
	l := edgeapi.Log{ProjectID: c.p.cfg.ProjectID, At: c.start.UTC(), RequestID: c.id, Method: c.r.Method,
		Path: c.r.URL.Path, Status: status, LatencyMs: int(time.Since(c.start).Milliseconds()), Role: c.role,
		UserID: c.userID, KeyID: c.keyID, IP: c.ip, BytesOut: c.w.bytes}
	e.meter.record(l, c.billed, c.storage, c.transforms)
}

// wake asks pgdock-server to resume a paused project, at most every 30
// seconds per project.
func (e *Edge) wake(ref string) {
	now := time.Now()
	if last, ok := e.waking.Load(ref); ok && now.Sub(last.(time.Time)) < 30*time.Second {
		return
	}
	e.waking.Store(ref, now)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := e.client.Wake(ctx, ref); err != nil {
			e.cfg.Log.Warn("edge wake", "project", ref, "err", err)
		}
	}()
}
