package console

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/schemaedit"
	"github.com/israel-duff/pgdock/internal/store"
)

// Schema editing (V2 §4.3): preview a change as DDL with risk notes, run
// exactly what was previewed with lock_timeout 5s, or export it as a
// migration.

const (
	// DDLLockTimeout makes DDL fail fast instead of queueing behind a long
	// transaction and blocking everyone behind it.
	DDLLockTimeout = 5 * time.Second
	// DDLStatementTimeout bounds a rewrite or an index build.
	DDLStatementTimeout = 10 * time.Minute
)

// Errors.
var (
	// ErrStalePlan: the change would now produce different SQL than the
	// preview the user saw.
	ErrStalePlan = errors.New("the change no longer matches its preview; preview it again")
	// ErrConfirm: a drop needs the object's name typed.
	ErrConfirm = errors.New("type the name of what you are dropping to confirm")
)

// DDLError is Postgres refusing a schema change.
type DDLError struct {
	Err       *Error
	Statement string
}

func (e *DDLError) Error() string { return e.Err.Message }

var funcCall = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

// gatherState reads what the planner needs about the objects c touches.
func gatherState(ctx context.Context, conn *pgx.Conn, c schemaedit.Change) (schemaedit.State, error) {
	var st schemaedit.State
	schema := c.Schema
	if schema == "" {
		schema = "public"
	}
	// New types must exist (to_regtype also accepts modifiers).
	var types []string
	if c.Column != nil {
		types = append(types, c.Column.Type)
	}
	for _, col := range c.Columns {
		types = append(types, col.Type)
	}
	if c.Type != nil {
		types = append(types, *c.Type)
	}
	for _, t := range types {
		if !schemaedit.ValidType(t) {
			return st, fmt.Errorf("%w: %q is not a type name", schemaedit.ErrInvalid, t)
		}
		// "bigint generated always as identity": check the type part.
		base := t
		if i := strings.Index(strings.ToLower(t), " generated "); i > 0 {
			base = t[:i]
		}
		var ok bool
		if err := conn.QueryRow(ctx, `SELECT to_regtype($1) IS NOT NULL`, base).Scan(&ok); err != nil {
			return st, fmt.Errorf("%w: %q is not a type name", schemaedit.ErrInvalid, t)
		}
		if !ok {
			return st, fmt.Errorf("%w: there is no type %q", schemaedit.ErrInvalid, t)
		}
	}
	if c.Table != "" {
		var oid *uint32
		if err := conn.QueryRow(ctx, `SELECT to_regclass($1)::oid`, schemaedit.Ident(schema)+"."+schemaedit.Ident(c.Table)).Scan(&oid); err != nil {
			return st, err
		}
		if oid != nil {
			if err := conn.QueryRow(ctx, `SELECT pg_total_relation_size($1), GREATEST(reltuples, 0)::int8 FROM pg_class WHERE oid = $1`, *oid).Scan(&st.SizeBytes, &st.Rows); err != nil {
				return st, err
			}
			if st.Rows == 0 { // never analysed: look
				var hasRows bool
				_ = conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+schemaedit.Ident(schema)+"."+schemaedit.Ident(c.Table)+`)`).Scan(&hasRows)
				if hasRows {
					st.Rows = 1
				}
			}
			if c.ColumnName != "" {
				var typid uint32
				var typmod int32
				err := conn.QueryRow(ctx, `
					SELECT format_type(a.atttypid, a.atttypmod), NOT a.attnotnull, pg_get_expr(d.adbin, d.adrelid), a.atttypid, a.atttypmod
					FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
					WHERE a.attrelid = $1 AND a.attname = $2 AND a.attnum > 0 AND NOT a.attisdropped`, *oid, c.ColumnName).
					Scan(&st.ColumnType, &st.ColumnNullable, &st.ColumnDefault, &typid, &typmod)
				if errors.Is(err, pgx.ErrNoRows) {
					return st, fmt.Errorf("%w: %s has no column %s", schemaedit.ErrInvalid, c.Table, c.ColumnName)
				}
				if err != nil {
					return st, err
				}
				if c.Type != nil {
					if st.TypeRewrites, err = rewrites(ctx, conn, typid, typmod, *c.Type); err != nil {
						return st, err
					}
				}
			}
		}
	}
	if c.Column != nil && c.Column.Default != nil {
		var err error
		if st.VolatileDefault, err = volatile(ctx, conn, *c.Column.Default); err != nil {
			return st, err
		}
	}
	switch c.Kind {
	case schemaedit.DropConstraint:
		_ = conn.QueryRow(ctx, `SELECT pg_get_constraintdef(con.oid) FROM pg_constraint con
			WHERE con.conrelid = to_regclass($1) AND con.conname = $2`, schemaedit.Ident(schema)+"."+schemaedit.Ident(c.Table), c.Name).Scan(&st.ObjectDef)
	case schemaedit.DropIndex:
		_ = conn.QueryRow(ctx, `SELECT pg_get_indexdef(to_regclass($1))`, schemaedit.Ident(schema)+"."+schemaedit.Ident(c.Name)).Scan(&st.ObjectDef)
	}
	return st, nil
}

var lenMod = regexp.MustCompile(`\((\d+)(?:,\s*(\d+))?\)`)

// rewrites says whether changing a column from (typid, typmod) to newType
// rewrites the table: not when the type is the same with a wider modifier
// (varchar(10) → varchar(20), numeric(8,2) → numeric(10,2)) or the cast is
// binary-coercible (varchar → text).
func rewrites(ctx context.Context, conn *pgx.Conn, typid uint32, typmod int32, newType string) (bool, error) {
	var newOID uint32
	var oldFull, newFull string
	if err := conn.QueryRow(ctx, `SELECT to_regtype($1)::oid, format_type($2, $3), $1`, newType, typid, typmod).Scan(&newOID, &oldFull, &newFull); err != nil {
		return true, err
	}
	if newOID == typid {
		oldM, newM := lenMod.FindStringSubmatch(oldFull), lenMod.FindStringSubmatch(newFull)
		switch {
		case oldM == nil && newM == nil:
			return false, nil
		case newM == nil: // the limit removed
			return false, nil
		case oldM == nil: // a limit added: checks (and, for numeric, rewrites)
			return true, nil
		}
		widen := atoi(newM[1]) >= atoi(oldM[1]) && oldM[2] == newM[2]
		return !widen, nil
	}
	var binary bool
	err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_cast WHERE castsource = $1 AND casttarget = $2 AND castmethod = 'b')`, typid, newOID).Scan(&binary)
	return !binary, err
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}

// volatile reports whether a default expression calls a volatile function
// (gen_random_uuid(), random(), clock_timestamp(), nextval()…), which makes
// ADD COLUMN rewrite the table.
func volatile(ctx context.Context, conn *pgx.Conn, expr string) (bool, error) {
	var names []string
	for _, m := range funcCall.FindAllStringSubmatch(expr, -1) {
		names = append(names, strings.ToLower(m[1]))
	}
	if len(names) == 0 {
		return false, nil
	}
	var v bool
	err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_proc WHERE proname = ANY($1) AND provolatile = 'v')`, names).Scan(&v)
	return v, err
}

// PlanSchema previews a change: its DDL, risk notes, and reverse.
func (s *Service) PlanSchema(ctx context.Context, projectID uuid.UUID, c schemaedit.Change) (schemaedit.Plan, error) {
	var plan schemaedit.Plan
	err := s.readOnly(ctx, projectID, func(conn *pgx.Conn) error {
		st, err := gatherState(ctx, conn, c)
		if err != nil {
			return err
		}
		plan, err = schemaedit.Build(c, st)
		return err
	})
	return plan, err
}

// Applied is a schema change that ran.
type Applied struct {
	Plan       schemaedit.Plan `json:"plan"`
	DurationMS int64           `json:"duration_ms"`
}

// ApplySchema runs a previewed change: hash must match the preview, and
// confirm the name of anything dropped.
func (s *Service) ApplySchema(ctx context.Context, projectID uuid.UUID, c schemaedit.Change, hash, confirm string) (Applied, error) {
	p, err := s.active(ctx, projectID)
	if err != nil {
		return Applied{}, err
	}
	set, err := store.DecodeProjectSettings(p.Settings)
	if err != nil {
		return Applied{}, err
	}
	if set.ConsoleReadOnly {
		return Applied{}, ErrReadOnly
	}
	ctx, cancel := context.WithTimeout(ctx, DDLStatementTimeout+30*time.Second)
	defer cancel()
	sess, err := s.open(ctx, p, uuid.New(), DDLStatementTimeout, false)
	if err != nil {
		return Applied{}, err
	}
	defer sess.close()
	conn := sess.conn
	st, err := gatherState(ctx, conn, c)
	if err != nil {
		return Applied{}, err
	}
	plan, err := schemaedit.Build(c, st)
	if err != nil {
		return Applied{}, err
	}
	if hash != plan.Hash {
		return Applied{Plan: plan}, ErrStalePlan
	}
	if plan.Confirm != "" && confirm != plan.Confirm {
		return Applied{Plan: plan}, ErrConfirm
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf("SET lock_timeout = %d", DDLLockTimeout.Milliseconds())); err != nil {
		return Applied{}, err
	}
	start := time.Now()
	ddlErr := func(sql string, err error) error { return &DDLError{Err: toError(err, false), Statement: sql} }
	if plan.Transactional() {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return Applied{}, err
		}
		for _, stmt := range plan.Statements {
			// ExecParams: one statement only, whatever the expressions hold.
			if _, err := conn.PgConn().ExecParams(ctx, stmt.SQL, nil, nil, nil, nil).Close(); err != nil {
				_ = tx.Rollback(context.Background())
				return Applied{Plan: plan}, ddlErr(stmt.SQL, err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return Applied{Plan: plan}, ddlErr("COMMIT", err)
		}
	} else {
		for _, stmt := range plan.Statements {
			if _, err := conn.PgConn().ExecParams(ctx, stmt.SQL, nil, nil, nil, nil).Close(); err != nil {
				return Applied{Plan: plan}, ddlErr(stmt.SQL, err)
			}
		}
	}
	out := Applied{Plan: plan, DurationMS: time.Since(start).Milliseconds()}
	// New tables and schemas get the read-only role's grants (V2 §4.3).
	if err := s.projects.EnsureReadOnlyRole(ctx, p); err != nil {
		s.log.Warn("re-grant read-only role after a schema change", "project_id", p.ID, "err", err)
	}
	return out, nil
}
