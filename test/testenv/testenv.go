// Package testenv runs a complete pgdock-server (API, workers, provisioning)
// in-process against real Postgres and PgBouncer, for the integration and
// isolation suites. `make test-integration` provides the environment;
// without it the tests skip.
//
//	PGDOCK_TEST_DATABASE_URL           admin URL for throwaway metadata DBs
//	PGDOCK_TEST_SHARED_ADMIN_URL       shared cluster superuser (pgdock_admin)
//	PGDOCK_TEST_SHARED_POOLER_HOST     cluster host as the poolers see it
//	PGDOCK_TEST_SHARED_POOLER_PORT
//	PGDOCK_TEST_POOLER_DIR             config dir the test poolers read
//	PGDOCK_TEST_POOLER_SESSION_ADDR    host:port of the session-mode pooler
//	PGDOCK_TEST_POOLER_POOLED_ADDR     host:port of the transaction-mode pooler
//	PGDOCK_TEST_POOLER_ADMIN_PASSWORD  admin console password
//
// The suites share one pooler pair, so run them with `go test -p 1`.
package testenv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/api"
	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/auth"
	"github.com/israel-duff/pgdock/internal/crypto"
	"github.com/israel-duff/pgdock/internal/jobs"
	"github.com/israel-duff/pgdock/internal/pooler"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/settings"
	"github.com/israel-duff/pgdock/internal/store/storetest"
)

// Env is a running control plane.
type Env struct {
	t        testing.TB
	URL      string // base URL of the HTTP API
	client   *http.Client
	csrf     string
	clock    *Clock
	totp     string // the owner's TOTP secret
	DB       *pgxpool.Pool
	Keyring  *crypto.Keyring
	Pooler   *pooler.Manager
	Service  *provision.Service
	Notifier *jobs.Notifier

	SharedAdminURL string
	SessionAddr    string
	PooledAddr     string
	admin          adminCreds
	log            *slog.Logger
}

type adminCreds struct{ user, password string }

// Options tweak the environment, mostly to inject failures.
type Options struct {
	// SmokePooledAddr overrides where smoke tests reach the transaction
	// pooler (point it at a closed port to make creates fail).
	SmokePooledAddr string
	// MaxAttempts overrides the create operation's attempt limit.
	MaxAttempts int
}

func need(t testing.TB, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Skipf("%s not set; run `make test-integration`", name)
	}
	return v
}

// Start brings up a control plane with a fresh metadata DB and registers
// the shared cluster. Everything stops when the test ends.
func Start(t testing.TB, opts Options) *Env {
	t.Helper()
	sharedURL := need(t, "PGDOCK_TEST_SHARED_ADMIN_URL")
	dir := moduleRelative(t, need(t, "PGDOCK_TEST_POOLER_DIR"))
	sessionAddr := need(t, "PGDOCK_TEST_POOLER_SESSION_ADDR")
	pooledAddr := need(t, "PGDOCK_TEST_POOLER_POOLED_ADDR")
	adminPW := need(t, "PGDOCK_TEST_POOLER_ADMIN_PASSWORD")
	poolerHost := os.Getenv("PGDOCK_TEST_SHARED_POOLER_HOST")
	poolerPort, _ := strconv.Atoi(os.Getenv("PGDOCK_TEST_SHARED_POOLER_PORT"))

	db := storetest.New(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if testing.Verbose() {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyring, err := crypto.NewKeyring(key)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := provision.RegisterSharedCluster(ctx, db, keyring, provision.SharedCluster{
		NodeName: "test", AdminURL: sharedURL, PoolerHost: poolerHost, PoolerPort: poolerPort,
	}, log); err != nil {
		cancel()
		t.Fatalf("register shared cluster: %v", err)
	}

	verifier, err := crypto.SCRAMVerifier(adminPW)
	if err != nil {
		t.Fatal(err)
	}
	var admins []*pooler.Admin
	for name, addr := range map[string]string{"session": sessionAddr, "transaction": pooledAddr} {
		a, err := pooler.NewAdmin(name, addr, "pgdock", adminPW, "require")
		if err != nil {
			t.Fatal(err)
		}
		admins = append(admins, a)
	}
	pm, err := pooler.NewManager(dir, 0o644, db, admins, []pooler.User{{Name: "pgdock", Secret: verifier}}, log)
	if err != nil {
		t.Fatal(err)
	}

	host, sport, _ := net.SplitHostPort(sessionAddr)
	_, pport, _ := net.SplitHostPort(pooledAddr)
	sp, _ := strconv.Atoi(sport)
	pp, _ := strconv.Atoi(pport)
	st := settings.New(db, host)
	cfg := provision.Config{
		DBHost: host, DBHostFunc: st.DBHost, SessionPort: sp, PooledPort: pp, SSLMode: "require",
		SmokeSessionAddr: sessionAddr, SmokePooledAddr: pooledAddr,
		SmokeSSLMode: "require", AdminSSLMode: "disable",
	}
	if opts.SmokePooledAddr != "" {
		cfg.SmokePooledAddr = opts.SmokePooledAddr
	}
	svc := provision.NewService(db, keyring, pm, cfg, log)

	kinds := svc.Kinds()
	if opts.MaxAttempts > 0 {
		k := kinds[provision.KindCreate]
		k.MaxAttempts = opts.MaxAttempts
		kinds[provision.KindCreate] = k
	}
	notifier := jobs.NewNotifier(db, log)
	runner := jobs.NewRunner(db, notifier, log, jobs.RunnerConfig{
		PollInterval: 100 * time.Millisecond, RetryBase: 50 * time.Millisecond, RetryMax: 200 * time.Millisecond,
	}, kinds)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); notifier.Run(ctx) }()
	go func() { defer wg.Done(); runner.Run(ctx) }()

	clock := &Clock{t: time.Now()}
	authSvc := auth.NewService(db, keyring, auth.Config{Now: clock.Now}, "test-setup-code", log)
	ts := httptest.NewServer(api.NewHandler(api.Options{
		Logger: log, DB: db, Notifier: notifier, StreamCtx: ctx, Projects: svc, Auth: authSvc, Settings: st,
		UI: fstest.MapFS{"index.html": {Data: []byte("ui")}}, UIIndex: "index.html",
	}))
	jar, _ := cookiejar.New(nil)

	e := &Env{
		t: t, URL: ts.URL, client: &http.Client{Jar: jar}, clock: clock,
		DB: db, Keyring: keyring, Pooler: pm, Service: svc, Notifier: notifier,
		SharedAdminURL: sharedURL, SessionAddr: sessionAddr, PooledAddr: pooledAddr,
		admin: adminCreds{"pgdock", adminPW}, log: log,
	}
	t.Cleanup(func() {
		ts.Close()
		cancel()
		wg.Wait()
		e.dropLeftovers()
		// Leave the test poolers with no routes from this run.
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		_ = pm.Sync(cctx)
	})
	if err := pm.Sync(ctx); err != nil {
		t.Fatalf("initial pooler sync: %v", err)
	}
	e.signUp()
	return e
}

// Clock is the auth service's clock. Each TOTP code it hands out moves it
// 30s on, so successive codes are never rejected as replays.
type Clock struct {
	mu sync.Mutex
	t  time.Time
}

// Now returns the current fake time.
func (c *Clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *Clock) advance() { c.mu.Lock(); c.t = c.t.Add(30 * time.Second); c.mu.Unlock() }

// Owner credentials created by the harness.
const (
	OwnerEmail    = "owner@example.com"
	OwnerPassword = "integration test password"
)

// TOTP returns a fresh code for the owner.
func (e *Env) TOTP() string {
	e.t.Helper()
	e.clock.advance()
	c, err := auth.TOTPCode(e.totp, e.clock.Now())
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

// signUp runs the first-run wizard over HTTP, leaving the client signed in.
func (e *Env) signUp() {
	e.t.Helper()
	var st gen.SessionState
	if code := e.Do("GET", "/api/v1/session", nil, &st); code != http.StatusOK {
		e.t.Fatalf("session: %d", code)
	}
	e.csrf = st.CsrfToken
	var enr gen.SetupEnrollment
	if code := e.Do("POST", "/api/v1/setup/begin", map[string]string{
		"setup_code": "test-setup-code", "email": OwnerEmail, "password": OwnerPassword,
	}, &enr); code != http.StatusOK {
		e.t.Fatalf("setup begin: %d", code)
	}
	e.totp = enr.TotpSecret
	if code := e.Do("POST", "/api/v1/setup/complete", map[string]string{
		"enrollment_token": enr.EnrollmentToken, "code": e.TOTP(),
	}, &st); code != http.StatusOK || !st.Authenticated {
		e.t.Fatalf("setup complete: %d", code)
	}
}

// Reauth performs step-up authentication, as destructive actions require.
func (e *Env) Reauth() {
	e.t.Helper()
	if code := e.Do("POST", "/api/v1/auth/reauth", map[string]string{
		"password": OwnerPassword, "code": e.TOTP(),
	}, nil); code != http.StatusNoContent {
		e.t.Fatalf("reauth: %d", code)
	}
}

// moduleRelative resolves a relative path against the module root, since
// `go test` runs each package in its own directory.
func moduleRelative(t testing.TB, p string) string {
	t.Helper()
	if filepath.IsAbs(p) {
		return p
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, p)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above the working directory to resolve %q", p)
		}
		dir = parent
	}
}

// dropLeftovers removes databases and roles of projects a failed test did
// not delete, so the shared cluster does not fill up.
func (e *Env) dropLeftovers() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := e.DB.Query(ctx, `SELECT db_name, owner_role FROM projects`)
	if err != nil {
		return
	}
	names, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ DB, Role string }])
	if err != nil || len(names) == 0 {
		return
	}
	_, _ = e.DB.Exec(ctx, `UPDATE projects SET deleted_at = now(), status = 'deleted' WHERE deleted_at IS NULL`)
	_ = e.Pooler.Sync(ctx)
	conn, err := pgx.Connect(ctx, e.SharedAdminURL)
	if err != nil {
		return
	}
	defer conn.Close(context.Background())
	for _, n := range names {
		_, _ = conn.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{n.DB}.Sanitize()+" WITH (FORCE)")
		_, _ = conn.Exec(ctx, "DROP ROLE IF EXISTS "+pgx.Identifier{n.Role}.Sanitize())
	}
}

// Do sends a JSON request to the API and decodes a JSON response into out
// (if non-nil). It returns the status code.
func (e *Env) Do(method, path string, body, out any) int {
	e.t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.URL+path, r)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.csrf != "" {
		req.Header.Set("X-CSRF-Token", e.csrf)
	}
	res, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			e.t.Fatalf("%s %s: decode %q: %v", method, path, b, err)
		}
	}
	return res.StatusCode
}

// CreateProject creates a project through the API and waits for it to be
// active. It returns the credentials response.
func (e *Env) CreateProject(name string) gen.ProjectCredentials {
	e.t.Helper()
	var c gen.ProjectCredentials
	if code := e.Do("POST", "/api/v1/projects", map[string]string{"name": name}, &c); code != http.StatusAccepted {
		e.t.Fatalf("create %q: status %d", name, code)
	}
	if op := e.WaitOperation(c.Operation.Id); op.Status != gen.Succeeded {
		e.t.Fatalf("create %q: operation %s: %v\n%s", name, op.Status, deref(op.Error), FormatLog(op))
	}
	return c
}

// WaitOperation streams an operation over SSE until it finishes, then
// returns its final state.
func (e *Env) WaitOperation(id uuid.UUID) gen.Operation {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", e.URL+"/api/v1/operations/"+id.String()+"/stream", nil)
	res, err := e.client.Do(req)
	if err != nil {
		e.t.Fatalf("stream operation %s: %v", id, err)
	}
	_, _ = io.Copy(io.Discard, res.Body) // the stream ends at the done event
	_ = res.Body.Close()
	if ctx.Err() != nil {
		e.t.Fatalf("operation %s did not finish in time", id)
	}
	var op gen.Operation
	if code := e.Do("GET", "/api/v1/operations/"+id.String(), nil, &op); code != http.StatusOK {
		e.t.Fatalf("get operation %s: status %d", id, code)
	}
	return op
}

// FormatLog renders an operation's step log for failure messages.
func FormatLog(op gen.Operation) string {
	var b strings.Builder
	for _, l := range op.Log {
		fmt.Fprintf(&b, "  [%s] %s: %s\n", l.Level, l.Step, l.Msg)
	}
	return b.String()
}

// Connect opens a client connection with a connection URL, as psql would.
func (e *Env) Connect(url string) (*pgx.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return pgx.Connect(ctx, url)
}

// MustConnect is Connect that fails the test on error and closes the
// connection when the test ends.
func (e *Env) MustConnect(url string) *pgx.Conn {
	e.t.Helper()
	c, err := e.Connect(url)
	if err != nil {
		e.t.Fatalf("connect %s: %v", RedactURL(url), err)
	}
	e.t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

// SharedAdmin connects to database on the shared cluster as pgdock_admin.
func (e *Env) SharedAdmin(database string) *pgx.Conn {
	e.t.Helper()
	cfg, err := pgx.ParseConfig(e.SharedAdminURL)
	if err != nil {
		e.t.Fatal(err)
	}
	cfg.Database = database
	c, err := pgx.ConnectConfig(context.Background(), cfg)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

// DirectURL rewrites a pooler URL to point straight at the shared cluster
// (bypassing the pooler), keeping user, password, and database.
func (e *Env) DirectURL(url, database string) string {
	e.t.Helper()
	pc, err := pgx.ParseConfig(url)
	if err != nil {
		e.t.Fatal(err)
	}
	ac, err := pgx.ParseConfig(e.SharedAdminURL)
	if err != nil {
		e.t.Fatal(err)
	}
	if database == "" {
		database = pc.Database
	}
	return fmt.Sprintf("postgres://%s:%s@%s/%s?sslmode=disable", pc.User, pc.Password,
		net.JoinHostPort(ac.Host, strconv.Itoa(int(ac.Port))), database)
}

// WithDatabase returns a pooler url with a different database name.
func WithDatabase(t testing.TB, url, database string) string {
	t.Helper()
	pc, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("postgres://%s:%s@%s/%s?sslmode=require", pc.User, pc.Password,
		net.JoinHostPort(pc.Host, strconv.Itoa(int(pc.Port))), database)
}

// RedactURL hides the password in a connection URL.
func RedactURL(url string) string {
	at := strings.LastIndex(url, "@")
	scheme := strings.Index(url, "://")
	if at < 0 || scheme < 0 {
		return url
	}
	userinfo := url[scheme+3 : at]
	if user, _, ok := strings.Cut(userinfo, ":"); ok {
		return url[:scheme+3] + user + ":***" + url[at:]
	}
	return url
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
