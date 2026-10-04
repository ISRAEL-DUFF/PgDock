package schemaedit

import (
	"strings"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

func TestAddColumn(t *testing.T) {
	p, err := Build(Change{Kind: AddColumn, Table: "posts", Column: &ColumnDef{Name: "summary", Type: "text", Default: ptr("''")}}, State{Rows: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Statements) != 1 || p.Statements[0].SQL != `ALTER TABLE "public"."posts" ADD COLUMN "summary" text DEFAULT ''` || !p.Transactional() {
		t.Fatalf("statements: %+v", p.Statements)
	}
	if len(p.Down) != 1 || p.Down[0] != `ALTER TABLE "public"."posts" DROP COLUMN "summary"` || p.Slug != "add_column_posts_summary" || p.Hash == "" {
		t.Fatalf("plan: %+v", p)
	}
	m, err := Render(p, FormatGoose, time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if m.Filename != "20261003120000_add_column_posts_summary.sql" ||
		!strings.Contains(m.Content, "-- +goose Up\nALTER TABLE \"public\".\"posts\" ADD COLUMN \"summary\" text DEFAULT '';\n") ||
		!strings.Contains(m.Content, "-- +goose Down\nALTER TABLE \"public\".\"posts\" DROP COLUMN \"summary\";\n") ||
		strings.Contains(m.Content, "NO TRANSACTION") {
		t.Fatalf("goose:\n%s", m.Content)
	}
	// NOT NULL without a default on a table with rows fails: say so.
	p, _ = Build(Change{Kind: AddColumn, Table: "posts", Column: &ColumnDef{Name: "x", Type: "int", Nullable: ptr(false)}}, State{Rows: 3})
	if p.Risks[0].Level != "danger" || !strings.Contains(p.Risks[0].Message, "fails") {
		t.Fatalf("not null risk: %+v", p.Risks)
	}
	p, _ = Build(Change{Kind: AddColumn, Table: "posts", Column: &ColumnDef{Name: "id2", Type: "uuid", Default: ptr("gen_random_uuid()")}}, State{SizeBytes: 3 << 30, VolatileDefault: true})
	if p.Risks[0].Level != "danger" || !strings.Contains(p.Risks[0].Message, "rewrites the table") || !strings.Contains(p.Risks[0].Message, "3.2 GB") {
		t.Fatalf("volatile default: %+v", p.Risks)
	}
}

func TestTypeChangeWarnsAboutRewrites(t *testing.T) {
	c := Change{Kind: AlterColumn, Table: "events", ColumnName: "id", Type: ptr("bigint")}
	p, err := Build(c, State{SizeBytes: 2_300_000_000, TypeRewrites: true, ColumnType: "integer"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Risks[0].Level != "danger" || p.Risks[0].Message != "Rewrites the table and blocks writes. Estimated size: 2.3 GB." {
		t.Fatalf("risks: %+v", p.Risks)
	}
	if p.Down[0] != `ALTER TABLE "public"."events" ALTER COLUMN "id" TYPE integer` || len(p.DownTODO) != 1 {
		t.Fatalf("down: %+v %+v", p.Down, p.DownTODO)
	}
	p, _ = Build(Change{Kind: AlterColumn, Table: "events", ColumnName: "name", Type: ptr("text")}, State{TypeRewrites: false})
	if p.Risks[0].Level != "info" {
		t.Fatalf("no rewrite: %+v", p.Risks)
	}
	p, _ = Build(Change{Kind: AlterColumn, Table: "events", ColumnName: "name", Nullable: ptr(false)}, State{SizeBytes: 1 << 30})
	if !strings.Contains(p.Risks[0].Message, "NOT VALID") || p.Risks[0].Level != "warning" {
		t.Fatalf("set not null: %+v", p.Risks)
	}
}

func TestIndexesAreConcurrent(t *testing.T) {
	p, err := Build(Change{Kind: CreateIndex, Table: "posts", KeyColumns: []string{"author_id", "created_at"}, Where: "deleted_at IS NULL"}, State{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Statements[0].SQL != `CREATE INDEX CONCURRENTLY "posts_author_id_created_at_idx" ON "public"."posts" ("author_id", "created_at") WHERE deleted_at IS NULL` || p.Transactional() {
		t.Fatalf("index: %+v", p.Statements)
	}
	for _, f := range []string{FormatGoose, FormatDbmate} {
		m, _ := Render(p, f, time.Now())
		if !strings.Contains(m.Content, "NO TRANSACTION") && !strings.Contains(m.Content, "transaction:false") {
			t.Errorf("%s: no transaction marker:\n%s", f, m.Content)
		}
	}
	p, _ = Build(Change{Kind: CreateIndex, Table: "t", KeyColumns: []string{"doc"}, Method: "gin", Concurrent: ptr(false)}, State{})
	if !p.Transactional() || !strings.Contains(p.Statements[0].SQL, "USING gin") || strings.Contains(p.Statements[0].SQL, "CONCURRENTLY") {
		t.Fatalf("plain gin index: %+v", p.Statements)
	}
}

func TestDropsNeedTheNameTyped(t *testing.T) {
	for _, c := range []Change{
		{Kind: DropTable, Table: "posts"},
		{Kind: DropColumn, Table: "posts", ColumnName: "body"},
		{Kind: DropSchema, Name: "archive"},
		{Kind: DropIndex, Name: "posts_idx"},
	} {
		p, err := Build(c, State{})
		if err != nil {
			t.Fatal(err)
		}
		if p.Confirm == "" || len(p.DownTODO) == 0 && c.Kind != DropIndex {
			t.Errorf("%s: %+v", c.Kind, p)
		}
	}
}

func TestQuotingAndValidation(t *testing.T) {
	p, err := Build(Change{Kind: CreateTable, Schema: "app", Table: `we"ird`, Columns: []ColumnDef{
		{Name: "id", Type: "bigint generated always as identity", PrimaryKey: true},
		{Name: "price", Type: "numeric(10, 2)", Nullable: ptr(false), Default: ptr("0")},
		{Name: "tags", Type: "text[]"},
	}, Comment: ptr("it's a table")}, State{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Statements[0].SQL, `CREATE TABLE "app"."we""ird"`) || !strings.Contains(p.Statements[0].SQL, `PRIMARY KEY ("id")`) ||
		p.Statements[1].SQL != `COMMENT ON TABLE "app"."we""ird" IS 'it''s a table'` {
		t.Fatalf("create table: %+v", p.Statements)
	}
	for _, bad := range []string{"int; drop table x", "text) --", "", "int,(x int)"} {
		if ValidType(bad) {
			t.Errorf("type %q accepted", bad)
		}
	}
	for _, good := range []string{"int", "character varying(255)", "timestamp with time zone", "public.mood", "numeric(10,2)", "jsonb[]", "double precision"} {
		if !ValidType(good) {
			t.Errorf("type %q refused", good)
		}
	}
	if _, err := Build(Change{Kind: "truncate"}, State{}); err == nil {
		t.Error("unknown kind accepted")
	}
	if _, err := Build(Change{Kind: AddForeignKey, Table: "a", KeyColumns: []string{"b_id"}, RefTable: "b", RefColumns: []string{"id"}, OnDelete: "explode"}, State{}); err == nil {
		t.Error("bad FK action accepted")
	}
}

func TestColumnConstraintsInline(t *testing.T) {
	p, err := Build(Change{Kind: CreateTable, Table: "orders", Columns: []ColumnDef{
		{Name: "id", Type: "bigint generated always as identity", PrimaryKey: true},
		{Name: "email", Type: "text", Unique: true, Check: ptr("email <> ''")},
		{Name: "customer_id", Type: "bigint", References: &ColumnRef{Table: "customers", Column: "id", OnDelete: "cascade"}},
	}}, State{})
	if err != nil {
		t.Fatal(err)
	}
	want := `CREATE TABLE "public"."orders" (
  "id" bigint generated always as identity NOT NULL,
  "email" text UNIQUE CHECK (email <> ''),
  "customer_id" bigint REFERENCES "public"."customers" ("id") ON DELETE CASCADE,
  PRIMARY KEY ("id")
)`
	if p.Statements[0].SQL != want {
		t.Fatalf("got\n%s", p.Statements[0].SQL)
	}
	_, err = Build(Change{Kind: AddColumn, Table: "orders", Column: &ColumnDef{Name: "x", Type: "int", References: &ColumnRef{Table: "t", Column: "id", OnDelete: "DROP"}}}, State{})
	if err == nil {
		t.Fatal("an unknown action passed")
	}
}

func TestSetCommentAndDuplicate(t *testing.T) {
	p, err := Build(Change{Kind: SetComment, Table: "orders", ColumnName: "email", Comment: ptr("who's ordering")}, State{OldComment: ptr("old")})
	if err != nil {
		t.Fatal(err)
	}
	if p.Statements[0].SQL != `COMMENT ON COLUMN "public"."orders"."email" IS 'who''s ordering'` || p.Down[0] != `COMMENT ON COLUMN "public"."orders"."email" IS 'old'` {
		t.Fatalf("comment: %+v", p)
	}
	p, err = Build(Change{Kind: DuplicateTable, Table: "orders", NewName: "orders_copy", WithData: true},
		State{CopyColumns: []string{"id", "email"}, IdentityColumns: []string{"id"}, SizeBytes: 1000})
	if err != nil {
		t.Fatal(err)
	}
	sqls := make([]string, len(p.Statements))
	for i, s := range p.Statements {
		sqls[i] = s.SQL
	}
	want := []string{
		`CREATE TABLE "public"."orders_copy" (LIKE "public"."orders" INCLUDING ALL)`,
		`INSERT INTO "public"."orders_copy" ("id", "email") OVERRIDING SYSTEM VALUE SELECT "id", "email" FROM "public"."orders"`,
		`SELECT setval(pg_get_serial_sequence('"public"."orders_copy"', 'id'), COALESCE(max("id"), 0) + 1, false) FROM "public"."orders_copy"`,
	}
	if strings.Join(sqls, "\n") != strings.Join(want, "\n") || p.Down[0] != `DROP TABLE "public"."orders_copy"` {
		t.Fatalf("duplicate:\n%s", strings.Join(sqls, "\n"))
	}
}

func TestBatch(t *testing.T) {
	c := Change{Kind: Batch, Table: "orders", Changes: []Change{
		{Kind: AddColumn, Column: &ColumnDef{Name: "note", Type: "text"}},
		{Kind: DropColumn, ColumnName: "legacy"},
		{Kind: SetComment, Comment: ptr("Orders")},
		{Kind: RenameTable, NewName: "purchases"},
	}}
	p, err := Build(c, State{Batch: make([]State, 4)})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Statements) != 4 || !p.Transactional() || p.Confirm != "legacy" || p.Slug != "alter_table_orders" {
		t.Fatalf("plan: %+v", p)
	}
	// The reverse runs backwards: the rename is undone first.
	if p.Down[0] != `ALTER TABLE "public"."purchases" RENAME TO "orders"` || p.Down[len(p.Down)-1] != `ALTER TABLE "public"."orders" DROP COLUMN "note"` {
		t.Fatalf("down: %+v", p.Down)
	}
	for _, bad := range []Change{
		{Kind: Batch, Table: "orders", Changes: []Change{{Kind: RenameTable, NewName: "x"}, {Kind: DropColumn, ColumnName: "y"}}},
		{Kind: Batch, Table: "orders", Changes: []Change{{Kind: CreateIndex, KeyColumns: []string{"a"}}}},
		{Kind: Batch, Table: "orders", Changes: []Change{{Kind: DropColumn, Table: "other", ColumnName: "y"}}},
		{Kind: Batch, Table: "orders"},
	} {
		if _, err := Build(bad, State{Batch: make([]State, len(bad.Changes))}); err == nil {
			t.Errorf("accepted %+v", bad.Changes)
		}
	}
}
