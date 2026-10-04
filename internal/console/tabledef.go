package console

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/israel-duff/pgdock/internal/provision"
)

// The table editor's footer: how many rows a table (or its filtered view)
// has, and its definition as DDL.

const (
	// estimateAbove is where an unfiltered count uses the planner's
	// estimate instead of counting (as Supabase Studio does).
	estimateAbove = 50_000
	// countTimeout bounds an exact count.
	countTimeout = "5s"
)

// RowCount is the number of rows the grid shows.
type RowCount struct {
	// Count is nil when an exact count took too long and there is no
	// estimate for a filtered grid.
	Count     *int64 `json:"count"`
	Estimated bool   `json:"estimated"`
}

// Count counts a table's rows, filtered as the grid is: the estimate for a
// big unfiltered table, an exact count otherwise (falling back to the
// estimate when that takes too long).
func (s *Service) Count(ctx context.Context, projectID uuid.UUID, schema, table string, filters []Filter, expr string) (RowCount, error) {
	var out RowCount
	err := s.readOnlyAs(ctx, projectID, GridQuery{Where: expr}.mode(), func(conn *pgx.Conn) error {
		r, err := lookupRelation(ctx, conn, schema, table)
		if err != nil {
			return err
		}
		rel := provision.Ident(schema) + "." + provision.Ident(table)
		var est *int64
		if err := conn.QueryRow(ctx, `SELECT CASE WHEN reltuples < 0 THEN NULL ELSE reltuples::int8 END FROM pg_class WHERE oid = to_regclass($1)`, rel).Scan(&est); err != nil {
			return err
		}
		if len(filters) == 0 && strings.TrimSpace(expr) == "" && est != nil && *est >= estimateAbove {
			out.Count, out.Estimated = est, true
			return nil
		}
		var params [][]byte
		cond, err := where(r, filters, expr, &params)
		if err != nil {
			return err
		}
		sql := "SELECT count(*)::text FROM " + rel
		if cond != "" {
			sql += " WHERE " + cond
		}
		for _, stmt := range []string{"SAVEPOINT pgdock_count", "SET LOCAL statement_timeout = '" + countTimeout + "'"} {
			if _, err := conn.Exec(ctx, stmt); err != nil {
				return err
			}
		}
		res := conn.PgConn().ExecParams(ctx, sql, params, nil, nil, nil).Read()
		if res.Err != nil {
			var pe *pgconn.PgError
			if errors.As(res.Err, &pe) && pe.Code == "57014" { // statement timeout
				_, _ = conn.Exec(ctx, "ROLLBACK TO SAVEPOINT pgdock_count")
				if len(filters) == 0 && strings.TrimSpace(expr) == "" {
					out.Count, out.Estimated = est, true
				}
				return nil
			}
			return gridErrorIn(res.Err, sql, strings.TrimSpace(expr))
		}
		n, err := parseInt64(string(res.Rows[0][0]))
		if err != nil {
			return err
		}
		out.Count = &n
		return nil
	})
	return out, err
}

func parseInt64(s string) (int64, error) {
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("not a count: " + s)
		}
		n = n*10 + int64(r-'0')
	}
	return n, nil
}

// Definition renders a table, view or materialized view as the DDL that
// would create it: columns, constraints, indexes and comments.
func (s *Service) Definition(ctx context.Context, projectID uuid.UUID, schema, table string) (string, error) {
	var out string
	err := s.readOnly(ctx, projectID, func(conn *pgx.Conn) error {
		r, err := lookupRelation(ctx, conn, schema, table)
		if err != nil {
			return err
		}
		out, err = definition(ctx, conn, r, schema, table)
		return err
	})
	return out, err
}

func definition(ctx context.Context, conn *pgx.Conn, r relation, schema, table string) (string, error) {
	rel := provision.Ident(schema) + "." + provision.Ident(table)
	var oid uint32
	var comment *string
	if err := conn.QueryRow(ctx, `SELECT to_regclass($1)::oid, obj_description(to_regclass($1), 'pg_class')`, rel).Scan(&oid, &comment); err != nil {
		return "", err
	}
	var b strings.Builder
	switch r.kind {
	case "v", "m":
		var def string
		if err := conn.QueryRow(ctx, `SELECT pg_get_viewdef($1, true)`, oid).Scan(&def); err != nil {
			return "", err
		}
		kw := "VIEW"
		if r.kind == "m" {
			kw = "MATERIALIZED VIEW"
		}
		b.WriteString("CREATE " + kw + " " + rel + " AS\n" + strings.TrimRight(def, "; \n") + ";\n")
	default:
		lines, comments, err := columnLines(ctx, conn, oid, rel)
		if err != nil {
			return "", err
		}
		rows, err := conn.Query(ctx, `
			SELECT conname, pg_get_constraintdef(oid) FROM pg_constraint
			WHERE conrelid = $1 AND contype IN ('p', 'u', 'f', 'c', 'x')
			ORDER BY contype = 'p' DESC, contype = 'u' DESC, contype = 'f' DESC, conname`, oid)
		if err != nil {
			return "", err
		}
		cons, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (string, error) {
			var name, def string
			err := row.Scan(&name, &def)
			return "CONSTRAINT " + provision.Ident(name) + " " + def, err
		})
		if err != nil {
			return "", err
		}
		lines = append(lines, cons...)
		kw := "TABLE"
		if r.kind == "f" {
			kw = "FOREIGN TABLE"
		}
		b.WriteString("CREATE " + kw + " " + rel + " (\n  " + strings.Join(lines, ",\n  ") + "\n)")
		if r.kind == "p" {
			var key string
			if err := conn.QueryRow(ctx, `SELECT pg_get_partkeydef($1)`, oid).Scan(&key); err != nil {
				return "", err
			}
			b.WriteString(" PARTITION BY " + key)
		}
		b.WriteString(";\n")
		rows, err = conn.Query(ctx, `
			SELECT pg_get_indexdef(i.indexrelid) FROM pg_index i
			WHERE i.indrelid = $1 AND NOT EXISTS (SELECT 1 FROM pg_constraint c WHERE c.conrelid = $1 AND c.conindid = i.indexrelid)
			ORDER BY i.indexrelid::regclass::text`, oid)
		if err != nil {
			return "", err
		}
		idx, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return "", err
		}
		if len(idx) > 0 {
			b.WriteString("\n" + strings.Join(idx, ";\n") + ";\n")
		}
		if len(comments) > 0 {
			b.WriteString("\n" + strings.Join(comments, "\n") + "\n")
		}
	}
	if comment != nil && *comment != "" {
		kw := map[string]string{"v": "VIEW", "m": "MATERIALIZED VIEW", "f": "FOREIGN TABLE"}[r.kind]
		if kw == "" {
			kw = "TABLE"
		}
		b.WriteString("\nCOMMENT ON " + kw + " " + rel + " IS " + literal(*comment) + ";\n")
	}
	return b.String(), nil
}

// columnLines renders a table's columns, and COMMENT statements for those
// with comments.
func columnLines(ctx context.Context, conn *pgx.Conn, oid uint32, rel string) ([]string, []string, error) {
	rows, err := conn.Query(ctx, `
		SELECT a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull, a.attidentity::text, a.attgenerated::text,
		       pg_get_expr(d.adbin, d.adrelid), col_description(a.attrelid, a.attnum)
		FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped ORDER BY a.attnum`, oid)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var lines, comments []string
	for rows.Next() {
		var name, typ, identity, generated string
		var notNull bool
		var def, comment *string
		if err := rows.Scan(&name, &typ, &notNull, &identity, &generated, &def, &comment); err != nil {
			return nil, nil, err
		}
		l := provision.Ident(name) + " " + typ
		switch {
		case identity == "a":
			l += " GENERATED ALWAYS AS IDENTITY"
		case identity == "d":
			l += " GENERATED BY DEFAULT AS IDENTITY"
		case generated == "s" && def != nil:
			l += " GENERATED ALWAYS AS (" + *def + ") STORED"
		case def != nil:
			l += " DEFAULT " + *def
		}
		if notNull && identity == "" {
			l += " NOT NULL"
		}
		lines = append(lines, l)
		if comment != nil && *comment != "" {
			comments = append(comments, "COMMENT ON COLUMN "+rel+"."+provision.Ident(name)+" IS "+literal(*comment)+";")
		}
	}
	return lines, comments, rows.Err()
}

func literal(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
