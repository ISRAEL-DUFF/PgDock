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
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/agentapi"
	"github.com/israel-duff/pgdock/internal/agentca"
	"github.com/israel-duff/pgdock/internal/alerts"
	"github.com/israel-duff/pgdock/internal/api"
	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/auth"
	"github.com/israel-duff/pgdock/internal/backup"
	"github.com/israel-duff/pgdock/internal/branching"
	"github.com/israel-duff/pgdock/internal/console"
	"github.com/israel-duff/pgdock/internal/crypto"
	"github.com/israel-duff/pgdock/internal/dedicated"
	"github.com/israel-duff/pgdock/internal/isocheck"
	"github.com/israel-duff/pgdock/internal/jobs"
	"github.com/israel-duff/pgdock/internal/mail"
	"github.com/israel-duff/pgdock/internal/metrics"
	"github.com/israel-duff/pgdock/internal/nodes"
	"github.com/israel-duff/pgdock/internal/orgs"
	"github.com/israel-duff/pgdock/internal/outbound"
	"github.com/israel-duff/pgdock/internal/pooler"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/schedjobs"
	"github.com/israel-duff/pgdock/internal/settings"
	"github.com/israel-duff/pgdock/internal/storage"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/internal/store/storetest"
	"github.com/israel-duff/pgdock/internal/tenancy"
	"github.com/israel-duff/pgdock/internal/tokens"
	"github.com/israel-duff/pgdock/internal/webhooks"
)

// Env is a running control plane.
type Env struct {
	t         testing.TB
	URL       string // base URL of the HTTP API
	client    *http.Client
	csrf      string
	clock     *Clock
	totp      string // the owner's TOTP secret
	DB        *pgxpool.Pool
	Keyring   *crypto.Keyring
	Pooler    *pooler.Manager
	Service   *provision.Service
	Notifier  *jobs.Notifier
	Backups   *backup.Service
	Nodes     *nodes.Service
	Dedicated *dedicated.Service
	// Console and Metrics back the M6 endpoints; tests call
	// Metrics.Collect themselves instead of waiting for the interval.
	Console   *console.Service
	Metrics   *metrics.Collector
	IsoChecks *isocheck.Service
	// Alerts is ticked by tests (Alerts.Tick) rather than on a timer.
	Alerts *alerts.Service
	// Auth, Orgs, and SMTP back the M8 account flows; SMTP receives every
	// email the server sends.
	Auth *auth.Service
	Orgs *orgs.Service
	SMTP *SMTPServer
	// Tenancy is the M9 controller; tests tick it (EnforceStorage, Reap,
	// RecordUsage, Sweep) rather than running its loops. Its clock is real
	// time plus TenancyAdvance.
	Tenancy *tenancy.Service
	// Tokens issues API tokens; its clock is the auth clock (Advance).
	Tokens *tokens.Service
	// Branches runs branching; tests call Branches.Sweep, whose clock is
	// the tenancy clock (TenancyAdvance).
	Branches *branching.Service
	// Webhooks, Jobs and Outbound run V2 §9 on the automation clock.
	Webhooks *webhooks.Service
	Jobs     *schedjobs.Service
	Outbound *outbound.Service
	// OrgID is the owner's personal organisation, where CreateProject puts
	// projects.
	OrgID uuid.UUID
	// MasterKey is the raw key behind Keyring (key rotation tests).
	MasterKey []byte
	// S3Link is set with Options.S3Link once ConfigureBackups ran.
	S3Link *Link

	s3Link        bool
	tenancyOffset atomic.Int64
	// automationOffset moves the webhooks', jobs' and outbound clock.
	automationOffset atomic.Int64
	agentRun         map[string][]string // docker exec arguments per node, for restarts
	// S3 is the fake object store, once ConfigureBackups ran.
	S3 *storage.Fake

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
	// AfterFreeze is passed to the dedicated service (promotion tests).
	AfterFreeze func(ctx context.Context) error
	// MetricsToken protects /metrics for scrapers.
	MetricsToken string
	// ExtraPoolers are checked for "pooler down" besides the test poolers.
	ExtraPoolers []*pooler.Admin
	// S3Link puts a cuttable TCP link (Env.S3Link) between agents and the
	// fake S3 ConfigureBackups starts.
	S3Link bool
	// TokenRate and OrgTokenRate override the API token rate limits
	// (requests per minute).
	TokenRate, OrgTokenRate int
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

	ca, err := agentca.LoadOrCreate(ctx, db, keyring)
	if err != nil {
		t.Fatal(err)
	}
	nodeSvc, err := nodes.NewService(db, ca, "", log)
	if err != nil {
		t.Fatal(err)
	}
	bcfg := backup.Config{}
	if host := os.Getenv("PGDOCK_TEST_METADATA_AGENT_HOST"); host != "" {
		h, p, _ := net.SplitHostPort(host)
		port, _ := strconv.Atoi(p)
		cc := db.Config().ConnConfig
		bcfg.MetadataPG = agentapi.PGConn{Host: h, Port: port, User: cc.User, Password: cc.Password, Database: cc.Database, SSLMode: "disable"}
	}
	backups := backup.NewService(db, keyring, nodeSvc, svc, bcfg, log)
	// The test server runs on the host: it reaches instances through the
	// ports agents publish on 127.0.0.1.
	ded := dedicated.New(db, keyring, nodeSvc, svc, backups, dedicated.Config{AdminVia: "published", ReadyTimeout: 3 * time.Minute, AfterFreeze: opts.AfterFreeze}, log)
	ded.Snapshot = backups.Snapshot
	svc.Instances = ded
	backups.Dedicated = ded

	kinds := svc.Kinds()
	for name, k := range backups.Kinds() {
		kinds[name] = k
	}
	for name, k := range ded.Kinds() {
		kinds[name] = k
	}
	isoChecks := isocheck.New(db, svc, log)
	for name, k := range isoChecks.Kinds() {
		kinds[name] = k
	}
	mailSvc := mail.New(db, keyring)
	e := &Env{}
	branchSvc := branching.New(db, svc, backups, nodeSvc, mailSvc, branching.Config{
		PublicURL: "https://pgdock.test",
		Now:       func() time.Time { return time.Now().Add(time.Duration(e.tenancyOffset.Load())) },
	}, log)
	for name, k := range branchSvc.Kinds() {
		kinds[name] = k
	}
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

	consoleSvc := console.New(db, svc, keyring, false, log)
	alertSvc := alerts.New(db, keyring, alerts.Config{PublicURL: "https://pgdock.test", Poolers: append(pm.Admins(), opts.ExtraPoolers...), PoolerGrace: time.Nanosecond}, log)
	collector := metrics.NewCollector(db, svc, pm, nodeSvc, time.Second, log)

	clock := &Clock{t: time.Now()}
	authSvc := auth.NewService(db, keyring, auth.Config{Now: clock.Now, PublicURL: "https://pgdock.test"}, "test-setup-code", log)
	smtpd := StartSMTP(t)
	host, port, _ := net.SplitHostPort(smtpd.Addr)
	portN, _ := strconv.Atoi(port)
	mailSvc.UseConfig(mail.Config{Host: host, Port: portN, From: "PGDock <pgdock@pgdock.test>", TLS: mail.TLSNone})
	authSvc.SetMailer(mailSvc)
	if err := authSvc.EnsureTerms(ctx); err != nil {
		t.Fatal(err)
	}
	orgSvc := orgs.New(db, authSvc, svc, mailSvc, "https://pgdock.test", log)
	authSvc.SetHooks(orgSvc.Hooks())
	tenancySvc := tenancy.New(db, svc, mailSvc, tenancy.Config{
		PublicURL: "https://pgdock.test",
		Now:       func() time.Time { return time.Now().Add(time.Duration(e.tenancyOffset.Load())) },
	}, log)
	tenancySvc.FinalBackup = func(ctx context.Context, p store.Project) error {
		_, err := backups.BackupNow(ctx, p.ID, nil)
		return err
	}
	ded.Quotas = tenancySvc
	// Webhooks and jobs, on a clock tests move (AutomationAdvance), polling
	// every 200ms.
	autoNow := func() time.Time { return time.Now().Add(time.Duration(e.automationOffset.Load())) }
	outboundSvc := outbound.New(db, outbound.Config{Now: autoNow}, log)
	webhookSvc := webhooks.New(db, keyring, svc, outboundSvc, tenancySvc, mailSvc, webhooks.Config{Poll: 200 * time.Millisecond, Now: autoNow, PublicURL: "https://pgdock.test"}, log)
	jobSvc := schedjobs.New(db, keyring, svc, outboundSvc, tenancySvc, mailSvc, schedjobs.Config{Tick: 200 * time.Millisecond, Now: autoNow, PublicURL: "https://pgdock.test"}, log)
	svc.RefreshWebhooks = webhookSvc.Reinstall
	tokenSvc := tokens.New(db, keyring, mailSvc, tokens.Config{Now: clock.Now, PublicURL: "https://pgdock.test"}, log)
	ts := httptest.NewUnstartedServer(api.NewHandler(api.Options{
		Orgs: orgSvc, Mail: mailSvc, Tenancy: tenancySvc, Branches: branchSvc,
		Webhooks: webhookSvc, Jobs: jobSvc, Outbound: outboundSvc,
		Tokens: tokenSvc, TokenRate: opts.TokenRate, OrgTokenRate: opts.OrgTokenRate, Now: clock.Now, PublicURL: "https://pgdock.test",
		Logger: log, DB: db, Notifier: notifier, StreamCtx: ctx, Projects: svc, Auth: authSvc, Settings: st,
		UI: fstest.MapFS{"index.html": {Data: []byte("ui")}}, UIIndex: "index.html",
		Backups: backups, Nodes: nodeSvc, Console: consoleSvc, IsoChecks: isoChecks, Alerts: alertSvc, MetricsInterval: time.Second, MetricsToken: opts.MetricsToken,
	}))
	// Listen where the agent container can reach us too.
	if gw := os.Getenv("PGDOCK_TEST_DOCKER_GATEWAY"); gw != "" {
		ln, err := net.Listen("tcp", net.JoinHostPort(gw, "0"))
		if err != nil {
			t.Fatalf("listen on the docker gateway %s: %v", gw, err)
		}
		_ = ts.Listener.Close()
		ts.Listener = ln
	}
	ts.Start()
	jar, _ := cookiejar.New(nil)

	*e = Env{
		t: t, URL: ts.URL, client: &http.Client{Jar: jar}, clock: clock, Tenancy: tenancySvc, Tokens: tokenSvc, Branches: branchSvc,
		Webhooks: webhookSvc, Jobs: jobSvc, Outbound: outboundSvc,
		DB: db, Keyring: keyring, Pooler: pm, Service: svc, Notifier: notifier, Backups: backups, Nodes: nodeSvc, Dedicated: ded,
		Console: consoleSvc, Metrics: collector, IsoChecks: isoChecks, Alerts: alertSvc,
		Auth: authSvc, Orgs: orgSvc, SMTP: smtpd,
		SharedAdminURL: sharedURL, SessionAddr: sessionAddr, PooledAddr: pooledAddr,
		admin: adminCreds{"pgdock", adminPW}, log: log, s3Link: opts.S3Link, MasterKey: key,
	}
	// After *e is set: the loops read e.automationOffset.
	wg.Add(2)
	go func() { defer wg.Done(); webhookSvc.Run(ctx) }()
	go func() { defer wg.Done(); jobSvc.Run(ctx) }()
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

// TenancyAdvance moves the tenancy service's clock on (the reaper's
// "now", usage periods).
func (e *Env) TenancyAdvance(d time.Duration) { e.tenancyOffset.Add(int64(d)) }

// AutomationAdvance moves the webhooks', jobs' and outbound clock by d
// (retries come due, rate buckets refill).
func (e *Env) AutomationAdvance(d time.Duration) { e.automationOffset.Add(int64(d)) }

// Advance moves the auth clock on, e.g. past the re-auth window.
func (e *Env) Advance(d time.Duration) {
	e.clock.mu.Lock()
	e.clock.t = e.clock.t.Add(d)
	e.clock.mu.Unlock()
}

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
	var orgsList gen.OrgList
	if code := e.Do("GET", "/api/v1/orgs", nil, &orgsList); code != http.StatusOK || len(orgsList.Items) == 0 || !orgsList.Items[0].Personal {
		e.t.Fatalf("orgs after setup: %d %+v", code, orgsList)
	}
	e.OrgID = orgsList.Items[0].Id
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
	rows, err := e.DB.Query(ctx, `SELECT db_name, owner_role FROM projects
		UNION ALL SELECT alias_db_name, legacy_owner_role FROM projects WHERE alias_db_name IS NOT NULL AND legacy_owner_role IS NOT NULL`)
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
		_, _ = conn.Exec(ctx, "DROP ROLE IF EXISTS "+pgx.Identifier{provision.ConsoleRole(n.DB)}.Sanitize())
		// The read-only group role and members' logins (V2 §3.5).
		if more, err := conn.Query(ctx, `SELECT rolname FROM pg_roles WHERE rolname = $1 OR starts_with(rolname, $2)`,
			provision.ReadOnlyRole(n.DB), n.DB+"_u_"); err == nil {
			if roles, err := pgx.CollectRows(more, pgx.RowTo[string]); err == nil {
				for _, r := range roles {
					_, _ = conn.Exec(ctx, "DROP OWNED BY "+pgx.Identifier{r}.Sanitize())
					_, _ = conn.Exec(ctx, "DROP ROLE IF EXISTS "+pgx.Identifier{r}.Sanitize())
				}
			}
		}
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
	if op := e.WaitOperation(c.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		e.t.Fatalf("create %q: operation %s: %v\n%s", name, op.Status, deref(op.Error), FormatLog(op))
	}
	return c
}

// WaitOperation streams an operation over SSE until it finishes, then
// returns its final state.
func (e *Env) WaitOperation(id uuid.UUID) gen.Operation {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", e.URL+"/api/v1/operations/"+id.String()+"/stream", nil)
	res, err := e.client.Do(req)
	if err != nil {
		e.t.Fatalf("stream operation %s: %v", id, err)
	}
	_, _ = io.Copy(io.Discard, res.Body) // the stream ends at the done event
	_ = res.Body.Close()
	var op gen.Operation
	if ctx.Err() != nil {
		e.Do("GET", "/api/v1/operations/"+id.String(), nil, &op)
		e.t.Fatalf("operation %s (%s) did not finish in time: %s\n%s", id, op.Kind, op.Status, FormatLog(op))
	}
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

// GetText sends a GET with the given headers, as the signed-in operator
// when withSession is set, and returns the status and body.
func (e *Env) GetText(path string, header http.Header, withSession bool) (int, string) {
	e.t.Helper()
	req, err := http.NewRequest("GET", e.URL+path, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	client := http.DefaultClient
	if withSession {
		client = e.client
	}
	res, err := client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}
