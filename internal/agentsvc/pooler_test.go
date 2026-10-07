package agentsvc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/agentapi"
	"github.com/israel-duff/pgdock/internal/floatip"
)

func bundle(gen int64, routes string) agentapi.PoolerBundle {
	files := map[string][]byte{
		"databases.ini": []byte(routes),
		"userlist.txt":  []byte(`"pgdock_admin" "SCRAM-SHA-256$..."` + "\n"),
		"server.crt":    []byte("cert"),
		"server.key":    []byte("key"),
	}
	return agentapi.PoolerBundle{Generation: gen, Hash: agentapi.PoolerHash(files), Files: files}
}

func listen(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	return ln.Addr().String()
}

func newTestPooler(t *testing.T, fip floatip.Provider) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	s := New(Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := s.EnablePooler(PoolerConfig{
		Dir: dir, SessionAddr: listen(t), PooledAddr: listen(t), ServerID: "1001", FloatIP: fip,
	}); err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestPoolerBundleApply(t *testing.T) {
	s, dir := newTestPooler(t, nil)
	p := s.pool

	if st := p.status(); st.Ready || !strings.Contains(st.Reason, "no configuration") {
		t.Fatalf("fresh host is ready: %+v", st)
	}
	if err := p.apply(bundle(2, "[databases]\na = host=x\n")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "databases.ini")); string(b) != "[databases]\na = host=x\n" {
		t.Fatalf("routes not written: %q", b)
	}
	if st := p.status(); !st.Ready || st.Generation != 2 || st.Stale {
		t.Fatalf("after apply: %+v", st)
	}

	// Tampered content, unknown files, missing files and old generations
	// are refused, and the served configuration stays.
	bad := bundle(3, "x")
	bad.Files["databases.ini"] = []byte("tampered")
	if err := p.apply(bad); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("tampered bundle: %v", err)
	}
	extra := bundle(3, "x")
	extra.Files["../../etc/passwd"] = []byte("x")
	extra.Hash = agentapi.PoolerHash(extra.Files)
	if err := p.apply(extra); err == nil || !strings.Contains(err.Error(), "unexpected file") {
		t.Fatalf("unexpected file: %v", err)
	}
	missing := bundle(3, "x")
	delete(missing.Files, "userlist.txt")
	missing.Hash = agentapi.PoolerHash(missing.Files)
	if err := p.apply(missing); err == nil {
		t.Fatal("a bundle without the auth file was accepted")
	}
	if err := p.apply(bundle(1, "old")); !errors.Is(err, errOlder) {
		t.Fatalf("older generation: %v", err)
	}
	if st := p.status(); st.Generation != 2 {
		t.Fatalf("generation moved: %+v", st)
	}

	// pgdock-server rendered generation 4 but this host missed the push:
	// it is stale and must not take the floating IP.
	if err := p.expect(agentapi.PoolerExpected{Generation: 4}); err != nil {
		t.Fatal(err)
	}
	if st := p.status(); st.Ready || !st.Stale {
		t.Fatalf("stale host is ready: %+v", st)
	}
	if err := p.apply(bundle(4, "new")); err != nil {
		t.Fatal(err)
	}
	if st := p.status(); !st.Ready {
		t.Fatalf("caught-up host not ready: %+v", st)
	}

	// State survives a restart.
	s2 := New(Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := s2.EnablePooler(PoolerConfig{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if st := s2.pool.status(); st.Generation != 4 || st.Expected != 4 {
		t.Fatalf("after restart: %+v", st)
	}
}

func TestPoolerReadinessNeedsBothPgBouncers(t *testing.T) {
	s, _ := newTestPooler(t, nil)
	s.pool.cfg.PooledAddr = "127.0.0.1:1" // nothing listens
	if err := s.pool.apply(bundle(1, "x")); err != nil {
		t.Fatal(err)
	}
	st := s.pool.status()
	if st.Ready || st.Transaction || !st.Session || !strings.Contains(st.Reason, "transaction PgBouncer") {
		t.Fatalf("half-down host: %+v", st)
	}
}

func TestPoolerLocalAPIAndClaim(t *testing.T) {
	fake := &floatip.Fake{Token: "t", IPID: "9"}
	hz := httptest.NewServer(fake)
	defer hz.Close()
	s, _ := newTestPooler(t, &floatip.Hetzner{API: hz.URL, Token: "t", IPID: "9"})
	h := s.LocalHandler()

	get := func() int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
		return rec.Code
	}
	if code := get(); code != http.StatusServiceUnavailable {
		t.Fatalf("ready before config: %d", code)
	}
	if err := s.pool.apply(bundle(1, "x")); err != nil {
		t.Fatal(err)
	}
	if code := get(); code != http.StatusOK {
		t.Fatalf("ready after config: %d", code)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/vrrp/bogus", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bogus state: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/vrrp/master", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("master: %d", rec.Code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for fake.Assigned() != 1001 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if fake.Assigned() != 1001 {
		t.Fatalf("floating IP not claimed: %v", fake.Assignments())
	}
	if st := s.pool.status(); st.VRRPState != "MASTER" || st.ClaimError != "" {
		t.Fatalf("status after claim: %+v", st)
	}

	if err := s.ServeLocal(context.Background(), "0.0.0.0:0"); err == nil {
		t.Fatal("the local API agreed to listen on a public address")
	}
}

func TestPoolerHandlersRefuseWithoutPoolerMode(t *testing.T) {
	s := New(Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, agentapi.PathPoolerStatus, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status on a database node: %d", rec.Code)
	}
}
