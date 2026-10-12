package services

import (
	"context"
	"slices"
	"strings"

	"github.com/israel-duff/pgdock/internal/datacat"
	"github.com/israel-duff/pgdock/internal/store"
)

// The API page's per-table docs and `pgdock policies list` (V4.1 §9.2,
// §9.4): what the data API exposes, from the same catalog as typegen,
// with each table's row-level security, its policies in the request
// roles' friendly names, and what anon and user may do.

// Described is a project's exposed API.
type Described struct {
	Ref       string
	Schemas   []string
	Tables    []TableDoc
	Functions []FunctionDoc
}

// TableDoc is one exposed relation.
type TableDoc struct {
	Schema, Name string
	Kind         string // table, view, materialized_view, foreign_table
	RLS          bool
	Public       bool // marked public: anon reads every row
	Columns      []ColumnDoc
	PrimaryKey   []string
	ForeignKeys  []ForeignKeyDoc // this table's
	ReferencedBy []ForeignKeyDoc // other tables' to this one
	Policies     []PolicyDoc
	// Access is what each request role may do, by its privileges (row-level
	// security then decides which rows).
	Access map[string]Access
}

// ColumnDoc is a column.
type ColumnDoc struct {
	Name      string
	Type      string
	Nullable  bool
	Default   string // the default expression, if any
	Identity  bool
	Generated bool
	Enum      []string
}

// ForeignKeyDoc is a foreign key, for embeds.
type ForeignKeyDoc struct {
	Name     string
	Columns  []string
	Table    string // the other side, schema.name
	RefCols  []string
	Embed    string // how to embed the other side in a select
	Multiple bool   // the other side is many rows
}

// PolicyDoc is a row-level security policy.
type PolicyDoc struct {
	Name       string
	Command    string // ALL, SELECT, INSERT, UPDATE, DELETE
	Permissive bool
	Roles      []string // anon, user, service, everyone, or a database role
	Using      string
	Check      string
}

// Access is a role's table privileges.
type Access struct {
	Select, Insert, Update, Delete bool
}

// FunctionDoc is a function callable at /data/v1/rpc/{name}.
type FunctionDoc struct {
	Schema, Name string
	Args         []ArgDoc
	Returns      string
	ReturnsSet   bool
	Volatility   string // immutable, stable, volatile
	Definer      bool
}

// ArgDoc is a function argument.
type ArgDoc struct {
	Name     string
	Type     string
	Optional bool
}

// FriendlyRole names a request role the way the API docs do.
func FriendlyRole(db, role string) string {
	switch role {
	case store.AnonRole(db):
		return "anon"
	case store.UserRole(db):
		return "user"
	case store.ServiceRole(db):
		return "service"
	case "public":
		return "everyone"
	}
	return role
}

var relKinds = map[byte]string{datacat.KindTable: "table", datacat.KindPart: "table", datacat.KindView: "view",
	datacat.KindMatView: "materialized_view", datacat.KindForeign: "foreign_table"}

// Describe reads p's exposed API.
func (s *Service) Describe(ctx context.Context, p store.Project) (Described, error) {
	schemas, public, err := s.exposure(ctx, p.ID)
	if err != nil {
		return Described{}, err
	}
	isPublic := map[string]bool{}
	for _, t := range public {
		if !strings.Contains(t, ".") {
			t = "public." + t
		}
		isPublic[t] = true
	}
	out := Described{Schemas: schemas, Tables: []TableDoc{}, Functions: []FunctionDoc{}}
	if svc, err := store.New(s.db).GetProjectServices(ctx, p.ID); err == nil {
		out.Ref = svc.Ref
	}
	conn, err := s.projects.AdminConn(ctx, p.InstanceID, p.DbName)
	if err != nil {
		return out, err
	}
	defer conn.Close(context.Background())
	cat, err := datacat.Introspect(ctx, conn, schemas)
	if err != nil {
		return out, err
	}

	// What the catalog doesn't carry: default expressions, identity, policies
	// and the request roles' privileges.
	defaults := map[string]string{}
	identity := map[string]bool{}
	rows, err := conn.Query(ctx, `SELECT n.nspname, c.relname, a.attname, coalesce(pg_get_expr(d.adbin, d.adrelid), ''), a.attidentity <> ''
		FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum AND a.attgenerated = ''
		WHERE n.nspname = ANY($1) AND a.attnum > 0 AND NOT a.attisdropped AND (d.oid IS NOT NULL OR a.attidentity <> '')`, schemas)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var sch, tbl, col, def string
		var ident bool
		if err := rows.Scan(&sch, &tbl, &col, &def, &ident); err != nil {
			rows.Close()
			return out, err
		}
		k := sch + "." + tbl + "." + col
		defaults[k], identity[k] = def, ident
	}
	rows.Close()
	policies := map[string][]PolicyDoc{}
	rows, err = conn.Query(ctx, `SELECT schemaname::text, tablename::text, policyname::text, cmd, permissive = 'PERMISSIVE', roles::text[],
		coalesce(qual, ''), coalesce(with_check, '') FROM pg_policies WHERE schemaname = ANY($1) ORDER BY 1, 2, 3`, schemas)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var sch, tbl string
		var pd PolicyDoc
		var roles []string
		if err := rows.Scan(&sch, &tbl, &pd.Name, &pd.Command, &pd.Permissive, &roles, &pd.Using, &pd.Check); err != nil {
			rows.Close()
			return out, err
		}
		for _, r := range roles {
			pd.Roles = append(pd.Roles, FriendlyRole(p.DbName, r))
		}
		policies[sch+"."+tbl] = append(policies[sch+"."+tbl], pd)
	}
	rows.Close()
	access := map[string]map[string]Access{}
	roleNames := map[string]string{store.AnonRole(p.DbName): "anon", store.UserRole(p.DbName): "user"}
	rows, err = conn.Query(ctx, `SELECT n.nspname, c.relname, r.rolname,
		has_table_privilege(r.oid, c.oid, 'SELECT'), has_table_privilege(r.oid, c.oid, 'INSERT'),
		has_table_privilege(r.oid, c.oid, 'UPDATE'), has_table_privilege(r.oid, c.oid, 'DELETE')
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace, pg_roles r
		WHERE n.nspname = ANY($1) AND c.relkind IN ('r', 'p', 'v', 'm', 'f') AND r.rolname = ANY($2)
		  AND has_schema_privilege(r.oid, n.oid, 'USAGE')`, schemas, []string{store.AnonRole(p.DbName), store.UserRole(p.DbName)})
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var sch, tbl, role string
		var a Access
		if err := rows.Scan(&sch, &tbl, &role, &a.Select, &a.Insert, &a.Update, &a.Delete); err != nil {
			rows.Close()
			return out, err
		}
		k := sch + "." + tbl
		if access[k] == nil {
			access[k] = map[string]Access{}
		}
		access[k][roleNames[role]] = a
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	for _, t := range cat.Tables() {
		key := t.Schema + "." + t.Name
		td := TableDoc{Schema: t.Schema, Name: t.Name, Kind: relKinds[t.Kind], RLS: t.RLS, Public: isPublic[key],
			PrimaryKey: append([]string{}, t.PK...), Columns: []ColumnDoc{}, ForeignKeys: []ForeignKeyDoc{}, ReferencedBy: []ForeignKeyDoc{},
			Policies: policies[key], Access: access[key]}
		if td.Policies == nil {
			td.Policies = []PolicyDoc{}
		}
		if td.Access == nil {
			td.Access = map[string]Access{}
		}
		for _, c := range t.Columns {
			k := key + "." + c.Name
			td.Columns = append(td.Columns, ColumnDoc{Name: c.Name, Type: c.Type, Nullable: c.Nullable, Default: defaults[k],
				Identity: identity[k], Generated: c.Generated, Enum: c.Enum})
		}
		for _, fk := range t.Out {
			td.ForeignKeys = append(td.ForeignKeys, ForeignKeyDoc{Name: fk.Name, Columns: fk.Cols, Table: fk.Ref.Schema + "." + fk.Ref.Name,
				RefCols: fk.RefCols, Embed: embedName(cat, fk.Ref), Multiple: false})
		}
		for _, fk := range t.In {
			td.ReferencedBy = append(td.ReferencedBy, ForeignKeyDoc{Name: fk.Name, Columns: fk.Cols, Table: fk.Table.Schema + "." + fk.Table.Name,
				RefCols: fk.RefCols, Embed: embedName(cat, fk.Table), Multiple: true})
		}
		out.Tables = append(out.Tables, td)
	}
	var names []string
	for k := range cat.Functions {
		names = append(names, k)
	}
	slices.Sort(names)
	vol := map[byte]string{'i': "immutable", 's': "stable", 'v': "volatile"}
	for _, k := range names {
		for _, f := range cat.Functions[k] {
			fd := FunctionDoc{Schema: f.Schema, Name: f.Name, Returns: f.Returns, ReturnsSet: f.RetSet, Volatility: vol[f.Volatile],
				Definer: f.Definer, Args: []ArgDoc{}}
			for _, a := range f.Args {
				fd.Args = append(fd.Args, ArgDoc{Name: a.Name, Type: a.Type, Optional: a.HasDefault})
			}
			out.Functions = append(out.Functions, fd)
		}
	}
	return out, nil
}

// embedName is how a select embeds t: its bare name in the first exposed
// schema, else schema-qualified.
func embedName(cat *datacat.Catalog, t *datacat.Table) string {
	if cat.Find(t.Name) == t {
		return t.Name
	}
	return t.Schema + "." + t.Name
}

// Policies lists p's policies by table (`pgdock policies list`), or one
// table's ("schema.table" or a bare name in an exposed schema).
func (s *Service) Policies(ctx context.Context, p store.Project, table string) ([]TableDoc, error) {
	d, err := s.Describe(ctx, p)
	if err != nil {
		return nil, err
	}
	var out []TableDoc
	for _, t := range d.Tables {
		if t.Kind != "table" {
			continue
		}
		if table != "" && table != t.Name && table != t.Schema+"."+t.Name {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}
