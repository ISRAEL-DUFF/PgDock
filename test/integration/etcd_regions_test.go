package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/regions"
	"github.com/israel-duff/pgdock/test/testenv"
)

// TestEtcdPerRegionAndReplace is V3.1-M2's done-when, with a writer on
// the HA project throughout. A Lagos project whose Patroni state is in the
// EU cluster (as V3 left it) keeps writing when that cluster goes, moves
// onto Lagos's own cluster with one short pause and no lost commits, then
// survives a member being destroyed and replaced (no failover, no client
// errors), switches over afterwards, and a drain of an etcd node moves its
// member automatically.
func TestEtcdPerRegionAndReplace(t *testing.T) {
	needDedicated(t)
	e := testenv.Start(t, testenv.Options{})
	c, ns := haProject(t, e) // eu-central: etcd on test, node-b, node-c; the project on test
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Dedicated.RunHAWatcher(ctx, time.Second)
	go e.Dedicated.Etcd.Run(ctx, 2*time.Second)

	// A fourth node, and every node in its own rack.
	var created gen.NodeCreated
	if code := e.Do("POST", "/api/v1/nodes", gen.CreateNodeRequest{Name: "node-d", PrivateAddr: "agent-test-4", Role: gen.CreateNodeRequestRoleDedicated}, &created); code != http.StatusCreated {
		t.Fatalf("create node-d: %d", code)
	}
	e.StartFourthAgent("node-d", created.Token)
	ns = append(ns, created.Node)
	for i, n := range ns {
		d := []string{"rack-1", "rack-2", "rack-3", "rack-4"}[i]
		if code := e.Do("PATCH", "/api/v1/nodes/"+n.Id.String(), gen.UpdateNodeRequest{FailureDomain: &d}, nil); code != http.StatusOK {
			t.Fatalf("rack for %s: %d", n.Name, code)
		}
	}
	var op gen.Operation
	if code := e.Do("POST", "/api/v1/projects/"+c.Project.Id.String()+"/ha", gen.HAEnableRequest{NodeId: &ns[1].Id}, &op); code != http.StatusAccepted {
		t.Fatalf("enable HA: %d", code)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("enable HA: %s %s\n%s", op.Status, deref(op.Error), testenv.FormatLog(op))
	}

	// The platform gains a Lagos region holding these nodes and the
	// project, whose state is still in the EU cluster.
	if _, err := e.Regions.Save(ctx, regions.Input{ID: "ng-lagos", Name: "Lagos", Country: "NG", Provider: "manual"}); err != nil {
		t.Fatal(err)
	}
	ids := []uuid.UUID{ns[0].Id, ns[1].Id, ns[2].Id, ns[3].Id}
	if _, err := e.DB.Exec(ctx, `UPDATE nodes SET region = 'ng-lagos' WHERE id = ANY($1)`, ids); err != nil {
		t.Fatal(err)
	}
	if _, err := e.DB.Exec(ctx, `UPDATE projects SET region = 'ng-lagos' WHERE id = $1`, c.Project.Id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = e.DB.Exec(bg, `UPDATE nodes SET region = 'eu-central' WHERE region = 'ng-lagos'`)
		_, _ = e.DB.Exec(bg, `UPDATE projects SET region = 'eu-central' WHERE region = 'ng-lagos'`)
		_, _ = e.DB.Exec(bg, `UPDATE instances SET etcd_region = NULL`)
		_, _ = e.DB.Exec(bg, `DELETE FROM etcd_members`)
		_, _ = e.DB.Exec(bg, `DELETE FROM regions WHERE id = 'ng-lagos'`)
	})
	ha := func() gen.HAStatus { return haStatus(t, e, c.Project.Id) }
	if st := ha(); st.EtcdRegion == nil || *st.EtcdRegion != "eu-central" || st.EtcdMoveAvailable == nil || *st.EtcdMoveAvailable {
		t.Fatalf("before Lagos has a cluster: %+v %v", st.EtcdRegion, st.EtcdMoveAvailable)
	}
	w := startWriter(t, e, c.Connection.PooledUrl)
	writes := func(what string, n int64, within time.Duration) {
		t.Helper()
		before := w.acked.Load()
		for deadline := time.Now().Add(within); w.acked.Load() < before+n; time.Sleep(100 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: %d commits in %s, want %d (%d client errors)", what, w.acked.Load()-before, within, n, w.errs.Load())
			}
		}
	}
	writes("before", 20, 30*time.Second)

	// 1. The EU cluster goes (its link is cut, as it were): failsafe keeps
	// the primary writing.
	for _, n := range ns[:3] {
		_ = exec.Command("docker", "rm", "-f", "pgdock-etcd-"+n.Id.String()).Run()
		_ = exec.Command("docker", "volume", "rm", "-f", "pgdock-etcd-"+n.Id.String()).Run()
	}
	if _, err := e.DB.Exec(ctx, `DELETE FROM etcd_members WHERE region = 'eu-central'`); err != nil {
		t.Fatal(err)
	}
	writes("without the EU cluster", 20, 30*time.Second)

	// 2. Lagos's own cluster, on three Lagos nodes in three racks.
	setupEtcd(t, e, []gen.Node{ns[0], ns[2], ns[3]})
	if st := ha(); st.EtcdMoveAvailable == nil || !*st.EtcdMoveAvailable {
		t.Fatalf("move not offered: %v", st.EtcdMoveAvailable)
	}

	// 3. The project moves onto it: one restart under the poolers.
	w.paused.Store(0)
	errsBefore := w.errs.Load()
	if code := e.Do("POST", "/api/v1/projects/"+c.Project.Id.String()+"/ha/etcd-move", nil, &op); code != http.StatusAccepted {
		t.Fatalf("move to Lagos's etcd: %d", code)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("move to Lagos's etcd: %s %s\n%s", op.Status, deref(op.Error), testenv.FormatLog(op))
	}
	t.Logf("move:\n%s", testenv.FormatLog(op))
	writes("after the move", 20, 30*time.Second)
	movePause := w.paused.Load()
	t.Logf("move: longest gap between commits %d ms, %d client errors", movePause, w.errs.Load()-errsBefore)
	if movePause > 10_000 {
		t.Errorf("writes paused %d ms during the move, want under 10 s", movePause)
	}
	st := ha()
	if st.EtcdRegion == nil || *st.EtcdRegion != "ng-lagos" || len(st.Members) != 2 || (st.EtcdMoveAvailable != nil && *st.EtcdMoveAvailable) {
		t.Fatalf("after the move: %v %+v", st.EtcdRegion, st.Members)
	}
	streaming := func(what string) {
		t.Helper()
		for deadline := time.Now().Add(90 * time.Second); ; time.Sleep(time.Second) {
			ok := 0
			for _, m := range ha().Members {
				if m.Role == gen.HAMemberRoleLeader || (m.State != nil && *m.State == "streaming") {
					ok++
				}
			}
			if ok == 2 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: members %+v", what, ha().Members)
			}
		}
	}
	streaming("after the move")
	failovers := len(ha().Failovers)

	// 4. A member is destroyed, container and data; Replace onto node-b.
	victim := ns[2]
	_ = exec.Command("docker", "rm", "-f", "pgdock-etcd-"+victim.Id.String()).Run()
	_ = exec.Command("docker", "volume", "rm", "-f", "pgdock-etcd-"+victim.Id.String()).Run()
	// As an operator would: once the cluster shows the member down and
	// the other two healthy (a new etcd leader may take a moment).
	var ec gen.EtcdCluster
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(time.Second) {
		e.Do("GET", "/api/v1/admin/etcd?region=ng-lagos", nil, &ec)
		up, down := 0, false
		for _, m := range ec.Members {
			if m.NodeId == victim.Id {
				down = m.Status != gen.EtcdMemberStatusHealthy
			} else if m.Status == gen.EtcdMemberStatusHealthy {
				up++
			}
		}
		if down && up == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("etcd after destroying a member: %+v", ec.Members)
		}
	}
	w.paused.Store(0)
	errsBefore = w.errs.Load()
	var rerr gen.Error
	code, body := e.DoBytes("POST", "/api/v1/admin/etcd/members/"+victim.Id.String()+"/replace", "application/json", []byte(`{"node_id":"`+ns[1].Id.String()+`"}`))
	if code != http.StatusAccepted {
		_ = json.Unmarshal(body, &rerr)
		t.Fatalf("replace: %d %s", code, body)
	}
	if err := json.Unmarshal(body, &op); err != nil {
		t.Fatal(err)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("replace: %s %s\n%s", op.Status, deref(op.Error), testenv.FormatLog(op))
	}
	t.Logf("replace:\n%s", testenv.FormatLog(op))
	e.Do("GET", "/api/v1/admin/etcd?region=ng-lagos", nil, &ec)
	healthy := map[uuid.UUID]bool{}
	for _, m := range ec.Members {
		healthy[m.NodeId] = m.Status == gen.EtcdMemberStatusHealthy
	}
	if !ec.Ready || len(ec.Members) != 3 || !healthy[ns[0].Id] || !healthy[ns[1].Id] || !healthy[ns[3].Id] {
		t.Fatalf("after the replacement: %+v", ec.Members)
	}
	writes("after the replacement", 20, 30*time.Second)
	if n := w.errs.Load() - errsBefore; n != 0 {
		t.Errorf("%d client errors during the replacement", n)
	}
	if got := len(ha().Failovers); got != failovers {
		t.Errorf("the replacement caused a failover: %+v", ha().Failovers)
	}

	// 5. HA still works on the repaired cluster: a switchover.
	if code := e.Do("POST", "/api/v1/projects/"+c.Project.Id.String()+"/switchover", gen.SwitchoverRequest{}, &op); code != http.StatusAccepted {
		t.Fatalf("switchover: %d", code)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("switchover after the replacement: %s %s\n%s", op.Status, deref(op.Error), testenv.FormatLog(op))
	}
	writes("after the switchover", 20, 30*time.Second)

	// 6. Draining an etcd node moves its member (to node-c, free again).
	var dr gen.DrainResult
	if code := e.Do("POST", "/api/v1/nodes/"+ns[3].Id.String()+"/drain", nil, &dr); code != http.StatusOK || dr.EtcdReplacement == nil {
		t.Fatalf("drain node-d: %d %+v", code, dr)
	}
	if op := e.WaitOperation(*dr.EtcdReplacement); op.Status != gen.OperationStatusSucceeded {
		for _, n := range []gen.Node{ns[0], ns[1], ns[3]} {
			out, _ := exec.Command("docker", "exec", "pgdock-etcd-"+n.Id.String(), "/usr/local/bin/etcdctl", "--endpoints=https://127.0.0.1:2379",
				"--cacert=/etc/pgdock-etcd/ca.pem", "--cert=/etc/pgdock-etcd/member.pem", "--key=/etc/pgdock-etcd/member-key.pem", "--insecure-skip-tls-verify",
				"endpoint", "status", "--cluster", "-w", "table").CombinedOutput()
			t.Logf("%s:\n%s", n.Name, out)
		}
		t.Fatalf("replacement on drain: %s %s\n%s", op.Status, deref(op.Error), testenv.FormatLog(op))
	}
	e.Do("GET", "/api/v1/admin/etcd?region=ng-lagos", nil, &ec)
	for _, m := range ec.Members {
		if m.NodeId == ns[3].Id {
			t.Fatalf("the drained node still holds a member: %+v", ec.Members)
		}
	}
	e.Do("DELETE", "/api/v1/nodes/"+ns[3].Id.String()+"/drain", nil, nil)
	writes("at the end", 20, 30*time.Second)
	w.Stop()
	conn := e.MustConnect(c.Connection.PooledUrl)
	defer conn.Close(context.Background())
	var count, maxN int64
	if err := conn.QueryRow(context.Background(), `SELECT count(*), coalesce(max(n), 0) FROM ledger`).Scan(&count, &maxN); err != nil {
		t.Fatal(err)
	}
	if maxN < w.acked.Load() || count != maxN {
		t.Fatalf("ledger: %d rows, max %d, %d acknowledged", count, maxN, w.acked.Load())
	}
	t.Logf("%d commits, %d client errors in all", w.acked.Load(), w.errs.Load())
}
