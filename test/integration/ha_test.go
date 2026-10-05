package integration

import (
	"context"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/dedicated"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/test/testenv"
)

// haProject is a dedicated project on node "test", with the etcd cluster
// on test, node-b and node-c, all three taking dedicated instances.
func haProject(t *testing.T, e *testenv.Env) (gen.ProjectCredentials, []gen.Node) {
	t.Helper()
	e.StartAgent()
	e.ConfigureBackups()
	e.SetNodeRole("test", "dedicated")
	ns := threeNodes(t, e, gen.CreateNodeRequestRoleDedicated)
	setupEtcd(t, e, ns)
	tier, profile, vol := gen.ProjectTierDedicated, "small", 5
	var c gen.ProjectCredentials
	nodeID := ns[0].Id
	if code := e.Do("POST", "/api/v1/projects", gen.CreateProjectRequest{Name: "Always on", Tier: &tier, Profile: &profile, VolumeGb: &vol, NodeId: &nodeID}, &c); code != http.StatusAccepted {
		t.Fatalf("create: %d", code)
	}
	if op := e.WaitOperation(c.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("create: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	return c, ns
}

// memberSQL runs a query as the instance's admin inside a member's
// container (local trust), so a standby can be read directly.
func memberSQL(t *testing.T, member uuid.UUID, db, query string) string {
	t.Helper()
	out, err := exec.Command("docker", "exec", "pgdock-"+member.String(), "psql", "-XAtq", "-h", "/var/run/postgresql",
		"-U", "pgdock_admin", "-d", db, "-c", query).CombinedOutput()
	if err != nil {
		t.Fatalf("psql on %s: %v: %s", member, err, out)
	}
	return strings.TrimSpace(string(out))
}

func members(t *testing.T, e *testenv.Env, instance uuid.UUID) []store.ListInstanceMembersRow {
	t.Helper()
	ms, err := store.New(e.DB).ListInstanceMembers(context.Background(), instance)
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

func TestEnableAndDisableHA(t *testing.T) {
	needDedicated(t)
	e := testenv.Start(t, testenv.Options{})
	c, ns := haProject(t, e)
	ctx := context.Background()
	p, err := store.New(e.DB).GetProject(ctx, c.Project.Id)
	if err != nil {
		t.Fatal(err)
	}

	w := startWriter(t, e, c.Connection.PooledUrl)
	time.Sleep(time.Second)
	standbyNode := ns[1].Id
	op, err := e.Dedicated.EnableHA(ctx, dedicated.HAParams{ProjectID: p.ID, NodeID: &standbyNode})
	if err != nil {
		t.Fatal(err)
	}
	g := e.WaitOperation(op.ID)
	t.Logf("enable HA:\n%s", testenv.FormatLog(g))
	if g.Status != gen.OperationStatusSucceeded {
		t.Fatalf("enable HA: %s %s", g.Status, deref(g.Error))
	}
	time.Sleep(time.Second)
	w.Stop()
	if w.paused.Load() >= 10_000 {
		t.Errorf("clients waited %d ms during the switch to Patroni", w.paused.Load())
	}
	t.Logf("longest gap between commits while HA was turned on: %d ms; %d commits, %d client errors", w.paused.Load(), w.acked.Load(), w.errs.Load())

	inst, err := store.New(e.DB).GetInstance(ctx, p.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if !inst.HaEnabled || !inst.Patroni {
		t.Fatalf("instance: ha %v patroni %v", inst.HaEnabled, inst.Patroni)
	}
	ms := members(t, e, inst.ID)
	if len(ms) != 2 {
		t.Fatalf("members: %+v", ms)
	}
	var standby uuid.UUID
	for _, m := range ms {
		switch {
		case m.ID == inst.ID && m.Role == "leader" && m.NodeID == ns[0].Id:
		case m.ID != inst.ID && m.Role == "replica" && m.NodeID == ns[1].Id:
			standby = m.ID
		default:
			t.Fatalf("member: %s on %s is %s", m.ID, m.NodeName, m.Role)
		}
	}
	// Every acknowledged commit is on the standby.
	deadline := time.Now().Add(30 * time.Second)
	for {
		n, _ := strconv.ParseInt(memberSQL(t, standby, p.DbName, `SELECT coalesce(max(n), 0) FROM ledger`), 10, 64)
		if n >= w.acked.Load() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("standby has ledger up to %d, %d acknowledged", n, w.acked.Load())
		}
		time.Sleep(500 * time.Millisecond)
	}
	if got := memberSQL(t, standby, p.DbName, `SELECT pg_is_in_recovery()`); got != "t" {
		t.Fatalf("standby in recovery: %s", got)
	}

	// Off again: the standby goes, the primary keeps serving.
	op, err = e.Dedicated.DisableHA(ctx, p.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if g := e.WaitOperation(op.ID); g.Status != gen.OperationStatusSucceeded {
		t.Fatalf("disable HA: %s %s\n%s", g.Status, deref(g.Error), testenv.FormatLog(g))
	}
	if ms := members(t, e, inst.ID); len(ms) != 1 || ms[0].ID != inst.ID {
		t.Fatalf("members after disabling: %+v", ms)
	}
	if out, err := exec.Command("docker", "inspect", "pgdock-"+standby.String()).CombinedOutput(); err == nil {
		t.Fatalf("the standby's container is still there: %s", out[:min(len(out), 200)])
	}
	conn := e.MustConnect(c.Connection.PooledUrl)
	defer conn.Close(ctx)
	var count int64
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM ledger`).Scan(&count); err != nil || count < w.acked.Load() {
		t.Fatalf("ledger after disabling HA: %d rows (%d acknowledged) %v", count, w.acked.Load(), err)
	}
}
