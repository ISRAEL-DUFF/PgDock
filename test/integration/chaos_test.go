package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/agentapi"
	"github.com/israel-duff/pgdock/internal/agentsvc"
	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/floatip"
	"github.com/israel-duff/pgdock/internal/freetier"
	"github.com/israel-duff/pgdock/internal/pooler"
	"github.com/israel-duff/pgdock/internal/regions"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/internal/waker"
	"github.com/israel-duff/pgdock/test/testenv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// TestChaosPoolerSplitBrain (M27): keepalived on both of a region's pooler
// hosts claims MASTER while another region's pair is healthy. The arbiter
// sees split brain in that region only and doesn't flap the IP; a single
// alert names the region; configuration keeps reaching both hosts; when the
// holder goes stale mid-split the IP moves to the healthy host, never the
// stale one; and everything clears once keepalived agrees again.
func TestChaosPoolerSplitBrain(t *testing.T) {
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
	hosts := []struct{ name, region, sid string }{{"edge-a", "eu-central", "3001"}, {"edge-b", "eu-central", "3002"}, {"edge-ng", "ng-lagos", "3003"}}
	for _, h := range hosts {
		n, err := q.InsertNode(ctx, store.InsertNodeParams{Name: h.name, PrivateAddr: "127.0.0.1", Role: "pooler", Region: h.region})
		if err != nil {
			t.Fatal(err)
		}
		fp := "test-" + h.name
		if _, err := q.CompleteNodeRegistration(ctx, store.CompleteNodeRegistrationParams{ID: n.ID, AgentCertFp: &fp, AgentPort: 7070}); err != nil {
			t.Fatal(err)
		}
		svc := agentsvc.New(agentsvc.Config{}, log)
		if err := svc.EnablePooler(agentsvc.PoolerConfig{Dir: t.TempDir(), SessionAddr: sessionAddr, PooledAddr: pooledAddr, ServerID: h.sid}); err != nil {
			t.Fatal(err)
		}
		d.agents[h.name] = svc
	}
	if _, err := e.DB.Exec(ctx, `UPDATE nodes SET status = 'healthy' WHERE role = 'pooler'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = e.DB.Exec(bg, `DELETE FROM alerts WHERE kind LIKE 'pooler%'`)
		_, _ = e.DB.Exec(bg, `DELETE FROM nodes WHERE role = 'pooler'`)
		_, _ = e.DB.Exec(bg, `DELETE FROM pooler_generations`)
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
	if err := pm.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	euIP, ngIP := &floatip.Fake{Token: "tok", IPID: "eu"}, &floatip.Fake{Token: "tok", IPID: "ng"}
	euSrv, ngSrv := httptest.NewServer(euIP), httptest.NewServer(ngIP)
	defer euSrv.Close()
	defer ngSrv.Close()
	arb := pooler.NewArbiter(pm, &floatip.Hetzner{API: euSrv.URL, Token: "tok", IPID: "eu"}, log)
	arb.SetRegionIPs(func(region string) floatip.Provider {
		if region == "ng-lagos" {
			return &floatip.Hetzner{API: ngSrv.URL, Token: "tok", IPID: "ng"}
		}
		return nil
	})
	arb.RepushEvery = 0
	vrrp := func(name, state string) {
		rec := httptest.NewRecorder()
		d.agents[name].LocalHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/vrrp/"+state, nil))
	}
	tick := func(n int) {
		t.Helper()
		for range n {
			if err := arb.Tick(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	status := func(name string) agentapi.PoolerStatus {
		var st agentapi.PoolerStatus
		rec := httptest.NewRecorder()
		d.agents[name].Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, agentapi.PathPoolerStatus, nil))
		_ = json.Unmarshal(rec.Body.Bytes(), &st)
		return st
	}
	splitAlerts := func() map[string]string {
		t.Helper()
		if err := e.Alerts.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		rows, err := e.DB.Query(ctx, `SELECT target_id, status FROM alerts WHERE kind = 'pooler_split_brain'`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var id, st string
			_ = rows.Scan(&id, &st)
			out[id] = st
		}
		return out
	}

	// 1. Normal: a MASTER in each region is not split brain.
	vrrp("edge-a", "MASTER")
	vrrp("edge-b", "BACKUP")
	vrrp("edge-ng", "MASTER")
	tick(2)
	if euIP.Assigned() != 3001 || ngIP.Assigned() != 3003 {
		t.Fatalf("floating IPs: eu %d, ng %d", euIP.Assigned(), ngIP.Assigned())
	}
	if arb.SnapshotFor("eu-central").SplitBrain || arb.SnapshotFor("ng-lagos").SplitBrain {
		t.Fatal("split brain with one MASTER per region")
	}
	if a := splitAlerts(); len(a) != 0 {
		t.Fatalf("split-brain alert with one MASTER per region: %v", a)
	}

	// 2. The eu-central pair loses sight of each other: both MASTER. The
	// arbiter sees it in that region only and keeps the IP where it is.
	vrrp("edge-b", "MASTER")
	tick(3)
	if !arb.SnapshotFor("eu-central").SplitBrain || arb.SnapshotFor("ng-lagos").SplitBrain {
		t.Fatalf("split brain: eu %v, ng %v", arb.SnapshotFor("eu-central").SplitBrain, arb.SnapshotFor("ng-lagos").SplitBrain)
	}
	if euIP.Assigned() != 3001 || ngIP.Assigned() != 3003 {
		t.Fatalf("the IP flapped during split brain: eu %d, ng %d", euIP.Assigned(), ngIP.Assigned())
	}
	var events int
	if err := e.DB.QueryRow(ctx, `SELECT count(*) FROM pooler_events WHERE kind = 'split_brain'`).Scan(&events); err != nil || events != 1 {
		t.Fatalf("split_brain events: %d (%v), want 1", events, err)
	}
	if a := splitAlerts(); len(a) != 1 || a["edge:eu-central"] != "firing" {
		t.Fatalf("split-brain alerts: %v", a)
	}

	// 3. A configuration change during split brain reaches both hosts, so
	// whichever one gets traffic serves the same routes.
	e.CreateProject("split-brain-change")
	if err := pm.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if a, b := status("edge-a"), status("edge-b"); a.Generation != b.Generation || a.Hash != b.Hash || !a.Ready || !b.Ready {
		t.Fatalf("hosts diverged during split brain: a %+v, b %+v", a, b)
	}

	// 4. The holder misses a push mid-split and goes stale: the IP moves to
	// the healthy host after the grace period, never to a stale one.
	d.set(d.pushFail, "edge-a", true)
	arb.RepushEvery = 1 << 62
	e.CreateProject("split-brain-stale")
	if err := pm.Sync(ctx); err != nil {
		t.Fatalf("a push one host missed failed the change: %v", err)
	}
	tick(1)
	if euIP.Assigned() != 3001 {
		t.Fatal("moved before the grace period")
	}
	tick(1)
	if euIP.Assigned() != 3002 {
		t.Fatalf("the IP stayed on a stale holder: %d", euIP.Assigned())
	}
	tick(2)
	if euIP.Assigned() != 3002 {
		t.Fatalf("the IP flapped back: %d", euIP.Assigned())
	}

	// 5. Recovery: edge-a is caught up and keepalived agrees again.
	d.set(d.pushFail, "edge-a", false)
	arb.RepushEvery = 0
	vrrp("edge-a", "BACKUP")
	tick(2)
	if s := status("edge-a"); !s.Ready || s.Stale {
		t.Fatalf("edge-a not caught up: %+v", s)
	}
	if arb.SnapshotFor("eu-central").SplitBrain {
		t.Fatal("split brain didn't clear")
	}
	if euIP.Assigned() != 3002 || ngIP.Assigned() != 3003 {
		t.Fatalf("after recovery: eu %d, ng %d", euIP.Assigned(), ngIP.Assigned())
	}
	if a := splitAlerts(); a["edge:eu-central"] != "resolved" {
		t.Fatalf("split-brain alert after recovery: %v", a)
	}
}

// TestChaosWakerFailure (M27): the waker dies while Free projects are
// paused. Connections to them don't get in and don't wake anything, an
// alert fires, the dashboard's resume still works, and the sweep stops
// pausing projects into a dead waker. When the waker is back, a client
// still waiting in the pooler gets its message, and the project wakes.
func TestChaosWakerFailure(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	ctx := context.Background()

	a := e.CreateProject("Sleepy A")
	b := e.CreateProject("Sleepy B")
	ids := []string{a.Project.Id.String(), b.Project.Id.String()}
	idle := func() {
		t.Helper()
		if _, err := e.DB.Exec(ctx, `UPDATE projects SET last_active_at = now() - interval '8 days', pause_warned_at = now() - interval '2 days'
			WHERE id = ANY($1::uuid[])`, ids); err != nil {
			t.Fatal(err)
		}
	}
	paused := func(c gen.Project) bool { return lifecycle(t, e, c).Lifecycle == freetier.Paused && !busy(t, e, c) }
	active := func(c gen.Project) bool { return lifecycle(t, e, c).Lifecycle == freetier.Active && !busy(t, e, c) }
	wakerAlert := func() string {
		t.Helper()
		if err := e.Alerts.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		var st string
		_ = e.DB.QueryRow(ctx, `SELECT status FROM alerts WHERE kind = 'waker_down' ORDER BY started_at DESC LIMIT 1`).Scan(&st)
		return st
	}
	// attempt is one client try with a deadline: the error it ends with.
	attempt := func(url string, within time.Duration) error {
		cctx, cancel := context.WithTimeout(ctx, within)
		defer cancel()
		c, err := pgx.Connect(cctx, url)
		if err != nil {
			return err
		}
		defer c.Close(context.Background())
		_, err = c.Exec(cctx, "SELECT 1")
		return err
	}

	idle()
	if sw, err := e.FreeTier.Sweep(ctx); err != nil || sw.Paused != 2 {
		t.Fatalf("pause: %+v %v", sw, err)
	}
	awaitPay(t, "the pauses", func() bool { return paused(a.Project) && paused(b.Project) })
	if st := wakerAlert(); st != "" {
		t.Fatalf("waker alert while it runs: %s", st)
	}

	// 1. The waker dies: a client doesn't get in and wakes nothing; an
	// alert says so.
	e.StopWaker(t)
	if st := wakerAlert(); st != "firing" {
		t.Fatalf("waker alert: %q", st)
	}
	if err := attempt(a.Connection.PooledUrl, 5*time.Second); err == nil {
		t.Fatal("connected to a paused project with the waker down")
	}
	if !paused(a.Project) {
		t.Fatalf("project A changed with the waker down: %s", lifecycle(t, e, a.Project).Lifecycle)
	}

	// 2. Resuming from the dashboard doesn't need the waker.
	if code := e.Do("POST", "/api/v1/projects/"+ids[0]+"/resume", nil, nil); code != 202 && code != 200 {
		t.Fatalf("resume: %d", code)
	}
	awaitPay(t, "the dashboard resume", func() bool { return active(a.Project) })
	if _, seen := untilConnects(t, e, a.Connection.PooledUrl, 30*time.Second); len(seen) > 1 {
		t.Logf("before A connected: %q", seen)
	}

	// 3. The sweep doesn't pause into a dead waker.
	idle()
	sw, err := e.FreeTier.Sweep(ctx)
	if err != nil || !sw.WakerDown || sw.Paused != 0 {
		t.Fatalf("sweep with the waker down: %+v %v", sw, err)
	}
	if !active(a.Project) {
		t.Fatal("project A was paused into a dead waker")
	}

	// 4. A client tries B while the waker is down and is held by the
	// pooler; the waker comes back and that same attempt gets its message.
	done := make(chan error, 1)
	go func() { done <- attempt(b.Connection.PooledUrl, 60*time.Second) }()
	time.Sleep(3 * time.Second)
	e.StartWaker(t)
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), waker.MsgResuming) {
			t.Fatalf("the held attempt ended with %v", err)
		}
	case <-time.After(70 * time.Second):
		t.Fatal("the held attempt never finished")
	}
	if _, seen := untilConnects(t, e, b.Connection.PooledUrl, 60*time.Second); len(seen) == 0 {
		t.Log("B connected on the first retry")
	}
	if !active(b.Project) {
		awaitPay(t, "B's resume", func() bool { return active(b.Project) })
	}
	if st := wakerAlert(); st != "resolved" {
		t.Fatalf("waker alert after recovery: %q", st)
	}

	// 5. With the waker back, pausing resumes.
	if sw, err := e.FreeTier.Sweep(ctx); err != nil || sw.WakerDown || sw.Paused != 1 {
		t.Fatalf("sweep after recovery: %+v %v", sw, err)
	}
}

// patroniConfig is a member's view of the cluster's dynamic configuration.
func patroniConfig(t *testing.T, e *testenv.Env, instance uuid.UUID) map[string]any {
	t.Helper()
	for _, m := range members(t, e, instance) {
		if m.RestHost == nil || m.RestPort == nil {
			continue
		}
		res, err := http.Get(fmt.Sprintf("http://%s:%d/config", *m.RestHost, *m.RestPort))
		if err != nil {
			continue
		}
		var cfg map[string]any
		err = json.NewDecoder(res.Body).Decode(&cfg)
		_ = res.Body.Close()
		if err == nil {
			return cfg
		}
	}
	return nil
}

// TestChaosEtcdMemberLoss (M27): an HA project keeps taking writes when an
// etcd member is lost, a switchover still works, and with failsafe mode
// even losing etcd's quorum doesn't demote the primary. Once the members
// are back, the cluster is healthy again and every acknowledged commit is
// there.
func TestChaosEtcdMemberLoss(t *testing.T) {
	needDedicated(t)
	e := testenv.Start(t, testenv.Options{})
	c, ns := haProject(t, e)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Dedicated.RunHAWatcher(ctx, time.Second)

	sync := true
	var op gen.Operation
	if code := e.Do("POST", "/api/v1/projects/"+c.Project.Id.String()+"/ha", gen.HAEnableRequest{NodeId: &ns[1].Id, Synchronous: &sync}, &op); code != http.StatusAccepted {
		t.Fatalf("enable HA: %d", code)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("enable HA: %s %s\n%s", op.Status, deref(op.Error), testenv.FormatLog(op))
	}
	p, err := store.New(e.DB).GetProject(ctx, c.Project.Id)
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(time.Second) {
		if cfg := patroniConfig(t, e, p.InstanceID); cfg != nil && cfg["failsafe_mode"] == true {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("failsafe_mode is off: %v", patroniConfig(t, e, p.InstanceID))
		}
	}

	etcd := func(n gen.Node) string { return "pgdock-etcd-" + n.Id.String() }
	docker := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
			t.Fatalf("docker %v: %v %s", args, err, out)
		}
	}
	t.Cleanup(func() {
		for _, n := range ns {
			_ = exec.Command("docker", "start", etcd(n)).Run()
		}
	})
	cluster := func() (ready bool, down int) {
		var ec gen.EtcdCluster
		e.Do("GET", "/api/v1/admin/etcd", nil, &ec)
		for _, m := range ec.Members {
			if m.Status != gen.EtcdMemberStatusHealthy {
				down++
			}
		}
		return ec.Ready, down
	}
	w := startWriter(t, e, c.Connection.PooledUrl)
	keepsWriting := func(what string, n int64, within time.Duration) {
		t.Helper()
		before := w.acked.Load()
		deadline := time.Now().Add(within)
		for w.acked.Load() < before+n {
			if time.Now().After(deadline) {
				t.Fatalf("%s: %d commits in %s, want %d (%d client errors)", what, w.acked.Load()-before, within, n, w.errs.Load())
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	keepsWriting("before", 20, 30*time.Second)

	// 1. One etcd member is lost: quorum holds, writes go on.
	docker("stop", "-t", "0", etcd(ns[2]))
	if ready, down := cluster(); !ready || down != 1 {
		t.Fatalf("etcd with a member down: ready %v, %d down", ready, down)
	}
	errsBefore := w.errs.Load()
	keepsWriting("one etcd member down", 50, 20*time.Second)
	if n := w.errs.Load() - errsBefore; n != 0 {
		t.Errorf("%d client errors with one etcd member down", n)
	}

	// 2. A switchover still works.
	if code := e.Do("POST", "/api/v1/projects/"+c.Project.Id.String()+"/switchover", gen.SwitchoverRequest{}, &op); code != http.StatusAccepted {
		t.Fatalf("switchover: %d", code)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("switchover with an etcd member down: %s %s\n%s", op.Status, deref(op.Error), testenv.FormatLog(op))
	}
	if l, ok := leaderOf(haStatus(t, e, c.Project.Id)); !ok || l.NodeId != ns[1].Id {
		t.Fatalf("leader after the switchover: %+v", haStatus(t, e, c.Project.Id).Members)
	}
	keepsWriting("after the switchover", 20, 30*time.Second)

	// 3. A second member is lost: no quorum. Failsafe mode keeps the
	// primary, which still reaches its standby, serving past the leader
	// key's TTL (20 s).
	docker("stop", "-t", "0", etcd(ns[0]))
	if ready, _ := cluster(); ready {
		t.Fatal("etcd reports ready without a quorum")
	}
	lost := time.Now()
	for time.Since(lost) < 45*time.Second {
		keepsWriting(fmt.Sprintf("%s without etcd quorum", time.Since(lost).Round(time.Second)), 10, 15*time.Second)
		time.Sleep(5 * time.Second)
	}

	// 4. The members come back: the cluster is healthy again, and the
	// project still has its leader and standby.
	docker("start", etcd(ns[0]))
	docker("start", etcd(ns[2]))
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(time.Second) {
		if ready, down := cluster(); ready && down == 0 {
			break
		}
		if time.Now().After(deadline) {
			ready, down := cluster()
			t.Fatalf("etcd after the members came back: ready %v, %d down", ready, down)
		}
	}
	keepsWriting("after etcd recovered", 20, 30*time.Second)
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(time.Second) {
		ok := 0
		for _, m := range haStatus(t, e, c.Project.Id).Members {
			if m.Role == gen.HAMemberRoleLeader || (m.Role == gen.HAMemberRoleSyncStandby && m.State != nil && *m.State == "streaming") {
				ok++
			}
		}
		if ok == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("members after etcd recovered: %+v", haStatus(t, e, c.Project.Id).Members)
		}
	}
	w.Stop()
	t.Logf("%d commits, %d client errors, longest gap between commits %d ms", w.acked.Load(), w.errs.Load(), w.paused.Load())
	conn := e.MustConnect(c.Connection.PooledUrl)
	defer conn.Close(ctx)
	var count, maxN int64
	if err := conn.QueryRow(ctx, `SELECT count(*), coalesce(max(n), 0) FROM ledger`).Scan(&count, &maxN); err != nil {
		t.Fatal(err)
	}
	if maxN < w.acked.Load() || count != maxN {
		t.Fatalf("ledger: %d rows, max %d, %d acknowledged", count, maxN, w.acked.Load())
	}
}
