package console

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/store"
)

// Row editing (V2 §4.2): staged changes saved in one transaction, each
// update and delete guarded by the row's xmin so nothing is silently
// overwritten.

const (
	// MaxChanges caps the rows one save may change.
	MaxChanges = 500
	// EditStatementTimeout and EditLockTimeout bound a save.
	EditStatementTimeout = 30 * time.Second
	EditLockTimeout      = 5 * time.Second
)

// Errors.
var (
	// ErrReadOnly: the project's console is read-only.
	ErrReadOnly = errors.New("the project's console is read-only")
	// ErrNotEditable: the table can't be edited (a view, no primary key…).
	ErrNotEditable = errors.New("the table can't be edited")
)

// Change is one staged edit. Values are text (nil for NULL), cast by
// Postgres to each column's type.
type Change struct {
	Op string `json:"op"` // insert, update, delete
	// Key is the row's primary key (update, delete).
	Key map[string]*string `json:"key,omitempty"`
	// Xmin is the row's xmin when it was loaded (update, delete).
	Xmin   string             `json:"xmin,omitempty"`
	Values map[string]*string `json:"values,omitempty"`
}

// SaveResult is a save's outcome: Applied, or why not.
type SaveResult struct {
	Applied bool `json:"applied"`
	// Rows are the inserted and updated rows as they are now, with their
	// new xmin, in change order (deletes have none).
	Rows    []SavedRow `json:"rows"`
	Columns []Column   `json:"columns"`
	// Conflict: someone changed or deleted a row since it was loaded.
	Conflict *Conflict `json:"conflict,omitempty"`
	// Failed is the change that Postgres refused, with its error.
	Failed *FailedChange `json:"failed,omitempty"`
	// Summary, e.g. "3 updates, 1 insert, 2 deletes".
	Summary string `json:"summary"`
}

// SavedRow is a row after the save.
type SavedRow struct {
	Index  int       `json:"index"`
	Xmin   string    `json:"xmin"`
	Values []*string `json:"values"`
}

// Conflict says which change hit a row that moved on, and what it holds
// now (Deleted when it is gone).
type Conflict struct {
	Index   int       `json:"index"`
	Deleted bool      `json:"deleted"`
	Xmin    string    `json:"xmin,omitempty"`
	Current []*string `json:"current,omitempty"`
}

// FailedChange is a change Postgres refused.
type FailedChange struct {
	Index int    `json:"index"`
	Error *Error `json:"error"`
}

// Summary describes a batch: "3 updates, 1 insert, 2 deletes".
func Summary(changes []Change) string {
	n := map[string]int{}
	for _, c := range changes {
		n[c.Op]++
	}
	var parts []string
	for _, op := range []string{"update", "insert", "delete"} {
		if n[op] == 0 {
			continue
		}
		w := op
		if n[op] > 1 {
			w += "s"
		}
		parts = append(parts, fmt.Sprintf("%d %s", n[op], w))
	}
	if len(parts) == 0 {
		return "no changes"
	}
	return strings.Join(parts, ", ")
}

// SaveRows applies changes to a table in one transaction (V2 §4.2).
func (s *Service) SaveRows(ctx context.Context, projectID uuid.UUID, schema, table string, changes []Change) (SaveResult, error) {
	res := SaveResult{Rows: []SavedRow{}, Columns: []Column{}, Summary: Summary(changes)}
	if len(changes) == 0 {
		return res, fmt.Errorf("%w: nothing to save", ErrBadQuery)
	}
	if len(changes) > MaxChanges {
		return res, fmt.Errorf("%w: at most %d changed rows per save", ErrBadQuery, MaxChanges)
	}
	p, err := s.active(ctx, projectID)
	if err != nil {
		return res, err
	}
	set, err := store.DecodeProjectSettings(p.Settings)
	if err != nil {
		return res, err
	}
	if set.ConsoleReadOnly {
		return res, ErrReadOnly
	}
	ctx, cancel := context.WithTimeout(ctx, EditStatementTimeout+15*time.Second)
	defer cancel()
	sess, err := s.open(ctx, p, uuid.New(), EditStatementTimeout, modeOwner)
	if err != nil {
		return res, err
	}
	defer sess.close()
	conn := sess.conn

	info, err := tableInfo(ctx, conn, schema, table)
	if err != nil {
		return res, err
	}
	if !info.Editable {
		return res, fmt.Errorf("%w: %s", ErrNotEditable, info.ReadOnlyReason)
	}
	writable := map[string]bool{}
	var allCols []string
	for _, c := range info.Columns {
		allCols = append(allCols, c.Name)
		writable[c.Name] = !c.Generated && c.Identity == ""
	}
	for i, c := range changes {
		if err := checkChange(c, info.PrimaryKey, allCols, writable); err != nil {
			return res, fmt.Errorf("change %d: %w", i+1, err)
		}
	}

	rel := provision.Ident(schema) + "." + provision.Ident(table)
	returning := " RETURNING xmin::text, *"
	if _, err := conn.Exec(ctx, fmt.Sprintf("BEGIN; SET LOCAL statement_timeout = %d; SET LOCAL lock_timeout = %d",
		EditStatementTimeout.Milliseconds(), EditLockTimeout.Milliseconds())); err != nil {
		return res, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.Exec(context.Background(), "ROLLBACK")
		}
	}()
	for i, c := range changes {
		var sql string
		var params [][]byte
		ph := func(v *string) string {
			if v == nil {
				params = append(params, nil)
			} else {
				params = append(params, []byte(*v))
			}
			return "$" + strconv.Itoa(len(params))
		}
		keyCond := func() string {
			parts := make([]string, 0, len(info.PrimaryKey)+1)
			for _, k := range info.PrimaryKey {
				parts = append(parts, provision.Ident(k)+" = "+ph(c.Key[k]))
			}
			x := c.Xmin
			parts = append(parts, "xmin::text = "+ph(&x))
			return strings.Join(parts, " AND ")
		}
		cols := sortedKeys(c.Values, allCols)
		switch c.Op {
		case "insert":
			if len(cols) == 0 {
				sql = "INSERT INTO " + rel + " DEFAULT VALUES" + returning
				break
			}
			names, vals := make([]string, len(cols)), make([]string, len(cols))
			for j, col := range cols {
				names[j], vals[j] = provision.Ident(col), ph(c.Values[col])
			}
			sql = "INSERT INTO " + rel + " (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(vals, ", ") + ")" + returning
		case "update":
			sets := make([]string, len(cols))
			for j, col := range cols {
				sets[j] = provision.Ident(col) + " = " + ph(c.Values[col])
			}
			sql = "UPDATE " + rel + " SET " + strings.Join(sets, ", ") + " WHERE " + keyCond() + returning
		case "delete":
			sql = "DELETE FROM " + rel + " WHERE " + keyCond() + " RETURNING xmin::text"
		}
		rr := conn.PgConn().ExecParams(ctx, sql, params, nil, nil, nil)
		var got [][]byte
		var fds []pgconn.FieldDescription
		for rr.NextRow() {
			got = slices.Clone(rr.Values())
			for j := range got {
				got[j] = slices.Clone(got[j])
			}
		}
		fds = rr.FieldDescriptions()
		tag, err := rr.Close()
		if err != nil {
			res.Failed = &FailedChange{Index: i, Error: toError(err, false)}
			return res, nil
		}
		if tag.RowsAffected() == 0 {
			// Changed or deleted since it was loaded: roll back and show
			// what is there now.
			_, _ = conn.Exec(ctx, "ROLLBACK")
			committed = true // nothing left to roll back
			res.Conflict, err = currentRow(ctx, conn, rel, info.PrimaryKey, c.Key)
			if err != nil {
				return res, err
			}
			res.Conflict.Index = i
			return res, nil
		}
		if c.Op != "delete" && got != nil {
			row := SavedRow{Index: i, Xmin: string(got[0])}
			for _, v := range got[1:] {
				if v == nil {
					row.Values = append(row.Values, nil)
					continue
				}
				str := cell(v)
				row.Values = append(row.Values, &str)
			}
			res.Rows = append(res.Rows, row)
			if len(res.Columns) == 0 {
				for _, fd := range fds[1:] {
					res.Columns = append(res.Columns, Column{Name: fd.Name, oid: fd.DataTypeOID})
				}
			}
		}
	}
	if _, err := conn.Exec(ctx, "COMMIT"); err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) { // a deferred constraint
			res.Failed = &FailedChange{Index: -1, Error: toError(pe, false)}
			return res, nil
		}
		return res, err
	}
	committed = true
	res.Applied = true
	if len(res.Columns) > 0 {
		cr := []Result{{Columns: res.Columns}}
		s.typeNames(ctx, conn, cr)
		res.Columns = cr[0].Columns
	}
	return res, nil
}

func checkChange(c Change, pk, cols []string, writable map[string]bool) error {
	switch c.Op {
	case "insert":
		if c.Key != nil || c.Xmin != "" {
			return fmt.Errorf("%w: an insert has no key or xmin", ErrBadQuery)
		}
	case "update", "delete":
		if c.Xmin == "" {
			return fmt.Errorf("%w: %s needs the row's xmin", ErrBadQuery, c.Op)
		}
		if len(c.Key) != len(pk) {
			return fmt.Errorf("%w: %s needs the full primary key (%s)", ErrBadQuery, c.Op, strings.Join(pk, ", "))
		}
		for _, k := range pk {
			if v, ok := c.Key[k]; !ok || v == nil {
				return fmt.Errorf("%w: %s needs the primary key column %s", ErrBadQuery, c.Op, k)
			}
		}
		if c.Op == "update" && len(c.Values) == 0 {
			return fmt.Errorf("%w: an update changes at least one column", ErrBadQuery)
		}
		if c.Op == "delete" && len(c.Values) > 0 {
			return fmt.Errorf("%w: a delete has no values", ErrBadQuery)
		}
	default:
		return fmt.Errorf("%w: op is insert, update or delete", ErrBadQuery)
	}
	for col := range c.Values {
		if !slices.Contains(cols, col) {
			return fmt.Errorf("%w: no column %q", ErrBadQuery, col)
		}
		if !writable[col] {
			return fmt.Errorf("%w: %s is a generated or identity column", ErrBadQuery, col)
		}
	}
	return nil
}

// sortedKeys returns m's keys in table column order.
func sortedKeys(m map[string]*string, order []string) []string {
	out := []string{}
	for _, c := range order {
		if _, ok := m[c]; ok {
			out = append(out, c)
		}
	}
	return out
}

// currentRow reads the row with key as it is now.
func currentRow(ctx context.Context, conn *pgx.Conn, rel string, pk []string, key map[string]*string) (*Conflict, error) {
	parts := make([]string, len(pk))
	params := make([][]byte, len(pk))
	for i, k := range pk {
		parts[i] = provision.Ident(k) + " = $" + strconv.Itoa(i+1)
		params[i] = []byte(*key[k])
	}
	rr := conn.PgConn().ExecParams(ctx, "SELECT xmin::text, * FROM "+rel+" WHERE "+strings.Join(parts, " AND "), params, nil, nil, nil)
	c := &Conflict{Deleted: true}
	for rr.NextRow() {
		vals := rr.Values()
		c.Deleted, c.Xmin = false, string(vals[0])
		for _, v := range vals[1:] {
			if v == nil {
				c.Current = append(c.Current, nil)
				continue
			}
			str := cell(v)
			c.Current = append(c.Current, &str)
		}
	}
	_, err := rr.Close()
	return c, err
}
