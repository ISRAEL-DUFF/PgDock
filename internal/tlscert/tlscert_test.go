package tlscert

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestSelfSigned(t *testing.T) {
	dir := t.TempDir()
	var reloads atomic.Int32
	m, err := New(Config{Mode: ModeSelfSigned, Dir: dir, Log: quiet,
		Reload: func(context.Context) error { reloads.Add(1); return nil }}, "db.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.EnsureNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	c, err := m.currentCert()
	if err != nil || c.VerifyHostname("db.example.com") != nil {
		t.Fatalf("cert: %v", err)
	}
	if st := m.Status(); st.State != "ok" || st.Mode != ModeSelfSigned || st.NotAfter.Before(time.Now().Add(300*24*time.Hour)) {
		t.Fatalf("status: %+v", st)
	}
	if fi, _ := os.Stat(filepath.Join(dir, KeyFile)); fi.Mode().Perm() != 0o640 {
		t.Fatalf("key mode %v", fi.Mode().Perm())
	}
	// Unchanged host: nothing rewritten, no reload.
	_ = m.EnsureNow(context.Background())
	if reloads.Load() != 1 {
		t.Fatalf("reloads = %d, want 1", reloads.Load())
	}
	// New host: a new certificate and a reload.
	m.SetHost("10.0.0.7")
	_ = m.EnsureNow(context.Background())
	c, _ = m.currentCert()
	if c.VerifyHostname("10.0.0.7") != nil || reloads.Load() != 2 {
		t.Fatalf("after host change: %v reloads=%d", c.VerifyHostname("10.0.0.7"), reloads.Load())
	}
}

func TestFilesMode(t *testing.T) {
	src, dir := t.TempDir(), t.TempDir()
	certPEM, keyPEM, _ := SelfSigned("db.example.com", time.Hour*24*90)
	_ = os.WriteFile(filepath.Join(src, "c.pem"), certPEM, 0o600)
	_ = os.WriteFile(filepath.Join(src, "k.pem"), keyPEM, 0o600)
	m, err := New(Config{Mode: ModeFiles, Dir: dir, Log: quiet,
		SourceCert: filepath.Join(src, "c.pem"), SourceKey: filepath.Join(src, "k.pem")}, "db.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.EnsureNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, CertFile))
	if string(got) != string(certPEM) {
		t.Fatal("files mode did not copy the certificate")
	}
	// A mismatched pair is refused and the old one stays.
	other, _, _ := SelfSigned("x.example.com", time.Hour)
	_ = os.WriteFile(filepath.Join(src, "c.pem"), other, 0o600)
	if err := m.EnsureNow(context.Background()); err == nil || m.Status().State != "error" {
		t.Fatalf("mismatched pair accepted: %v %+v", err, m.Status())
	}
	if got, _ := os.ReadFile(filepath.Join(dir, CertFile)); string(got) != string(certPEM) {
		t.Fatal("bad pair replaced the good one")
	}
}

func TestACMEFallsBackForIPs(t *testing.T) {
	m, err := New(Config{Mode: ModeACME, Dir: t.TempDir(), DataDir: t.TempDir(), ChallengePort: 8080, Log: quiet}, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	err = m.EnsureNow(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not a public DNS name") {
		t.Fatalf("expected fallback error, got %v", err)
	}
	if c, cerr := m.currentCert(); cerr != nil || c.VerifyHostname("127.0.0.1") != nil {
		t.Fatalf("no self-signed stand-in: %v", cerr)
	}
}

// TestACMEWithPebble obtains a real certificate from Pebble (Let's
// Encrypt's test CA) over HTTP-01: Pebble resolves the name through
// pebble-challtestsrv to 127.0.0.1 and fetches the challenge from our
// handler on :5002. Needs PGDOCK_TEST_PEBBLE_DIR with the pebble and
// pebble-challtestsrv binaries (`make test-acme` installs them).
func TestACMEWithPebble(t *testing.T) {
	bin := os.Getenv("PGDOCK_TEST_PEBBLE_DIR")
	if bin == "" {
		t.Skip("PGDOCK_TEST_PEBBLE_DIR not set; run `make test-acme`")
	}
	root := pebbleModuleDir(t)
	work := t.TempDir()

	start := func(name string, env []string, args ...string) {
		cmd := exec.Command(filepath.Join(bin, name), args...)
		cmd.Env = append(os.Environ(), env...)
		cmd.Dir = root
		if testing.Verbose() {
			cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	}
	start("pebble-challtestsrv", nil, "-defaultIPv4", "127.0.0.1", "-defaultIPv6", "",
		"-dnsserver", "127.0.0.1:8053", "-http01", "", "-https01", "", "-tlsalpn01", "", "-doh", "",
		"-management", "127.0.0.1:8055")
	waitPort(t, "127.0.0.1:8055") // DNS (UDP) comes up with the management API
	cfgPath := filepath.Join(work, "pebble.json")
	cfg := map[string]any{"pebble": map[string]any{
		"listenAddress": "127.0.0.1:14000", "managementListenAddress": "127.0.0.1:15000",
		"certificate": "test/certs/localhost/cert.pem", "privateKey": "test/certs/localhost/key.pem",
		"httpPort": 5002, "tlsPort": 5001, "ocspResponderURL": "", "externalAccountBindingRequired": false,
	}}
	b, _ := json.Marshal(cfg)
	_ = os.WriteFile(cfgPath, b, 0o600)
	start("pebble", []string{"PEBBLE_VA_NOSLEEP=1", "PEBBLE_WFE_NONCEREJECT=0"}, "-config", cfgPath, "-dnsserver", "127.0.0.1:8053")

	// Our HTTP-01 responder, where Caddy would forward port 80.
	dir := t.TempDir()
	var reloads atomic.Int32
	roots := x509.NewCertPool()
	pemBytes, err := os.ReadFile(filepath.Join(root, "test/certs/pebble.minica.pem"))
	if err != nil {
		t.Fatal(err)
	}
	roots.AppendCertsFromPEM(pemBytes)
	m, err := New(Config{
		Mode: ModeACME, Dir: dir, DataDir: t.TempDir(), Log: quiet,
		ACMECA: "https://127.0.0.1:14000/dir", ACMERoots: roots, ACMEEmail: "ops@example.com", ChallengePort: 5002,
		Reload: func(context.Context) error { reloads.Add(1); return nil },
	}, "db.pgdock.test")
	if err != nil {
		t.Fatal(err)
	}
	var challenges atomic.Int32
	ln, err := net.Listen("tcp", "127.0.0.1:5002")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: m.HTTPChallengeHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	})), ConnState: func(net.Conn, http.ConnState) {}}
	srv.Handler = countChallenges(srv.Handler, &challenges)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	waitPort(t, "127.0.0.1:14000")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := m.EnsureNow(ctx); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	c, err := m.currentCert()
	if err != nil {
		t.Fatal(err)
	}
	if c.VerifyHostname("db.pgdock.test") != nil || !strings.Contains(m.Status().Issuer, "Pebble") {
		t.Fatalf("not a Pebble certificate for the host: issuer=%q status=%+v", c.Issuer, m.Status())
	}
	if challenges.Load() == 0 {
		t.Fatal("Pebble never fetched an HTTP-01 challenge")
	}
	if reloads.Load() == 0 {
		t.Fatal("poolers were not reloaded with the new certificate")
	}
	// The written pair is a full chain with a matching key.
	if _, err := tls.LoadX509KeyPair(filepath.Join(dir, CertFile), filepath.Join(dir, KeyFile)); err != nil {
		t.Fatal(err)
	}
}

func countChallenges(next http.Handler, n *atomic.Int32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/.well-known/acme-challenge/") {
			n.Add(1)
		}
		next.ServeHTTP(w, r)
	})
}

func waitPort(t *testing.T, addr string) {
	t.Helper()
	for range 100 {
		if c, err := net.Dial("tcp", addr); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s never came up", addr)
}

func pebbleModuleDir(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "mod", "download", "-json", "github.com/letsencrypt/pebble/v2@v2.10.1").Output()
	if err != nil {
		t.Fatalf("locate pebble module: %v", err)
	}
	var m struct{ Dir string }
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	return m.Dir
}
