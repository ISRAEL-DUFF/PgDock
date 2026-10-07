package edge

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// The data API's view of a project's schema (V4 §3.1): the tables, views
// and their columns, keys and foreign keys in the exposed schemas, read
// from the catalog. It is cached per project and re-read when a cheap
// fingerprint of the catalog changes (checked at most every
// fingerprintEvery, inside a request's transaction).
const fingerprintEvery = 2 * time.Second

// Relation kinds.
const (
	kindTable    = 'r'
	kindPart     = 'p'
	kindView     = 'v'
	kindMatView  = 'm'
	kindForeign  = 'f'
	maxRelations = 5000
)

// Column is one column.
type Column struct {
	Name     string
	Type     string // format_type, for casts
	Category byte   // pg_type.typcategory: N numeric, S string, B bool, D date/time, U user (uuid, json…), A array…
	TypName  string // pg_type.typname: int4, jsonb, tsvector…
	Nullable bool
	Num      int16
}

// JSON reports whether the column is json or jsonb.
func (c *Column) JSON() bool { return c.TypName == "json" || c.TypName == "jsonb" }

// ForeignKey is a foreign key from Table's Cols to Ref's RefCols.
type ForeignKey struct {
	Name    string
	Table   *Table
	Cols    []string
	Ref     *Table
	RefCols []string
}

// Table is a table, view, materialized view or foreign table.
type Table struct {
	Schema   string
	Name     string
	Kind     byte
	RLS      bool // row-level security enabled (tables)
	Invoker  bool // security_invoker (views)
	Columns  []*Column
	byName   map[string]*Column
	PK       []string
	Out      []*ForeignKey // this table's foreign keys
	In       []*ForeignKey // foreign keys that reference this table
	Estimate float64       // reltuples
}

// Col is the named column, or nil.
func (t *Table) Col(name string) *Column { return t.byName[name] }

// Qualified is the quoted schema.table.
func (t *Table) Qualified() string {
	return pgx.Identifier{t.Schema, t.Name}.Sanitize()
}

// Catalog is a project's exposed relations.
type Catalog struct {
	Schemas     []string
	Fingerprint string
	byName      map[string]*Table // "schema.name"
	ordered     []*Table
}

// Find resolves a table name in a URL: "schema.table", or "table" in the
// first exposed schema that has it.
func (c *Catalog) Find(name string) *Table {
	if t := c.byName[name]; t != nil && strings.Contains(name, ".") {
		return t
	}
	for _, s := range c.Schemas {
		if t := c.byName[s+"."+name]; t != nil {
			return t
		}
	}
	return nil
}

// Tables lists the relations in catalog order.
func (c *Catalog) Tables() []*Table { return c.ordered }

// catalogState is a project's cached catalog.
type catalogState struct {
	mu      sync.Mutex
	cat     *Catalog
	checked time.Time
	shapes  map[string]float64 // normalised query -> estimated cost
}

const fingerprintSQL = `SELECT
  (SELECT coalesce(sum(c.xmin::text::bigint), 0) + count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname = ANY($1))::text || ':' ||
  (SELECT coalesce(sum(a.xmin::text::bigint), 0) + count(*) FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid
    JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = ANY($1) AND a.attnum > 0)::text || ':' ||
  (SELECT coalesce(sum(k.xmin::text::bigint), 0) + count(*) FROM pg_constraint k JOIN pg_namespace n ON n.oid = k.connamespace
    WHERE n.nspname = ANY($1))::text`

// catalog returns p's catalog, re-reading it in tx when it may be stale.
func (e *Edge) catalog(ctx context.Context, p *project, tx pgx.Tx) (*Catalog, *catalogState, error) {
	st := p.catalog
	st.mu.Lock()
	defer st.mu.Unlock()
	schemas := p.exposed()
	fresh := st.cat != nil && time.Since(st.checked) < fingerprintEvery && sameStrings(st.cat.Schemas, schemas)
	if fresh {
		return st.cat, st, nil
	}
	var fp string
	if err := tx.QueryRow(ctx, fingerprintSQL, schemas).Scan(&fp); err != nil {
		return nil, nil, err
	}
	st.checked = time.Now()
	if st.cat != nil && st.cat.Fingerprint == fp && sameStrings(st.cat.Schemas, schemas) {
		return st.cat, st, nil
	}
	cat, err := introspect(ctx, tx, schemas)
	if err != nil {
		return nil, nil, err
	}
	cat.Fingerprint = fp
	st.cat, st.shapes = cat, map[string]float64{}
	return cat, st, nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func introspect(ctx context.Context, tx pgx.Tx, schemas []string) (*Catalog, error) {
	cat := &Catalog{Schemas: schemas, byName: map[string]*Table{}}
	rows, err := tx.Query(ctx, `SELECT c.oid, n.nspname, c.relname, c.relkind::text, c.relrowsecurity,
		  coalesce(c.reloptions::text[] && ARRAY['security_invoker=true', 'security_invoker=on', 'security_invoker=1'], false),
		  greatest(c.reltuples, 0)::float8
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = ANY($1) AND c.relkind IN ('r', 'p', 'v', 'm', 'f') AND NOT c.relispartition
		ORDER BY n.nspname, c.relname LIMIT $2`, schemas, maxRelations)
	if err != nil {
		return nil, err
	}
	byOID := map[uint32]*Table{}
	var oids []uint32
	for rows.Next() {
		var oid uint32
		var kind string
		t := &Table{byName: map[string]*Column{}}
		if err := rows.Scan(&oid, &t.Schema, &t.Name, &kind, &t.RLS, &t.Invoker, &t.Estimate); err != nil {
			rows.Close()
			return nil, err
		}
		t.Kind = kind[0]
		byOID[oid] = t
		oids = append(oids, oid)
		cat.byName[t.Schema+"."+t.Name] = t
		cat.ordered = append(cat.ordered, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(oids) == 0 {
		return cat, nil
	}
	rows, err = tx.Query(ctx, `SELECT a.attrelid, a.attname, format_type(a.atttypid, a.atttypmod), t.typcategory::text, t.typname,
		  NOT a.attnotnull, a.attnum
		FROM pg_attribute a JOIN pg_type t ON t.oid = a.atttypid
		WHERE a.attrelid = ANY($1) AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attrelid, a.attnum`, oids)
	if err != nil {
		return nil, err
	}
	nums := map[uint32]map[int16]string{}
	for rows.Next() {
		var oid uint32
		var cat string
		c := &Column{}
		if err := rows.Scan(&oid, &c.Name, &c.Type, &cat, &c.TypName, &c.Nullable, &c.Num); err != nil {
			rows.Close()
			return nil, err
		}
		c.Category = cat[0]
		t := byOID[oid]
		t.Columns = append(t.Columns, c)
		t.byName[c.Name] = c
		if nums[oid] == nil {
			nums[oid] = map[int16]string{}
		}
		nums[oid][c.Num] = c.Name
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = tx.Query(ctx, `SELECT conname, contype::text, conrelid, conkey, confrelid, confkey FROM pg_constraint
		WHERE contype IN ('p', 'f') AND conrelid = ANY($1) ORDER BY conrelid, conname`, oids)
	if err != nil {
		return nil, err
	}
	names := func(oid uint32, keys []int16) []string {
		out := make([]string, 0, len(keys))
		for _, k := range keys {
			out = append(out, nums[oid][k])
		}
		return out
	}
	for rows.Next() {
		var name, typ string
		var rel, frel uint32
		var keys, fkeys []int16
		if err := rows.Scan(&name, &typ, &rel, &keys, &frel, &fkeys); err != nil {
			rows.Close()
			return nil, err
		}
		t := byOID[rel]
		switch typ {
		case "p":
			t.PK = names(rel, keys)
		case "f":
			ref := byOID[frel]
			if ref == nil {
				continue // references a table outside the exposed schemas
			}
			fk := &ForeignKey{Name: name, Table: t, Cols: names(rel, keys), Ref: ref, RefCols: names(frel, fkeys)}
			t.Out = append(t.Out, fk)
			ref.In = append(ref.In, fk)
		}
	}
	rows.Close()
	return cat, rows.Err()
}
