package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/auth"
	"github.com/israel-duff/pgdock/internal/store"
)

// SecurityOptions configures cookies and proxy trust.
type SecurityOptions struct {
	// SecureCookies sets the Secure flag and the __Host- name prefix. Turn
	// it off only for plain-HTTP development.
	SecureCookies bool
	// TrustedProxies are the addresses whose X-Forwarded-For is believed
	// (e.g. Caddy). Empty trusts no one: the TCP peer is the client.
	TrustedProxies []netip.Prefix
}

const (
	csrfHeader   = "X-CSRF-Token"
	sessionName  = "pgdock_session"
	csrfName     = "pgdock_csrf"
	secureprefix = "__Host-"
)

func (s *Server) cookieName(base string) string {
	if s.sec.SecureCookies {
		return secureprefix + base
	}
	return base
}

type ctxKey int

const (
	keyIP ctxKey = iota
	keySession
	keyAudit
)

// clientIP returns the request's client address, honouring
// X-Forwarded-For only from trusted proxies (rightmost untrusted hop).
func (s *Server) clientIP(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	peer = peer.Unmap()
	if !s.trusted(peer) {
		return peer
	}
	hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		a = a.Unmap()
		if !s.trusted(a) {
			return a
		}
		peer = a
	}
	return peer
}

func (s *Server) trusted(a netip.Addr) bool {
	for _, p := range s.sec.TrustedProxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func ipFrom(ctx context.Context) *netip.Addr {
	if a, ok := ctx.Value(keyIP).(netip.Addr); ok && a.IsValid() {
		return &a
	}
	return nil
}

func ipString(ctx context.Context) string {
	if a := ipFrom(ctx); a != nil {
		return a.String()
	}
	return "unknown"
}

func sessionFrom(ctx context.Context) (auth.Session, bool) {
	s, ok := ctx.Value(keySession).(auth.Session)
	return s, ok
}

// operatorID returns the signed-in operator, for created_by columns.
func operatorID(ctx context.Context) *uuid.UUID {
	if s, ok := sessionFrom(ctx); ok {
		id := s.OperatorID
		return &id
	}
	return nil
}

// ---- Audit -----------------------------------------------------------------

// auditInfo is filled in by handlers while a mutating request runs.
type auditInfo struct {
	skip       bool
	operatorID *uuid.UUID
	targetType string
	targetID   string
	detail     map[string]any
}

func auditFrom(ctx context.Context) *auditInfo {
	if a, ok := ctx.Value(keyAudit).(*auditInfo); ok {
		return a
	}
	return &auditInfo{} // tests calling handlers directly
}

func (a *auditInfo) target(typ, id string) { a.targetType, a.targetID = typ, id }

func (a *auditInfo) set(k string, v any) {
	if a.detail == nil {
		a.detail = map[string]any{}
	}
	a.detail[k] = v
}

// auditActions names mutating routes. Unlisted ones fall back to
// "<METHOD> <pattern>".
var auditActions = map[string]string{
	"POST /api/v1/auth/login":                    "auth.login",
	"POST /api/v1/auth/totp":                     "auth.totp",
	"POST /api/v1/auth/reauth":                   "auth.reauth",
	"POST /api/v1/auth/logout":                   "auth.logout",
	"POST /api/v1/setup/begin":                   "setup.begin",
	"POST /api/v1/setup/complete":                "setup.complete",
	"POST /api/v1/projects":                      "project.create",
	"DELETE /api/v1/projects/{id}":               "project.delete",
	"PATCH /api/v1/projects/{id}/settings":       "project.update",
	"POST /api/v1/projects/{id}/rotate-password": "project.rotate_password",
	"PUT /api/v1/settings/db-host":               "settings.db_host",
	"POST /api/v1/dev/operations":                "dev.operation",
	"POST /api/v1/settings/db-host/check":        "", // read-only check
	"POST /api/v1/projects/{id}/backups":         "backup.create",
	"POST /api/v1/backups/{id}/restore":          "backup.restore",
	"POST /api/v1/restore-tests":                 "backup.restore_test",
	"PUT /api/v1/settings/storage":               "settings.storage",
	"POST /api/v1/settings/storage/test":         "settings.storage_test",
	"POST /api/v1/settings/backup-key":           "settings.backup_key.generate",
	"POST /api/v1/settings/backup-key/export":    "settings.backup_key.export",
	"POST /api/v1/settings/backup-key/confirm":   "settings.backup_key.confirm",
	"POST /api/v1/imports/preflight":             "import.preflight",
	"POST /api/v1/imports":                       "import.create",
	"POST /api/v1/nodes/{id}/registration-token": "node.registration_token",
	"POST /api/v1/agent/register":                "node.agent_register",
	"POST /api/v1/nodes":                         "node.create",
	"DELETE /api/v1/nodes/{id}":                  "node.remove",
	"PATCH /api/v1/nodes/{id}":                   "node.update",
	"POST /api/v1/nodes/{id}/shared-cluster":     "node.shared_cluster",
	"POST /api/v1/projects/{id}/pitr":            "backup.pitr",
	"POST /api/v1/projects/{id}/instance":        "project.instance",
	"POST /api/v1/projects/{id}/promote":         "project.promote",
	"POST /api/v1/projects/{id}/sql":             "project.console",
	"POST /api/v1/projects/{id}/sql/cancel":      "project.console_cancel",
	"POST /api/v1/projects/{id}/extensions":      "project.extension",
	"POST /api/v1/security/isolation-checks":     "security.isolation_check",
	"PUT /api/v1/settings/alerts":                "settings.alerts",
	"POST /api/v1/settings/alerts/test":          "settings.alerts_test",
}

func outcomeFor(status int) string {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusTooManyRequests:
		return "denied"
	case status >= 400:
		return "failure"
	default:
		return "success"
	}
}

func (s *Server) writeAudit(r *http.Request, action string, status int, info *auditInfo) {
	if s.db == nil || action == "" || info.skip {
		return
	}
	opID := info.operatorID
	if opID == nil {
		opID = operatorID(r.Context())
	}
	detail := map[string]any{"status": status, "request_id": middleware.GetReqID(r.Context())}
	for k, v := range info.detail {
		detail[k] = v
	}
	b, _ := json.Marshal(detail)
	ua := r.UserAgent()
	if len(ua) > 512 {
		ua = ua[:512]
	}
	params := store.InsertAuditParams{
		OperatorID: opID, Action: action, Detail: b, Ip: ipFrom(r.Context()),
		Outcome: outcomeFor(status),
	}
	if info.targetType != "" {
		params.TargetType = &info.targetType
	}
	if info.targetID != "" {
		params.TargetID = &info.targetID
	}
	if ua != "" {
		params.UserAgent = &ua
	}
	// The audit row must outlive a client that hung up.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	if err := store.New(s.db).InsertAudit(ctx, params); err != nil {
		s.log.Error("write audit log", "action", action, "err", err)
	}
}

// ---- Middleware --------------------------------------------------------------

// publicAPI lists API routes that work without a session.
var publicAPI = map[string]bool{
	"/api/v1/session":        true,
	"/api/v1/version":        true,
	"/api/v1/auth/login":     true,
	"/api/v1/auth/totp":      true,
	"/api/v1/setup/begin":    true,
	"/api/v1/setup/complete": true,
	"/api/v1/agent/register": true,
}

// csrfExempt lists mutating routes called by programs, not browsers. They
// authenticate with a token in the body and ignore cookies, so CSRF does
// not apply.
var csrfExempt = map[string]bool{
	"POST /api/v1/agent/register": true,
}

// reauthRequired lists destructive routes needing a recent step-up auth
// (spec §7.2).
var reauthRequired = map[string]bool{
	"DELETE /api/v1/projects/{id}":            true,
	"POST /api/v1/settings/backup-key/export": true,
	"DELETE /api/v1/nodes/{id}":               true,
	// POST /api/v1/backups/{id}/restore checks it for mode in_place only.
}

func isMutating(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// guard enforces CSRF, authentication, and re-authentication on /api, and
// audits every mutating API request.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), keyIP, s.clientIP(r))
		r = r.WithContext(ctx)
		if !isAPIPath(r.URL.Path) || s.auth == nil {
			next.ServeHTTP(w, r)
			return
		}

		mutating := isMutating(r.Method)
		// Resolve the route now: requests refused below never reach routing.
		pattern := matchPattern(r)
		info := &auditInfo{}
		ctx = context.WithValue(ctx, keyAudit, info)
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		if mutating {
			defer func() {
				action, ok := auditActions[r.Method+" "+pattern]
				if !ok {
					action = r.Method + " " + pattern
				}
				s.writeAudit(r, action, ww.Status(), info)
			}()
		}

		if mutating && !csrfExempt[r.Method+" "+pattern] {
			if msg := s.checkCSRF(r); msg != "" {
				writeError(ww, http.StatusForbidden, "csrf", msg)
				return
			}
		}

		if c, err := r.Cookie(s.cookieName(sessionName)); err == nil {
			sess, err := s.auth.Authenticate(ctx, c.Value)
			if err == nil {
				ctx = context.WithValue(ctx, keySession, sess)
			} else if !isNoSession(err) {
				s.log.Error("authenticate session", "err", err)
				writeError(ww, http.StatusInternalServerError, "internal", "internal error")
				return
			}
		}
		r = r.WithContext(ctx)

		if !publicAPI[r.URL.Path] {
			sess, ok := sessionFrom(ctx)
			if !ok {
				writeError(ww, http.StatusUnauthorized, "unauthenticated", "sign in to continue")
				return
			}
			if key := r.Method + " " + pattern; reauthRequired[key] && !s.auth.RecentlyReauthenticated(sess) {
				writeError(ww, http.StatusForbidden, "reauth_required", "confirm your password and code to continue")
				return
			}
		}
		next.ServeHTTP(ww, r)
	})
}

func isNoSession(err error) bool { return errors.Is(err, auth.ErrNoSession) }

// checkCSRF implements double-submit: the X-CSRF-Token header must equal the
// csrf cookie, which only same-origin script can read. An Origin header, when
// sent, must also name this host.
func (s *Server) checkCSRF(r *http.Request) string {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			return "cross-origin request refused"
		}
	}
	c, err := r.Cookie(s.cookieName(csrfName))
	h := r.Header.Get(csrfHeader)
	if err != nil || c.Value == "" || h == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(h)) != 1 {
		return "missing or invalid CSRF token; reload the page"
	}
	return ""
}

// ensureCSRF returns the request's CSRF token, issuing a cookie if needed.
func (s *Server) ensureCSRF(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(s.cookieName(csrfName)); err == nil && len(c.Value) >= 32 {
		return c.Value
	}
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	tok := base64.RawURLEncoding.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName(csrfName), Value: tok, Path: "/",
		Secure: s.sec.SecureCookies, SameSite: http.SameSiteStrictMode,
		MaxAge: int((30 * 24 * time.Hour).Seconds()),
	})
	return tok
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName(sessionName), Value: token, Path: "/",
		HttpOnly: true, Secure: s.sec.SecureCookies, SameSite: http.SameSiteStrictMode,
		MaxAge: int((7 * 24 * time.Hour).Seconds()),
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName(sessionName), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.sec.SecureCookies, SameSite: http.SameSiteStrictMode,
	})
}

// matchPattern finds the pattern r will be routed to, before routing.
func matchPattern(r *http.Request) string {
	rc := chi.RouteContext(r.Context())
	if rc == nil || rc.Routes == nil {
		return r.URL.Path
	}
	tctx := chi.NewRouteContext()
	if rc.Routes.Match(tctx, r.Method, r.URL.Path) {
		return tctx.RoutePattern()
	}
	return r.URL.Path
}

// securityHeaders sets conservative browser security headers.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if !isAPIPath(r.URL.Path) {
			h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; "+
				"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		}
		next.ServeHTTP(w, r)
	})
}
