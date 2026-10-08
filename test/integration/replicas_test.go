package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/test/testenv"
)

func replicaList(t *testing.T, e *testenv.Env, project uuid.UUID) gen.ReplicaList {
	t.Helper()
	var l gen.ReplicaList
	if code := e.Do("GET", "/api/v1/projects/"+project.String()+"/replicas", nil, &l); code != http.StatusOK {
		t.Fatalf("GET replicas: %d", code)
	}
	return l
}

// waitReplicas polls the replica list until ok accepts it.
func waitReplicas(t *testing.T, e *testenv.Env, project uuid.UUID, within time.Duration, what string, ok func(gen.ReplicaList) bool) gen.ReplicaList {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		l := replicaList(t, e, project)
		if ok(l) {
			return l
		}
		if time.Now().After(deadline) {
			var desc []string
			for _, r := range l.Replicas {
				desc = append(desc, fmt.Sprintf("%s on %s: %s, in rotation %v, %d ms / %d bytes behind", r.Id, r.NodeName, r.Status, r.InRotation, derefInt(r.LagMs), derefInt(r.LagBytes)))
			}
			t.Fatalf("%s: %s", what, strings.Join(desc, "; "))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func replicaByID(l gen.ReplicaList, id uuid.UUID) (gen.ReadReplica, bool) {
	for _, r := range l.Replicas {
		if r.Id == id {
			return r, true
		}
	}
	return gen.ReadReplica{}, false
}

// readOnlyURL is the project's pooled URL on its <db>_ro route.
func readOnlyURL(t *testing.T, pooled, readDB string) string {
	t.Helper()
	u, err := url.Parse(pooled)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + readDB
	return u.String()
}

// spread runs n concurrent queries through pool and returns what each
// returned.
func spread(t *testing.T, pool *pgxpool.Pool, n int, query string) []string {
	t.Helper()
	var mu sync.Mutex
	var out []string
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var v string
			if err := pool.QueryRow(context.Background(), query).Scan(&v); err != nil {
				t.Errorf("read route: %v", err)
				return
			}
			mu.Lock()
			out = append(out, v)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

func distinct(vs []string) map[string]int {
	m := map[string]int{}
	for _, v := range vs {
		m[v]++
	}
	return m
}

// TestReadReplicas is M35's done-when: read traffic shifts to replicas, a
// lagging replica leaves rotation automatically (and comes back), and
// replicas follow a new primary after an HA failover. Then a replica is
// detached into a standalone project and the other deleted.
func TestReadReplicas(t *testing.T) {
	needDedicated(t)
	e := testenv.Start(t, testenv.Options{})
	c, ns := haProject(t, e) // the project on test; node-b, node-c take instances
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Dedicated.RunHAWatcher(ctx, time.Second)
	var created gen.NodeCreated
	if code := e.Do("POST", "/api/v1/nodes", gen.CreateNodeRequest{Name: "node-d", PrivateAddr: "agent-test-4", Role: gen.CreateNodeRequestRoleDedicated}, &created); code != http.StatusCreated {
		t.Fatalf("create node-d: %d", code)
	}
	e.StartFourthAgent("node-d", created.Token)
	ns = append(ns, created.Node)
	pid := c.Project.Id
	q := store.New(e.DB)
	p, err := q.GetProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}

	app := e.MustConnect(c.Connection.PooledUrl)
	if _, err := app.Exec(ctx, `CREATE TABLE items (id int PRIMARY KEY, v text); INSERT INTO items SELECT g, 'item ' || g FROM generate_series(1, 100) g`); err != nil {
		t.Fatal(err)
	}
	_ = app.Close(ctx)
	// Backend services, so the data API's replica routing is covered too.
	var en gen.BackendServicesEnabled
	if code := e.Do("POST", "/api/v1/projects/"+pid.String()+"/services", nil, &en); code != http.StatusAccepted {
		t.Fatalf("enable services: %d", code)
	}
	if op := e.WaitOperation(en.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("enable services: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	var pub string
	for _, k := range en.Keys {
		if k.Key.Kind == gen.ApiKeyKindPublishable {
			pub = k.Value
		}
	}
	ref := *en.Services.Ref

	// 1. A replica on node-c: the instance goes under Patroni first.
	addReplica := func(node gen.Node) uuid.UUID {
		t.Helper()
		var op gen.Operation
		if code := e.Do("POST", "/api/v1/projects/"+pid.String()+"/replicas", gen.ReplicaCreateRequest{NodeId: &node.Id}, &op); code != http.StatusAccepted {
			t.Fatalf("create replica on %s: %d", node.Name, code)
		}
		if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
			t.Fatalf("create replica on %s: %s %s\n%s", node.Name, op.Status, deref(op.Error), testenv.FormatLog(op))
		}
		t.Logf("replica on %s:\n%s", node.Name, testenv.FormatLog(op))
		for _, r := range replicaList(t, e, pid).Replicas {
			if r.NodeId == node.Id {
				return r.Id
			}
		}
		t.Fatalf("no replica listed on %s", node.Name)
		return uuid.Nil
	}
	r1 := addReplica(ns[2])
	l := waitReplicas(t, e, pid, 30*time.Second, "first replica in rotation", func(l gen.ReplicaList) bool {
		r, ok := replicaByID(l, r1)
		return ok && r.InRotation && r.Status == gen.ReadReplicaStatusStreaming
	})
	if l.ReadDatabase != p.DbName+"_ro" || l.ReadUrl == nil || l.MaxLagMs != 3000 {
		t.Fatalf("replica list: %+v", l)
	}
	inst, err := q.GetInstance(ctx, p.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if !inst.Patroni || inst.HaEnabled {
		t.Fatalf("instance after the first replica: patroni %v ha %v", inst.Patroni, inst.HaEnabled)
	}

	// Reads through <db>_ro land on the replica; writes there fail.
	ro := readOnlyURL(t, c.Connection.PooledUrl, l.ReadDatabase)
	roConn := e.MustConnect(ro)
	var inRecovery bool
	var n int
	if err := roConn.QueryRow(ctx, `SELECT pg_is_in_recovery(), (SELECT count(*) FROM items)`).Scan(&inRecovery, &n); err != nil || !inRecovery || n != 100 {
		t.Fatalf("read route: recovery %v, %d items, %v", inRecovery, n, err)
	}
	if _, err := roConn.Exec(ctx, `INSERT INTO items VALUES (1000, 'no')`); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("a write through the read route: %v", err)
	}
	_ = roConn.Close(ctx)

	// HA on, its standby on node-b: replicas aren't HA members.
	var op gen.Operation
	if code := e.Do("POST", "/api/v1/projects/"+pid.String()+"/ha", gen.HAEnableRequest{NodeId: &ns[1].Id}, &op); code != http.StatusAccepted {
		t.Fatalf("enable HA: %d", code)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("enable HA: %s %s\n%s", op.Status, deref(op.Error), testenv.FormatLog(op))
	}
	if st := haStatus(t, e, pid); len(st.Members) != 2 {
		t.Fatalf("HA members (replicas left out): %+v", st.Members)
	}

	// 2. A second replica on node-d; reads spread over both.
	r2 := addReplica(ns[3])
	waitReplicas(t, e, pid, 30*time.Second, "both replicas in rotation", func(l gen.ReplicaList) bool {
		a, _ := replicaByID(l, r1)
		b, _ := replicaByID(l, r2)
		return len(l.Replicas) == 2 && a.InRotation && b.InRotation
	})
	// A third is refused.
	if code := e.Do("POST", "/api/v1/projects/"+pid.String()+"/replicas", gen.ReplicaCreateRequest{}, nil); code != http.StatusConflict {
		t.Fatalf("a third replica: %d", code)
	}
	cfg, err := pgxpool.ParseConfig(ro)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	hosts := map[string]int{}
	for range 5 {
		for h, k := range distinct(spread(t, pool, 8, `SELECT inet_server_addr()::text || ' ' || pg_is_in_recovery()::text FROM pg_sleep(0.3)`)) {
			hosts[h] += k
		}
		if len(hosts) >= 2 {
			break
		}
	}
	for h := range hosts {
		if !strings.HasSuffix(h, " true") {
			t.Fatalf("a read through the read route reached the primary: %v", hosts)
		}
	}
	if len(hosts) < 2 {
		t.Fatalf("reads went to one replica only: %v", hosts)
	}
	t.Logf("reads across the replicas: %v", hosts)

	// The data API: GETs with Read-Replica: allowed go to a replica.
	ed := e.StartEdge()
	health := func(headers ...string) bool {
		t.Helper()
		var replica bool
		for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(300 * time.Millisecond) {
			code, _, body := ed.Do(ref, "GET", "/data/v1/health", nil, append([]string{"apikey", pub}, headers...)...)
			if code == http.StatusOK {
				replica = strings.Contains(body, `"replica":true`)
				if len(headers) == 0 || replica || time.Now().After(deadline) {
					return replica
				}
			} else if time.Now().After(deadline) {
				t.Fatalf("health: %d %s", code, body)
			}
		}
	}
	if !health("Read-Replica", "allowed") {
		t.Fatal("a GET with Read-Replica: allowed didn't reach a replica")
	}
	if health() {
		t.Fatal("a GET without the header reached a replica")
	}

	// 3. Replica 1 falls behind while the primary keeps writing: a query
	// there holds a lock that a schema change on the primary needs, so
	// replay waits (up to max_standby_streaming_delay, 30 s). It leaves
	// rotation, and reads go to replica 2 only.
	w := startWriter(t, e, c.Connection.PooledUrl)
	hold := exec.Command("docker", "exec", "pgdock-"+r1.String(), "psql", "-XAtq", "-h", "/var/run/postgresql", "-U", "pgdock_admin",
		"-d", p.DbName, "-c", "BEGIN; SELECT count(*) FROM items; SELECT pg_sleep(60); COMMIT")
	if err := hold.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hold.Process.Kill(); _ = hold.Wait() }()
	time.Sleep(time.Second)
	alter := e.MustConnect(c.Connection.PooledUrl)
	if _, err := alter.Exec(ctx, `ALTER TABLE items ADD COLUMN extra int`); err != nil {
		t.Fatal(err)
	}
	_ = alter.Close(ctx)
	behind := time.Now()
	waitReplicas(t, e, pid, 25*time.Second, "the lagging replica out of rotation", func(l gen.ReplicaList) bool {
		a, _ := replicaByID(l, r1)
		b, _ := replicaByID(l, r2)
		return !a.InRotation && a.Status == gen.ReadReplicaStatusLagging && b.InRotation
	})
	t.Logf("the lagging replica left rotation after %s", time.Since(behind).Round(100*time.Millisecond))
	time.Sleep(time.Second) // the poolers reload
	for v, k := range distinct(spread(t, pool, 16, `SELECT count(*)::text FROM pg_attribute WHERE attrelid = 'items'::regclass AND attname = 'extra' AND NOT attisdropped`)) {
		if v != "1" {
			t.Fatalf("%d reads reached the lagging replica", k)
		}
	}
	// The query goes; replay catches up and the replica is back.
	memberSQL(t, r1, p.DbName, `SELECT count(pg_terminate_backend(pid)) FROM pg_stat_activity WHERE query LIKE '%pg_sleep(60)%' AND pid <> pg_backend_pid()`)
	waitReplicas(t, e, pid, 40*time.Second, "the caught-up replica back in rotation", func(l gen.ReplicaList) bool {
		a, _ := replicaByID(l, r1)
		return a.InRotation && a.Status == gen.ReadReplicaStatusStreaming
	})

	// 4. The primary dies: the HA standby on node-b takes over (never a
	// replica), and both replicas follow it.
	leader, ok := leaderOf(haStatus(t, e, pid))
	if !ok {
		t.Fatal("no leader")
	}
	if out, err := exec.Command("docker", "kill", "pgdock-"+leader.Id.String()).CombinedOutput(); err != nil {
		t.Fatalf("kill the primary: %v %s", err, out)
	}
	killed := time.Now()
	before := w.acked.Load()
	for deadline := time.Now().Add(90 * time.Second); w.acked.Load() < before+20; time.Sleep(200 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("no writes after the primary died: %d acked before, %d now", before, w.acked.Load())
		}
	}
	t.Logf("writes back %s after the primary died", time.Since(killed).Round(100*time.Millisecond))
	w.Stop()
	var newLeader gen.HAMember
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(500 * time.Millisecond) {
		if newLeader, ok = leaderOf(haStatus(t, e, pid)); ok && newLeader.Id != leader.Id {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("leader after the failover: %+v", haStatus(t, e, pid).Members)
		}
	}
	if newLeader.NodeId != ns[1].Id {
		t.Fatalf("the new primary is on %s, not the HA standby's node", newLeader.NodeName)
	}
	// Each replica streams from the new primary and has its writes.
	last := strconv.FormatInt(w.acked.Load(), 10)
	for _, r := range []uuid.UUID{r1, r2} {
		for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(500 * time.Millisecond) {
			sender := memberSQL(t, r, p.DbName, `SELECT coalesce((SELECT sender_host FROM pg_stat_wal_receiver WHERE status = 'streaming'), '')`)
			have := memberSQL(t, r, p.DbName, `SELECT coalesce(max(n), 0) >= `+last+` FROM ledger`)
			if strings.Contains(sender, newLeader.Id.String()) && have == "t" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("replica %s after the failover: streaming from %q, has the writes: %s", r, sender, have)
			}
		}
	}
	waitReplicas(t, e, pid, 30*time.Second, "replicas in rotation after the failover", func(l gen.ReplicaList) bool {
		a, _ := replicaByID(l, r1)
		b, _ := replicaByID(l, r2)
		return a.InRotation && b.InRotation
	})
	t.Logf("both replicas follow the new primary %s after %s", newLeader.NodeName, time.Since(killed).Round(100*time.Millisecond))

	// 5. Replica 2 becomes a project of its own.
	var cr gen.ProjectCredentials
	if code := e.Do("POST", "/api/v1/projects/"+pid.String()+"/replicas/"+r2.String()+"/detach", gen.ReplicaDetachRequest{Name: "Analytics"}, &cr); code != http.StatusAccepted {
		t.Fatalf("detach: %d", code)
	}
	if op := e.WaitOperation(cr.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("detach: %s %s\n%s", op.Status, deref(op.Error), testenv.FormatLog(op))
	} else {
		t.Logf("detach:\n%s", testenv.FormatLog(op))
	}
	dc := e.MustConnect(cr.Connection.PooledUrl)
	if err := dc.QueryRow(ctx, `SELECT pg_is_in_recovery(), (SELECT count(*) FROM items)`).Scan(&inRecovery, &n); err != nil || inRecovery || n != 100 {
		t.Fatalf("detached project: recovery %v, %d items, %v", inRecovery, n, err)
	}
	if _, err := dc.Exec(ctx, `INSERT INTO items VALUES (1000, 'mine now')`); err != nil {
		t.Fatalf("write to the detached project: %v", err)
	}
	_ = dc.Close(ctx)
	if l := replicaList(t, e, pid); len(l.Replicas) != 1 || l.Replicas[0].Id != r1 {
		t.Fatalf("replicas after the detach: %+v", l.Replicas)
	}
	src := e.MustConnect(c.Connection.PooledUrl)
	if err := src.QueryRow(ctx, `SELECT count(*) FROM items`).Scan(&n); err != nil || n != 100 {
		t.Fatalf("the source after the detach: %d items, %v", n, err)
	}
	_ = src.Close(ctx)

	// 6. Replica 1 goes, and with it the read route.
	if code := e.Do("DELETE", "/api/v1/projects/"+pid.String()+"/replicas/"+r1.String(), nil, &op); code != http.StatusAccepted {
		t.Fatalf("delete replica: %d", code)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("delete replica: %s %s\n%s", op.Status, deref(op.Error), testenv.FormatLog(op))
	}
	if l := replicaList(t, e, pid); len(l.Replicas) != 0 {
		t.Fatalf("replicas after the delete: %+v", l.Replicas)
	}
	if out, err := exec.Command("docker", "inspect", "pgdock-"+r1.String()).CombinedOutput(); err == nil {
		t.Fatalf("the replica's container is still there: %s", out[:min(len(out), 200)])
	}
	if st := haStatus(t, e, pid); len(st.Members) != 2 {
		t.Fatalf("HA members after the replicas went: %+v", st.Members)
	}
}

func derefInt(p *int64) int64 {
	if p == nil {
		return -1
	}
	return *p
}
