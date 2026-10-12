package integration

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/insights"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/test/testenv"
)

// TestQueryInsightsMissingIndex is M26's done-when (V3 §8): a seeded
// workload that filters a large table on an unindexed column shows up in
// top queries, produces a suggestion for exactly that index, and applying
// the suggested statement measurably reduces the query's mean time.
// Along the way: EXPLAIN as the project's role, the slow-query log,
// unused and duplicate indexes, bloat and a blocking chain.
func TestQueryInsightsMissingIndex(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	c := e.CreateProject("Shop")
	pid := c.Project.Id.String()
	app := e.MustConnect(c.Connection.PooledUrl)
	defer app.Close(ctx)
	for _, stmt := range []string{
		`CREATE TABLE customers (id int PRIMARY KEY, name text NOT NULL)`,
		`INSERT INTO customers SELECT g, 'customer ' || g FROM generate_series(1, 2000) g`,
		`CREATE TABLE orders (id bigserial PRIMARY KEY, customer_id int NOT NULL REFERENCES customers, total numeric(10,2) NOT NULL,
		   status text NOT NULL DEFAULT 'open', created_at timestamptz NOT NULL DEFAULT now())`,
		`INSERT INTO orders (customer_id, total) SELECT 1 + (g % 2000), (g % 997) / 10.0 FROM generate_series(1, 300000) g`,
		`CREATE INDEX orders_created_idx ON orders (created_at)`,
		`CREATE INDEX orders_created_dup_idx ON orders (created_at)`,
		`ANALYZE`,
	} {
		if _, err := app.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	p, err := store.New(e.DB).GetProject(ctx, c.Project.Id)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	e.Insights.Now = func() time.Time { return clock }
	collect := func() {
		t.Helper()
		if err := e.Insights.CollectProject(ctx, p); err != nil {
			t.Fatalf("collect: %v", err)
		}
	}
	collect() // the baseline: history before PGDock looked isn't counted

	const lookup = `SELECT count(*), sum(total) FROM orders WHERE customer_id = $1`
	workload := func(n int) {
		t.Helper()
		for i := range n {
			// Literals inlined (simple protocol), as many ORMs send them: the
			// captured example can then be explained as it ran.
			if _, err := app.Exec(ctx, lookup, pgx.QueryExecModeSimpleProtocol, 1+i*37%2000); err != nil {
				t.Fatal(err)
			}
		}
	}
	workload(40)
	collect()

	// Top queries: the lookup, with 40 calls.
	var top gen.InsightQueryList
	if code := e.Do("GET", "/api/v1/projects/"+pid+"/insights/queries?range=1h", nil, &top); code != http.StatusOK {
		t.Fatalf("top queries: %d", code)
	}
	var q gen.InsightQuery
	for _, it := range top.Items {
		if strings.Contains(it.Query, "FROM orders WHERE customer_id =") {
			q = it
		}
	}
	if q.QueryId == "" || q.Calls != 40 || q.MeanMs <= 0 || !q.HasExample {
		t.Fatalf("lookup in top queries: %+v (all: %+v)", q, top.Items)
	}
	before := q.MeanMs

	// EXPLAIN on the captured example: a sequential scan of orders.
	var plan gen.InsightPlan
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/insights/explain", gen.InsightExplainRequest{QueryId: q.QueryId}, &plan); code != http.StatusOK {
		t.Fatalf("explain: %d", code)
	}
	if plan.Generic || !slices.Contains(plan.SeqScans, "public.orders") || !strings.Contains(plan.Statement, "customer_id =") {
		t.Fatalf("plan: %+v", plan)
	}
	generic := true
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/insights/explain", gen.InsightExplainRequest{QueryId: q.QueryId, Generic: &generic}, &plan); code != http.StatusOK || !plan.Generic {
		t.Fatalf("generic explain: %d %+v", code, plan)
	}

	// The suggestion: orders (customer_id), for this query, and also
	// because the foreign key has no index.
	var rep gen.IndexReport
	if code := e.Do("GET", "/api/v1/projects/"+pid+"/insights/indexes", nil, &rep); code != http.StatusOK {
		t.Fatalf("indexes: %d", code)
	}
	var sg *gen.IndexSuggestion
	for i, s := range rep.Suggestions {
		if s.Table == "orders" && slices.Equal(s.Columns, []string{"customer_id"}) {
			sg = &rep.Suggestions[i]
		}
	}
	if sg == nil || !slices.Contains(sg.QueryIds, q.QueryId) || !slices.Contains(sg.Reasons, gen.SeqScanFilter) || !slices.Contains(sg.Reasons, gen.UnindexedForeignKey) {
		t.Fatalf("suggestion: %+v (all %+v)", sg, rep.Suggestions)
	}
	if sg.Statement != `CREATE INDEX CONCURRENTLY orders_customer_id_idx ON public.orders (customer_id);` || sg.Change.Kind != "create_index" {
		t.Fatalf("statement %q change %+v", sg.Statement, sg.Change)
	}
	if len(rep.Duplicates) != 1 || rep.Duplicates[0].Name != "orders_created_dup_idx" || !rep.Duplicates[0].Exact {
		t.Fatalf("duplicates: %+v", rep.Duplicates)
	}
	if !slices.ContainsFunc(rep.Unused, func(i gen.IndexInfo) bool { return i.Name == "orders_created_idx" }) {
		t.Fatalf("unused: %+v", rep.Unused)
	}
	// The migration renders through the table editor's endpoint (V2 §4.3).
	var mig gen.SchemaMigration
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/schema/migration", map[string]any{"change": sg.Change, "format": "goose"}, &mig); code != http.StatusOK {
		t.Fatalf("migration: %d", code)
	}

	// Apply the suggested statement; the next interval's mean drops.
	if _, err := app.Exec(ctx, sg.Statement); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := app.Exec(ctx, "ANALYZE orders"); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(10 * time.Minute)
	workload(40)
	collect()
	var det gen.InsightQueryDetail
	if code := e.Do("GET", "/api/v1/projects/"+pid+"/insights/queries/"+q.QueryId+"?range=1h", nil, &det); code != http.StatusOK {
		t.Fatalf("detail: %d", code)
	}
	if len(det.Series) != 2 || det.Query.Calls != 80 {
		t.Fatalf("series: %+v", det)
	}
	after := det.Series[1].MeanMs
	t.Logf("mean time before %.2f ms, after the index %.2f ms", before, after)
	if after > before/3 {
		t.Fatalf("mean %.2f ms after the index, %.2f ms before: not measurably faster", after, before)
	}
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/insights/explain", gen.InsightExplainRequest{QueryId: q.QueryId}, &plan); code != http.StatusOK || !slices.Contains(plan.Indexes, "orders_customer_id_idx") {
		t.Fatalf("plan after the index: %d %+v", code, plan)
	}
	if code := e.Do("GET", "/api/v1/projects/"+pid+"/insights/indexes", nil, &rep); code != http.StatusOK ||
		slices.ContainsFunc(rep.Suggestions, func(s gen.IndexSuggestion) bool { return s.Table == "orders" && s.Columns[0] == "customer_id" }) {
		t.Fatalf("suggestion still offered after the index: %+v", rep.Suggestions)
	}

	// Slow queries (the test threshold is 200 ms): one between snapshots,
	// one seen running.
	clock = clock.Add(10 * time.Minute)
	if _, err := app.Exec(ctx, "SELECT pg_sleep(0.3)"); err != nil {
		t.Fatal(err)
	}
	other := e.MustConnect(c.Connection.SessionUrl)
	defer other.Close(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = other.Exec(ctx, "SELECT pg_sleep(2), 'running'") }()
	time.Sleep(700 * time.Millisecond)
	collect()
	wg.Wait()
	var slow gen.SlowQueryList
	if code := e.Do("GET", "/api/v1/projects/"+pid+"/insights/slow?range=1h", nil, &slow); code != http.StatusOK {
		t.Fatalf("slow: %d", code)
	}
	var sources []string
	for _, s := range slow.Items {
		if strings.Contains(s.Query, "pg_sleep") {
			sources = append(sources, string(s.Source))
		}
	}
	if !slices.Contains(sources, "snapshot") || !slices.Contains(sources, "running") || slow.ThresholdMs != 200 {
		t.Fatalf("slow queries: %v %+v", sources, slow)
	}

	// Bloat: most rows deleted, no vacuum yet.
	if _, err := app.Exec(ctx, "ALTER TABLE orders SET (autovacuum_enabled = false)"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Exec(ctx, "DELETE FROM orders WHERE id % 10 <> 0"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Exec(ctx, "ANALYZE orders"); err != nil {
		t.Fatal(err)
	}
	var bloat gen.BloatList
	if code := e.Do("GET", "/api/v1/projects/"+pid+"/insights/bloat", nil, &bloat); code != http.StatusOK || len(bloat.Items) == 0 ||
		bloat.Items[0].Table != "orders" || bloat.Items[0].BloatRatio < 0.7 {
		t.Fatalf("bloat: %d %+v", code, bloat.Items)
	}

	// A blocking chain: a transaction holding a row lock, an update waiting.
	holder := e.MustConnect(c.Connection.SessionUrl)
	defer holder.Close(ctx)
	if _, err := holder.Exec(ctx, "BEGIN; UPDATE customers SET name = 'held' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	waiter := e.MustConnect(c.Connection.SessionUrl)
	defer waiter.Close(ctx)
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = waiter.Exec(ctx, "UPDATE customers SET name = 'waiting' WHERE id = 1") }()
	var locks gen.LockList
	deadline := time.Now().Add(10 * time.Second)
	for len(locks.Items) == 0 && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		if code := e.Do("GET", "/api/v1/projects/"+pid+"/insights/locks", nil, &locks); code != http.StatusOK {
			t.Fatalf("locks: %d", code)
		}
	}
	if len(locks.Items) != 1 || !strings.Contains(locks.Items[0].Blocked.Query, "'waiting'") || len(locks.Items[0].Blockers) != 1 ||
		!strings.Contains(locks.Items[0].Blockers[0].Query, "'held'") || !strings.Contains(locks.Items[0].Lock, "ShareLock") {
		t.Fatalf("locks: %+v", locks.Items)
	}
	if _, err := holder.Exec(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	wg.Wait()

	// With the production default (Pro and Team), a Free organisation's
	// shared project has no insights.
	gated := insights.New(e.DB, e.Service, e.Console, insights.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if ok, err := gated.Available(ctx, p); err != nil || ok {
		t.Fatalf("a Free shared project has insights: %v %v", ok, err)
	}
	if _, err := gated.Top(ctx, p, time.Hour, "", 10); !errors.Is(err, insights.ErrNotAvailable) {
		t.Fatalf("top queries on Free: %v", err)
	}
}

// TestQueryInsightsHypopg checks the estimate on a dedicated instance,
// whose image ships hypopg: with the extension enabled, the suggestion
// carries the planner's cost with and without the hypothetical index.
// Without it, the report says hypopg can be enabled.
func TestQueryInsightsHypopg(t *testing.T) {
	needDedicated(t)
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	e.ConfigureBackups()
	e.SetNodeRole("test", "dedicated")
	ctx := context.Background()
	tier, profile, vol := gen.ProjectTierDedicated, "small", 5
	var c gen.ProjectCredentials
	if code := e.Do("POST", "/api/v1/projects", gen.CreateProjectRequest{Name: "Ledger", Tier: &tier, Profile: &profile, VolumeGb: &vol}, &c); code != http.StatusAccepted {
		t.Fatalf("create: %d", code)
	}
	if op := e.WaitOperation(c.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("create: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	pid := c.Project.Id.String()
	app := e.MustConnect(c.Connection.PooledUrl)
	defer app.Close(ctx)
	for _, stmt := range []string{
		`CREATE TABLE entries (id bigserial PRIMARY KEY, account int NOT NULL, amount bigint NOT NULL)`,
		`INSERT INTO entries (account, amount) SELECT g % 5000, g FROM generate_series(1, 200000) g`,
		`ANALYZE entries`,
	} {
		if _, err := app.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	p, _ := store.New(e.DB).GetProject(ctx, c.Project.Id)
	if err := e.Insights.CollectProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		if _, err := app.Exec(ctx, `SELECT sum(amount) FROM entries WHERE account = $1`, pgx.QueryExecModeSimpleProtocol, i*13); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Insights.CollectProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	var rep gen.IndexReport
	if code := e.Do("GET", "/api/v1/projects/"+pid+"/insights/indexes", nil, &rep); code != http.StatusOK || rep.Hypopg != gen.Available {
		t.Fatalf("before enabling hypopg: %d %s", code, rep.Hypopg)
	}
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/extensions", gen.EnableExtensionRequest{Name: "hypopg"}, nil); code != http.StatusOK {
		t.Fatalf("enable hypopg: %d", code)
	}
	if code := e.Do("GET", "/api/v1/projects/"+pid+"/insights/indexes", nil, &rep); code != http.StatusOK || rep.Hypopg != gen.Installed {
		t.Fatalf("after enabling hypopg: %d %s", code, rep.Hypopg)
	}
	var sg *gen.IndexSuggestion
	for i, s := range rep.Suggestions {
		if s.Table == "entries" && slices.Equal(s.Columns, []string{"account"}) {
			sg = &rep.Suggestions[i]
		}
	}
	if sg == nil || sg.Estimate == nil || !sg.Estimate.UsesIndex || sg.Estimate.Improvement < 0.5 {
		t.Fatalf("suggestion with estimate: %+v", sg)
	}
	t.Logf("hypopg: cost %.0f → %.0f (%.0f%% better)", sg.Estimate.CostBefore, sg.Estimate.CostAfter, sg.Estimate.Improvement*100)
}
