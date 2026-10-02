// Package api implements the PGDock HTTP API and serves the embedded web UI.
package api

import (
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/israel-duff/pgdock/internal/alerts"
	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/auth"
	"github.com/israel-duff/pgdock/internal/backup"
	"github.com/israel-duff/pgdock/internal/console"
	"github.com/israel-duff/pgdock/internal/isocheck"
	"github.com/israel-duff/pgdock/internal/jobs"
	"github.com/israel-duff/pgdock/internal/mail"
	"github.com/israel-duff/pgdock/internal/nodes"
	"github.com/israel-duff/pgdock/internal/orgs"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/settings"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/internal/version"
)

// Server implements gen.ServerInterface. Endpoints that later milestones
// implement fall through to gen.Unimplemented (501).
type Server struct {
	gen.Unimplemented
	log       *slog.Logger
	db        DB
	streamer  *jobs.Streamer
	projects  *provision.Service
	dev       bool
	auth      *auth.Service
	sec       SecurityOptions
	settings  *settings.Store
	publicIPs []netip.Addr
	tls       func() gen.TlsStatus
	backups   *backup.Service
	nodes     *nodes.Service
	console   *console.Service
	isochecks *isocheck.Service
	alerts    *alerts.Service
	orgs      *orgs.Service
	mail      *mail.Service

	metricsInterval time.Duration
	metricsToken    string
}

// DB is the metadata database: queries plus a health check.
type DB interface {
	store.DBTX
	Ping(ctx context.Context) error
}

var _ gen.ServerInterface = (*Server)(nil)

// Options configures NewHandler.
type Options struct {
	Logger *slog.Logger
	// DB and Notifier back the operations endpoints and readiness. Both
	// may be nil in tests that only exercise static routes.
	DB       DB
	Notifier *jobs.Notifier
	// DevEndpoints enables /api/v1/dev/* (PGDOCK_DEV_ENDPOINTS).
	DevEndpoints bool
	// StreamCtx ends open SSE streams when done; nil means never.
	StreamCtx context.Context
	// Projects runs provisioning; nil disables the projects endpoints.
	Projects *provision.Service
	// UI is the web UI build output; UIIndex names its entry document.
	UI      fs.FS
	UIIndex string

	// Auth signs operators in and guards every non-public API route.
	// It is required unless InsecureNoAuth is set (unit tests only).
	Auth           *auth.Service
	InsecureNoAuth bool
	Security       SecurityOptions
	// Settings holds the editable DB hostname; PublicIPs are this server's
	// public addresses for the DNS check; TLS reports pooler TLS status.
	Settings  *settings.Store
	PublicIPs []netip.Addr
	TLS       func() gen.TlsStatus
	// Backups and Nodes run backups, restores, imports, and agents; nil
	// disables those endpoints.
	Backups *backup.Service
	Nodes   *nodes.Service
	// Console runs the SQL console, table browser, and extensions; nil
	// disables them. MetricsInterval is the sampling interval (for
	// /metrics freshness); MetricsToken lets scrapers read /metrics.
	Console   *console.Service
	IsoChecks *isocheck.Service
	Alerts    *alerts.Service
	// Orgs runs organisations and memberships; Mail is the platform SMTP.
	Orgs            *orgs.Service
	Mail            *mail.Service
	MetricsInterval time.Duration
	MetricsToken    string
}

// NewHandler returns the root HTTP handler: the API under /api, health
// probes at /healthz and /readyz, and the SPA everywhere else.
func NewHandler(opts Options) http.Handler {
	if opts.Auth == nil && !opts.InsecureNoAuth {
		panic("api: Options.Auth is required")
	}
	s := &Server{
		log: opts.Logger, db: opts.DB, dev: opts.DevEndpoints, projects: opts.Projects,
		auth: opts.Auth, sec: opts.Security, settings: opts.Settings, publicIPs: opts.PublicIPs, tls: opts.TLS,
		backups: opts.Backups, nodes: opts.Nodes, console: opts.Console, isochecks: opts.IsoChecks, alerts: opts.Alerts, orgs: opts.Orgs, mail: opts.Mail,
		metricsInterval: opts.MetricsInterval, metricsToken: opts.MetricsToken,
	}
	if opts.DB != nil && opts.Notifier != nil {
		streamCtx := opts.StreamCtx
		if streamCtx == nil {
			streamCtx = context.Background()
		}
		s.streamer = jobs.NewStreamer(streamCtx, opts.DB, opts.Notifier, opts.Logger)
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(requestLogger(opts.Logger))
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders)
	r.Use(s.guard)

	gen.HandlerWithOptions(s, gen.ChiServerOptions{
		BaseRouter: r,
		ErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		},
	})

	spa := SPAHandler(opts.UI, opts.UIIndex)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		if isAPIPath(r.URL.Path) {
			writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
			return
		}
		spa.ServeHTTP(w, r)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	})
	return r
}

func isAPIPath(p string) bool {
	return p == "/api" || strings.HasPrefix(p, "/api/")
}

// GetVersion implements GET /api/v1/version.
func (s *Server) GetVersion(w http.ResponseWriter, _ *http.Request) {
	v := version.Get()
	writeJSON(w, http.StatusOK, gen.Version{
		Version:   v.Version,
		Commit:    v.Commit,
		BuildDate: v.BuildDate,
		GoVersion: v.GoVersion,
	})
}

// GetHealthz implements GET /healthz.
func (s *Server) GetHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, gen.Health{Status: "ok"})
}

// GetReadyz implements GET /readyz: ready when the metadata DB answers.
func (s *Server) GetReadyz(w http.ResponseWriter, r *http.Request) {
	if s.db != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.db.Ping(ctx); err != nil {
			s.log.Warn("readiness check failed", "err", err)
			writeJSON(w, http.StatusServiceUnavailable, gen.Health{Status: "metadata database unavailable"})
			return
		}
	}
	writeJSON(w, http.StatusOK, gen.Health{Status: "ok"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, gen.Error{Code: code, Message: msg})
}

func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()
			next.ServeHTTP(ww, r)
			log.LogAttrs(r.Context(), slog.LevelInfo, "http request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", ww.Status()),
				slog.Int("bytes", ww.BytesWritten()),
				slog.Duration("duration", time.Since(start)),
				slog.String("request_id", middleware.GetReqID(r.Context())),
			)
		})
	}
}
