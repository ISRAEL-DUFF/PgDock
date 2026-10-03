package integration

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/test/testenv"
)

// TestTableEditorAPI covers what the Studio-style table editor needs
// (docs/ui-redesign.md, phase 2): numbered pages, several sorts, the row
// count, the table's DDL, and the richer schema changes behind its side
// panels.
func TestTableEditorAPI(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	c := e.CreateProject("Studio")
	id := c.Project.Id
	if r := runSQL(t, e, id.String(), `
		CREATE TABLE customers (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, name text NOT NULL);
		COMMENT ON TABLE customers IS 'People who order';
		CREATE TABLE orders (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, customer_id bigint REFERENCES customers (id),
			status text NOT NULL DEFAULT 'new', total numeric(10,2), doubled numeric GENERATED ALWAYS AS (total * 2) STORED);
		CREATE INDEX orders_status_idx ON orders (status);
		INSERT INTO customers (name) VALUES ('Ada'), ('Grace');
		INSERT INTO orders (customer_id, status, total) SELECT 1 + g % 2, CASE WHEN g % 3 = 0 THEN 'paid' ELSE 'new' END, g FROM generate_series(1, 250) g`); r.Error != nil {
		t.Fatal(sqlErr(r))
	}
	order := func(q url.Values, col string, desc bool) url.Values {
		b, _ := json.Marshal(map[string]any{"column": col, "desc": desc})
		q.Add("order", string(b))
		return q
	}

	// Numbered pages of up to 1,000 rows, sorted by two columns.
	q := order(order(url.Values{"limit": {"100"}, "offset": {"100"}}, "status", false), "total", true)
	page := rowsWith(t, e, id, "orders", q)
	if page.Order != "offset" || len(page.Rows) != 100 || page.Next == nil {
		t.Fatalf("page 2: order %s, %d rows", page.Order, len(page.Rows))
	}
	// "new" rows come before "paid", each by total descending: the 101st
	// "new" row by total.
	if got := *page.Rows[0][3]; got != "100.00" {
		t.Fatalf("first row of page 2 has total %s", got)
	}
	if code := e.Do("GET", tablePath(id, "orders")+"/rows?limit=1001", nil, nil); code != http.StatusBadRequest {
		t.Fatalf("limit 1001: %d", code)
	}
	if code := e.Do("GET", tablePath(id, "orders")+"/rows?offset=-1", nil, nil); code != http.StatusBadRequest {
		t.Fatalf("negative offset: %d", code)
	}

	// The count, filtered or not.
	var n gen.RowCount
	if code := e.Do("GET", tablePath(id, "orders")+"/count", nil, &n); code != http.StatusOK || n.Count == nil || *n.Count != 250 || n.Estimated {
		t.Fatalf("count: %d %+v", code, n)
	}
	if code := e.Do("GET", tablePath(id, "orders")+"/count?"+filterQ(map[string]any{"column": "status", "op": "eq", "value": "paid"}).Encode(), nil, &n); code != http.StatusOK || *n.Count != 83 {
		t.Fatalf("filtered count: %d %+v", code, n)
	}

	// The definition reads as DDL that recreates the table.
	var def gen.TableDefinition
	if code := e.Do("GET", tablePath(id, "orders")+"/definition", nil, &def); code != http.StatusOK {
		t.Fatalf("definition: %d", code)
	}
	for _, want := range []string{
		`CREATE TABLE "public"."orders" (`,
		`"id" bigint GENERATED ALWAYS AS IDENTITY`,
		`"status" text DEFAULT 'new'::text NOT NULL`,
		`"doubled" numeric GENERATED ALWAYS AS ((total * (2)::numeric)) STORED`,
		`CONSTRAINT "orders_pkey" PRIMARY KEY (id)`,
		`FOREIGN KEY (customer_id) REFERENCES customers(id)`,
		`CREATE INDEX orders_status_idx ON public.orders USING btree (status);`,
	} {
		if !strings.Contains(def.Sql, want) {
			t.Errorf("definition lacks %q:\n%s", want, def.Sql)
		}
	}
	e.Do("GET", tablePath(id, "customers")+"/definition", nil, &def)
	if !strings.Contains(def.Sql, `COMMENT ON TABLE "public"."customers" IS 'People who order';`) {
		t.Errorf("comment missing:\n%s", def.Sql)
	}

	base := "/api/v1/projects/" + id.String() + "/schema/"
	apply := func(change map[string]any, confirm string) gen.SchemaPlan {
		t.Helper()
		var plan gen.SchemaPlan
		if code := e.Do("POST", base+"preview", map[string]any{"change": change}, &plan); code != http.StatusOK {
			t.Fatalf("preview %v: %d", change["kind"], code)
		}
		var ee gen.Error
		if code := e.Do("POST", base+"apply", map[string]any{"change": change, "hash": plan.Hash, "confirm": confirm}, &ee); code != http.StatusOK {
			t.Fatalf("apply %v: %d %+v", change["kind"], code, ee)
		}
		return plan
	}

	// A table with a foreign key and column constraints, in one go.
	apply(map[string]any{"kind": "create_table", "table": "notes", "comment": "Order notes", "columns": []map[string]any{
		{"name": "id", "type": "bigint generated always as identity", "primary_key": true},
		{"name": "order_id", "type": "bigint", "nullable": false, "references": map[string]any{"table": "orders", "column": "id", "on_delete": "CASCADE"}},
		{"name": "slug", "type": "text", "unique": true, "check": "slug <> ''"},
	}}, "")
	if r := runSQL(t, e, id.String(), `INSERT INTO notes (order_id, slug) VALUES (1, 'a'); INSERT INTO notes (order_id, slug) VALUES (999, 'b')`); r.Error == nil || !strings.Contains(r.Error.Message, "foreign key") {
		t.Fatalf("the foreign key didn't hold: %+v", r.Error)
	}

	// Duplicate a table with its rows; the copy's identity carries on.
	apply(map[string]any{"kind": "duplicate_table", "table": "orders", "new_name": "orders_copy", "with_data": true}, "")
	if got := onlyValue(t, runSQL(t, e, id.String(), `INSERT INTO orders_copy (status, total) VALUES ('new', 1) RETURNING id`)); got != "251" {
		t.Fatalf("next id in the copy: %s", got)
	}
	e.Do("GET", tablePath(id, "orders_copy")+"/count", nil, &n)
	if *n.Count != 251 {
		t.Fatalf("copied rows: %+v", n)
	}

	// Edit table: several changes in one transaction, with the rename last.
	plan := apply(map[string]any{"kind": "batch", "table": "orders_copy", "changes": []map[string]any{
		{"kind": "add_column", "column": map[string]any{"name": "note", "type": "text"}},
		{"kind": "drop_column", "column_name": "doubled"},
		{"kind": "set_comment", "comment": "A copy"},
		{"kind": "set_comment", "column_name": "status", "comment": "Where it is"},
		{"kind": "rename_table", "new_name": "orders_archive"},
	}}, "doubled")
	if len(plan.Statements) != 5 || plan.Slug != "alter_table_orders_copy" {
		t.Fatalf("batch plan: %+v", plan)
	}
	if got := onlyValue(t, runSQL(t, e, id.String(), `SELECT obj_description('orders_archive'::regclass, 'pg_class') || '/' || col_description('orders_archive'::regclass, 3)`)); got != "A copy/Where it is" {
		t.Fatalf("comments: %s", got)
	}
	// A failing change rolls the whole batch back.
	bad := map[string]any{"kind": "batch", "table": "orders_archive", "changes": []map[string]any{
		{"kind": "add_column", "column": map[string]any{"name": "extra", "type": "text"}},
		{"kind": "alter_column", "column_name": "status", "type": "int"},
	}}
	var bp gen.SchemaPlan
	e.Do("POST", base+"preview", map[string]any{"change": bad}, &bp)
	if code := e.Do("POST", base+"apply", map[string]any{"change": bad, "hash": bp.Hash}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("failing batch: %d", code)
	}
	if got := onlyValue(t, runSQL(t, e, id.String(), `SELECT count(*) FROM pg_attribute WHERE attrelid = 'orders_archive'::regclass AND attname = 'extra'`)); got != "0" {
		t.Fatal("half a batch was applied")
	}
}
