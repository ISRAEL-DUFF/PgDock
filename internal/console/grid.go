package console

import (
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/israel-duff/pgdock/internal/provision"
)

// The grid (V2 §4.1): filters compiled to parameterised WHERE clauses,
// sorting, keyset or offset pagination, xmin for conflict detection, and
// exports.

const (
	// MaxExport caps a CSV or JSON export (V2 §4.1).
	MaxExport = 100_000
	// LargeOffset is where offset pagination warns.
	LargeOffset = 10_000
	xminCol     = "__pgdock_xmin"
)

// ErrBadQuery is an invalid filter or sort.
var ErrBadQuery = errors.New("invalid filter or sort")

// Filter is one column condition.
type Filter struct {
	Column string   `json:"column"`
	Op     string   `json:"op"` // eq neq lt lte gt gte contains is_null not_null in
	Value  *string  `json:"value,omitempty"`
	Values []string `json:"values,omitempty"`
}

// Sort orders the grid by a column.
type Sort struct {
	Column string `json:"column"`
	Desc   bool   `json:"desc"`
}

// GridQuery is what the grid asks for.
type GridQuery struct {
	Filters []Filter
	Sort    *Sort
	After   string
	Limit   int
}

// Page is one page of a table's rows.
type Page struct {
	Columns []Column    `json:"columns"`
	Rows    [][]*string `json:"rows"`
	// Xmin holds each row's xmin when the table is editable (V2 §4.2).
	Xmin []string `json:"xmin,omitempty"`
	// Next is the cursor of the following page; empty on the last one.
	Next string `json:"next,omitempty"`
	// Order is how rows are paged: "primary_key" (keyset), "ctid", or
	// "offset" (a sort on another column, or a view).
	Order   string   `json:"order"`
	KeyCols []string `json:"key_columns"`
	// LargeOffset warns that offset paging is getting slow.
	LargeOffset bool `json:"large_offset,omitempty"`
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

// relation is what the grid knows of a table before querying it.
type relation struct {
	kind string // pg_class.relkind
	pk   []string
	cols []string
}

func lookupRelation(ctx context.Context, conn *pgx.Conn, schema, table string) (relation, error) {
	var r relation
	err := conn.QueryRow(ctx, `
		SELECT c.relkind::text,
		       COALESCE((SELECT array_agg(a.attname ORDER BY k.ord) FROM pg_index ix
		                 CROSS JOIN LATERAL unnest(ix.indkey) WITH ORDINALITY AS k(attnum, ord)
		                 JOIN pg_attribute a ON a.attrelid = ix.indrelid AND a.attnum = k.attnum
		                 WHERE ix.indrelid = c.oid AND ix.indisprimary), '{}'),
		       COALESCE((SELECT array_agg(a.attname ORDER BY a.attnum) FROM pg_attribute a
		                 WHERE a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped), '{}')
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind IN ('r', 'p', 'v', 'm', 'f') AND `+userSchemas,
		schema, table).Scan(&r.kind, &r.pk, &r.cols)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNoTable
	}
	return r, err
}

// editableKind: plain tables (not views, partitioned parents or foreign
// tables) can be edited when they have a primary key.
func (r relation) editable() bool { return r.kind == "r" && len(r.pk) > 0 }

// where compiles filters to a parameterised condition. Column names are
// checked against the table and quoted; values are always parameters.
func where(r relation, filters []Filter, params *[][]byte) (string, error) {
	var conds []string
	ph := func(v string) string {
		*params = append(*params, []byte(v))
		return "$" + strconv.Itoa(len(*params))
	}
	for _, f := range filters {
		if !slices.Contains(r.cols, f.Column) {
			return "", fmt.Errorf("%w: no column %q", ErrBadQuery, f.Column)
		}
		col := provision.Ident(f.Column)
		needValue := func() (string, error) {
			if f.Value == nil {
				return "", fmt.Errorf("%w: %s needs a value", ErrBadQuery, f.Op)
			}
			return *f.Value, nil
		}
		ops := map[string]string{"eq": "=", "neq": "IS DISTINCT FROM", "lt": "<", "lte": "<=", "gt": ">", "gte": ">="}
		switch op := f.Op; {
		case ops[op] != "":
			v, err := needValue()
			if err != nil {
				return "", err
			}
			conds = append(conds, col+" "+ops[op]+" "+ph(v))
		case op == "contains":
			v, err := needValue()
			if err != nil {
				return "", err
			}
			conds = append(conds, "strpos(lower("+col+"::text), lower("+ph(v)+")) > 0")
		case op == "is_null":
			conds = append(conds, col+" IS NULL")
		case op == "not_null":
			conds = append(conds, col+" IS NOT NULL")
		case op == "in":
			if len(f.Values) == 0 || len(f.Values) > 1000 {
				return "", fmt.Errorf("%w: in takes 1 to 1,000 values", ErrBadQuery)
			}
			conds = append(conds, col+" = ANY("+ph(arrayLiteral(f.Values))+")")
		default:
			return "", fmt.Errorf("%w: unknown operator %q", ErrBadQuery, f.Op)
		}
	}
	return strings.Join(conds, " AND "), nil
}

// arrayLiteral renders values as a Postgres array literal, every element
// quoted.
func arrayLiteral(vals []string) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, v := range vals {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

// gridSQL builds the page query. extra is the number of hidden leading
// columns (xmin, ctid).
type gridSQL struct {
	sql    string
	params [][]byte
	extra  int
	order  string
	xmin   bool
	keyed  bool // keyset on the primary key
}

func buildGrid(r relation, schema, table string, q GridQuery, cur cursor, limit int) (gridSQL, error) {
	g := gridSQL{}
	rel := provision.Ident(schema) + "." + provision.Ident(table)
	cond, err := where(r, q.Filters, &g.params)
	if err != nil {
		return g, err
	}
	conds := []string{}
	if cond != "" {
		conds = append(conds, cond)
	}
	if q.Sort != nil && !slices.Contains(r.cols, q.Sort.Column) {
		return g, fmt.Errorf("%w: no column %q", ErrBadQuery, q.Sort.Column)
	}
	sel := "*"
	if r.editable() {
		g.xmin, g.extra = true, 1
		sel = "xmin::text AS " + provision.Ident(xminCol) + ", *"
	}
	dir := ""
	if q.Sort != nil && q.Sort.Desc {
		dir = " DESC"
	}
	var order string
	switch {
	case len(r.pk) > 0 && (q.Sort == nil || (len(r.pk) == 1 && q.Sort.Column == r.pk[0])):
		g.order, g.keyed = "primary_key", true
		if cur.CTID != "" || cur.Offset != 0 || (cur.Key != nil && len(cur.Key) != len(r.pk)) {
			return g, ErrBadCursor
		}
		cols := make([]string, len(r.pk))
		for i, c := range r.pk {
			cols[i] = provision.Ident(c)
		}
		key := strings.Join(cols, ", ")
		if cur.Key != nil {
			ph := make([]string, len(cur.Key))
			for i, v := range cur.Key {
				g.params = append(g.params, []byte(v))
				ph[i] = "$" + strconv.Itoa(len(g.params))
			}
			cmp := ">"
			if dir != "" {
				cmp = "<"
			}
			conds = append(conds, "("+key+") "+cmp+" ("+strings.Join(ph, ", ")+")")
		}
		parts := make([]string, len(cols))
		for i, c := range cols {
			parts[i] = c + dir
		}
		order = " ORDER BY " + strings.Join(parts, ", ")
	case q.Sort == nil && (r.kind == "r" || r.kind == "m"):
		g.order, g.extra = "ctid", g.extra+1
		sel = "ctid, " + sel
		if cur.Key != nil || cur.Offset != 0 {
			return g, ErrBadCursor
		}
		if cur.CTID != "" {
			g.params = append(g.params, []byte(cur.CTID))
			conds = append(conds, "ctid > $"+strconv.Itoa(len(g.params))+"::tid")
		}
		order = " ORDER BY ctid"
	default:
		g.order = "offset"
		if cur.Key != nil || cur.CTID != "" {
			return g, ErrBadCursor
		}
		if q.Sort != nil {
			order = " ORDER BY " + provision.Ident(q.Sort.Column) + dir
			if dir != "" {
				order += " NULLS LAST"
			}
			// A stable tiebreak, so pages don't repeat rows.
			for _, c := range r.pk {
				order += ", " + provision.Ident(c)
			}
		}
	}
	g.sql = "SELECT " + sel + " FROM " + rel
	if len(conds) > 0 {
		g.sql += " WHERE " + strings.Join(conds, " AND ")
	}
	g.sql += order
	if g.order == "offset" && cur.Offset > 0 {
		g.sql += " OFFSET " + strconv.FormatInt(cur.Offset, 10)
	}
	if limit > 0 {
		g.sql += " LIMIT " + strconv.Itoa(limit)
	}
	return g, nil
}

// Rows returns a page of a table's rows (spec §8.6), filtered and sorted
// as asked (V2 §4.1).
func (s *Service) Rows(ctx context.Context, projectID uuid.UUID, schema, table string, q GridQuery) (Page, error) {
	limit := q.Limit
	if limit <= 0 || limit > PageSize {
		limit = PageSize
	}
	cur, err := decodeCursor(q.After)
	if err != nil {
		return Page{}, err
	}
	var page Page
	err = s.readOnly(ctx, projectID, func(conn *pgx.Conn) error {
		r, err := lookupRelation(ctx, conn, schema, table)
		if err != nil {
			return err
		}
		g, err := buildGrid(r, schema, table, q, cur, limit+1)
		if err != nil {
			return err
		}
		page.Order, page.KeyCols = g.order, r.pk
		if !g.keyed {
			page.KeyCols = []string{}
		}
		page.LargeOffset = g.order == "offset" && cur.Offset >= LargeOffset
		rr := conn.PgConn().ExecParams(ctx, g.sql, g.params, nil, nil, nil)
		fds := rr.FieldDescriptions()
		keyIdx := make([]int, len(r.pk))
		for i, k := range r.pk {
			keyIdx[i] = -1
			for j, fd := range fds {
				if j >= g.extra && fd.Name == k {
					keyIdx[i] = j
					break
				}
			}
		}
		ctidIdx := -1
		if g.order == "ctid" {
			ctidIdx = 0
		}
		page.Rows = [][]*string{}
		if g.xmin {
			page.Xmin = []string{}
		}
		var last [][]byte
		for rr.NextRow() {
			vals := rr.Values()
			if len(page.Rows) == limit {
				switch g.order {
				case "primary_key":
					key := make([]string, len(r.pk))
					for i, j := range keyIdx {
						if j < 0 || last[j] == nil {
							return fmt.Errorf("primary key column %s not in the result", r.pk[i])
						}
						key[i] = string(last[j])
					}
					page.Next = encodeCursor(cursor{Key: key})
				case "ctid":
					page.Next = encodeCursor(cursor{CTID: string(last[ctidIdx])})
				default:
					page.Next = encodeCursor(cursor{Offset: cur.Offset + int64(limit)})
				}
				continue
			}
			last = make([][]byte, len(vals))
			row := make([]*string, 0, len(vals)-g.extra)
			for i, v := range vals {
				if v != nil {
					last[i] = append([]byte(nil), v...)
				}
				if i < g.extra {
					if g.xmin && fds[i].Name == xminCol {
						page.Xmin = append(page.Xmin, string(v))
					}
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
			return gridError(err)
		}
		page.Columns = []Column{}
		for _, fd := range fds[g.extra:] {
			page.Columns = append(page.Columns, Column{Name: fd.Name, oid: fd.DataTypeOID})
		}
		res := []Result{{Columns: page.Columns}}
		s.typeNames(ctx, conn, res)
		page.Columns = res[0].Columns
		return nil
	})
	return page, err
}

// QueryError is a Postgres error from a grid query (a bad filter value).
type QueryError struct{ Err *Error }

func (e *QueryError) Error() string { return e.Err.Message }

func gridError(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		if pe.Code == "42501" {
			return fmt.Errorf("%w: %s", ErrDenied, pe.Message)
		}
		// 22xxx data exceptions and 42883 (no operator) come from filter
		// values that don't fit the column.
		if strings.HasPrefix(pe.Code, "22") || pe.Code == "42883" || pe.Code == "42804" {
			return &QueryError{Err: toError(pe, false)}
		}
	}
	return err
}

// Export writes a table's filtered, sorted rows as CSV or JSON, at most
// MaxExport of them (V2 §4.1). It returns how many it wrote and whether
// there were more.
func (s *Service) Export(ctx context.Context, projectID uuid.UUID, schema, table string, q GridQuery, format string, w io.Writer) (int, bool, error) {
	if format != "csv" && format != "json" {
		return 0, false, fmt.Errorf("%w: format is csv or json", ErrBadQuery)
	}
	var n int
	var more bool
	err := s.readOnly(ctx, projectID, func(conn *pgx.Conn) error {
		r, err := lookupRelation(ctx, conn, schema, table)
		if err != nil {
			return err
		}
		q.After = ""
		g, err := buildGrid(r, schema, table, q, cursor{}, MaxExport+1)
		if err != nil {
			return err
		}
		rr := conn.PgConn().ExecParams(ctx, g.sql, g.params, nil, nil, nil)
		fds := rr.FieldDescriptions()[g.extra:]
		names := make([]string, len(fds))
		for i, fd := range fds {
			names[i] = fd.Name
		}
		var cw *csv.Writer
		if format == "csv" {
			cw = csv.NewWriter(w)
			_ = cw.Write(names)
		} else if _, err := io.WriteString(w, "["); err != nil {
			return err
		}
		for rr.NextRow() {
			if n == MaxExport {
				more = true
				continue
			}
			vals := rr.Values()[g.extra:]
			if cw != nil {
				rec := make([]string, len(vals))
				for i, v := range vals {
					rec[i] = string(v)
				}
				_ = cw.Write(rec)
			} else {
				obj := make(map[string]*string, len(vals))
				for i, v := range vals {
					if v != nil {
						str := string(v)
						obj[names[i]] = &str
					} else {
						obj[names[i]] = nil
					}
				}
				b, _ := json.Marshal(orderedRow{names, obj})
				if n > 0 {
					_, _ = io.WriteString(w, ",\n")
				}
				if _, err := w.Write(b); err != nil {
					return err
				}
			}
			n++
		}
		if _, err := rr.Close(); err != nil {
			return gridError(err)
		}
		if cw != nil {
			cw.Flush()
			return cw.Error()
		}
		_, err = io.WriteString(w, "]\n")
		return err
	})
	return n, more, err
}

// orderedRow marshals a row as an object in column order.
type orderedRow struct {
	names []string
	vals  map[string]*string
}

func (o orderedRow) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, n := range o.names {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(n)
		v, _ := json.Marshal(o.vals[n])
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}
