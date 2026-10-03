package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/test/testenv"
)

func tablePath(id uuid.UUID, table string) string {
	return "/api/v1/projects/" + id.String() + "/tables/public/" + table
}

func rowsWith(t *testing.T, e *testenv.Env, id uuid.UUID, table string, query url.Values) gen.TablePage {
	t.Helper()
	var p gen.TablePage
	path := tablePath(id, table) + "/rows"
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	if code := e.Do("GET", path, nil, &p); code != http.StatusOK {
		t.Fatalf("rows %s: %d", path, code)
	}
	return p
}

func filterQ(fs ...map[string]any) url.Values {
	q := url.Values{}
	for _, f := range fs {
		b, _ := json.Marshal(f)
		q.Add("filter", string(b))
	}
	return q
}

// TestRowEditConflict is M11's first done-when (V2 §4.2): two sessions
// editing the same row produce a conflict, never a silent overwrite.
func TestRowEditConflict(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	c := e.CreateProject("Editor")
	id := c.Project.Id
	if r := runSQL(t, e, id.String(), `CREATE TABLE posts (id serial PRIMARY KEY, title text NOT NULL, body text, words int GENERATED ALWAYS AS (length(coalesce(body, ''))) STORED,
		status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'live')));
		INSERT INTO posts (title, body) VALUES ('First', 'hello'), ('Second', 'world')`); r.Error != nil {
		t.Fatal(sqlErr(r))
	}
	// Both sessions load the first row with its xmin.
	page := rowsWith(t, e, id, "posts", filterQ(map[string]any{"column": "id", "op": "eq", "value": "1"}))
	if page.Xmin == nil || len(*page.Xmin) != 1 || len(page.Rows) != 1 {
		t.Fatalf("rows with xmin: %+v", page)
	}
	xmin := (*page.Xmin)[0]
	key := map[string]any{"id": "1"}
	save := func(changes ...map[string]any) (int, gen.SaveRowsResult) {
		var res gen.SaveRowsResult
		code := e.Do("POST", tablePath(id, "posts")+"/changes", map[string]any{"changes": changes}, &res)
		return code, res
	}

	// Session A saves first.
	code, res := save(map[string]any{"op": "update", "key": key, "xmin": xmin, "values": map[string]any{"title": "First (A)"}})
	if code != http.StatusOK || !res.Applied || len(res.Rows) != 1 || res.Rows[0].Xmin == xmin || res.Summary != "1 update" {
		t.Fatalf("session A: %d %+v", code, res)
	}
	// Session B saves the same row from the same snapshot: a conflict, with
	// A's version of the row, and nothing written.
	code, res = save(
		map[string]any{"op": "update", "key": map[string]any{"id": "2"}, "xmin": (*rowsWith(t, e, id, "posts", filterQ(map[string]any{"column": "id", "op": "eq", "value": "2"})).Xmin)[0], "values": map[string]any{"title": "Second (B)"}},
		map[string]any{"op": "update", "key": key, "xmin": xmin, "values": map[string]any{"title": "First (B)"}},
	)
	if code != http.StatusConflict || res.Applied || res.Conflict == nil || res.Conflict.Index != 1 || res.Conflict.Deleted ||
		res.Conflict.Current == nil || *(*res.Conflict.Current)[1] != "First (A)" {
		t.Fatalf("session B: %d %+v", code, res)
	}
	if got := onlyValue(t, runSQL(t, e, id.String(), "SELECT string_agg(title, ',' ORDER BY id) FROM posts")); got != "First (A),Second" {
		t.Fatalf("after the conflict: %s", got)
	}
	// Deleting a row someone changed is a conflict too; deleting one
	// someone deleted shows it gone.
	code, res = save(map[string]any{"op": "delete", "key": key, "xmin": xmin})
	if code != http.StatusConflict || res.Conflict == nil || res.Conflict.Deleted {
		t.Fatalf("stale delete: %d %+v", code, res)
	}
	runSQL(t, e, id.String(), "DELETE FROM posts WHERE id = 2")
	code, res = save(map[string]any{"op": "update", "key": map[string]any{"id": "2"}, "xmin": "1", "values": map[string]any{"title": "x"}})
	if code != http.StatusConflict || res.Conflict == nil || !res.Conflict.Deleted {
		t.Fatalf("update of a deleted row: %d %+v", code, res)
	}

	// One save is one transaction: a failing change rolls back the batch.
	fresh := (*rowsWith(t, e, id, "posts", nil).Xmin)[0]
	code, res = save(
		map[string]any{"op": "insert", "values": map[string]any{"title": "Third"}},
		map[string]any{"op": "update", "key": key, "xmin": fresh, "values": map[string]any{"status": "bogus"}},
	)
	if code != http.StatusUnprocessableEntity || res.Failed == nil || res.Failed.Index != 1 || deref(res.Failed.Error.Code) != "23514" {
		t.Fatalf("check violation: %d %+v", code, res)
	}
	if got := onlyValue(t, runSQL(t, e, id.String(), "SELECT count(*) FROM posts")); got != "1" {
		t.Fatalf("rows after the rolled-back batch: %s", got)
	}
	// Inserts take defaults; generated and identity columns are refused.
	code, res = save(map[string]any{"op": "insert", "values": map[string]any{"title": "Third", "body": nil}},
		map[string]any{"op": "update", "key": key, "xmin": fresh, "values": map[string]any{"body": "longer body"}})
	if code != http.StatusOK || len(res.Rows) != 2 || res.Summary != "1 update, 1 insert" {
		t.Fatalf("insert and update: %d %+v", code, res)
	}
	byName := map[string]int{}
	for i, col := range res.Columns {
		byName[col.Name] = i
	}
	if v := res.Rows[0].Values; *v[byName["status"]] != "draft" || v[byName["body"]] != nil {
		t.Fatalf("inserted row: %+v", v)
	}
	if v := res.Rows[1].Values; *v[byName["words"]] != "11" {
		t.Fatalf("generated column after update: %+v", v)
	}
	if code, _ := save(map[string]any{"op": "insert", "values": map[string]any{"title": "x", "words": "3"}}); code != http.StatusBadRequest {
		t.Fatalf("writing a generated column: %d", code)
	}
	if code, _ := save(map[string]any{"op": "update", "key": key, "values": map[string]any{"title": "x"}}); code != http.StatusBadRequest {
		t.Fatalf("an update without xmin: %d", code)
	}
	many := make([]map[string]any, 501)
	for i := range many {
		many[i] = map[string]any{"op": "insert", "values": map[string]any{"title": "x"}}
	}
	if code, _ := save(many...); code != http.StatusBadRequest {
		t.Fatalf("501 changes: %d", code)
	}

	// Tables without a primary key, and views, are read-only.
	runSQL(t, e, id.String(), "CREATE TABLE log (msg text); CREATE VIEW live AS SELECT * FROM posts WHERE status = 'live'")
	for _, tbl := range []string{"log", "live"} {
		var info gen.TableInfo
		if code := e.Do("GET", tablePath(id, tbl), nil, &info); code != http.StatusOK || info.Editable || info.ReadOnlyReason == nil {
			t.Fatalf("%s info: %d %+v", tbl, code, info)
		}
		if p := rowsWith(t, e, id, tbl, nil); p.Xmin != nil {
			t.Fatalf("%s has xmin", tbl)
		}
		if code := e.Do("POST", tablePath(id, tbl)+"/changes", map[string]any{"changes": []map[string]any{{"op": "insert", "values": map[string]any{"msg": "x"}}}}, nil); code != http.StatusBadRequest {
			t.Fatalf("editing %s: %d", tbl, code)
		}
	}
	var info gen.TableInfo
	e.Do("GET", tablePath(id, "posts"), nil, &info)
	if !info.Editable || len(info.PrimaryKey) != 1 || len(info.Constraints) == 0 {
		t.Fatalf("posts info: %+v", info)
	}
	for _, col := range info.Columns {
		if col.Name == "words" && !col.Generated || col.Name == "id" && col.Default == nil {
			t.Fatalf("column %+v", col)
		}
	}

	// The console's read-only toggle turns editing off.
	var upd gen.ProjectUpdated
	if code := e.Do("PATCH", "/api/v1/projects/"+id.String()+"/settings", map[string]any{"settings": map[string]any{"console_read_only": true}}, &upd); code != http.StatusOK {
		t.Fatalf("read-only toggle: %d", code)
	}
	if upd.Operation != nil {
		e.WaitOperation(upd.Operation.Id)
	}
	e.Do("GET", tablePath(id, "posts"), nil, &info)
	if info.Editable {
		t.Fatal("editable with the console read-only")
	}
	if code, _ := save(map[string]any{"op": "insert", "values": map[string]any{"title": "x"}}); code != http.StatusForbidden {
		t.Fatalf("saving with the console read-only: %d", code)
	}
}

// TestGridFiltersSortAndExport covers V2 §4.1.
func TestGridFiltersSortAndExport(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	c := e.CreateProject("Grid")
	id := c.Project.Id
	if r := runSQL(t, e, id.String(), `CREATE TABLE authors (id int PRIMARY KEY, name text);
		CREATE TABLE books (id int PRIMARY KEY, author_id int REFERENCES authors(id) ON DELETE CASCADE, title text, price numeric, tags text[]);
		INSERT INTO authors VALUES (1, 'Ann'), (2, 'Bob');
		INSERT INTO books SELECT g, 1 + g % 2, 'Book ' || g, g * 1.5, CASE WHEN g % 3 = 0 THEN ARRAY['sci-fi'] END FROM generate_series(1, 120) g`); r.Error != nil {
		t.Fatal(sqlErr(r))
	}
	// Filters are values, never SQL.
	p := rowsWith(t, e, id, "books", filterQ(
		map[string]any{"column": "author_id", "op": "eq", "value": "2"},
		map[string]any{"column": "price", "op": "gte", "value": "150"},
		map[string]any{"column": "title", "op": "contains", "value": "BOOK 1"},
	))
	if len(p.Rows) != 10 { // 101, 103, …, 119
		t.Fatalf("filtered: %d rows", len(p.Rows))
	}
	p = rowsWith(t, e, id, "books", filterQ(map[string]any{"column": "title", "op": "eq", "value": "x'; DROP TABLE books; --"}))
	if len(p.Rows) != 0 {
		t.Fatal("an injection matched rows")
	}
	p = rowsWith(t, e, id, "books", filterQ(map[string]any{"column": "id", "op": "in", "values": []string{"3", "5", "7"}}, map[string]any{"column": "tags", "op": "not_null"}))
	if len(p.Rows) != 1 || *p.Rows[0][0] != "3" {
		t.Fatalf("in + not null: %+v", p.Rows)
	}
	if code := e.Do("GET", tablePath(id, "books")+"/rows?"+filterQ(map[string]any{"column": "nope", "op": "eq", "value": "1"}).Encode(), nil, nil); code != http.StatusBadRequest {
		t.Fatalf("unknown column: %d", code)
	}
	var bad gen.Error
	if code := e.Do("GET", tablePath(id, "books")+"/rows?"+filterQ(map[string]any{"column": "price", "op": "gt", "value": "cheap"}).Encode(), nil, &bad); code != http.StatusBadRequest || bad.SqlError == nil {
		t.Fatalf("a value that doesn't fit the column: %d %+v", code, bad)
	}
	// Sorting by the primary key pages by keyset, descending too; another
	// column by offset.
	p = rowsWith(t, e, id, "books", url.Values{"sort": {"id"}, "desc": {"true"}})
	if p.Order != gen.TablePageOrderPrimaryKey || *p.Rows[0][0] != "120" || p.Next == nil {
		t.Fatalf("desc by id: %s %v", p.Order, *p.Rows[0][0])
	}
	q := url.Values{"sort": {"id"}, "desc": {"true"}, "after": {*p.Next}}
	if p2 := rowsWith(t, e, id, "books", q); *p2.Rows[0][0] != "70" {
		t.Fatalf("second page desc: %v", *p2.Rows[0][0])
	}
	p = rowsWith(t, e, id, "books", url.Values{"sort": {"title"}})
	if p.Order != gen.TablePageOrderOffset || *p.Rows[0][2] != "Book 1" || *p.Rows[1][2] != "Book 10" {
		t.Fatalf("by title: %s %v", p.Order, *p.Rows[1][2])
	}
	// The foreign key panel reads the referenced row by its key.
	var info gen.TableInfo
	e.Do("GET", tablePath(id, "books"), nil, &info)
	if len(info.ForeignKeys) != 1 || info.ForeignKeys[0].RefTable != "authors" || info.ForeignKeys[0].OnDelete != "CASCADE" {
		t.Fatalf("foreign keys: %+v", info.ForeignKeys)
	}
	if a := rowsWith(t, e, id, "authors", filterQ(map[string]any{"column": "id", "op": "eq", "value": "2"})); len(a.Rows) != 1 || *a.Rows[0][1] != "Bob" {
		t.Fatalf("referenced row: %+v", a.Rows)
	}
	// Export: CSV and JSON, filtered.
	code, body := e.GetText(tablePath(id, "books")+"/export?format=csv&"+filterQ(map[string]any{"column": "author_id", "op": "eq", "value": "1"}).Encode(), nil, true)
	if code != http.StatusOK || !strings.HasPrefix(body, "id,author_id,title,price,tags\n") || strings.Count(body, "\n") != 61 {
		t.Fatalf("csv: %d %q…", code, body[:min(len(body), 200)])
	}
	code, body = e.GetText(tablePath(id, "books")+"/export?format=json&sort=id&desc=true", nil, true)
	var rows []map[string]*string
	if code != http.StatusOK || json.Unmarshal([]byte(body), &rows) != nil || len(rows) != 120 || *rows[0]["id"] != "120" || rows[1]["tags"] != nil {
		t.Fatalf("json: %d %d", code, len(rows))
	}
}

// TestTypeChangeShowsRewriteWarning is M11's second done-when (V2 §4.3): a
// large type change shows the rewrite warning before anything runs.
func TestTypeChangeShowsRewriteWarning(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	c := e.CreateProject("Schema")
	id := c.Project.Id
	if r := runSQL(t, e, id.String(), `CREATE TABLE events (id int PRIMARY KEY, name varchar(20), payload text);
		INSERT INTO events SELECT g, 'e' || g, repeat('x', 200) FROM generate_series(1, 200000) g; ANALYZE events`); r.Error != nil {
		t.Fatal(sqlErr(r))
	}
	base := "/api/v1/projects/" + id.String() + "/schema/"
	change := map[string]any{"kind": "alter_column", "table": "events", "column_name": "id", "type": "bigint"}
	var plan gen.SchemaPlan
	if code := e.Do("POST", base+"preview", map[string]any{"change": change}, &plan); code != http.StatusOK {
		t.Fatalf("preview: %d", code)
	}
	if len(plan.Statements) != 1 || plan.Statements[0].Sql != `ALTER TABLE "public"."events" ALTER COLUMN "id" TYPE bigint` {
		t.Fatalf("statements: %+v", plan.Statements)
	}
	if len(plan.Risks) != 1 || plan.Risks[0].Level == gen.SchemaRiskLevelInfo ||
		!strings.HasPrefix(plan.Risks[0].Message, "Rewrites the table and blocks writes. Estimated size: ") || !strings.Contains(plan.Risks[0].Message, "MB") {
		t.Fatalf("risks: %+v", plan.Risks)
	}
	// Nothing runs without the previewed plan.
	var ee gen.Error
	if code := e.Do("POST", base+"apply", map[string]any{"change": change, "hash": "0000"}, &ee); code != http.StatusConflict || ee.Code != "stale_plan" {
		t.Fatalf("apply without the preview: %d %+v", code, ee)
	}
	if got := onlyValue(t, runSQL(t, e, id.String(), "SELECT format_type(atttypid, atttypmod) FROM pg_attribute WHERE attrelid = 'events'::regclass AND attname = 'id'")); got != "integer" {
		t.Fatalf("type after a refused apply: %s", got)
	}
	var applied gen.SchemaApplied
	if code := e.Do("POST", base+"apply", map[string]any{"change": change, "hash": plan.Hash}, &applied); code != http.StatusOK {
		t.Fatalf("apply: %d", code)
	}
	if got := onlyValue(t, runSQL(t, e, id.String(), "SELECT format_type(atttypid, atttypmod) FROM pg_attribute WHERE attrelid = 'events'::regclass AND attname = 'id'")); got != "bigint" {
		t.Fatalf("type after apply: %s", got)
	}
	// The audit log keeps the SQL.
	var detail string
	if err := e.DB.QueryRow(context.Background(), `SELECT detail::text FROM audit_log WHERE action = 'project.schema.change' ORDER BY created_at DESC LIMIT 1`).Scan(&detail); err != nil ||
		!strings.Contains(detail, `ALTER COLUMN \"id\" TYPE bigint`) {
		t.Fatalf("audit: %s %v", detail, err)
	}
	// Widening a varchar doesn't rewrite; SET NOT NULL warns about the scan.
	for _, cse := range []struct {
		change map[string]any
		want   string
	}{
		{map[string]any{"kind": "alter_column", "table": "events", "column_name": "name", "type": "varchar(50)"}, "doesn't rewrite"},
		{map[string]any{"kind": "alter_column", "table": "events", "column_name": "name", "type": "text"}, "doesn't rewrite"},
		{map[string]any{"kind": "alter_column", "table": "events", "column_name": "name", "type": "varchar(10)"}, "Rewrites the table"},
		{map[string]any{"kind": "alter_column", "table": "events", "column_name": "name", "nullable": false}, "NOT VALID"},
		{map[string]any{"kind": "add_column", "table": "events", "column": map[string]any{"name": "ref", "type": "uuid", "default": "gen_random_uuid()"}}, "volatile"},
		{map[string]any{"kind": "add_column", "table": "events", "column": map[string]any{"name": "seen", "type": "timestamptz", "default": "now()"}}, "doesn't rewrite"},
	} {
		var p gen.SchemaPlan
		if code := e.Do("POST", base+"preview", map[string]any{"change": cse.change}, &p); code != http.StatusOK || len(p.Risks) == 0 || !strings.Contains(p.Risks[0].Message, cse.want) {
			t.Errorf("%v: %d %+v", cse.change, code, p.Risks)
		}
	}
	if code := e.Do("POST", base+"preview", map[string]any{"change": map[string]any{"kind": "alter_column", "table": "events", "column_name": "id", "type": "nosuchtype"}}, nil); code != http.StatusBadRequest {
		t.Fatalf("unknown type: %d", code)
	}

	// DDL waits at most 5 s for a lock.
	conn := e.MustConnect(c.Connection.SessionUrl)
	ctx := context.Background()
	if _, err := conn.Exec(ctx, "BEGIN; LOCK TABLE events IN ACCESS SHARE MODE"); err != nil {
		t.Fatal(err)
	}
	add := map[string]any{"kind": "add_column", "table": "events", "column": map[string]any{"name": "note", "type": "text"}}
	e.Do("POST", base+"preview", map[string]any{"change": add}, &plan)
	start := time.Now()
	if code := e.Do("POST", base+"apply", map[string]any{"change": add, "hash": plan.Hash}, &ee); code != http.StatusUnprocessableEntity || ee.SqlError == nil || deref(ee.SqlError.Code) != "55P03" {
		t.Fatalf("apply behind a lock: %d %+v", code, ee)
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Fatalf("waited %s for the lock", d)
	}
	_, _ = conn.Exec(ctx, "ROLLBACK")

	// Drops need the name typed; indexes are built concurrently.
	drop := map[string]any{"kind": "drop_column", "table": "events", "column_name": "payload"}
	e.Do("POST", base+"preview", map[string]any{"change": drop}, &plan)
	if plan.Confirm == nil || *plan.Confirm != "payload" {
		t.Fatalf("drop preview: %+v", plan)
	}
	if code := e.Do("POST", base+"apply", map[string]any{"change": drop, "hash": plan.Hash, "confirm": "nope"}, &ee); code != http.StatusBadRequest || ee.Code != "confirm_required" {
		t.Fatalf("drop unconfirmed: %d", code)
	}
	if code := e.Do("POST", base+"apply", map[string]any{"change": drop, "hash": plan.Hash, "confirm": "payload"}, nil); code != http.StatusOK {
		t.Fatalf("drop: %d", code)
	}
	idx := map[string]any{"kind": "create_index", "table": "events", "key_columns": []string{"name"}}
	e.Do("POST", base+"preview", map[string]any{"change": idx}, &plan)
	if !strings.Contains(plan.Statements[0].Sql, "CONCURRENTLY") || plan.Statements[0].Transactional {
		t.Fatalf("index plan: %+v", plan.Statements)
	}
	if code := e.Do("POST", base+"apply", map[string]any{"change": idx, "hash": plan.Hash}, nil); code != http.StatusOK {
		t.Fatalf("create index: %d", code)
	}
	// A new table is readable by the read-only role straight away.
	tbl := map[string]any{"kind": "create_table", "table": "tags", "columns": []map[string]any{
		{"name": "id", "type": "bigint generated always as identity", "primary_key": true}, {"name": "label", "type": "text", "nullable": false}}}
	e.Do("POST", base+"preview", map[string]any{"change": tbl}, &plan)
	if code := e.Do("POST", base+"apply", map[string]any{"change": tbl, "hash": plan.Hash}, nil); code != http.StatusOK {
		t.Fatalf("create table: %d", code)
	}
	ro := c.Project.DbName + "_ro"
	if got := onlyValue(t, runSQL(t, e, id.String(), "SELECT has_table_privilege('"+ro+"', 'public.tags', 'SELECT')")); got != "t" {
		t.Fatalf("read-only role on the new table: %s", got)
	}
}

// TestAddedColumnMigrationAppliesElsewhere is M11's third done-when (V2
// §4.3): an added column exports as a goose migration that applies cleanly
// to another database, and migrates down again.
func TestAddedColumnMigrationAppliesElsewhere(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	dev := e.CreateProject("Dev")
	prod := e.CreateProject("Prod")
	for _, p := range []gen.ProjectCredentials{dev, prod} {
		if r := runSQL(t, e, p.Project.Id.String(), "CREATE TABLE posts (id serial PRIMARY KEY, title text NOT NULL); INSERT INTO posts (title) VALUES ('hello')"); r.Error != nil {
			t.Fatal(sqlErr(r))
		}
	}
	change := map[string]any{"kind": "add_column", "table": "posts", "column": map[string]any{"name": "summary", "type": "text", "default": "''", "nullable": false}}
	var m gen.SchemaMigration
	if code := e.Do("POST", "/api/v1/projects/"+dev.Project.Id.String()+"/schema/migration", map[string]any{"change": change, "format": "goose"}, &m); code != http.StatusOK {
		t.Fatalf("migration: %d", code)
	}
	if !strings.HasSuffix(m.Filename, "_add_column_posts_summary.sql") || !strings.Contains(m.Content, "-- +goose Up") || !strings.Contains(m.Content, "-- +goose Down") {
		t.Fatalf("migration:\n%s %s", m.Filename, m.Content)
	}
	var prefs gen.EditorPreferences
	if code := e.Do("GET", "/api/v1/projects/"+dev.Project.Id.String()+"/editor-preferences", nil, &prefs); code != http.StatusOK || prefs.MigrationFormat != gen.EditorPreferencesMigrationFormatGoose {
		t.Fatalf("remembered format: %d %+v", code, prefs)
	}

	// Apply it to the other project with goose, as a deploy would.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, m.Filename), []byte(m.Content), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", prod.Connection.SessionUrl)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(db, dir); err != nil {
		t.Fatalf("goose up: %v\n%s", err, m.Content)
	}
	var summary string
	if err := db.QueryRow("SELECT summary FROM posts WHERE id = 1").Scan(&summary); err != nil || summary != "" {
		t.Fatalf("the new column on prod: %q %v", summary, err)
	}
	if err := goose.Down(db, dir); err != nil {
		t.Fatalf("goose down: %v", err)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM information_schema.columns WHERE table_name = 'posts' AND column_name = 'summary'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("after down: %d %v", n, err)
	}
	// The other formats render too.
	for _, f := range []string{"sql", "dbmate"} {
		var mm gen.SchemaMigration
		if code := e.Do("POST", "/api/v1/projects/"+dev.Project.Id.String()+"/schema/migration", map[string]any{"change": change, "format": f}, &mm); code != http.StatusOK || !strings.Contains(mm.Content, "ADD COLUMN \"summary\"") {
			t.Fatalf("%s: %d %s", f, code, mm.Content)
		}
	}
	// The source project is untouched by exporting.
	if got := onlyValue(t, runSQL(t, e, dev.Project.Id.String(), "SELECT count(*) FROM information_schema.columns WHERE table_name = 'posts' AND column_name = 'summary'")); got != "0" {
		t.Fatalf("export changed dev: %s", got)
	}
}

// TestEditorPermissions: read-only members can browse and export but not
// edit rows or schema (V2 §2.3).
func TestEditorPermissions(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	c := e.CreateProject("Shared")
	id := c.Project.Id
	runSQL(t, e, id.String(), "CREATE TABLE notes (id int PRIMARY KEY, body text); INSERT INTO notes VALUES (1, 'x')")
	var inv gen.InvitationCreated
	if code := e.Do("POST", "/api/v1/orgs/"+e.OrgID.String()+"/members", map[string]any{"email": "viewer@example.com", "role": "member",
		"projects": []map[string]any{{"project_id": id, "role": "read_only"}}}, &inv); code != http.StatusCreated {
		t.Fatalf("invite: %d", code)
	}
	viewer := e.AcceptInvitation(e.MailToken("viewer@example.com", "invitation"), "viewer@example.com")
	var info gen.TableInfo
	if code := viewer.Do("GET", tablePath(id, "notes"), nil, &info); code != http.StatusOK || info.Editable || deref(info.ReadOnlyReason) != "Your role on this project is read-only." {
		t.Fatalf("viewer info: %d %+v", code, info)
	}
	if code := viewer.Do("GET", tablePath(id, "notes")+"/rows", nil, nil); code != http.StatusOK {
		t.Fatalf("viewer rows: %d", code)
	}
	if code := viewer.Do("POST", tablePath(id, "notes")+"/changes", map[string]any{"changes": []map[string]any{{"op": "insert", "values": map[string]any{"id": "2"}}}}, nil); code != http.StatusForbidden {
		t.Fatalf("viewer save: %d", code)
	}
	var plan gen.SchemaPlan
	change := map[string]any{"kind": "add_column", "table": "notes", "column": map[string]any{"name": "x", "type": "int"}}
	if code := viewer.Do("POST", "/api/v1/projects/"+id.String()+"/schema/preview", map[string]any{"change": change}, &plan); code != http.StatusOK {
		t.Fatalf("viewer preview: %d", code)
	}
	if code := viewer.Do("POST", "/api/v1/projects/"+id.String()+"/schema/apply", map[string]any{"change": change, "hash": plan.Hash}, nil); code != http.StatusForbidden {
		t.Fatalf("viewer apply: %d", code)
	}
	// A write token can edit rows, but dropping needs the admin scope.
	token := e.CreateToken(map[string]any{"name": "ci", "org_id": e.OrgID, "scopes": []string{"write"}})
	xmin := (*rowsWith(t, e, id, "notes", nil).Xmin)[0]
	if code, raw := e.BearerDo(token, "POST", tablePath(id, "notes")+"/changes", map[string]any{"changes": []map[string]any{{"op": "update", "key": map[string]any{"id": "1"}, "xmin": xmin, "values": map[string]any{"body": "y"}}}}, nil); code != http.StatusOK {
		t.Fatalf("token save: %d %s", code, raw)
	}
	drop := map[string]any{"kind": "drop_table", "table": "notes"}
	e.Do("POST", "/api/v1/projects/"+id.String()+"/schema/preview", map[string]any{"change": drop}, &plan)
	if code, raw := e.BearerDo(token, "POST", "/api/v1/projects/"+id.String()+"/schema/apply", map[string]any{"change": drop, "hash": plan.Hash, "confirm": "notes"}, nil); code != http.StatusForbidden {
		t.Fatalf("token drop: %d %s", code, raw)
	}
	_ = io.Discard
}
