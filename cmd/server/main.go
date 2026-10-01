// Command pgdock-server is the PGDock control plane: REST API, embedded web
// UI, and (in later milestones) job workers and pooler management.
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
	"syscall"
	"time"

	"github.com/israel-duff/pgdock/internal/api"
	"github.com/israel-duff/pgdock/internal/config"
	"github.com/israel-duff/pgdock/internal/logging"
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
	flag.Parse()

	v := version.Get()
	if *showVersion {
		fmt.Printf("pgdock-server %s (commit %s, built %s, %s)\n", v.Version, v.Commit, v.BuildDate, v.GoVersion)
		return nil
	}
	if *requireUI {
		if !web.HasUI() {
			return errors.New("binary embeds only the placeholder UI; build with `make build`")
		}
		fmt.Println("web UI embedded")
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logging.New(os.Stderr, cfg.LogFormat, cfg.LogLevel)
	slog.SetDefault(log)

	index := web.IndexFile
	if !web.HasUI() {
		index = web.PlaceholderFile
		log.Warn("web UI not embedded; serving placeholder page")
	}

	srv := &http.Server{
		Addr: cfg.ListenAddr,
		Handler: api.NewHandler(api.Options{
			Logger:  log,
			UI:      web.Dist(),
			UIIndex: index,
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
