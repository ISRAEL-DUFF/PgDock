package console

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PageSize is the table browser's page (spec §8.6).
const PageSize = 50

// MaxPageSize is the largest page the table editor asks for.
const MaxPageSize = 1000

// ErrNoTable means the schema or table does not exist (or is not one the
// browser shows).
var ErrNoTable = errors.New("no such table or view")

// ErrBadCursor means a page cursor did not come from this browser.
var ErrBadCursor = errors.New("invalid page cursor")

// Schema is a project database's browsable objects.
type Schema struct {
	Schemas []SchemaNode `json:"schemas"`
}

// SchemaNode is one schema and its tables and views.
type SchemaNode struct {
	Name   string  `json:"name"`
	Tables []Table `json:"tables"`
	Enums  []Enum  `json:"enums"`
}

// Enum is an enumerated type, for the table editor's type picker.
type Enum struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

// Table is a table, view, or materialized view.
type Table struct {
	Name        string     `json:"name"`
	Kind        string     `json:"kind"`
	RowEstimate *int64     `json:"row_estimate"`
	SizeBytes   int64      `json:"size_bytes"`
	Comment     *string    `json:"comment"`
	PrimaryKey  []string   `json:"primary_key"`
	Columns     []TableCol `json:"columns"`
	Indexes     []TableIdx `json:"indexes"`
	schema      string
	oid         uint32
}

// TableCol is a column of a table.
type TableCol struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Nullable bool    `json:"nullable"`
	Default  *string `json:"default"`
}

// TableIdx is an index on a table.
type TableIdx struct {
	Name       string `json:"name"`
	Definition string `json:"definition"`
	Primary    bool   `json:"primary"`
	Unique     bool   `json:"unique"`
}

var relKinds = map[string]string{
	"r": "table", "p": "partitioned_table", "v": "view", "m": "materialized_view", "f": "foreign_table",
}

// userSchemas excludes system schemas.
const userSchemas = `n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg\_toast%' AND n.nspname NOT LIKE 'pg\_temp%'`

// readOnly runs f in a read-only transaction of a console session.
func (s *Service) readOnly(ctx context.Context, projectID uuid.UUID, f func(*pgx.Conn) error) error {
	return s.readOnlyAs(ctx, projectID, modeOwner, f)
}

// readOnlyAs is readOnly as the owner (modeOwner) or as the project's
// read-only role (modeReadOnly), both inside BEGIN READ ONLY.
func (s *Service) readOnlyAs(ctx context.Context, projectID uuid.UUID, m mode, f func(*pgx.Conn) error) error {
	p, err := s.active(ctx, projectID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout+15*time.Second)
	defer cancel()
	sess, err := s.open(ctx, p, uuid.New(), DefaultTimeout, m)
	if err != nil {
		return err
	}
	defer sess.close()
	if _, err := sess.conn.Exec(ctx, "BEGIN READ ONLY"); err != nil {
		return err
	}
	defer func() { _, _ = sess.conn.Exec(context.Background(), "ROLLBACK") }()
	return f(sess.conn)
}

// Schema returns the schema tree with columns and indexes (spec §8.6).
func (s *Service) Schema(ctx context.Context, projectID uuid.UUID) (Schema, error) {
	out := Schema{Schemas: []SchemaNode{}}
	err := s.readOnly(ctx, projectID, func(conn *pgx.Conn) error {
		rows, err := conn.Query(ctx, `SELECT n.nspname FROM pg_namespace n WHERE `+userSchemas+` ORDER BY n.nspname = 'public' DESC, 1`)
		if err != nil {
			return err
		}
		names, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		idx := map[string]int{}
		for i, n := range names {
			idx[n] = i
			out.Schemas = append(out.Schemas, SchemaNode{Name: n, Tables: []Table{}, Enums: []Enum{}})
		}

		rows, err = conn.Query(ctx, `
			SELECT c.oid::int8, n.nspname, c.relname, c.relkind::text,
			       CASE WHEN c.reltuples < 0 THEN NULL ELSE c.reltuples::int8 END,
			       pg_total_relation_size(c.oid), obj_description(c.oid, 'pg_class'),
			       COALESCE((SELECT array_agg(a.attname ORDER BY k.ord) FROM pg_index ix
			                 CROSS JOIN LATERAL unnest(ix.indkey) WITH ORDINALITY AS k(attnum, ord)
			                 JOIN pg_attribute a ON a.attrelid = ix.indrelid AND a.attnum = k.attnum
			                 WHERE ix.indrelid = c.oid AND ix.indisprimary), '{}')
			FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f') AND NOT c.relispartition AND `+userSchemas+`
			ORDER BY 2, 3`)
		if err != nil {
			return err
		}
		byOID := map[uint32]*Table{}
		var tables []Table
		for rows.Next() {
			var t Table
			var oid int64
			var kind string
			if err := rows.Scan(&oid, &t.schema, &t.Name, &kind, &t.RowEstimate, &t.SizeBytes, &t.Comment, &t.PrimaryKey); err != nil {
				rows.Close()
				return err
			}
			t.oid, t.Kind = uint32(oid), relKinds[kind] //nolint:gosec // oids are 32-bit
			t.Columns, t.Indexes = []TableCol{}, []TableIdx{}
			tables = append(tables, t)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for i := range tables {
			byOID[tables[i].oid] = &tables[i]
		}

		rows, err = conn.Query(ctx, `
			SELECT a.attrelid::int8, a.attname, format_type(a.atttypid, a.atttypmod), NOT a.attnotnull,
			       pg_get_expr(d.adbin, d.adrelid)
			FROM pg_attribute a
			JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
			LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
			WHERE a.attnum > 0 AND NOT a.attisdropped AND c.relkind IN ('r', 'p', 'v', 'm', 'f') AND `+userSchemas+`
			ORDER BY a.attrelid, a.attnum`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var oid int64
			var col TableCol
			if err := rows.Scan(&oid, &col.Name, &col.Type, &col.Nullable, &col.Default); err != nil {
				rows.Close()
				return err
			}
			if t := byOID[uint32(oid)]; t != nil { //nolint:gosec // oids are 32-bit
				t.Columns = append(t.Columns, col)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		rows, err = conn.Query(ctx, `
			SELECT ix.indrelid::int8, i.relname, pg_get_indexdef(ix.indexrelid), ix.indisprimary, ix.indisunique
			FROM pg_index ix
			JOIN pg_class i ON i.oid = ix.indexrelid JOIN pg_namespace n ON n.oid = i.relnamespace
			WHERE `+userSchemas+`
			ORDER BY ix.indrelid, NOT ix.indisprimary, i.relname`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var oid int64
			var ix TableIdx
			if err := rows.Scan(&oid, &ix.Name, &ix.Definition, &ix.Primary, &ix.Unique); err != nil {
				rows.Close()
				return err
			}
			if t := byOID[uint32(oid)]; t != nil { //nolint:gosec // oids are 32-bit
				t.Indexes = append(t.Indexes, ix)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		for _, t := range tables {
			if i, ok := idx[t.schema]; ok {
				out.Schemas[i].Tables = append(out.Schemas[i].Tables, t)
			}
		}

		rows, err = conn.Query(ctx, `
			SELECT n.nspname, t.typname, array_agg(e.enumlabel ORDER BY e.enumsortorder)
			FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace JOIN pg_enum e ON e.enumtypid = t.oid
			WHERE `+userSchemas+`
			GROUP BY n.nspname, t.typname ORDER BY 1, 2`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var schema string
			var en Enum
			if err := rows.Scan(&schema, &en.Name, &en.Values); err != nil {
				return err
			}
			if i, ok := idx[schema]; ok {
				out.Schemas[i].Enums = append(out.Schemas[i].Enums, en)
			}
		}
		return rows.Err()
	})
	return out, err
}

// ErrDenied means the project role may not read the table.
var ErrDenied = errors.New("permission denied")
