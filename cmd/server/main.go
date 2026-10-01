// Command pgdock-server is the PGDock control plane: REST API, embedded web
// UI, operation workers, and (in later milestones) pooler management.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/api"
	"github.com/israel-duff/pgdock/internal/config"
	"github.com/israel-duff/pgdock/internal/crypto"
	"github.com/israel-duff/pgdock/internal/jobs"
	"github.com/israel-duff/pgdock/internal/logging"
	"github.com/israel-duff/pgdock/internal/pooler"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/store"
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

	kinds := map[string]jobs.Kind{jobs.KindNoop: jobs.Noop()}
	projects, err := setupProvisioning(ctx, cfg, pool, keyring, log)
	if err != nil {
		return err
	}
	if projects != nil {
		for name, k := range projects.Kinds() {
			kinds[name] = k
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

	srv := &http.Server{
		Addr: cfg.ListenAddr,
		Handler: api.NewHandler(api.Options{
			Logger:       log,
			DB:           pool,
			Notifier:     notifier,
			DevEndpoints: cfg.DevEndpoints,
			StreamCtx:    bgCtx,
			Projects:     projects,
			UI:           web.Dist(),
			UIIndex:      index,
		}),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
	}

	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return err
	}
	log.Info("pgdock-server listening",
		"addr", ln.Addr().String(), "version", v.Version, "commit", v.Commit)

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
func setupProvisioning(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, keyring *crypto.Keyring, log *slog.Logger) (*provision.Service, error) {
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
		return nil, nil
	}

	// A stable, secret salt keeps the admin entry identical across syncs.
	adminVerifier, err := crypto.SCRAMVerifierWithSalt(pc.AdminPassword,
		keyring.Derive("pooler admin scram salt:"+pc.AdminUser, 16), crypto.SCRAMIterations)
	if err != nil {
		return nil, fmt.Errorf("pooler admin password: %w", err)
	}
	var admins []*pooler.Admin
	for _, a := range []struct{ name, addr string }{{"session", pc.SessionAddr}, {"transaction", pc.PooledAddr}} {
		adm, err := pooler.NewAdmin(a.name, a.addr, pc.AdminUser, pc.AdminPassword, pc.SSLMode)
		if err != nil {
			return nil, err
		}
		admins = append(admins, adm)
	}
	pm, err := pooler.NewManager(pc.ConfigDir, pc.FileMode, pool, admins,
		[]pooler.User{{Name: pc.AdminUser, Secret: adminVerifier}}, log)
	if err != nil {
		return nil, err
	}
	// Bring the poolers in line with the metadata DB (e.g. after a restore
	// or a lost reload). Failure is not fatal: every flow syncs again.
	syncCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := pm.Sync(syncCtx); err != nil {
		log.Error("initial pooler sync failed", "err", err)
	} else {
		log.Info("pooler config synced", "dir", pc.ConfigDir)
	}

	return provision.NewService(pool, keyring, pm, provision.Config{
		DBHost:           cfg.Public.Host,
		SessionPort:      cfg.Public.SessionPort,
		PooledPort:       cfg.Public.PooledPort,
		SSLMode:          cfg.Public.SSLMode,
		SmokeSessionAddr: pc.SessionAddr,
		SmokePooledAddr:  pc.PooledAddr,
		SmokeSSLMode:     pc.SSLMode,
	}, log), nil
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
