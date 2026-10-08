// Command pgdock-edge is the backend services gateway (V4 §2.1): one per
// region, serving every project with backend services at
// https://<ref>.<domain>. It is stateless: it follows pgdock-server's
// configuration feed and reports usage back.
//
//	pgdock-edge run
//	pgdock-edge version
//
// Configuration is by environment; see docs/backend-services.md.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/israel-duff/pgdock/internal/edge"
	"github.com/israel-duff/pgdock/internal/logging"
	"github.com/israel-duff/pgdock/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "pgdock-edge:", err)
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
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "version", "--version", "-version":
		v := version.Get()
		fmt.Printf("pgdock-edge %s (commit %s, built %s, %s)\n", v.Version, v.Commit, v.BuildDate, v.GoVersion)
		return nil
	case "run":
	default:
		return fmt.Errorf("unknown command %q: use run or version", cmd)
	}
	host, _ := os.Hostname()
	cfg := edge.Config{
		Name:          env("PGDOCK_EDGE_NAME", host),
		ControlURL:    os.Getenv("PGDOCK_EDGE_CONTROL_URL"),
		Secret:        os.Getenv("PGDOCK_EDGE_SECRET"),
		Region:        os.Getenv("PGDOCK_EDGE_REGION"),
		Domain:        os.Getenv("PGDOCK_EDGE_DOMAIN"),
		PoolerAddr:    os.Getenv("PGDOCK_EDGE_POOLER_ADDR"),
		SessionAddr:   os.Getenv("PGDOCK_EDGE_SESSION_ADDR"),
		PoolerSSLMode: env("PGDOCK_EDGE_POOLER_SSLMODE", "require"),
	}
	switch {
	case cfg.ControlURL == "":
		return errors.New("PGDOCK_EDGE_CONTROL_URL is required (pgdock-server's URL)")
	case len(cfg.Secret) < 32:
		return errors.New("PGDOCK_EDGE_SECRET is required: at least 32 characters, the same as pgdock-server's")
	case cfg.Domain == "":
		return errors.New("PGDOCK_EDGE_DOMAIN is required (projects are served at <ref>.<domain>)")
	}
	for _, s := range strings.Split(os.Getenv("PGDOCK_EDGE_TRUSTED_PROXIES"), ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return fmt.Errorf("PGDOCK_EDGE_TRUSTED_PROXIES: %w", err)
		}
		cfg.TrustedProxies = append(cfg.TrustedProxies, p)
	}
	log := logging.New(os.Stderr, env("PGDOCK_EDGE_LOG_FORMAT", "json"), slog.LevelInfo)
	cfg.Log = log

	e := edge.New(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); e.Run(ctx) }()

	certFile, keyFile := os.Getenv("PGDOCK_EDGE_TLS_CERT"), os.Getenv("PGDOCK_EDGE_TLS_KEY")
	listen := env("PGDOCK_EDGE_LISTEN", ":8443")
	srv := &http.Server{Addr: listen, Handler: e, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute,
		MaxHeaderBytes: 64 << 10}
	errc := make(chan error, 1)
	go func() {
		if certFile == "" {
			log.Warn("serving plain HTTP: put a TLS terminator in front, or set PGDOCK_EDGE_TLS_CERT and PGDOCK_EDGE_TLS_KEY", "listen", listen)
			errc <- srv.ListenAndServe()
			return
		}
		certs := &reloadingCert{cert: certFile, key: keyFile}
		if _, err := certs.get(nil); err != nil {
			errc <- err
			return
		}
		srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: certs.get}
		log.Info("pgdock-edge listening", "listen", listen, "domain", cfg.Domain, "region", cfg.Region)
		errc <- srv.ListenAndServeTLS("", "")
	}()
	select {
	case err := <-errc:
		stop()
		wg.Wait()
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	wg.Wait()
	return nil
}

// reloadingCert serves the wildcard certificate from files, re-reading them
// when they change (a DNS-01 renewal writes new ones).
type reloadingCert struct {
	cert, key string
	mu        sync.Mutex
	loaded    *tls.Certificate
	modTime   time.Time
	checked   time.Time
}

func (r *reloadingCert) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loaded != nil && time.Since(r.checked) < time.Minute {
		return r.loaded, nil
	}
	r.checked = time.Now()
	st, err := os.Stat(r.cert)
	if err != nil {
		if r.loaded != nil {
			return r.loaded, nil
		}
		return nil, err
	}
	if r.loaded != nil && st.ModTime().Equal(r.modTime) {
		return r.loaded, nil
	}
	c, err := tls.LoadX509KeyPair(r.cert, r.key)
	if err != nil {
		if r.loaded != nil {
			return r.loaded, nil // keep serving the old one
		}
		return nil, err
	}
	r.loaded, r.modTime = &c, st.ModTime()
	return r.loaded, nil
}
