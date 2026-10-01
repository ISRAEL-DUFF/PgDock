// Command pgdock-server is the PGDock control plane: REST API, embedded web
// UI, operation workers, and (in later milestones) pooler management.
package main

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/base32"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/agentapi"
	"github.com/israel-duff/pgdock/internal/agentca"
	"github.com/israel-duff/pgdock/internal/api"
	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/auth"
	"github.com/israel-duff/pgdock/internal/backup"
	"github.com/israel-duff/pgdock/internal/config"
	"github.com/israel-duff/pgdock/internal/crypto"
	"github.com/israel-duff/pgdock/internal/jobs"
	"github.com/israel-duff/pgdock/internal/logging"
	"github.com/israel-duff/pgdock/internal/nodes"
	"github.com/israel-duff/pgdock/internal/pooler"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/settings"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/internal/tlscert"
	"github.com/israel-duff/pgdock/internal/version"
	"github.com/israel-duff/pgdock/web"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "pgdock-server:", err)
		os.Exit(1)
	}
}

func run() error {
	showVersion := flag.Bool("version", false, "print version and exit")
	requireUI := flag.Bool("require-ui", false, "exit with an error if only the placeholder UI is embedded")
	genKey := flag.Bool("gen-master-key", false, "print a new random master key for PGDOCK_MASTER_KEY and exit")
	healthcheck := flag.String("healthcheck", "", "GET this URL's /healthz and exit 0 if healthy (for container health checks)")
	flag.Parse()

	v := version.Get()
	switch {
	case *showVersion:
		fmt.Printf("pgdock-server %s (commit %s, built %s, %s)\n", v.Version, v.Commit, v.BuildDate, v.GoVersion)
		return nil
	case *requireUI:
		if !web.HasUI() {
			return errors.New("binary embeds only the placeholder UI; build with `make build`")
		}
		fmt.Println("web UI embedded")
		return nil
	case *healthcheck != "":
		c := &http.Client{Timeout: 3 * time.Second}
		res, err := c.Get(strings.TrimRight(*healthcheck, "/") + "/healthz")
		if err != nil {
			return err
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("healthz: %s", res.Status)
		}
		return nil
	case *genKey:
		k, err := crypto.GenerateKey()
		if err != nil {
			return err
		}
		fmt.Println(crypto.EncodeKey(k))
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logging.New(os.Stderr, cfg.LogFormat, cfg.LogLevel)
	slog.SetDefault(log)

	// Validate the master key at startup; secrets that use it arrive in M1+.
	keyring, err := crypto.NewKeyring(cfg.MasterKey, cfg.PreviousMasterKeys...)
	if err != nil {
		return err
	}
	log.Info("master key loaded", "key_id", keyring.PrimaryID().String(), "previous_keys", len(cfg.PreviousMasterKeys))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := store.Migrate(ctx, pool, log); err != nil {
		return err
	}

	// Background services stop when bgCtx is cancelled, after HTTP drains.
	bgCtx, stopBG := context.WithCancel(context.WithoutCancel(ctx))
	defer stopBG()
	var bg sync.WaitGroup

	settingsStore := settings.New(pool, cfg.Public.Host)
	if err := settingsStore.Load(ctx); err != nil {
		return err
	}

	authSvc, err := setupAuth(ctx, cfg, pool, keyring, log)
	if err != nil {
		return err
	}
	bg.Add(1)
	go func() { defer bg.Done(); sweepAuth(bgCtx, authSvc, log) }()

	kinds := map[string]jobs.Kind{jobs.KindNoop: jobs.Noop()}
	projects, pm, err := setupProvisioning(ctx, cfg, pool, keyring, settingsStore, log)
	if err != nil {
		return err
	}
	if projects != nil {
		for name, k := range projects.Kinds() {
			kinds[name] = k
		}
	}

	var backups *backup.Service
	var nodeSvc *nodes.Service
	if projects != nil {
		if backups, nodeSvc, err = setupBackups(ctx, cfg, pool, keyring, projects, log); err != nil {
			return err
		}
		for name, k := range backups.Kinds() {
			kinds[name] = k
		}
		bg.Add(2)
		go func() { defer bg.Done(); nodeSvc.Run(bgCtx, 30*time.Second) }()
		go func() { defer bg.Done(); backups.Run(bgCtx) }()
	}

	var certs *tlscert.Manager
	if pm != nil {
		if certs, err = setupPoolerTLS(cfg, pm, settingsStore, log); err != nil {
			return err
		}
	}

	notifier := jobs.NewNotifier(pool, log)
	runner := jobs.NewRunner(pool, notifier, log, jobs.RunnerConfig{Concurrency: cfg.Workers}, kinds)
	bg.Add(2)
	go func() { defer bg.Done(); notifier.Run(bgCtx) }()
	go func() { defer bg.Done(); runner.Run(bgCtx) }()

	index := web.IndexFile
	if !web.HasUI() {
		index = web.PlaceholderFile
		log.Warn("web UI not embedded; serving placeholder page")
	}
	if cfg.DevEndpoints {
		log.Warn("development endpoints enabled (PGDOCK_DEV_ENDPOINTS); do not use in production")
	}

	tlsStatus := func() gen.TlsStatus { return gen.TlsStatus{Mode: gen.TlsStatusModeOff, State: gen.TlsStatusStateOff} }
	if certs != nil {
		tlsStatus = func() gen.TlsStatus { return toAPITLS(certs.Status()) }
	}
	handler := api.NewHandler(api.Options{
		Logger:       log,
		DB:           pool,
		Notifier:     notifier,
		DevEndpoints: cfg.DevEndpoints,
		StreamCtx:    bgCtx,
		Projects:     projects,
		UI:           web.Dist(),
		UIIndex:      index,
		Auth:         authSvc,
		Security: api.SecurityOptions{
			SecureCookies:  cfg.Web.SecureCookies,
			TrustedProxies: cfg.Web.TrustedProxies,
		},
		Settings:  settingsStore,
		PublicIPs: cfg.Web.PublicIPs,
		TLS:       tlsStatus,
		Backups:   backups,
		Nodes:     nodeSvc,
	})
	if certs != nil {
		handler = certs.HTTPChallengeHandler(handler)
	}
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
	}

	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return err
	}
	log.Info("pgdock-server listening",
		"addr", ln.Addr().String(), "version", v.Version, "commit", v.Commit)
	if certs != nil {
		// Only once listening: ACME challenges are answered by this server.
		bg.Add(1)
		go func() { defer bg.Done(); certs.Run(bgCtx) }()
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	var serveErr error
	select {
	case serveErr = <-errc:
	case <-ctx.Done():
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	// Stopping background work ends open SSE streams, which would otherwise
	// hold Shutdown until its timeout. Workers requeue interrupted attempts.
	srv.RegisterOnShutdown(stopBG)
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("http shutdown", "err", err)
		_ = srv.Close()
	}
	stopBG()
	bg.Wait()

	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}

// setupProvisioning registers the configured shared cluster and builds the
// provisioning service. It returns nil when no pooler is configured.
// setupAuth builds the auth service. Before the owner exists it logs the
// one-time setup code the first-run wizard asks for.
func setupAuth(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, keyring *crypto.Keyring, log *slog.Logger) (*auth.Service, error) {
	if !cfg.Web.SecureCookies {
		log.Warn("PGDOCK_COOKIE_SECURE=false: session cookies are sent over plain HTTP; development only")
	}
	code := cfg.Web.SetupCode
	if code == "" {
		b := make([]byte, 9)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		code = base32.StdEncoding.EncodeToString(b)
	}
	svc := auth.NewService(pool, keyring, auth.Config{}, code, log)
	needed, err := svc.SetupNeeded(ctx)
	if err != nil {
		return nil, err
	}
	if needed {
		log.Warn("first-run setup required: open the web UI and enter this setup code", "setup_code", code)
	}
	return svc, nil
}

func sweepAuth(ctx context.Context, svc *auth.Service, log *slog.Logger) {
	t := time.NewTicker(15 * time.Minute)
	defer t.Stop()
	for {
		if err := svc.Sweep(ctx); err != nil && ctx.Err() == nil {
			log.Warn("sweep expired sessions", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// setupPoolerTLS builds the certificate manager for the poolers and makes
// sure a certificate exists before they need one.
func setupPoolerTLS(cfg config.Config, pm *pooler.Manager, st *settings.Store, log *slog.Logger) (*tlscert.Manager, error) {
	mode, err := tlscert.ParseMode(cfg.PoolerTLS.Mode)
	if err != nil {
		return nil, err
	}
	tc := tlscert.Config{
		Mode: mode, Dir: pm.Dir(), FileMode: pm.FileMode(), Reload: pm.Reload,
		DataDir: cfg.PoolerTLS.DataDir, ACMEEmail: cfg.PoolerTLS.ACMEEmail, ACMECA: cfg.PoolerTLS.ACMECA,
		SourceCert: cfg.PoolerTLS.CertFile, SourceKey: cfg.PoolerTLS.KeyFile, Log: log,
	}
	if _, port, err := net.SplitHostPort(cfg.ListenAddr); err == nil {
		tc.ChallengePort, _ = strconv.Atoi(port)
	}
	if path := cfg.PoolerTLS.ACMECARoots; path != "" {
		pemBytes, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("PGDOCK_ACME_CA_ROOTS: %w", err)
		}
		tc.ACMERoots = x509.NewCertPool()
		if !tc.ACMERoots.AppendCertsFromPEM(pemBytes) {
			return nil, fmt.Errorf("PGDOCK_ACME_CA_ROOTS: no certificates in %s", path)
		}
	}
	m, err := tlscert.New(tc, st.DBHost())
	if err != nil {
		return nil, err
	}
	if err := m.Bootstrap(); err != nil {
		return nil, fmt.Errorf("pooler TLS: %w", err)
	}
	st.OnDBHostChange(m.SetHost)
	log.Info("pooler TLS", "mode", mode, "host", st.DBHost())
	return m, nil
}

func toAPITLS(s tlscert.Status) gen.TlsStatus {
	out := gen.TlsStatus{Mode: gen.TlsStatusMode(s.Mode), State: gen.TlsStatusState(s.State)}
	if s.Host != "" {
		out.Host = &s.Host
	}
	if s.Issuer != "" {
		out.Issuer = &s.Issuer
	}
	if !s.NotAfter.IsZero() {
		out.NotAfter = &s.NotAfter
	}
	if s.Err != "" {
		out.Error = &s.Err
	}
	return out
}

func setupProvisioning(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, keyring *crypto.Keyring, st *settings.Store, log *slog.Logger) (*provision.Service, *pooler.Manager, error) {
	if cfg.Shared.AdminURL != "" {
		// A cluster that is down at boot should not keep the control plane
		// down; creates fail until it is back.
		if err := provision.RegisterSharedCluster(ctx, pool, keyring, provision.SharedCluster{
			NodeName:   cfg.Shared.NodeName,
			AdminURL:   cfg.Shared.AdminURL,
			PoolerHost: cfg.Shared.PoolerHost,
			PoolerPort: cfg.Shared.PoolerPort,
		}, log); err != nil {
			log.Error("could not register shared cluster", "err", err)
		}
	}
	pc := cfg.Pooler
	if pc.ConfigDir == "" {
		log.Warn("project provisioning disabled: PGDOCK_POOLER_CONFIG_DIR is not set")
		return nil, nil, nil
	}

	// A stable, secret salt keeps the admin entry identical across syncs.
	adminVerifier, err := crypto.SCRAMVerifierWithSalt(pc.AdminPassword,
		keyring.Derive("pooler admin scram salt:"+pc.AdminUser, 16), crypto.SCRAMIterations)
	if err != nil {
		return nil, nil, fmt.Errorf("pooler admin password: %w", err)
	}
	var admins []*pooler.Admin
	for _, a := range []struct{ name, addr string }{{"session", pc.SessionAddr}, {"transaction", pc.PooledAddr}} {
		adm, err := pooler.NewAdmin(a.name, a.addr, pc.AdminUser, pc.AdminPassword, pc.SSLMode)
		if err != nil {
			return nil, nil, err
		}
		admins = append(admins, adm)
	}
	pm, err := pooler.NewManager(pc.ConfigDir, pc.FileMode, pool, admins,
		[]pooler.User{{Name: pc.AdminUser, Secret: adminVerifier}}, log)
	if err != nil {
		return nil, nil, err
	}
	// Bring the poolers in line with the metadata DB (e.g. after a restore
	// or a lost reload). Failure is not fatal: every flow syncs again.
	syncCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := pm.Sync(syncCtx); err != nil {
		// Normal on first boot of the bundle: the poolers start after us.
		log.Warn("initial pooler sync incomplete; the files are written and the next sync reloads the poolers", "err", err)
	} else {
		log.Info("pooler config synced", "dir", pc.ConfigDir)
	}

	return provision.NewService(pool, keyring, pm, provision.Config{
		DBHost:           cfg.Public.Host,
		DBHostFunc:       st.DBHost,
		SessionPort:      cfg.Public.SessionPort,
		PooledPort:       cfg.Public.PooledPort,
		SSLMode:          cfg.Public.SSLMode,
		SmokeSessionAddr: pc.SessionAddr,
		SmokePooledAddr:  pc.PooledAddr,
		SmokeSSLMode:     pc.SSLMode,
	}, log), pm, nil
}

// setupBackups builds the agent CA, the nodes service, and the backup
// service (which hooks final backups into deletes).
func setupBackups(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, keyring *crypto.Keyring, projects *provision.Service, log *slog.Logger) (*backup.Service, *nodes.Service, error) {
	ca, err := agentca.LoadOrCreate(ctx, pool, keyring)
	if err != nil {
		return nil, nil, fmt.Errorf("agent CA: %w", err)
	}
	ns, err := nodes.NewService(pool, ca, cfg.Backups.AgentBootstrapToken, log)
	if err != nil {
		return nil, nil, err
	}
	bc := backup.Config{Hour: cfg.Backups.Hour, Jitter: cfg.Backups.Jitter}
	if u := cfg.Backups.MetadataURL; u != "" {
		pg, err := agentapi.ParseURL(u)
		if err != nil {
			return nil, nil, fmt.Errorf("metadata backup URL: %w", err)
		}
		bc.MetadataPG = pg
	}
	return backup.NewService(pool, keyring, ns, projects, bc, log), ns, nil
}

func connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pcfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("PGDOCK_DATABASE_URL: %w", err)
	}
	if pcfg.ConnConfig.RuntimeParams["application_name"] == "" {
		pcfg.ConnConfig.RuntimeParams["application_name"] = "pgdock-server"
	}
	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to metadata database: %w", err)
	}
	return pool, nil
}
