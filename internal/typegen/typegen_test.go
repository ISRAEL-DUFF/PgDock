package typegen

import (
	"go/format"
	"strings"
	"testing"

	"github.com/israel-duff/pgdock/internal/datacat"
)

func testCatalog() *datacat.Catalog {
	todos := &datacat.Table{Schema: "public", Name: "todos", Kind: datacat.KindTable, ByName: map[string]*datacat.Column{}}
	for _, c := range []*datacat.Column{
		{Name: "id", TypName: "int8", Category: 'N', HasDefault: true},
		{Name: "owner_id", TypName: "uuid", Category: 'U'},
		{Name: "title", TypName: "text", Category: 'S'},
		{Name: "done", TypName: "bool", Category: 'B', HasDefault: true},
		{Name: "meta", TypName: "jsonb", Category: 'U', Nullable: true},
		{Name: "tags", TypName: "_text", Category: 'A', Nullable: true},
		{Name: "status", TypName: "todo_status", Category: 'E', Enum: []string{"open", "closed"}, HasDefault: true},
		{Name: "created_at", TypName: "timestamptz", Category: 'D', HasDefault: true},
		{Name: "search", TypName: "tsvector", Category: 'U', Nullable: true, Generated: true},
	} {
		todos.Columns = append(todos.Columns, c)
		todos.ByName[c.Name] = c
	}
	view := &datacat.Table{Schema: "public", Name: "open_todos", Kind: datacat.KindView, Columns: todos.Columns[:3]}
	cat := &datacat.Catalog{Schemas: []string{"public"}, ByName: map[string]*datacat.Table{"public.todos": todos, "public.open_todos": view},
		Ordered: []*datacat.Table{view, todos}, Functions: map[string][]*datacat.Function{
			"public.search_todos": {{Schema: "public", Name: "search_todos", RetSet: true, Volatile: 's',
				Args:    []datacat.Arg{{Name: "q", TypName: "text", Category: 'S'}, {Name: "max", TypName: "int4", Category: 'N', HasDefault: true}},
				Columns: todos.Columns[:3]}},
		}}
	return cat
}

func TestTS(t *testing.T) {
	out, err := Generate(testCatalog(), TS, Options{Ref: "k7f3m2q9"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"export interface Database {",
		"title: string;",
		"meta: Json | null;",
		"tags: string[] | null;",
		`status: "open" | "closed";`,
		"id?: number;",      // insert: has a default
		"owner_id: string;", // insert: required
		"search_todos: {",
		"Args: { q: string; max?: number; };",
		"export type Todos = Database[\"public\"][\"Tables\"][\"todos\"][\"Row\"];",
		"export type OpenTodos = Database[\"public\"][\"Views\"][\"open_todos\"][\"Row\"];",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("TS lacks %q:\n%s", want, out)
		}
	}
	// A generated column is in the row, never in an insert.
	ins := out[strings.Index(out, "Insert: {"):strings.Index(out, "Update: {")]
	if strings.Contains(ins, "search") {
		t.Error("the generated column is insertable")
	}
}

func TestGo(t *testing.T) {
	out, err := Generate(testCatalog(), Go, Options{Ref: "k7f3m2q9", Package: "todo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := format.Source([]byte(out)); err != nil {
		t.Fatalf("generated Go doesn't parse: %v\n%s", err, out)
	}
	for _, want := range []string{"package todo", "type Todos struct", "OwnerID string `json:\"owner_id\"`",
		"ID *int64 `json:\"id,omitempty\"`", "CreatedAt time.Time `json:\"created_at\"`", "const TodosTable = \"todos\"", "type TodosUpdate struct"} {
		if !strings.Contains(out, want) {
			t.Errorf("Go lacks %q:\n%s", want, out)
		}
	}
}

func TestDart(t *testing.T) {
	out, err := Generate(testCatalog(), Dart, Options{Ref: "k7f3m2q9"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"class Todos {", "final DateTime createdAt;", "createdAt: DateTime.parse(json['created_at'] as String),",
		"factory Todos.fromJson(Map<String, dynamic> json)", "'created_at': createdAt.toIso8601String(),", "final List<String>? tags;"} {
		if !strings.Contains(out, want) {
			t.Errorf("Dart lacks %q:\n%s", want, out)
		}
	}
	if _, err := Generate(testCatalog(), "cobol", Options{}); err == nil {
		t.Fatal("an unknown language")
	}
}
