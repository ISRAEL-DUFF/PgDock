package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/israel-duff/pgdock/internal/agentapi"
	"github.com/israel-duff/pgdock/internal/agentsvc"
	"github.com/israel-duff/pgdock/internal/floatip"
	"github.com/israel-duff/pgdock/internal/pooler"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/test/testenv"
)

// poolerHostsDriver stands in for the nodes service: it calls each pooler
// host's agent handler directly (mTLS and registration are covered by the
// Docker HA test), and can make a host's agent fail.
type poolerHostsDriver struct {
	db     store.DBTX
	agents map[string]*agentsvc.Service // by node name

	mu       sync.Mutex
	pushFail map[string]bool // Push fails
	down     map[string]bool // every call fails
}

func (d *poolerHostsDriver) Hosts(ctx context.Context) ([]store.Node, error) {
	return store.New(d.db).PoolerHosts(ctx)
}

func (d *poolerHostsDriver) call(n store.Node, method, path string, in, out any) error {
	d.mu.Lock()
	down := d.down[n.Name]
	d.mu.Unlock()
	if down {
		return fmt.Errorf("agent %s: connection refused", n.Name)
	}
	b, _ := json.Marshal(in)
	rec := httptest.NewRecorder()
	d.agents[n.Name].Handler().ServeHTTP(rec, httptest.NewRequest(method, path, bytes.NewReader(b)))
	if rec.Code != http.StatusOK {
		return fmt.Errorf("agent %s: %d %s", n.Name, rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	return json.Unmarshal(rec.Body.Bytes(), out)
}

func (d *poolerHostsDriver) Push(_ context.Context, n store.Node, b agentapi.PoolerBundle) (agentapi.PoolerStatus, error) {
	var st agentapi.PoolerStatus
	d.mu.Lock()
	fail := d.pushFail[n.Name]
	d.mu.Unlock()
	if fail {
		return st, errors.New("push timed out")
	}
	return st, d.call(n, http.MethodPut, agentapi.PathPoolerConfig, b, &st)
}

func (d *poolerHostsDriver) Expect(_ context.Context, n store.Node, e agentapi.PoolerExpected) (agentapi.PoolerStatus, error) {
	var st agentapi.PoolerStatus
	return st, d.call(n, http.MethodPut, agentapi.PathPoolerExpected, e, &st)
}

func (d *poolerHostsDriver) set(m map[string]bool, name string, v bool) {
	d.mu.Lock()
	m[name] = v
	d.mu.Unlock()
}

// TestPoolerHostsSyncAndArbiter covers M17's control-plane side (V3
// §2.1): the configuration reaches both pooler hosts with matching hashes,
// a host that misses a push is stale (keepalived's check fails there) and
// is caught up, and the arbiter moves the floating IP off a dead or stale
// host when keepalived doesn't.
func TestPoolerHostsSyncAndArbiter(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	q := store.New(e.DB)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// The hosts' admin consoles are the test PgBouncers (both "hosts" sit
	// at 127.0.0.1), so RELOADs really happen.
	sessionAddr := os.Getenv("PGDOCK_TEST_POOLER_SESSION_ADDR")
	pooledAddr := os.Getenv("PGDOCK_TEST_POOLER_POOLED_ADDR")
	port := func(addr string) int {
		p, _ := strconv.Atoi(addr[strings.LastIndex(addr, ":")+1:])
		return p
	}

	d := &poolerHostsDriver{db: e.DB, agents: map[string]*agentsvc.Service{}, pushFail: map[string]bool{}, down: map[string]bool{}}
	dirs := map[string]string{}
	for i, name := range []string{"edge-a", "edge-b"} {
		n, err := q.InsertNode(ctx, store.InsertNodeParams{Name: name, PrivateAddr: "127.0.0.1", Role: "pooler", Region: "eu-central"})
		if err != nil {
			t.Fatal(err)
		}
		fp := "test-" + name
		if _, err := q.CompleteNodeRegistration(ctx, store.CompleteNodeRegistrationParams{ID: n.ID, AgentCertFp: &fp, AgentPort: 7070}); err != nil {
			t.Fatal(err)
		}
		svc := agentsvc.New(agentsvc.Config{}, log)
		dirs[name] = t.TempDir()
		if err := svc.EnablePooler(agentsvc.PoolerConfig{
			Dir: dirs[name], SessionAddr: sessionAddr, PooledAddr: pooledAddr, ServerID: strconv.Itoa(1001 + i),
		}); err != nil {
			t.Fatal(err)
		}
		d.agents[name] = svc
	}
	t.Cleanup(func() {
		_, _ = e.DB.Exec(context.Background(), `DELETE FROM nodes WHERE role = 'pooler'`)
		_, _ = e.DB.Exec(context.Background(), `DELETE FROM pooler_generations`)
	})

	dir := t.TempDir()
	adminPW := os.Getenv("PGDOCK_TEST_POOLER_ADMIN_PASSWORD")
	pm, err := pooler.NewManager(dir, 0o640, e.DB, nil, []pooler.User{{Name: "pgdock", Secret: "SCRAM-SHA-256$4096:x"}}, log)
	if err != nil {
		t.Fatal(err)
	}
	pm.SetHostDriver(d, pooler.HostAdminConfig{
		User: "pgdock", Password: adminPW, SSLMode: "prefer", SessionPort: port(sessionAddr), PooledPort: port(pooledAddr),
	})

	status := func(name string) agentapi.PoolerStatus {
		var st agentapi.PoolerStatus
		rec := httptest.NewRecorder()
		d.agents[name].Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, agentapi.PathPoolerStatus, nil))
		_ = json.Unmarshal(rec.Body.Bytes(), &st)
		return st
	}
	sameFiles := func(name string) {
		t.Helper()
		for _, f := range []string{"databases.ini", "userlist.txt"} {
			want, _ := os.ReadFile(filepath.Join(dir, f))
			got, _ := os.ReadFile(filepath.Join(dirs[name], f))
			if !bytes.Equal(want, got) {
				t.Fatalf("%s: %s differs from the control plane's", name, f)
			}
		}
	}

	// 1. A sync reaches both hosts, with the same generation and hash.
	if err := pm.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	a, b := status("edge-a"), status("edge-b")
	if !a.Ready || !b.Ready || a.Hash != b.Hash || a.Generation != b.Generation || a.Generation == 0 {
		t.Fatalf("after sync: a %+v, b %+v", a, b)
	}
	sameFiles("edge-a")
	sameFiles("edge-b")
	if got := len(pm.Admins()); got != 4 {
		t.Fatalf("admin consoles: %d, want 4 (two per host)", got)
	}

	// 2. edge-b misses a push: the change still succeeds (edge-a has it),
	// edge-b knows it is stale and is not ready, so keepalived can't move
	// the floating IP to it.
	d.set(d.pushFail, "edge-b", true)
	if err := os.WriteFile(filepath.Join(dir, "server.crt"), []byte("renewed certificate"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := pm.Reload(ctx); err != nil {
		t.Fatalf("a push one host missed failed the change: %v", err)
	}
	a, b = status("edge-a"), status("edge-b")
	if !a.Ready || a.Generation != b.Generation+1 || !b.Stale || b.Ready {
		t.Fatalf("after a missed push: a %+v, b %+v", a, b)
	}
	var failedPushes int
	if err := e.DB.QueryRow(ctx, `SELECT count(*) FROM pooler_events WHERE kind = 'push_failed'`).Scan(&failedPushes); err != nil || failedPushes == 0 {
		t.Fatalf("push_failed events: %d (%v)", failedPushes, err)
	}

	// 3. The arbiter: the floating IP is unassigned, edge-a is the only
	// healthy host, so after the grace period it goes there.
	fake := &floatip.Fake{Token: "tok", IPID: "77"}
	hz := httptest.NewServer(fake)
	defer hz.Close()
	arb := pooler.NewArbiter(pm, &floatip.Hetzner{API: hz.URL, Token: "tok", IPID: "77"}, log)
	arb.RepushEvery = 1 << 62 // no catch-up yet
	for range 2 {
		if err := arb.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if fake.Assigned() != 1001 {
		t.Fatalf("floating IP on %d, want edge-a (1001)", fake.Assigned())
	}
	snap := arb.Snapshot()
	if snap.HolderName != "edge-a" || len(snap.Hosts) != 2 {
		t.Fatalf("snapshot: %+v", snap)
	}
	var sid *string
	_ = e.DB.QueryRow(ctx, `SELECT provider_server_id FROM nodes WHERE name = 'edge-b'`).Scan(&sid)
	if sid == nil || *sid != "1002" {
		t.Fatalf("edge-b's server ID was not recorded: %v", sid)
	}

	// 4. edge-b's agent works again: the arbiter catches it up.
	d.set(d.pushFail, "edge-b", false)
	arb.RepushEvery = 0
	if err := arb.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if b = status("edge-b"); !b.Ready || b.Stale {
		t.Fatalf("edge-b not caught up: %+v", b)
	}
	sameFiles("edge-b")

	// 5. edge-a dies and keepalived doesn't move the IP (split network):
	// after the grace period the arbiter moves it to edge-b.
	d.set(d.down, "edge-a", true)
	if err := arb.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if fake.Assigned() != 1001 {
		t.Fatal("the arbiter moved the IP before the grace period")
	}
	if err := arb.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if fake.Assigned() != 1002 {
		t.Fatalf("floating IP on %d, want edge-b (1002)", fake.Assigned())
	}
	// Commands now skip the dead host instead of failing on it.
	if got := len(pm.Admins()); got != 2 {
		t.Fatalf("admin consoles with edge-a down: %d, want 2", got)
	}
	var kinds []string
	rows, _ := q.ListPoolerEvents(ctx, 50)
	for _, r := range rows {
		kinds = append(kinds, r.Kind)
	}
	joined := strings.Join(kinds, ",")
	for _, want := range []string{"push_failed", "reassigned", "recovered"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no %s event in %v", want, kinds)
		}
	}

	// 6. Split brain: both hosts report keepalived MASTER.
	d.set(d.down, "edge-a", false)
	for _, name := range []string{"edge-a", "edge-b"} {
		rec := httptest.NewRecorder()
		d.agents[name].LocalHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/vrrp/MASTER", nil))
	}
	if err := arb.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if !arb.Snapshot().SplitBrain {
		t.Fatalf("split brain not seen: %+v", arb.Snapshot())
	}
	if fake.Assigned() != 1002 {
		t.Fatalf("the arbiter moved a healthy holder's IP during split brain: %d", fake.Assigned())
	}

	// 7. keepalived on edge-a claims the IP itself (its notify script): the
	// arbiter sees the move, records it, and leaves it there.
	if err := (&floatip.Hetzner{API: hz.URL, Token: "tok", IPID: "77"}).Assign(ctx, "1001"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := arb.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if fake.Assigned() != 1001 {
		t.Fatalf("the arbiter fought keepalived's move: IP on %d", fake.Assigned())
	}
	var took int
	if err := e.DB.QueryRow(ctx, `SELECT count(*) FROM pooler_events e JOIN nodes n ON n.id = e.node_id
		WHERE e.kind = 'took_ip' AND n.name = 'edge-a'`).Scan(&took); err != nil || took == 0 {
		t.Fatalf("keepalived's move was not recorded: %d (%v)", took, err)
	}
}
