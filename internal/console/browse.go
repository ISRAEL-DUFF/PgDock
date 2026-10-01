package console

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/israel-duff/pgdock/internal/provision"
)

// PageSize is the table browser's page (spec §8.6).
const PageSize = 50

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
	p, err := s.active(ctx, projectID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout+15*time.Second)
	defer cancel()
	sess, err := s.open(ctx, p, uuid.New(), DefaultTimeout)
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
			out.Schemas = append(out.Schemas, SchemaNode{Name: n, Tables: []Table{}})
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
		return nil
	})
	return out, err
}

// Page is one page of a table's rows.
type Page struct {
	Columns []Column    `json:"columns"`
	Rows    [][]*string `json:"rows"`
	// Next is the cursor of the following page; empty on the last one.
	Next string `json:"next,omitempty"`
	// Order is how rows are paged: "primary_key", "ctid", or "offset" (a
	// view or a partitioned table without a primary key).
	Order   string   `json:"order"`
	KeyCols []string `json:"key_columns"`
}

// cursor is the decoded page cursor.
type cursor struct {
	Key    []string `json:"k,omitempty"`
	CTID   string   `json:"c,omitempty"`
	Offset int64    `json:"o,omitempty"`
}

func decodeCursor(s string) (cursor, error) {
	var c cursor
	if s == "" {
		return c, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(b, &c) != nil || c.Offset < 0 {
		return c, ErrBadCursor
	}
	return c, nil
}

func encodeCursor(c cursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Rows returns a page of a table's rows, keyset-paginated on the primary
// key where there is one (spec §8.6).
func (s *Service) Rows(ctx context.Context, projectID uuid.UUID, schema, table, after string, limit int) (Page, error) {
	if limit <= 0 || limit > PageSize {
		limit = PageSize
	}
	cur, err := decodeCursor(after)
	if err != nil {
		return Page{}, err
	}
	var page Page
	err = s.readOnly(ctx, projectID, func(conn *pgx.Conn) error {
		var kind string
		var pk []string
		err := conn.QueryRow(ctx, `
			SELECT c.relkind::text,
			       COALESCE((SELECT array_agg(a.attname ORDER BY k.ord) FROM pg_index ix
			                 CROSS JOIN LATERAL unnest(ix.indkey) WITH ORDINALITY AS k(attnum, ord)
			                 JOIN pg_attribute a ON a.attrelid = ix.indrelid AND a.attnum = k.attnum
			                 WHERE ix.indrelid = c.oid AND ix.indisprimary), '{}')
			FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind IN ('r', 'p', 'v', 'm', 'f') AND `+userSchemas,
			schema, table).Scan(&kind, &pk)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoTable
		}
		if err != nil {
			return err
		}
		rel := provision.Ident(schema) + "." + provision.Ident(table)
		var sql string
		var params [][]byte
		switch {
		case len(pk) > 0:
			page.Order, page.KeyCols = "primary_key", pk
			if cur.CTID != "" || cur.Offset != 0 || (cur.Key != nil && len(cur.Key) != len(pk)) {
				return ErrBadCursor
			}
			cols := make([]string, len(pk))
			for i, c := range pk {
				cols[i] = provision.Ident(c)
			}
			order := strings.Join(cols, ", ")
			sql = "SELECT * FROM " + rel
			if cur.Key != nil {
				ph := make([]string, len(pk))
				for i, v := range cur.Key {
					ph[i] = "$" + strconv.Itoa(i+1)
					params = append(params, []byte(v))
				}
				sql += " WHERE (" + order + ") > (" + strings.Join(ph, ", ") + ")"
			}
			sql += " ORDER BY " + order
		case kind == "r" || kind == "m":
			page.Order, page.KeyCols = "ctid", []string{}
			if cur.Key != nil || cur.Offset != 0 {
				return ErrBadCursor
			}
			sql = "SELECT ctid, * FROM " + rel
			if cur.CTID != "" {
				sql += " WHERE ctid > $1::tid"
				params = append(params, []byte(cur.CTID))
			}
			sql += " ORDER BY ctid"
		default:
			page.Order, page.KeyCols = "offset", []string{}
			if cur.Key != nil || cur.CTID != "" {
				return ErrBadCursor
			}
			sql = "SELECT * FROM " + rel + " OFFSET " + strconv.FormatInt(cur.Offset, 10)
		}
		sql += " LIMIT " + strconv.Itoa(limit+1)

		rr := conn.PgConn().ExecParams(ctx, sql, params, nil, nil, nil)
		fds := rr.FieldDescriptions()
		skip := 0
		if page.Order == "ctid" {
			skip = 1 // the ctid column only feeds the cursor
		}
		keyIdx := make([]int, len(pk))
		for i, k := range pk {
			keyIdx[i] = -1
			for j, fd := range fds {
				if fd.Name == k {
					keyIdx[i] = j
					break
				}
			}
		}
		page.Rows = [][]*string{}
		var last [][]byte
		for rr.NextRow() {
			vals := rr.Values()
			if len(page.Rows) == limit {
				switch page.Order {
				case "primary_key":
					key := make([]string, len(pk))
					for i, j := range keyIdx {
						if j < 0 || last[j] == nil {
							return fmt.Errorf("primary key column %s not in the result", pk[i])
						}
						key[i] = string(last[j])
					}
					page.Next = encodeCursor(cursor{Key: key})
				case "ctid":
					page.Next = encodeCursor(cursor{CTID: string(last[0])})
				default:
					page.Next = encodeCursor(cursor{Offset: cur.Offset + int64(limit)})
				}
				continue
			}
			last = make([][]byte, len(vals))
			row := make([]*string, 0, len(vals)-skip)
			for i, v := range vals {
				if v != nil {
					last[i] = append([]byte(nil), v...)
				}
				if i < skip {
					continue
				}
				if v == nil {
					row = append(row, nil)
					continue
				}
				str := cell(v)
				row = append(row, &str)
			}
			page.Rows = append(page.Rows, row)
		}
		if _, err := rr.Close(); err != nil {
			var pe *pgconn.PgError
			if errors.As(err, &pe) && pe.Code == "42501" {
				return fmt.Errorf("%w: %s", ErrDenied, pe.Message)
			}
			return err
		}
		page.Columns = []Column{}
		for _, fd := range fds[skip:] {
			page.Columns = append(page.Columns, Column{Name: fd.Name, oid: fd.DataTypeOID})
		}
		res := []Result{{Columns: page.Columns}}
		s.typeNames(ctx, conn, res)
		page.Columns = res[0].Columns
		return nil
	})
	return page, err
}

// ErrDenied means the project role may not read the table.
var ErrDenied = errors.New("permission denied")
