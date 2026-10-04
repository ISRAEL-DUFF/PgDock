package integration

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/metrics"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/test/testenv"
)

func runSQL(t *testing.T, e *testenv.Env, id, query string, opts ...func(*gen.SqlRequest)) gen.SqlResult {
	t.Helper()
	req := gen.SqlRequest{Query: query, QueryId: uuid.New()}
	for _, o := range opts {
		o(&req)
	}
	var out gen.SqlResult
	if code := e.Do("POST", "/api/v1/projects/"+id+"/sql", req, &out); code != http.StatusOK {
		t.Fatalf("sql %q: status %d", query, code)
	}
	return out
}

func sqlErr(r gen.SqlResult) string {
	if r.Error == nil {
		return ""
	}
	return deref(r.Error.Code) + " " + r.Error.Message
}

func onlyValue(t *testing.T, r gen.SqlResult) string {
	t.Helper()
	if r.Error != nil || len(r.Results) == 0 {
		t.Fatalf("want a value, got %s %+v", sqlErr(r), r.Results)
	}
	last := r.Results[len(r.Results)-1]
	if len(last.Rows) != 1 || len(last.Rows[0]) != 1 || last.Rows[0][0] == nil {
		t.Fatalf("want one value, got %+v", last)
	}
	return *last.Rows[0][0]
}

// TestSQLConsole covers spec §8.5: SET ROLE scoping, multi-statement
// results, the row cap, notices, timeouts, cancel, the read-only toggle,
// and that only an audit entry (no SQL) is stored.
func TestSQLConsole(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	c := e.CreateProject("Console")
	other := e.CreateProject("Neighbour")
	id := c.Project.Id.String()
	seedHobby(t, e, c.Connection.PooledUrl)

	// Queries run as the project role, from the console login.
	r := runSQL(t, e, id, "SELECT current_user || ' ' || session_user")
	if got, want := onlyValue(t, r), c.Project.OwnerRole+" "+provision.ConsoleRole(c.Project.DbName); got != want {
		t.Fatalf("identity %q, want %q", got, want)
	}
	if r.Results[0].Columns[0].Type != "text" || r.ReadOnly {
		t.Fatalf("columns %+v read_only %v", r.Results[0].Columns, r.ReadOnly)
	}

	// Leaving the project role gains nothing: the console login inherits
	// no privileges, and other roles are out of reach.
	for _, q := range []string{
		"RESET ROLE; SELECT count(*) FROM notes",
		"RESET ROLE; CREATE TABLE escaped (id int)",
		"SET ROLE pgdock",
		"SET ROLE " + other.Project.OwnerRole,
		"SET SESSION AUTHORIZATION pgdock",
		"SELECT rolpassword FROM pg_authid",
		"SELECT set_config('role', 'pgdock', false); SELECT count(*) FROM pg_authid",
		"CREATE ROLE sneaky LOGIN",
		"COPY notes TO PROGRAM 'id'",
	} {
		if r := runSQL(t, e, id, q); r.Error == nil {
			t.Errorf("%q succeeded: %+v", q, r.Results)
		}
	}
	if code := e.Do("POST", "/api/v1/projects/"+id+"/sql", gen.SqlRequest{Query: "SELECT 1", QueryId: uuid.New()}, nil); code != http.StatusOK {
		t.Fatalf("console after escape attempts: %d", code)
	}

	// Several statements, each with its own result; the grid caps at 1,000.
	r = runSQL(t, e, id, "CREATE TABLE big (id int PRIMARY KEY); INSERT INTO big SELECT generate_series(1, 1500); SELECT * FROM big ORDER BY id")
	if r.Error != nil || len(r.Results) != 3 {
		t.Fatalf("multi: %s %+v", sqlErr(r), r.Results)
	}
	if r.Results[0].Command != "CREATE TABLE" || r.Results[1].RowCount != 1500 {
		t.Fatalf("commands: %q %d", r.Results[0].Command, r.Results[1].RowCount)
	}
	if sel := r.Results[2]; len(sel.Rows) != 1000 || !sel.Truncated || sel.RowCount != 1500 || *sel.Rows[999][0] != "1000" {
		t.Fatalf("cap: rows %d truncated %v count %d", len(sel.Rows), sel.Truncated, sel.RowCount)
	}
	// The new table belongs to the project role.
	if got := onlyValue(t, runSQL(t, e, id, "SELECT tableowner FROM pg_tables WHERE tablename = 'big'")); got != c.Project.OwnerRole {
		t.Fatalf("owner %q", got)
	}

	// NULLs, notices, and errors with positions; statements before an error
	// still report.
	r = runSQL(t, e, id, "SELECT NULL::int AS n, 'x' AS s")
	if r.Results[0].Rows[0][0] != nil || *r.Results[0].Rows[0][1] != "x" || r.Results[0].Columns[0].Type != "integer" {
		t.Fatalf("null row %+v", r.Results[0])
	}
	r = runSQL(t, e, id, "DO $$ BEGIN RAISE NOTICE 'hello from %', current_user; END $$")
	if len(r.Notices) != 1 || !strings.Contains(r.Notices[0], "hello from "+c.Project.OwnerRole) {
		t.Fatalf("notices %v", r.Notices)
	}
	r = runSQL(t, e, id, "SELECT 1; SELECT 1/0")
	if r.Error == nil || deref(r.Error.Code) != "22012" {
		t.Fatalf("division: %+v", r.Error)
	}
	r = runSQL(t, e, id, "SELEC 1")
	if r.Error == nil || deref(r.Error.Code) != "42601" || r.Error.Position == nil || *r.Error.Position != 1 {
		t.Fatalf("syntax: %+v", r.Error)
	}

	// The statement timeout stops long queries.
	one := 1
	r = runSQL(t, e, id, "SELECT pg_sleep(10)", func(q *gen.SqlRequest) { q.TimeoutSeconds = &one })
	if r.Error == nil || deref(r.Error.Code) != "57014" || r.DurationMs > 5000 {
		t.Fatalf("timeout: %+v after %dms", r.Error, r.DurationMs)
	}

	// Cancel finds the running query by its id.
	qid := uuid.New()
	done := make(chan gen.SqlResult, 1)
	go func() {
		var out gen.SqlResult
		e.Do("POST", "/api/v1/projects/"+id+"/sql", gen.SqlRequest{Query: "SELECT pg_sleep(30)", QueryId: qid}, &out)
		done <- out
	}()
	var cancelled gen.SqlCancelResult
	for deadline := time.Now().Add(10 * time.Second); !cancelled.Cancelled; {
		if time.Now().After(deadline) {
			t.Fatal("cancel found no running query")
		}
		time.Sleep(100 * time.Millisecond)
		e.Do("POST", "/api/v1/projects/"+id+"/sql/cancel", gen.SqlCancelRequest{QueryId: qid}, &cancelled)
	}
	select {
	case r := <-done:
		if r.Error == nil || deref(r.Error.Code) != "57014" || !strings.Contains(r.Error.Message, "user request") {
			t.Fatalf("cancelled query: %+v", r.Error)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled query did not return")
	}
	var nothing gen.SqlCancelResult
	if e.Do("POST", "/api/v1/projects/"+id+"/sql/cancel", gen.SqlCancelRequest{QueryId: uuid.New()}, &nothing); nothing.Cancelled {
		t.Fatal("cancelled an unknown query")
	}

	// A read-only submission sees data and refuses writes, even ones that
	// try to leave the transaction.
	ro := func(q *gen.SqlRequest) { t := true; q.ReadOnly = &t }
	if got := onlyValue(t, runSQL(t, e, id, "SELECT count(*) FROM notes", ro)); got != "2000" {
		t.Fatalf("read-only count %q", got)
	}
	for q, code := range map[string]string{
		"INSERT INTO notes (body) VALUES ('nope')":       "25006",
		"SET TRANSACTION READ WRITE":                     "25001",
		"SELECT nextval('notes_id_seq')":                 "25006",
		"COMMIT; INSERT INTO notes (body) VALUES ('no')": "42601",
	} {
		r := runSQL(t, e, id, q, ro)
		if r.Error == nil || deref(r.Error.Code) != code || !r.ReadOnly {
			t.Errorf("read-only %q: %+v, want %s", q, r.Error, code)
		}
	}

	// The project toggle makes every submission read-only (spec §8.5).
	var upd gen.ProjectUpdated
	if code := e.Do("PATCH", "/api/v1/projects/"+id+"/settings", map[string]any{"settings": map[string]any{"console_read_only": true}}, &upd); code != http.StatusOK {
		t.Fatalf("toggle: %d", code)
	}
	if upd.Operation != nil {
		e.WaitOperation(upd.Operation.Id)
	}
	r = runSQL(t, e, id, "DELETE FROM notes")
	if r.Error == nil || deref(r.Error.Code) != "25006" || !strings.Contains(deref(r.Error.Hint), "read-only") || !r.ReadOnly {
		t.Fatalf("toggle on: %+v", r.Error)
	}
	if got := onlyValue(t, runSQL(t, e, id, "SELECT count(*) FROM notes")); got != "2000" {
		t.Fatalf("after refused delete: %q", got)
	}

	// Audit: one entry per use, never the query text.
	var entries int
	var leaked bool
	if err := e.DB.QueryRow(ctx, `SELECT count(*), bool_or(detail::text LIKE '%notes%' OR detail::text LIKE '%pg_sleep%')
		FROM audit_log WHERE action = 'project.console' AND target_id = $1`, id).Scan(&entries, &leaked); err != nil || entries < 20 || leaked {
		t.Fatalf("audit: %d entries, query text stored: %v (%v)", entries, leaked, err)
	}

	// Bad requests.
	for _, body := range []map[string]any{
		{"query": "  ", "query_id": uuid.New()},
		{"query": "SELECT 1", "query_id": uuid.New(), "timeout_seconds": 0},
		{"query": "SELECT 1", "query_id": uuid.New(), "timeout_seconds": 301},
		{"query": "SELECT 1"},
	} {
		if code := e.Do("POST", "/api/v1/projects/"+id+"/sql", body, nil); code != http.StatusBadRequest {
			t.Errorf("bad request %v: %d", body, code)
		}
	}

	// Deleting the project removes its console login too.
	var op gen.Operation
	e.Reauth()
	if code := e.Do("DELETE", "/api/v1/projects/"+id+"?skip_final_backup=true&confirm="+url.QueryEscape(c.Project.Name), nil, &op); code != http.StatusAccepted {
		t.Fatalf("delete: %d", code)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("delete: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	var roles int
	admin := e.SharedAdmin("postgres")
	if err := admin.QueryRow(ctx, "SELECT count(*) FROM pg_roles WHERE rolname = $1", provision.ConsoleRole(c.Project.DbName)).Scan(&roles); err != nil || roles != 0 {
		t.Fatalf("console role left behind: %d %v", roles, err)
	}
}

// TestTableBrowser covers spec §8.6: the schema tree and keyset pages.
func TestTableBrowser(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	c := e.CreateProject("Browse")
	id := c.Project.Id.String()
	seedHobby(t, e, c.Connection.PooledUrl)
	app := e.MustConnect(c.Connection.PooledUrl)
	for _, stmt := range []string{
		`CREATE TABLE logs (msg text)`,
		`INSERT INTO logs SELECT 'line ' || g FROM generate_series(1, 120) g`,
		`CREATE TABLE pairs (a int, b text, v text, PRIMARY KEY (a, b))`,
		`INSERT INTO pairs SELECT g % 7, 'k' || g, 'v' || g FROM generate_series(1, 130) g`,
		`COMMENT ON TABLE notes IS 'Short notes'`,
		`ANALYZE notes`,
	} {
		if _, err := app.Exec(context.Background(), stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	var sc gen.DbSchema
	if code := e.Do("GET", "/api/v1/projects/"+id+"/schema", nil, &sc); code != http.StatusOK {
		t.Fatalf("schema: %d", code)
	}
	if len(sc.Schemas) < 2 || sc.Schemas[0].Name != "public" {
		t.Fatalf("schemas %+v", sc.Schemas)
	}
	tables := map[string]gen.DbTable{}
	for _, s := range sc.Schemas {
		for _, tb := range s.Tables {
			tables[s.Name+"."+tb.Name] = tb
		}
	}
	notes := tables["public.notes"]
	if notes.Kind != "table" || notes.RowEstimate == nil || *notes.RowEstimate != 2000 || notes.SizeBytes == 0 ||
		deref(notes.Comment) != "Short notes" || !slices.Equal(notes.PrimaryKey, []string{"id"}) {
		t.Fatalf("notes %+v", notes)
	}
	if len(notes.Columns) != 2 || notes.Columns[0].Name != "id" || notes.Columns[0].Type != "integer" || notes.Columns[0].Nullable ||
		!strings.HasPrefix(deref(notes.Columns[0].Default), "nextval(") || notes.Columns[1].Type != "text" {
		t.Fatalf("notes columns %+v", notes.Columns)
	}
	if len(notes.Indexes) != 2 || !notes.Indexes[0].Primary || notes.Indexes[1].Name != "notes_body" {
		t.Fatalf("notes indexes %+v", notes.Indexes)
	}
	if tables["public.recent"].Kind != "view" || tables["app.settings"].Kind != "table" {
		t.Fatalf("kinds: %+v %+v", tables["public.recent"], tables["app.settings"])
	}

	// pages walks a table and returns every first-column value in order.
	pages := func(schema, table string, wantOrder gen.TablePageOrder) ([]string, int) {
		t.Helper()
		var vals []string
		after, n := "", 0
		for {
			path := "/api/v1/projects/" + id + "/tables/" + url.PathEscape(schema) + "/" + url.PathEscape(table) + "/rows"
			if after != "" {
				path += "?after=" + url.QueryEscape(after)
			}
			var p gen.TablePage
			if code := e.Do("GET", path, nil, &p); code != http.StatusOK {
				t.Fatalf("rows %s.%s page %d: %d", schema, table, n, code)
			}
			if p.Order != wantOrder || len(p.Rows) > 50 {
				t.Fatalf("%s.%s: order %s, %d rows", schema, table, p.Order, len(p.Rows))
			}
			n++
			for _, row := range p.Rows {
				vals = append(vals, deref(row[0]))
			}
			if p.Next == nil {
				return vals, n
			}
			after = *p.Next
		}
	}
	ids, n := pages("public", "notes", gen.TablePageOrderPrimaryKey)
	if len(ids) != 2000 || n != 40 {
		t.Fatalf("notes: %d rows in %d pages", len(ids), n)
	}
	for i, v := range ids {
		if v != strconv.Itoa(i+1) {
			t.Fatalf("notes row %d is id %s", i, v)
		}
	}
	if got, _ := pages("public", "pairs", gen.TablePageOrderPrimaryKey); len(got) != 130 {
		t.Fatalf("pairs: %d rows", len(got))
	}
	if got, n := pages("public", "logs", gen.TablePageOrderCtid); len(got) != 120 || n != 3 || got[119] != "line 120" {
		t.Fatalf("logs: %d rows in %d pages", len(got), n)
	}
	if got, _ := pages("public", "recent", gen.TablePageOrderOffset); len(got) != 0 {
		t.Fatalf("recent view: %d rows", len(got))
	}
	if got, _ := pages("app", "settings", gen.TablePageOrderPrimaryKey); !slices.Equal(got, []string{"theme"}) {
		t.Fatalf("app.settings: %v", got)
	}

	// Rows are read with the project's own permissions, read-only.
	var p gen.TablePage
	if code := e.Do("GET", "/api/v1/projects/"+id+"/tables/public/notes/rows?limit=3", nil, &p); code != http.StatusOK || len(p.Rows) != 3 ||
		len(p.Columns) != 2 || p.Columns[1].Name != "body" || p.Columns[1].Type != "text" {
		t.Fatalf("limit 3: %d %+v", code, p)
	}
	for path, want := range map[string]int{
		"/tables/public/nope/rows":                http.StatusNotFound,
		"/tables/pg_catalog/pg_authid/rows":       http.StatusNotFound,
		"/tables/public/notes/rows?after=garbage": http.StatusBadRequest,
		"/tables/public/notes/rows?limit=1001":    http.StatusBadRequest,
	} {
		if code := e.Do("GET", "/api/v1/projects/"+id+path, nil, nil); code != want {
			t.Errorf("%s: %d, want %d", path, code, want)
		}
	}
}

// TestExtensionsAndMetrics covers the extension allow-list (spec §7.4) and
// project metrics (spec §8.7): collection, series, top queries,
// downsampling, and /metrics.
func TestExtensionsAndMetrics(t *testing.T) {
	token := "metrics-token-for-the-integration-test"
	e := testenv.Start(t, testenv.Options{MetricsToken: token})
	ctx := context.Background()
	c := e.CreateProject("Measured")
	id := c.Project.Id.String()
	seedHobby(t, e, c.Connection.PooledUrl)

	var exts gen.ExtensionList
	if code := e.Do("GET", "/api/v1/projects/"+id+"/extensions", nil, &exts); code != http.StatusOK {
		t.Fatalf("extensions: %d", code)
	}
	byName := map[string]gen.Extension{}
	for _, x := range exts.Items {
		byName[x.Name] = x
	}
	if x := byName["pgcrypto"]; !x.Allowed || !x.Available || x.InstalledVersion != nil || x.Tier != "shared" {
		t.Fatalf("pgcrypto %+v", x)
	}
	if x := byName["postgis"]; x.Allowed || x.Tier != "dedicated" {
		t.Fatalf("postgis %+v", x)
	}
	for _, name := range []string{"pgcrypto", "pg_stat_statements"} {
		exts = gen.ExtensionList{}
		if code := e.Do("POST", "/api/v1/projects/"+id+"/extensions", gen.EnableExtensionRequest{Name: name}, &exts); code != http.StatusOK {
			t.Fatalf("enable %s: %d", name, code)
		}
	}
	for _, x := range exts.Items {
		if (x.Name == "pgcrypto" || x.Name == "pg_stat_statements") && (x.InstalledVersion == nil || deref(x.Schema) != "public") {
			t.Fatalf("%s after enable: %+v", x.Name, x)
		}
	}
	for _, name := range []string{"postgis", "plpython3u", "dblink"} {
		if code := e.Do("POST", "/api/v1/projects/"+id+"/extensions", gen.EnableExtensionRequest{Name: name}, nil); code != http.StatusBadRequest {
			t.Errorf("enable %s: %d, want 400", name, code)
		}
	}
	if got := onlyValue(t, runSQL(t, e, id, "SELECT length(crypt('pw', gen_salt('bf')))")); got != "60" {
		t.Fatalf("pgcrypto from the console: %q", got)
	}
	var recorded []string
	err := e.DB.QueryRow(ctx, "SELECT extensions FROM projects WHERE id = $1", id).Scan(&recorded)
	slices.Sort(recorded)
	if err != nil || !slices.Equal(recorded, []string{"pg_stat_statements", "pgcrypto"}) {
		t.Fatalf("projects.extensions %v %v", recorded, err)
	}

	// Traffic: an app holding a pooled connection and committing.
	app := e.MustConnect(c.Connection.PooledUrl)
	if err := e.Metrics.Collect(ctx); err != nil {
		t.Fatalf("collect: %v", err)
	}
	for i := 0; i < 50; i++ {
		if _, err := app.Exec(ctx, "INSERT INTO notes (body) VALUES ('traffic')"); err != nil {
			t.Fatal(err)
		}
		if _, err := app.Exec(ctx, "SELECT count(*) FROM notes WHERE body = 'traffic'"); err != nil {
			t.Fatal(err)
		}
	}
	// Backends flush their statistics when idle, up to 10s later.
	var m gen.MetricsResponse
	last := map[string]float64{}
	for deadline := time.Now().Add(30 * time.Second); last[metrics.TPS] <= 0; {
		if time.Now().After(deadline) {
			t.Fatalf("no transactions seen: %v", last)
		}
		time.Sleep(time.Second)
		if err := e.Metrics.Collect(ctx); err != nil {
			t.Fatalf("collect: %v", err)
		}
		m = gen.MetricsResponse{}
		if code := e.Do("GET", "/api/v1/projects/"+id+"/metrics?range=1h", nil, &m); code != http.StatusOK || m.Resolution != "1m" {
			t.Fatalf("metrics: %d %+v", code, m)
		}
		for _, s := range m.Series {
			if len(s.Points) > 0 {
				last[s.Metric] = s.Points[len(s.Points)-1].Value
			}
		}
	}
	for _, name := range []string{metrics.SizeBytes, metrics.ConnectionsActive, metrics.ConnectionsIdle, metrics.PoolerClients, metrics.TPS, metrics.CacheHitRatio} {
		if _, ok := last[name]; !ok {
			t.Errorf("no %s series: %v", name, last)
		}
	}
	if last[metrics.SizeBytes] < 1<<20 || last[metrics.TPS] <= 0 || last[metrics.PoolerClients] < 1 || last[metrics.CacheHitRatio] <= 0 || last[metrics.CacheHitRatio] > 1 {
		t.Fatalf("values: %v", last)
	}
	if m.TopQueries == nil || !m.TopQueries.Available || len(m.TopQueries.Items) == 0 {
		t.Fatalf("top queries: %+v", m.TopQueries)
	}
	if !slices.ContainsFunc(m.TopQueries.Items, func(q gen.TopQuery) bool { return strings.Contains(q.Query, "INSERT INTO notes") && q.Calls >= 50 }) {
		t.Fatalf("the app's insert is not among the top queries: %+v", m.TopQueries.Items)
	}
	if slices.ContainsFunc(m.TopQueries.Items, func(q gen.TopQuery) bool {
		return strings.Contains(q.Query, "gen_salt") || strings.HasPrefix(q.Query, "SET ROLE")
	}) {
		t.Fatalf("console queries are tracked: %+v", m.TopQueries.Items)
	}
	var one gen.MetricsResponse
	if e.Do("GET", "/api/v1/projects/"+id+"/metrics?range=24h&metric=size_bytes", nil, &one); len(one.Series) != 1 || one.Series[0].Metric != "size_bytes" {
		t.Fatalf("metric filter: %+v", one.Series)
	}
	if code := e.Do("GET", "/api/v1/projects/"+id+"/metrics?range=5y", nil, nil); code != http.StatusBadRequest {
		t.Fatalf("bad range: %d", code)
	}

	// Downsampling: 1-minute points of a completed hour become an hourly
	// average served by the 7-day range; old points are pruned.
	hour := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	for i, v := range []float64{10, 20, 30} {
		if _, err := e.DB.Exec(ctx, `INSERT INTO metric_points VALUES ('project', $1, 'size_bytes', $2, '1m', $3)`,
			id, hour.Add(time.Duration(i)*time.Minute), v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.DB.Exec(ctx, `INSERT INTO metric_points VALUES ('project', $1, 'size_bytes', now() - interval '2 days', '1m', 1),
		('project', $1, 'size_bytes', now() - interval '40 days', '1h', 1)`, id); err != nil {
		t.Fatal(err)
	}
	if err := e.Metrics.Downsample(ctx, time.Now().Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var week gen.MetricsResponse
	e.Do("GET", "/api/v1/projects/"+id+"/metrics?range=7d&metric=size_bytes", nil, &week)
	if week.Resolution != "1h" || len(week.Series) != 1 {
		t.Fatalf("7d: %+v", week)
	}
	found := false
	for _, p := range week.Series[0].Points {
		if p.Ts.Equal(hour) && p.Value == 20 {
			found = true
		}
	}
	if !found || len(week.Series[0].Points) < 2 {
		t.Fatalf("hourly average missing: %+v", week.Series[0].Points)
	}
	var stale int
	if err := e.DB.QueryRow(ctx, `SELECT count(*) FROM metric_points WHERE scope_id = $1 AND
		((resolution = '1m' AND ts < now() - interval '1 day') OR ts < now() - interval '30 days')`, id).Scan(&stale); err != nil || stale != 0 {
		t.Fatalf("pruning left %d points (%v)", stale, err)
	}

	// /metrics: a token or a session, nothing else.
	if code, _ := e.GetText("/metrics", nil, false); code != http.StatusUnauthorized {
		t.Fatalf("anonymous /metrics: %d", code)
	}
	if code, _ := e.GetText("/metrics", http.Header{"Authorization": {"Bearer wrong-token-wrong-token-wrong"}}, false); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", code)
	}
	code, body := e.GetText("/metrics", http.Header{"Authorization": {"Bearer " + token}}, false)
	if code != http.StatusOK || !strings.Contains(body, "# TYPE pgdock_project_size_bytes gauge") ||
		!strings.Contains(body, `pgdock_project_tps{project_id="`+id+`",project="Measured",database="`+c.Project.DbName+`",tier="shared"} `) {
		t.Fatalf("token /metrics: %d\n%s", code, body)
	}
	if code, _ := e.GetText("/metrics", nil, true); code != http.StatusOK {
		t.Fatalf("session /metrics: %d", code)
	}
}

// TestNodeMetrics checks agent-reported node metrics and rates.
func TestNodeMetrics(t *testing.T) {
	needDedicated(t)
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	ctx := context.Background()
	ns, err := e.Nodes.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var node *gen.Node
	for _, n := range ns {
		if n.Name == "test" {
			if st := e.Nodes.Check(ctx, n); !st.Reachable {
				t.Fatalf("agent unreachable: %s", st.Err)
			}
			node = &gen.Node{Id: n.ID}
		}
	}
	if node == nil {
		t.Fatal("no test node")
	}
	if err := e.Metrics.Collect(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	for _, n := range ns {
		if n.ID == node.Id {
			e.Nodes.Check(ctx, n)
		}
	}
	if err := e.Metrics.Collect(ctx); err != nil {
		t.Fatal(err)
	}
	var m gen.MetricsResponse
	if code := e.Do("GET", "/api/v1/nodes/"+node.Id.String()+"/metrics?range=1h", nil, &m); code != http.StatusOK {
		t.Fatalf("node metrics: %d", code)
	}
	have := map[string]float64{}
	for _, s := range m.Series {
		have[s.Metric] = s.Points[len(s.Points)-1].Value
	}
	for _, name := range []string{metrics.CPUPercent, metrics.Load1, metrics.MemUsedBytes, metrics.MemTotalBytes, metrics.DiskUsedBytes, metrics.DiskTotalBytes} {
		if _, ok := have[name]; !ok {
			t.Errorf("no %s: %v", name, have)
		}
	}
	if have[metrics.CPUPercent] < 0 || have[metrics.CPUPercent] > 100 || have[metrics.MemUsedBytes] <= 0 || have[metrics.MemUsedBytes] > have[metrics.MemTotalBytes] {
		t.Fatalf("values: %v", have)
	}
}
