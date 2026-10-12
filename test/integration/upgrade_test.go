package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/test/testenv"
)

func serverMajor(t *testing.T, e *testenv.Env, url string) int {
	t.Helper()
	conn := e.MustConnect(url)
	defer conn.Close(context.Background())
	var v int
	if err := conn.QueryRow(context.Background(), `SELECT current_setting('server_version_num')::int / 10000`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func upgradeAndWait(t *testing.T, e *testenv.Env, project string, to int) (gen.Operation, gen.Move) {
	t.Helper()
	var pf gen.UpgradePreflight
	if code := e.Do("POST", "/api/v1/projects/"+project+"/upgrade/preflight", gen.UpgradeRequest{PgVersion: to}, &pf); code != http.StatusOK || !pf.Eligible {
		t.Fatalf("preflight: %d %+v", code, pf)
	}
	var schema string
	for _, c := range pf.Checks {
		if c.Name == gen.UpgradeCheckNameSchema {
			schema = c.Message
		}
	}
	if schema == "" {
		t.Fatalf("preflight has no schema check: %+v", pf.Checks)
	}
	t.Logf("preflight: %s", schema)
	var op gen.Operation
	if code := e.Do("POST", "/api/v1/projects/"+project+"/upgrade", gen.UpgradeRequest{PgVersion: to}, &op); code != http.StatusAccepted {
		t.Fatalf("upgrade: %d", code)
	}
	op = e.WaitOperation(op.Id)
	if op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("upgrade: %s %s\n%s", op.Status, deref(op.Error), testenv.FormatLog(op))
	}
	var moves gen.MoveList
	e.Do("GET", "/api/v1/projects/"+project+"/moves", nil, &moves)
	if len(moves.Items) == 0 {
		t.Fatal("no move recorded")
	}
	return op, moves.Items[0]
}

// TestSharedMajorUpgrade is M18's 17 → 18 upgrade on the shared tier: a
// project on a Postgres 17 shared cluster moves to the 18 one by logical
// replication under continuous writes, with every commit kept.
func TestSharedMajorUpgrade(t *testing.T) {
	needDedicated(t) // the Postgres 17 cluster is agent-run
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	ctx := context.Background()

	var created gen.NodeCreated
	e.Do("POST", "/api/v1/nodes", gen.CreateNodeRequest{Name: "node-17", PrivateAddr: "agent-test-2", Role: gen.CreateNodeRequestRoleShared}, &created)
	e.StartSecondAgent("node-17", created.Token)
	v17 := 17
	var op gen.Operation
	if code := e.Do("POST", "/api/v1/nodes/"+created.Node.Id.String()+"/shared-cluster", gen.SharedClusterRequest{MemoryMb: 512, PgVersion: &v17}, &op); code != http.StatusAccepted {
		t.Fatalf("shared cluster: %d", code)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("Postgres 17 shared cluster: %s\n%s", op.Status, testenv.FormatLog(op))
	}

	var c gen.ProjectCredentials
	if code := e.Do("POST", "/api/v1/projects", gen.CreateProjectRequest{Name: "Old version", PgVersion: &v17}, &c); code != http.StatusAccepted {
		t.Fatalf("create on 17: %d", code)
	}
	if op := e.WaitOperation(c.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("create: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	if v := serverMajor(t, e, c.Connection.PooledUrl); v != 17 {
		t.Fatalf("new project runs Postgres %d, want 17", v)
	}
	conn := e.MustConnect(c.Connection.PooledUrl)
	if _, err := conn.Exec(ctx, `CREATE TABLE notes (id bigserial PRIMARY KEY, body text);
		INSERT INTO notes (body) SELECT md5(g::text) FROM generate_series(1, 5000) g;
		CREATE VIEW recent AS SELECT * FROM notes ORDER BY id DESC LIMIT 10`); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close(ctx)

	// Refused: not newer, not supported.
	for _, v := range []int{17, 15} {
		var pf gen.UpgradePreflight
		if code := e.Do("POST", "/api/v1/projects/"+c.Project.Id.String()+"/upgrade/preflight", gen.UpgradeRequest{PgVersion: v}, &pf); code != http.StatusOK || pf.Eligible {
			t.Fatalf("upgrade to %d: %d %+v", v, code, pf)
		}
	}

	w := startWriter(t, e, c.Connection.PooledUrl)
	time.Sleep(time.Second)
	op, mv := upgradeAndWait(t, e, c.Project.Id.String(), 18)
	time.Sleep(500 * time.Millisecond)
	w.Stop()
	log := testenv.FormatLog(op)
	t.Logf("upgrade log:\n%s", log)
	if mv.Mode != gen.MoveModeLogical || mv.FreezeMs == nil || *mv.FreezeMs >= 5000 {
		t.Fatalf("move: %+v", mv)
	}
	for _, want := range []string{"Postgres 17 → 18", "upgraded to Postgres 18"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q", want)
		}
	}
	if v := serverMajor(t, e, c.Connection.PooledUrl); v != 18 {
		t.Fatalf("after the upgrade: Postgres %d", v)
	}
	conn = e.MustConnect(c.Connection.PooledUrl)
	defer conn.Close(ctx)
	var count, maxN, notes int64
	if err := conn.QueryRow(ctx, `SELECT count(*), coalesce(max(n), 0), (SELECT count(*) FROM recent) FROM ledger`).Scan(&count, &maxN, &notes); err != nil {
		t.Fatal(err)
	}
	if maxN < w.acked.Load() || count != maxN || notes != 10 {
		t.Fatalf("after the upgrade: ledger %d rows max %d (acked %d), view rows %d", count, maxN, w.acked.Load(), notes)
	}
	t.Logf("17 → 18: writes paused %d ms; %d commits acknowledged", *mv.FreezeMs, w.acked.Load())
}

// TestDedicatedMajorUpgrade: a dedicated Postgres 17 project gets a new
// Postgres 18 instance; the old one is stopped and kept.
func TestDedicatedMajorUpgrade(t *testing.T) {
	needDedicated(t)
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	e.ConfigureBackups()
	e.SetNodeRole("test", "both")
	ctx := context.Background()

	tier := gen.ProjectTierDedicated
	profile, vol, v17 := "small", 5, 17
	var c gen.ProjectCredentials
	if code := e.Do("POST", "/api/v1/projects", gen.CreateProjectRequest{Name: "Dedicated 17", Tier: &tier, Profile: &profile, VolumeGb: &vol, PgVersion: &v17}, &c); code != http.StatusAccepted {
		t.Fatalf("create: %d", code)
	}
	if op := e.WaitOperation(c.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("create: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	if v := serverMajor(t, e, c.Connection.PooledUrl); v != 17 {
		t.Fatalf("dedicated project runs Postgres %d, want 17", v)
	}
	var before gen.Project
	e.Do("GET", "/api/v1/projects/"+c.Project.Id.String(), nil, &before)

	w := startWriter(t, e, c.Connection.PooledUrl)
	time.Sleep(time.Second)
	op, mv := upgradeAndWait(t, e, c.Project.Id.String(), 18)
	w.Stop()
	if mv.Mode != gen.MoveModeLogical || mv.FreezeMs == nil || *mv.FreezeMs >= 5000 {
		t.Fatalf("move: %+v\n%s", mv, testenv.FormatLog(op))
	}
	if v := serverMajor(t, e, c.Connection.PooledUrl); v != 18 {
		t.Fatalf("after the upgrade: Postgres %d", v)
	}
	var after gen.Project
	e.Do("GET", "/api/v1/projects/"+c.Project.Id.String(), nil, &after)
	if after.Instance == nil || after.Instance.PgVersion != 18 || after.Instance.Id == before.Instance.Id || before.Instance.PgVersion != 17 {
		t.Fatalf("instances: before %+v after %+v", before.Instance, after.Instance)
	}
	conn := e.MustConnect(c.Connection.PooledUrl)
	defer conn.Close(ctx)
	var count, maxN int64
	if err := conn.QueryRow(ctx, `SELECT count(*), coalesce(max(n), 0) FROM ledger`).Scan(&count, &maxN); err != nil || maxN < w.acked.Load() || count != maxN {
		t.Fatalf("ledger after the upgrade: %d rows max %d (acked %d) %v", count, maxN, w.acked.Load(), err)
	}
	t.Logf("dedicated 17 → 18: writes paused %d ms; %d commits acknowledged", *mv.FreezeMs, w.acked.Load())
}
