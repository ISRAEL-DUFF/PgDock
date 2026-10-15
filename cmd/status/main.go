// Command xenibase-status is Xenibase's public status page (V3 §2.6). Run it on
// infrastructure separate from Xenibase (another provider) so an outage
// can't take down the page that announces it:
//
//	xenibase-status run [--config /etc/xenibase-status/status.toml]
//	xenibase-status check-config [--config ...]
//	xenibase-status version
//
// See docs/status-page.md and deploy/status/status.example.toml.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/israel-duff/xenibase/internal/logging"
	"github.com/israel-duff/xenibase/internal/statuspage"
	"github.com/israel-duff/xenibase/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "xenibase-status:", err)
		os.Exit(1)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func run(args []string) error {
	cmd := "run"
	if len(args) > 0 && (args[0] == "--version" || args[0] == "-version" || args[0] != "" && args[0][0] != '-') {
		cmd, args = args[0], args[1:]
	}
	if cmd == "version" || cmd == "-version" || cmd == "--version" {
		v := version.Get()
		fmt.Printf("xenibase-status %s (commit %s, built %s, %s)\n", v.Version, v.Commit, v.BuildDate, v.GoVersion)
		return nil
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	path := fs.String("config", env("XENIBASE_STATUS_CONFIG", "/etc/xenibase-status/status.toml"), "configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := statuspage.LoadConfig(*path)
	if err != nil {
		return err
	}
	switch cmd {
	case "check-config":
		fmt.Printf("%s: %d components, checks every %s\n", *path, len(cfg.Components), cfg.Interval.Duration)
		return nil
	case "run":
	default:
		return fmt.Errorf("unknown command %q: use run, check-config or version", cmd)
	}

	log := logging.New(os.Stderr, env("XENIBASE_STATUS_LOG_FORMAT", "json"), slog.LevelInfo)
	svc, err := statuspage.New(cfg, log)
	if err != nil {
		return err
	}
	defer func() { _ = svc.Close() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{Addr: cfg.Listen, Handler: svc.Handler(), ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	go svc.Run(ctx)
	log.Info("xenibase-status listening", "addr", cfg.Listen, "components", len(cfg.Components), "version", version.Version)
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(sctx)
}
