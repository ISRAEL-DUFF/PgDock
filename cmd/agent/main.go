// Command pgdock-agent runs on each node and executes pgdock-server's
// commands over mutual TLS: health, host metrics, and pg_dump/pg_restore
// based dumps, restores, and copies (spec §3.2). Dedicated instances arrive
// in M4.
//
//	pgdock-agent register --server https://pgdock.example.com --token <one-time>
//	pgdock-agent run
//	pgdock-agent decrypt --key-file pgdock-backup-key.txt < x.dump.enc > x.dump
//
// Environment variables mirror the flags (PGDOCK_AGENT_*).
package main

import (
	"context"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/israel-duff/pgdock/internal/agentsvc"
	"github.com/israel-duff/pgdock/internal/backupfmt"
	"github.com/israel-duff/pgdock/internal/floatip"
	"github.com/israel-duff/pgdock/internal/logging"
	"github.com/israel-duff/pgdock/internal/version"
)

// DefaultPGImage is the instance image this release was built and tested
// with (deploy/images/postgres).
const DefaultPGImage = "pgdock-postgres:18-walg3.0.9"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "pgdock-agent:", err)
		os.Exit(1)
	}
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

type opts struct {
	state, listen, server, token, bootstrap, node, advertise, caFile, pgBin, diskPath string
	dockerHost, image, network, publish, dbAllow                                      string
	insecure                                                                          bool

	// Pooler hosts (V3 §2.1).
	poolerDir, poolerSession, poolerPooled, poolerLocal string
	serverID, hetznerToken, hetznerAPI, floatingIP      string
}

func flags(name string, args []string) (*opts, error) {
	o := &opts{}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.StringVar(&o.state, "state", env("PGDOCK_AGENT_STATE_DIR", "/var/lib/pgdock-agent"), "state directory (key, certificate, pinned CA)")
	fs.StringVar(&o.listen, "listen", env("PGDOCK_AGENT_LISTEN", ":7070"), "mTLS listen address")
	fs.StringVar(&o.server, "server", env("PGDOCK_AGENT_SERVER", ""), "pgdock-server URL, for registration")
	fs.StringVar(&o.token, "token", env("PGDOCK_AGENT_TOKEN", ""), "one-time registration token")
	fs.StringVar(&o.bootstrap, "bootstrap-token", env("PGDOCK_AGENT_BOOTSTRAP_TOKEN", ""), "install-time bootstrap token (with --node)")
	fs.StringVar(&o.node, "node", env("PGDOCK_AGENT_NODE", ""), "node name, with --bootstrap-token")
	fs.StringVar(&o.advertise, "advertise", env("PGDOCK_AGENT_ADVERTISE", ""), "host[:port] pgdock-server dials to reach this agent")
	fs.StringVar(&o.caFile, "server-ca", env("PGDOCK_AGENT_SERVER_CA", ""), "PEM file to verify pgdock-server's HTTPS certificate")
	fs.BoolVar(&o.insecure, "insecure-skip-verify", env("PGDOCK_AGENT_INSECURE_SKIP_VERIFY", "") == "true", "do not verify pgdock-server's HTTPS certificate (development only)")
	fs.StringVar(&o.pgBin, "pg-bin", env("PGDOCK_AGENT_PG_BIN", ""), "directory with pg_dump and pg_restore (default: $PATH)")
	fs.StringVar(&o.diskPath, "disk-path", env("PGDOCK_AGENT_DISK_PATH", "/"), "path whose disk usage is reported")
	fs.StringVar(&o.dockerHost, "docker", env("PGDOCK_AGENT_DOCKER", env("DOCKER_HOST", "")), "Docker daemon for instances (default: the local socket)")
	fs.StringVar(&o.image, "pg-image", env("PGDOCK_AGENT_PG_IMAGE", DefaultPGImage), "Postgres + WAL-G image for instances")
	fs.StringVar(&o.network, "network", env("PGDOCK_AGENT_NETWORK", ""), "Docker network instances join (reached by container name)")
	fs.StringVar(&o.publish, "publish", env("PGDOCK_AGENT_PUBLISH", ""), "node address to publish instance ports on (e.g. its private IP)")
	fs.StringVar(&o.dbAllow, "db-allow", env("PGDOCK_AGENT_DB_ALLOW", defaultDBAllow),
		"comma-separated CIDRs new instances accept logins from: the control plane and poolers (spec §7.1)")
	fs.StringVar(&o.poolerDir, "pooler-dir", env("PGDOCK_AGENT_POOLER_DIR", ""), "pooler host: directory the PgBouncers read their pgdock files from (enables pooler mode)")
	fs.StringVar(&o.poolerSession, "pooler-session-addr", env("PGDOCK_AGENT_POOLER_SESSION_ADDR", "127.0.0.1:5432"), "pooler host: the session PgBouncer, checked for readiness")
	fs.StringVar(&o.poolerPooled, "pooler-pooled-addr", env("PGDOCK_AGENT_POOLER_POOLED_ADDR", "127.0.0.1:6543"), "pooler host: the transaction PgBouncer, checked for readiness")
	fs.StringVar(&o.poolerLocal, "pooler-local", env("PGDOCK_AGENT_POOLER_LOCAL", "127.0.0.1:7071"), "pooler host: loopback address for keepalived's check and notify calls")
	fs.StringVar(&o.serverID, "server-id", env("PGDOCK_AGENT_SERVER_ID", ""), "pooler host: the provider's ID for this machine, or \"hetzner-metadata\" to ask Hetzner's metadata service")
	fs.StringVar(&o.hetznerToken, "hetzner-token", env("PGDOCK_AGENT_HETZNER_TOKEN", ""), "pooler host: Hetzner Cloud API token for assigning the floating IP")
	fs.StringVar(&o.hetznerAPI, "hetzner-api", env("PGDOCK_AGENT_HETZNER_API", floatip.DefaultHetznerAPI), "pooler host: Hetzner Cloud API base URL")
	fs.StringVar(&o.floatingIP, "floating-ip-id", env("PGDOCK_AGENT_FLOATING_IP_ID", ""), "pooler host: the floating IP's ID; empty when keepalived alone moves the address")
	return o, fs.Parse(args)
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: pgdock-agent register|run|decrypt|version [flags]")
	}
	switch args[0] {
	case "version", "-version", "--version":
		v := version.Get()
		fmt.Printf("pgdock-agent %s (commit %s, built %s, %s)\n", v.Version, v.Commit, v.BuildDate, v.GoVersion)
		return nil
	case "decrypt":
		return decrypt(args[1:])
	case "register":
		o, err := flags("register", args[1:])
		if err != nil {
			return err
		}
		st, err := register(context.Background(), o)
		if err != nil {
			return err
		}
		fmt.Printf("registered as node %s; state in %s\n", st.NodeID, st.Dir)
		return nil
	case "run":
		o, err := flags("run", args[1:])
		if err != nil {
			return err
		}
		return serve(o)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func register(ctx context.Context, o *opts) (*agentsvc.State, error) {
	if o.server == "" {
		return nil, errors.New("--server is required to register")
	}
	token := o.token
	if token == "" {
		token = o.bootstrap
	}
	if token == "" {
		return nil, errors.New("--token (or --bootstrap-token with --node) is required to register")
	}
	host, port, err := advertise(o)
	if err != nil {
		return nil, err
	}
	ro := agentsvc.RegisterOptions{
		Server: o.server, Token: token, AdvertiseHost: host, AdvertisePort: port,
		Version: version.Get().Version, InsecureSkipVerify: o.insecure,
	}
	if o.token == "" {
		ro.Node = o.node
	}
	if o.caFile != "" {
		pemBytes, err := os.ReadFile(o.caFile)
		if err != nil {
			return nil, err
		}
		ro.RootCAs = x509.NewCertPool()
		ro.RootCAs.AppendCertsFromPEM(pemBytes)
	}
	return agentsvc.Register(ctx, o.state, ro)
}

func advertise(o *opts) (string, int, error) {
	_, lport, _ := net.SplitHostPort(o.listen)
	port, _ := strconv.Atoi(lport)
	if o.advertise == "" {
		h, err := os.Hostname()
		return h, port, err
	}
	h, p, err := net.SplitHostPort(o.advertise)
	if err != nil {
		return o.advertise, port, nil //nolint:nilerr // a bare host
	}
	port, err = strconv.Atoi(p)
	return h, port, err
}

func serve(o *opts) error {
	log := logging.New(os.Stderr, env("PGDOCK_AGENT_LOG_FORMAT", "json"), slog.LevelInfo)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := agentsvc.LoadState(o.state)
	if errors.Is(err, fs.ErrNotExist) {
		// First start: register if given a token (install bundles do this).
		log.Info("not registered yet; registering", "server", o.server, "node", o.node)
		for attempt := 1; ; attempt++ {
			rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			st, err = register(rctx, o)
			cancel()
			if err == nil || attempt == 30 || ctx.Err() != nil {
				break
			}
			log.Warn("registration failed; retrying", "err", err)
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
		}
	}
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", o.listen)
	if err != nil {
		return err
	}
	cidrs, err := parseCIDRs(o.dbAllow)
	if err != nil {
		return err
	}
	svc := agentsvc.New(agentsvc.Config{
		Version: version.Get().Version, NodeID: st.NodeID, PGBinDir: o.pgBin, DiskPath: o.diskPath,
		Instances: agentsvc.InstanceConfig{Docker: o.dockerHost, Image: o.image, Network: o.network, PublishAddr: o.publish, HBAAllow: cidrs},
	}, log)
	if o.poolerDir != "" {
		if err := enablePooler(ctx, svc, o, log); err != nil {
			return err
		}
	}
	log.Info("pgdock-agent listening", "addr", ln.Addr().String(), "node_id", st.NodeID, "version", version.Get().Version)
	return svc.Serve(ctx, ln, agentsvc.TLSConfig(st.Cert, st.CA))
}

// enablePooler turns on pooler-host mode and the loopback API keepalived
// calls (V3 §2.1).
func enablePooler(ctx context.Context, svc *agentsvc.Service, o *opts, log *slog.Logger) error {
	serverID := o.serverID
	if serverID == "hetzner-metadata" {
		mctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		id, err := floatip.HetznerServerID(mctx)
		cancel()
		if err != nil {
			return fmt.Errorf("--server-id hetzner-metadata: %w", err)
		}
		serverID = id
	}
	var fip floatip.Provider = floatip.None{}
	if o.floatingIP != "" {
		if o.hetznerToken == "" || serverID == "" {
			return errors.New("--floating-ip-id needs --hetzner-token and --server-id")
		}
		fip = &floatip.Hetzner{API: o.hetznerAPI, Token: o.hetznerToken, IPID: o.floatingIP}
	}
	if err := svc.EnablePooler(agentsvc.PoolerConfig{
		Dir: o.poolerDir, SessionAddr: o.poolerSession, PooledAddr: o.poolerPooled, ServerID: serverID, FloatIP: fip,
	}); err != nil {
		return err
	}
	go func() {
		if err := svc.ServeLocal(ctx, o.poolerLocal); err != nil {
			log.Error("pooler local API stopped", "err", err)
		}
	}()
	log.Info("pooler host mode", "dir", o.poolerDir, "local", o.poolerLocal, "server_id", serverID, "floating_ip", o.floatingIP)
	return nil
}

// decrypt turns a backup object back into a pg_dump archive with the
// downloaded backup key, without pgdock-server: the disaster-recovery path
// for metadata self-backups (docs/disaster-recovery.md).
func decrypt(args []string) error {
	fs := flag.NewFlagSet("decrypt", flag.ContinueOnError)
	keyFile := fs.String("key-file", "", "the backup key file downloaded from PGDock")
	in := fs.String("in", "-", "encrypted object (- for stdin)")
	out := fs.String("out", "-", "pg_dump archive to write (- for stdout)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *keyFile == "" {
		return errors.New("decrypt: --key-file is required")
	}
	raw, err := os.ReadFile(*keyFile)
	if err != nil {
		return err
	}
	key, err := backupfmt.DecodeKey(string(raw))
	if err != nil {
		return fmt.Errorf("%s: %w", *keyFile, err)
	}
	r, w := io.Reader(os.Stdin), io.Writer(os.Stdout)
	if *in != "-" {
		f, err := os.Open(*in)
		if err != nil {
			return err
		}
		defer f.Close()
		r = f
	}
	var outFile *os.File
	if *out != "-" {
		if outFile, err = os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err != nil {
			return err
		}
		w = outFile
	}
	dec, err := backupfmt.OpenWithBackupKey(r, key)
	if err == nil {
		_, err = io.Copy(w, dec)
	}
	if outFile != nil {
		if cerr := outFile.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(*out)
		}
	}
	if err != nil {
		return fmt.Errorf("decrypt: %w", err)
	}
	return nil
}

// defaultDBAllow is every private range: the control plane and poolers
// reach instances over a private network.
const defaultDBAllow = "10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,127.0.0.1/32"

func parseCIDRs(s string) ([]string, error) {
	var out []string
	for _, c := range strings.Split(s, ",") {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		p, err := netip.ParsePrefix(c)
		if err != nil {
			return nil, fmt.Errorf("-db-allow: %q is not a CIDR: %w", c, err)
		}
		out = append(out, p.Masked().String())
	}
	return out, nil
}
