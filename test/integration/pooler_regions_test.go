package integration

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/agentsvc"
	"github.com/israel-duff/pgdock/internal/pooler"
	"github.com/israel-duff/pgdock/internal/regions"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/test/testenv"
)

// TestPoolerRegions covers M25's pooler side (V3 §6.1): each region's
// pooler hosts route only that region's projects, a project's connection
// string names its region's hostname, and after a region move the old
// region keeps routing it until the 30-day forward ends.
func TestPoolerRegions(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	q := store.New(e.DB)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	if _, err := e.Regions.Save(ctx, regions.Input{ID: "ng-lagos", Name: "Lagos", Country: "NG", PoolerHost: "db.ng.pgdock.test"}); err != nil {
		t.Fatal(err)
	}
	sessionAddr := os.Getenv("PGDOCK_TEST_POOLER_SESSION_ADDR")
	pooledAddr := os.Getenv("PGDOCK_TEST_POOLER_POOLED_ADDR")
	port := func(addr string) int {
		p, _ := strconv.Atoi(addr[strings.LastIndex(addr, ":")+1:])
		return p
	}
	d := &poolerHostsDriver{db: e.DB, agents: map[string]*agentsvc.Service{}, pushFail: map[string]bool{}, down: map[string]bool{}}
	dirs := map[string]string{}
	for i, h := range []struct{ name, region string }{{"edge-eu", "eu-central"}, {"edge-ng", "ng-lagos"}} {
		n, err := q.InsertNode(ctx, store.InsertNodeParams{Name: h.name, PrivateAddr: "127.0.0.1", Role: "pooler", Region: h.region})
		if err != nil {
			t.Fatal(err)
		}
		fp := "test-" + h.name
		if _, err := q.CompleteNodeRegistration(ctx, store.CompleteNodeRegistrationParams{ID: n.ID, AgentCertFp: &fp, AgentPort: 7070}); err != nil {
			t.Fatal(err)
		}
		svc := agentsvc.New(agentsvc.Config{}, log)
		dirs[h.name] = t.TempDir()
		if err := svc.EnablePooler(agentsvc.PoolerConfig{
			Dir: dirs[h.name], SessionAddr: sessionAddr, PooledAddr: pooledAddr, ServerID: strconv.Itoa(2001 + i),
		}); err != nil {
			t.Fatal(err)
		}
		d.agents[h.name] = svc
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = e.DB.Exec(bg, `DELETE FROM nodes WHERE role = 'pooler'`)
		_, _ = e.DB.Exec(bg, `DELETE FROM pooler_generations`)
		_, _ = e.DB.Exec(bg, `UPDATE projects SET region = 'eu-central', forward_region = NULL, forward_until = NULL`)
		_, _ = e.DB.Exec(bg, `DELETE FROM regions WHERE id = 'ng-lagos'`)
	})

	pm, err := pooler.NewManager(t.TempDir(), 0o640, e.DB, nil, []pooler.User{{Name: "pgdock", Secret: "SCRAM-SHA-256$4096:x"}}, log)
	if err != nil {
		t.Fatal(err)
	}
	pm.SetHome("eu-central")
	pm.SetHostDriver(d, pooler.HostAdminConfig{
		User: "pgdock", Password: os.Getenv("PGDOCK_TEST_POOLER_ADMIN_PASSWORD"), SSLMode: "prefer",
		SessionPort: port(sessionAddr), PooledPort: port(pooledAddr),
	})

	a := e.CreateProject("regional-a")
	b := e.CreateProject("regional-b")
	dbA, dbB := a.Connection.Database, b.Connection.Database
	if _, err := e.DB.Exec(ctx, `UPDATE projects SET region = 'ng-lagos' WHERE id = $1`, b.Project.Id); err != nil {
		t.Fatal(err)
	}
	routes := func(host string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dirs[host], "databases.ini"))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	check := func(step, host, db string, want bool) {
		t.Helper()
		if got := strings.Contains(routes(host), db+" "); got != want {
			t.Fatalf("%s: %s routes %s = %v, want %v\n%s", step, host, db, got, want, routes(host))
		}
	}

	// 1. Each region's hosts route only their own projects.
	if err := pm.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	check("split", "edge-eu", dbA, true)
	check("split", "edge-eu", dbB, false)
	check("split", "edge-ng", dbB, true)
	check("split", "edge-ng", dbA, false)
	var gens int
	if err := e.DB.QueryRow(ctx, `SELECT count(*) FROM pooler_generations WHERE region IN ('eu-central', 'ng-lagos')`).Scan(&gens); err != nil || gens != 2 {
		t.Fatalf("per-region generations: %d (%v)", gens, err)
	}

	// 2. The connection string names the region's hostname.
	pb, err := q.GetProject(ctx, b.Project.Id)
	if err != nil {
		t.Fatal(err)
	}
	if h := e.Service.ConnectionFor(pb).Host; h != "db.ng.pgdock.test" {
		t.Fatalf("Lagos project's host: %q", h)
	}
	pa, err := q.GetProject(ctx, a.Project.Id)
	if err != nil {
		t.Fatal(err)
	}
	if h := e.Service.ConnectionFor(pa).Host; h == "db.ng.pgdock.test" {
		t.Fatalf("eu-central project got the Lagos hostname")
	}

	// 3. A moves to Lagos: eu-central keeps routing it for the forward.
	if err := q.SetProjectRegion(ctx, store.SetProjectRegionParams{ID: a.Project.Id, Region: "ng-lagos", ForwardUntil: ptrTime(time.Now().Add(30 * 24 * time.Hour))}); err != nil {
		t.Fatal(err)
	}
	if err := pm.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	check("forwarding", "edge-eu", dbA, true)
	check("forwarding", "edge-ng", dbA, true)

	// 4. The forward ends: eu-central drops it.
	if _, err := e.DB.Exec(ctx, `UPDATE projects SET forward_until = now() - interval '1 minute' WHERE id = $1`, a.Project.Id); err != nil {
		t.Fatal(err)
	}
	if err := pm.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	check("forward ended", "edge-eu", dbA, false)
	check("forward ended", "edge-ng", dbA, true)
}

func ptrTime(t time.Time) *time.Time { return &t }
