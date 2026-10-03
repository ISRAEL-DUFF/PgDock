package load

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/test/testenv"
)

// dropTenants drops the databases and roles PGDock created on the cluster
// at adminURL.
func dropTenants(adminURL string) {
	ctx := context.Background()
	c, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return
	}
	defer c.Close(ctx)
	rows, _ := c.Query(ctx, `SELECT datname FROM pg_database WHERE datname LIKE 'p\_%'`)
	dbs, _ := pgx.CollectRows(rows, pgx.RowTo[string])
	for _, d := range dbs {
		_, _ = c.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{d}.Sanitize()+" WITH (FORCE)")
	}
	rows, _ = c.Query(ctx, `SELECT rolname FROM pg_roles WHERE rolname LIKE 'p\_%' ORDER BY rolname DESC`)
	roles, _ := pgx.CollectRows(rows, pgx.RowTo[string])
	for _, r := range roles {
		_, _ = c.Exec(ctx, "DROP ROLE IF EXISTS "+pgx.Identifier{r}.Sanitize())
	}
}

// TestLoadV2 is V2 §14 M16's load check: 30 orgs and 300 projects on two
// shared nodes; 20 projects with webhooks at 50 events/s each; 100 jobs a
// minute; 50 branches created at once; a quiet project's latency
// throughout. It writes tmp/load-report-v2.md.
func TestLoadV2(t *testing.T) {
	if os.Getenv("PGDOCK_TEST_LOAD") == "" {
		t.Skip("set PGDOCK_TEST_LOAD=1 (make test-load)")
	}
	load2 := os.Getenv("PGDOCK_TEST_LOAD2_ADMIN_URL")
	if load2 == "" {
		t.Skip("PGDOCK_TEST_LOAD2_ADMIN_URL not set (make test-load)")
	}
	orgsN := envInt("PGDOCK_LOAD_ORGS", 30)
	total := envInt("PGDOCK_LOAD_V2_PROJECTS", 300)
	hooksN := envInt("PGDOCK_LOAD_WEBHOOK_PROJECTS", 20)
	evRate := envInt("PGDOCK_LOAD_EVENTS_PER_SEC", 50) // per webhook project
	evSecs := envInt("PGDOCK_LOAD_EVENT_SECONDS", 30)
	jobsN := envInt("PGDOCK_LOAD_JOBS", 100)
	jobMins := envInt("PGDOCK_LOAD_JOB_MINUTES", 3)
	branchesN := envInt("PGDOCK_LOAD_BRANCHES", 50)
	parallel := envInt("PGDOCK_LOAD_PARALLEL", 8)
	if hooksN+jobsN+1 > total {
		t.Fatalf("%d projects can't hold %d webhook and %d job projects", total, hooksN, jobsN)
	}

	dropTenants(load2)
	t.Cleanup(func() { dropTenants(load2) })
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	ctx := context.Background()
	// The second node: its own agent and shared cluster.
	var node gen.NodeCreated
	if code := e.Do("POST", "/api/v1/nodes", gen.CreateNodeRequest{Name: "node-b", PrivateAddr: "agent-test-2", Role: gen.CreateNodeRequestRoleShared}, &node); code != http.StatusCreated {
		t.Fatalf("create node: %d", code)
	}
	e.StartSecondAgent("node-b", node.Token)
	if err := provision.RegisterSharedCluster(ctx, e.DB, e.Keyring, provision.SharedCluster{
		NodeName: "node-b", AdminURL: load2, PoolerHost: os.Getenv("PGDOCK_TEST_LOAD2_POOLER_HOST"), PoolerPort: 5432,
	}, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	mainAdmin := e.SharedAdmin("postgres")
	defer mainAdmin.Close(ctx)
	t.Cleanup(func() {
		// The main cluster's tenant databases from this run.
		rows, _ := e.DB.Query(context.Background(), `SELECT db_name FROM projects`)
		names, _ := pgx.CollectRows(rows, pgx.RowTo[string])
		c := e.SharedAdmin("postgres")
		defer c.Close(context.Background())
		for _, n := range names {
			_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+pgx.Identifier{n}.Sanitize()+" WITH (FORCE)")
		}
	})
	secondAdmin, err := pgx.Connect(ctx, load2)
	if err != nil {
		t.Fatal(err)
	}
	defer secondAdmin.Close(ctx)

	report := &strings.Builder{}
	fmt.Fprintf(report, "# Load check (V2)\n\n%s: %d orgs, %d projects on two shared nodes; %d webhook projects at %d events/s each for %ds; %d jobs a minute for %d minutes; %d branches at once.\n\n",
		time.Now().UTC().Format(time.RFC3339), orgsN, total, hooksN, evRate, evSecs, jobsN, jobMins, branchesN)

	// Orgs, with quotas out of the way: this measures the platform, not
	// the plans.
	orgs := make([]uuid.UUID, orgsN)
	for i := range orgs {
		orgs[i] = e.CreateOrg(fmt.Sprintf("Load org %02d", i))
		if code := e.Do("PATCH", "/api/v1/admin/orgs/"+orgs[i].String(), map[string]any{"limit_overrides": map[string]int64{
			"projects": 1000, "branches": 1000, "webhook_deliveries_per_min": 1000000, "scheduled_jobs": 1000,
			"operations_in_flight": 1000, "shared_storage_mb": 1000000, "project_storage_mb": 100000, "job_min_interval_s": 60,
		}}, nil); code != http.StatusOK {
			t.Fatalf("overrides: %d", code)
		}
		if code := e.Do("PUT", "/api/v1/admin/orgs/"+orgs[i].String()+"/outbound", gen.OutboundAllowlist{Hosts: []string{"127.0.0.1"}}, nil); code != http.StatusOK {
			t.Fatalf("allow-list: %d", code)
		}
	}

	// 1. The projects, round-robin over the orgs, `parallel` at a time.
	creds := make([]gen.ProjectCredentials, total)
	lat := make([]time.Duration, total)
	var wg sync.WaitGroup
	sem := make(chan struct{}, parallel)
	errs := make(chan error, total)
	start := time.Now()
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			t0 := time.Now()
			var c gen.ProjectCredentials
			if code := e.Do("POST", "/api/v1/projects", map[string]any{"name": fmt.Sprintf("load %03d", i), "org_id": orgs[i%orgsN]}, &c); code != http.StatusAccepted {
				errs <- fmt.Errorf("create %d: status %d", i, code)
				return
			}
			if op := e.WaitOperation(c.Operation.Id); op.Status != gen.OperationStatusSucceeded {
				errs <- fmt.Errorf("create %d: %s %s", i, op.Status, testenv.FormatLog(op))
				return
			}
			creds[i], lat[i] = c, time.Since(t0)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	createdIn := time.Since(start)
	rows, err := e.DB.Query(ctx, `SELECT n.name, count(*) FROM projects p JOIN instances i ON i.id = p.instance_id JOIN nodes n ON n.id = i.node_id
		WHERE p.deleted_at IS NULL GROUP BY n.name ORDER BY n.name`)
	if err != nil {
		t.Fatal(err)
	}
	spread := map[string]int{}
	for rows.Next() {
		var n string
		var c int
		if err := rows.Scan(&n, &c); err != nil {
			t.Fatal(err)
		}
		spread[n] = c
	}
	rows.Close()
	fmt.Fprintf(report, "## Projects\n\n| | |\n| --- | --- |\n| created | %d in %s (%d at a time) |\n| p50 / p95 / max | %s / %s / %s |\n| per node | %v |\n\n",
		total, createdIn.Round(time.Second), parallel, ms(pct(lat, .5)), ms(pct(lat, .95)), ms(pct(lat, 1)), spread)

	// A quiet project probes throughout.
	quiet := creds[total-1]
	qc := e.MustConnect(quiet.Connection.SessionUrl)
	if _, err := qc.Exec(ctx, `CREATE TABLE probe AS SELECT g AS id FROM generate_series(1, 1000) g; CREATE INDEX ON probe (id); ANALYZE probe`); err != nil {
		t.Fatal(err)
	}
	qc.Close(ctx)
	quietConn := e.MustConnect(quiet.Connection.PooledUrl)
	defer quietConn.Close(ctx)
	idle, err := probe(ctx, quietConn, 500)
	if err != nil {
		t.Fatal(err)
	}

	// Both clusters' client backends, sampled throughout.
	var maxMain, maxSecond atomic.Int64
	watchStop := make(chan struct{})
	go func() {
		for {
			select {
			case <-watchStop:
				return
			case <-time.After(time.Second):
			}
			for _, x := range []struct {
				c   *pgx.Conn
				max *atomic.Int64
			}{{mainAdmin, &maxMain}, {secondAdmin, &maxSecond}} {
				var n int64
				if x.c.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'client backend'`).Scan(&n) == nil && n > x.max.Load() {
					x.max.Store(n)
				}
			}
		}
	}()

	// 2. Webhooks: a receiver that measures commit-to-delivery latency and
	// checks each webhook's order.
	type delivery struct {
		n   int
		lag time.Duration
	}
	var rmu sync.Mutex
	got := map[string][]delivery{}
	var received atomic.Int64
	rc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev struct {
			Record struct {
				N  int       `json:"n"`
				At time.Time `json:"at"`
			} `json:"record"`
		}
		body, _ := io.ReadAll(r.Body)
		if json.Unmarshal(body, &ev) == nil {
			rmu.Lock()
			got[r.URL.Path] = append(got[r.URL.Path], delivery{ev.Record.N, time.Since(ev.Record.At)})
			rmu.Unlock()
			received.Add(1)
		}
	}))
	defer rc.Close()
	for i := 0; i < hooksN; i++ {
		c := e.MustConnect(creds[i].Connection.SessionUrl)
		if _, err := c.Exec(ctx, `CREATE TABLE ev (n int PRIMARY KEY, at timestamptz NOT NULL DEFAULT clock_timestamp())`); err != nil {
			t.Fatal(err)
		}
		c.Close(ctx)
		var wc gen.WebhookCreated
		if code := e.Do("POST", "/api/v1/projects/"+creds[i].Project.Id.String()+"/webhooks", gen.WebhookRequest{
			Name: "ev", Tables: []string{"ev"}, Url: fmt.Sprintf("%s/p%d", rc.URL, i), Events: []gen.WebhookRequestEvents{gen.WebhookRequestEventsINSERT},
		}, &wc); code != http.StatusCreated {
			t.Fatalf("webhook %d: %d", i, code)
		}
	}

	// 3. Jobs: one a minute on each of jobsN projects.
	jobIDs := make([]uuid.UUID, 0, jobsN)
	sqlText := "INSERT INTO tick DEFAULT VALUES"
	for i := hooksN; i < hooksN+jobsN; i++ {
		c := e.MustConnect(creds[i].Connection.SessionUrl)
		if _, err := c.Exec(ctx, `CREATE TABLE tick (at timestamptz NOT NULL DEFAULT now())`); err != nil {
			t.Fatal(err)
		}
		c.Close(ctx)
		var cj gen.JobCreated
		if code := e.Do("POST", "/api/v1/projects/"+creds[i].Project.Id.String()+"/jobs", gen.JobRequest{Name: "tick", Cron: "* * * * *", Kind: gen.JobRequestKindSql, Sql: &sqlText}, &cj); code != http.StatusCreated {
			t.Fatalf("job %d: %d", i, code)
		}
		jobIDs = append(jobIDs, cj.Job.Id)
	}
	// Count whole minutes from the next one.
	jobsFrom := time.Now().Truncate(time.Minute).Add(time.Minute)
	jobsUntil := jobsFrom.Add(time.Duration(jobMins) * time.Minute)

	// Phase A: the event writers, the jobs and the probe at once.
	stop := make(chan struct{})
	var during []time.Duration
	var probeErr error
	var pwg sync.WaitGroup
	pwg.Add(1)
	go func() { defer pwg.Done(); during, probeErr = probeFor(ctx, quietConn, stop) }()
	var writeErr atomic.Value
	var wwg sync.WaitGroup
	for i := 0; i < hooksN; i++ {
		wwg.Add(1)
		go func(i int) {
			defer wwg.Done()
			c, err := e.Connect(creds[i].Connection.PooledUrl)
			if err != nil {
				writeErr.Store(err)
				return
			}
			defer c.Close(ctx)
			tick := time.NewTicker(time.Second / time.Duration(evRate))
			defer tick.Stop()
			for n := 1; n <= evRate*evSecs; n++ {
				<-tick.C
				if _, err := c.Exec(ctx, `INSERT INTO ev (n) VALUES ($1)`, n); err != nil {
					writeErr.Store(fmt.Errorf("project %d event %d: %w", i, n, err))
					return
				}
			}
		}(i)
	}
	wwg.Wait()
	if v := writeErr.Load(); v != nil {
		t.Fatalf("event writer: %v", v)
	}
	want := int64(hooksN * evRate * evSecs)
	deadline := time.Now().Add(3 * time.Minute)
	for received.Load() < want && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	if wait := time.Until(jobsUntil.Add(15 * time.Second)); wait > 0 {
		time.Sleep(wait)
	}
	close(stop)
	pwg.Wait()
	if probeErr != nil {
		t.Fatalf("neighbour probe: %v", probeErr)
	}

	var lags []time.Duration
	outOfOrder := 0
	rmu.Lock()
	for _, ds := range got {
		for k, d := range ds {
			lags = append(lags, d.lag)
			if k > 0 && d.n != ds[k-1].n+1 {
				outOfOrder++
			}
		}
	}
	rmu.Unlock()
	var failed int
	if err := e.DB.QueryRow(ctx, `SELECT count(*) FROM webhook_deliveries WHERE NOT succeeded`).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(report, "## Webhooks\n\n| | |\n| --- | --- |\n| events | %d of %d delivered (%d out of order, %d failed attempts) |\n| commit → delivery p50 / p95 / p99 / max | %s / %s / %s / %s |\n\n",
		received.Load(), want, outOfOrder, failed, ms(pct(lags, .5)), ms(pct(lags, .95)), ms(pct(lags, .99)), ms(pct(lags, 1)))

	var runs, okRuns int
	var delays []time.Duration
	jrows, err := e.DB.Query(ctx, `SELECT status, extract(epoch FROM started_at - scheduled_for)::float8 FROM job_runs
		WHERE job_id = ANY($1) AND scheduled_for >= $2 AND scheduled_for < $3 AND started_at IS NOT NULL`, jobIDs, jobsFrom, jobsUntil)
	if err != nil {
		t.Fatal(err)
	}
	for jrows.Next() {
		var st string
		var secs float64
		if err := jrows.Scan(&st, &secs); err != nil {
			t.Fatal(err)
		}
		d := time.Duration(secs * float64(time.Second))
		runs++
		if st == "succeeded" {
			okRuns++
		}
		delays = append(delays, d)
	}
	jrows.Close()
	wantRuns := jobsN * jobMins
	fmt.Fprintf(report, "## Scheduled jobs\n\n| | |\n| --- | --- |\n| runs | %d of %d due (%d succeeded) |\n| start delay p50 / p95 / max | %s / %s / %s |\n\n",
		runs, wantRuns, okRuns, ms(pct(delays, .5)), ms(pct(delays, .95)), ms(pct(delays, 1)))

	// Phase B: branches at once, with the probe running.
	parents := creds[hooksN+jobsN : total-1]
	if len(parents) < branchesN {
		t.Fatalf("only %d projects to branch", len(parents))
	}
	parents = parents[:branchesN]
	for _, p := range parents {
		c := e.MustConnect(p.Connection.SessionUrl)
		if _, err := c.Exec(ctx, `CREATE TABLE items AS SELECT g AS id, md5(g::text) AS v FROM generate_series(1, 2000) g`); err != nil {
			t.Fatal(err)
		}
		c.Close(ctx)
	}
	stop = make(chan struct{})
	var duringBranches []time.Duration
	pwg.Add(1)
	go func() { defer pwg.Done(); duringBranches, probeErr = probeFor(ctx, quietConn, stop) }()
	blat := make([]time.Duration, len(parents))
	berrs := make(chan error, len(parents))
	bstart := time.Now()
	live := gen.BranchRequestSourceLive
	ttl := 0
	for i, p := range parents {
		wg.Add(1)
		go func(i int, p gen.ProjectCredentials) {
			defer wg.Done()
			t0 := time.Now()
			var bc gen.ProjectCredentials
			if code := e.Do("POST", "/api/v1/projects/"+p.Project.Id.String()+"/branches", gen.BranchRequest{Name: fmt.Sprintf("load-%02d", i), Source: &live, TtlHours: &ttl}, &bc); code != http.StatusAccepted {
				berrs <- fmt.Errorf("branch %d: %d", i, code)
				return
			}
			if op := e.WaitOperation(bc.Operation.Id); op.Status != gen.OperationStatusSucceeded {
				berrs <- fmt.Errorf("branch %d: %s %s", i, op.Status, testenv.FormatLog(op))
				return
			}
			blat[i] = time.Since(t0)
		}(i, p)
	}
	wg.Wait()
	branchesIn := time.Since(bstart)
	close(stop)
	pwg.Wait()
	close(watchStop)
	close(berrs)
	for err := range berrs {
		t.Error(err)
	}
	if probeErr != nil {
		t.Fatalf("neighbour probe: %v", probeErr)
	}
	fmt.Fprintf(report, "## Branches\n\n| | |\n| --- | --- |\n| created | %d at once in %s |\n| p50 / p95 / max | %s / %s / %s |\n\n",
		len(parents), branchesIn.Round(time.Second), ms(pct(blat, .5)), ms(pct(blat, .95)), ms(pct(blat, 1)))

	var maxConnMain, maxConnSecond int64
	_ = mainAdmin.QueryRow(ctx, `SELECT current_setting('max_connections')::int`).Scan(&maxConnMain)
	_ = secondAdmin.QueryRow(ctx, `SELECT current_setting('max_connections')::int`).Scan(&maxConnSecond)
	fmt.Fprintf(report, "## Quiet neighbour (transaction pooler) and backends\n\n| | p50 | p95 | p99 |\n| --- | --- | --- | --- |\n")
	fmt.Fprintf(report, "| idle | %s | %s | %s |\n| webhooks and jobs | %s | %s | %s |\n| branches | %s | %s | %s |\n\n",
		ms(pct(idle, .5)), ms(pct(idle, .95)), ms(pct(idle, .99)),
		ms(pct(during, .5)), ms(pct(during, .95)), ms(pct(during, .99)),
		ms(pct(duringBranches, .5)), ms(pct(duringBranches, .95)), ms(pct(duringBranches, .99)))
	fmt.Fprintf(report, "Peak client backends: %d of %d on the first node, %d of %d on the second.\n",
		maxMain.Load(), maxConnMain, maxSecond.Load(), maxConnSecond)

	if err := os.MkdirAll(filepath.Join("..", "..", "tmp"), 0o755); err == nil {
		_ = os.WriteFile(filepath.Join("..", "..", "tmp", "load-report-v2.md"), []byte(report.String()), 0o644)
	}
	t.Log("\n" + report.String())

	// Budgets.
	if p95 := pct(lat, .95); p95 > 30*time.Second {
		t.Errorf("create p95 %s over 30s", p95)
	}
	for _, n := range []string{"test", "node-b"} {
		if spread[n] < total/5 {
			t.Errorf("node %s holds %d of %d projects", n, spread[n], total)
		}
	}
	if received.Load() != want || outOfOrder != 0 {
		t.Errorf("webhooks: %d of %d delivered, %d out of order", received.Load(), want, outOfOrder)
	}
	if p95 := pct(lags, .95); p95 > 5*time.Second {
		t.Errorf("webhook delivery p95 %s over 5s", p95)
	}
	if runs < wantRuns*95/100 || okRuns != runs {
		t.Errorf("jobs: %d runs of %d due, %d succeeded", runs, wantRuns, okRuns)
	}
	if p95 := pct(delays, .95); p95 > 10*time.Second {
		t.Errorf("job start delay p95 %s over 10s", p95)
	}
	if p95 := pct(blat, .95); p95 > 3*time.Minute {
		t.Errorf("branch p95 %s over 3 minutes", p95)
	}
	for name, d := range map[string][]time.Duration{"webhooks and jobs": during, "branches": duringBranches} {
		if p95 := pct(d, .95); p95 > 250*time.Millisecond {
			t.Errorf("neighbour p95 %s during %s, over 250ms", p95, name)
		}
	}
	if maxMain.Load() >= maxConnMain || maxSecond.Load() >= maxConnSecond {
		t.Errorf("backends reached max_connections")
	}
}
