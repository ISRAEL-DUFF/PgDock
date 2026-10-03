// Package schemaedit turns a schema-change request from the visual editor
// into DDL, risk notes, and a reverse migration (V2 §4.3). It is pure: the
// caller gathers the State it needs from the database and executes the
// Plan.
//
// Identifiers are always quoted. Types are checked against a strict
// pattern (and by the caller with to_regtype). Expressions (defaults,
// checks, index predicates, USING) are SQL by nature; they run as the
// project's owner role, who could run them in the SQL console anyway, and
// the preview shows exactly what will run.
package schemaedit

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// ErrInvalid is a request the editor can't turn into DDL.
var ErrInvalid = errors.New("invalid schema change")

// Kinds of change.
const (
	CreateSchema   = "create_schema"
	DropSchema     = "drop_schema"
	CreateTable    = "create_table"
	RenameTable    = "rename_table"
	DropTable      = "drop_table"
	AddColumn      = "add_column"
	RenameColumn   = "rename_column"
	DropColumn     = "drop_column"
	AlterColumn    = "alter_column"
	AddCheck       = "add_check"
	AddUnique      = "add_unique"
	AddForeignKey  = "add_foreign_key"
	DropConstraint = "drop_constraint"
	CreateIndex    = "create_index"
	DropIndex      = "drop_index"
	CreateEnum     = "create_enum"
	AddEnumValue   = "add_enum_value"
)

// Kinds lists every supported change.
var Kinds = []string{CreateSchema, DropSchema, CreateTable, RenameTable, DropTable, AddColumn, RenameColumn, DropColumn,
	AlterColumn, AddCheck, AddUnique, AddForeignKey, DropConstraint, CreateIndex, DropIndex, CreateEnum, AddEnumValue}

// ColumnDef is a new column.
type ColumnDef struct {
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	Nullable   *bool   `json:"nullable,omitempty"` // default true
	Default    *string `json:"default,omitempty"`
	PrimaryKey bool    `json:"primary_key,omitempty"`
	Comment    *string `json:"comment,omitempty"`
}

func (c ColumnDef) nullable() bool { return c.Nullable == nil || *c.Nullable }

// Change is one schema-change request. Which fields apply depends on Kind.
type Change struct {
	Kind   string `json:"kind"`
	Schema string `json:"schema"`
	// Table is the table changed (or created); Name names a new object
	// (schema, constraint, index, enum) or the one dropped.
	Table   string `json:"table,omitempty"`
	Name    string `json:"name,omitempty"`
	NewName string `json:"new_name,omitempty"`
	// Column is the column added; ColumnName the one renamed, dropped or
	// altered.
	Column     *ColumnDef  `json:"column,omitempty"`
	Columns    []ColumnDef `json:"columns,omitempty"` // create_table
	ColumnName string      `json:"column_name,omitempty"`
	Comment    *string     `json:"comment,omitempty"`

	// alter_column: any of a new type (with USING), a new default or
	// dropping it, and nullability.
	Type        *string `json:"type,omitempty"`
	Using       *string `json:"using,omitempty"`
	Default     *string `json:"default,omitempty"`
	DropDefault bool    `json:"drop_default,omitempty"`
	Nullable    *bool   `json:"nullable,omitempty"`

	// Constraints and indexes.
	Expression  string   `json:"expression,omitempty"`  // check
	KeyColumns  []string `json:"key_columns,omitempty"` // unique, foreign key, index
	RefSchema   string   `json:"ref_schema,omitempty"`
	RefTable    string   `json:"ref_table,omitempty"`
	RefColumns  []string `json:"ref_columns,omitempty"`
	OnDelete    string   `json:"on_delete,omitempty"`
	OnUpdate    string   `json:"on_update,omitempty"`
	NotValid    bool     `json:"not_valid,omitempty"`
	Method      string   `json:"method,omitempty"` // btree, gin, gist, brin
	Unique      bool     `json:"unique,omitempty"`
	Where       string   `json:"where,omitempty"`
	Concurrent  *bool    `json:"concurrently,omitempty"` // default true
	Cascade     bool     `json:"cascade,omitempty"`      // drop_schema
	Values      []string `json:"values,omitempty"`       // create_enum
	Value       string   `json:"value,omitempty"`        // add_enum_value
	BeforeValue string   `json:"before_value,omitempty"`
}

// State is what the planner needs to know about the database.
type State struct {
	// SizeBytes and Rows describe the table changed (0 when new).
	SizeBytes int64
	Rows      int64
	// The column altered, renamed or dropped, as it is now.
	ColumnType     string
	ColumnNullable bool
	ColumnDefault  *string
	// TypeRewrites: the type change rewrites the table (not a
	// binary-coercible or widening change).
	TypeRewrites bool
	// VolatileDefault: the new column's default is volatile, so ADD COLUMN
	// rewrites the table.
	VolatileDefault bool
	// The definition of the constraint or index dropped, for Down.
	ObjectDef string
}

// Statement is one DDL statement.
type Statement struct {
	SQL string `json:"sql"`
	// Transactional statements run together in one transaction;
	// non-transactional ones (CREATE INDEX CONCURRENTLY) alone.
	Transactional bool `json:"transactional"`
}

// Risk is a note from the rules engine.
type Risk struct {
	Level   string `json:"level"` // info, warning, danger
	Message string `json:"message"`
}

// Plan is a change turned into DDL.
type Plan struct {
	Statements []Statement `json:"statements"`
	Risks      []Risk      `json:"risks"`
	// Confirm is the object name the user must type to run it (drops).
	Confirm string `json:"confirm,omitempty"`
	// Down reverses the change where that is unambiguous; DownTODO notes
	// where it isn't.
	Down     []string `json:"down"`
	DownTODO []string `json:"down_todo"`
	// Slug names the migration, e.g. add_column_posts_summary.
	Slug string `json:"slug"`
	// Hash identifies the statements; running a plan needs it, so what
	// runs is what was previewed.
	Hash string `json:"hash"`
}

// Transactional reports whether every statement can run in one
// transaction.
func (p Plan) Transactional() bool {
	for _, s := range p.Statements {
		if !s.Transactional {
			return false
		}
	}
	return true
}

// Ident quotes an identifier.
func Ident(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// Literal quotes a string literal.
func Literal(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

var typeRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?( [A-Za-z_][A-Za-z0-9_]*){0,3}( ?\([0-9]+( ?, ?[0-9]+)?\))?( [A-Za-z_]+){0,3}(\[\])*$`)

// ValidType reports whether t looks like a type name: identifiers, an
// optional schema, modifiers like (10,2), and [] suffixes.
func ValidType(t string) bool { return len(t) <= 100 && typeRe.MatchString(strings.TrimSpace(t)) }

func checkName(what, s string) error {
	if s == "" {
		return fmt.Errorf("%w: give the %s a name", ErrInvalid, what)
	}
	if len(s) > 63 {
		return fmt.Errorf("%w: the %s name is longer than 63 bytes", ErrInvalid, what)
	}
	return nil
}

func checkType(t string) error {
	if !ValidType(t) {
		return fmt.Errorf("%w: %q is not a type name", ErrInvalid, t)
	}
	return nil
}

func checkExpr(what, e string) error {
	if strings.TrimSpace(e) == "" {
		return fmt.Errorf("%w: the %s is empty", ErrInvalid, what)
	}
	if len(e) > 4000 {
		return fmt.Errorf("%w: the %s is too long", ErrInvalid, what)
	}
	return nil
}

func qrel(schema, table string) string { return Ident(schema) + "." + Ident(table) }

func idents(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = Ident(n)
	}
	return strings.Join(q, ", ")
}

// HumanBytes renders a size like 2.3 GB.
func HumanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}

// bigTable is where a blocking rewrite becomes a danger rather than a
// warning.
const bigTable = 100 << 20

func (st State) rewriteLevel() string {
	if st.SizeBytes >= bigTable {
		return "danger"
	}
	return "warning"
}

func slugPart(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "_") {
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func autoName(table string, cols []string, suffix string) string {
	n := table
	if len(cols) > 0 {
		n += "_" + strings.Join(cols, "_")
	}
	n += "_" + suffix
	if len(n) > 63 {
		sum := sha256.Sum256([]byte(n))
		n = n[:54] + "_" + hex.EncodeToString(sum[:])[:8]
	}
	return n
}

var fkActions = []string{"", "NO ACTION", "RESTRICT", "CASCADE", "SET NULL", "SET DEFAULT"}
var indexMethods = []string{"", "btree", "gin", "gist", "brin", "hash"}

// Build turns c into a plan, given the database state.
func Build(c Change, st State) (Plan, error) {
	p := Plan{Risks: []Risk{}, Down: []string{}, DownTODO: []string{}}
	tx := func(sql string) { p.Statements = append(p.Statements, Statement{SQL: sql, Transactional: true}) }
	risk := func(level, msg string) { p.Risks = append(p.Risks, Risk{Level: level, Message: msg}) }
	if c.Schema == "" && c.Kind != CreateSchema && c.Kind != DropSchema {
		c.Schema = "public"
	}
	needTable := func() error { return checkName("table", c.Table) }
	rel := qrel(c.Schema, c.Table)

	switch c.Kind {
	case CreateSchema:
		if err := checkName("schema", c.Name); err != nil {
			return p, err
		}
		tx("CREATE SCHEMA " + Ident(c.Name))
		p.Down = append(p.Down, "DROP SCHEMA "+Ident(c.Name))
		p.Slug = "create_schema_" + slugPart(c.Name)

	case DropSchema:
		if err := checkName("schema", c.Name); err != nil {
			return p, err
		}
		sql := "DROP SCHEMA " + Ident(c.Name)
		if c.Cascade {
			sql += " CASCADE"
			risk("danger", "Drops the schema and everything in it: tables, data, types and functions. This can't be undone.")
		} else {
			risk("warning", "Fails unless the schema is empty.")
		}
		tx(sql)
		p.Confirm = c.Name
		p.DownTODO = append(p.DownTODO, "recreate schema "+c.Name+" and its contents (a dropped schema can't be restored from the migration)")
		p.Slug = "drop_schema_" + slugPart(c.Name)

	case CreateTable:
		if err := needTable(); err != nil {
			return p, err
		}
		if len(c.Columns) == 0 {
			return p, fmt.Errorf("%w: a table needs at least one column", ErrInvalid)
		}
		var defs, pk []string
		seen := map[string]bool{}
		for _, col := range c.Columns {
			if err := checkName("column", col.Name); err != nil {
				return p, err
			}
			if seen[col.Name] {
				return p, fmt.Errorf("%w: column %s appears twice", ErrInvalid, col.Name)
			}
			seen[col.Name] = true
			def, err := columnSQL(col)
			if err != nil {
				return p, err
			}
			defs = append(defs, def)
			if col.PrimaryKey {
				pk = append(pk, col.Name)
			}
		}
		if len(pk) > 0 {
			defs = append(defs, "PRIMARY KEY ("+idents(pk)+")")
		} else {
			risk("warning", "The table has no primary key: its rows can't be edited in the table editor.")
		}
		tx("CREATE TABLE " + rel + " (\n  " + strings.Join(defs, ",\n  ") + "\n)")
		if c.Comment != nil && *c.Comment != "" {
			tx("COMMENT ON TABLE " + rel + " IS " + Literal(*c.Comment))
		}
		for _, col := range c.Columns {
			if col.Comment != nil && *col.Comment != "" {
				tx("COMMENT ON COLUMN " + rel + "." + Ident(col.Name) + " IS " + Literal(*col.Comment))
			}
		}
		p.Down = append(p.Down, "DROP TABLE "+rel)
		p.Slug = "create_table_" + slugPart(c.Table)

	case RenameTable:
		if err := needTable(); err != nil {
			return p, err
		}
		if err := checkName("table", c.NewName); err != nil {
			return p, err
		}
		tx("ALTER TABLE " + rel + " RENAME TO " + Ident(c.NewName))
		risk("warning", "Apps, views and functions that use the old name "+c.Table+" stop working until they use "+c.NewName+".")
		p.Down = append(p.Down, "ALTER TABLE "+qrel(c.Schema, c.NewName)+" RENAME TO "+Ident(c.Table))
		p.Slug = "rename_table_" + slugPart(c.Table) + "_to_" + slugPart(c.NewName)

	case DropTable:
		if err := needTable(); err != nil {
			return p, err
		}
		tx("DROP TABLE " + rel)
		msg := "Deletes the table and all its rows. This can't be undone (except by restoring a backup)."
		if st.SizeBytes > 0 {
			msg += " It holds " + HumanBytes(st.SizeBytes) + "."
		}
		risk("danger", msg)
		p.Confirm = c.Table
		p.DownTODO = append(p.DownTODO, "recreate table "+c.Table+" (its data is gone)")
		p.Slug = "drop_table_" + slugPart(c.Table)

	case AddColumn:
		if err := needTable(); err != nil {
			return p, err
		}
		if c.Column == nil {
			return p, fmt.Errorf("%w: describe the column to add", ErrInvalid)
		}
		col := *c.Column
		if err := checkName("column", col.Name); err != nil {
			return p, err
		}
		if col.PrimaryKey {
			return p, fmt.Errorf("%w: add a primary key with a unique constraint instead", ErrInvalid)
		}
		def, err := columnSQL(col)
		if err != nil {
			return p, err
		}
		tx("ALTER TABLE " + rel + " ADD COLUMN " + def)
		if col.Comment != nil && *col.Comment != "" {
			tx("COMMENT ON COLUMN " + rel + "." + Ident(col.Name) + " IS " + Literal(*col.Comment))
		}
		switch {
		case col.Default != nil && st.VolatileDefault:
			risk(st.rewriteLevel(), "The default is volatile, so adding the column rewrites the table and blocks writes while it does. Estimated size: "+HumanBytes(st.SizeBytes)+". Add it without a default, then backfill in batches.")
		case !col.nullable() && col.Default == nil && st.Rows > 0:
			risk("danger", "The column is NOT NULL without a default, so this fails: the table already has rows. Give it a default, or add it nullable and backfill first.")
		default:
			risk("info", "Adding a column with no default or a constant one doesn't rewrite the table.")
		}
		p.Down = append(p.Down, "ALTER TABLE "+rel+" DROP COLUMN "+Ident(col.Name))
		p.Slug = "add_column_" + slugPart(c.Table) + "_" + slugPart(col.Name)

	case RenameColumn:
		if err := needTable(); err != nil {
			return p, err
		}
		if err := checkName("column", c.ColumnName); err != nil {
			return p, err
		}
		if err := checkName("column", c.NewName); err != nil {
			return p, err
		}
		tx("ALTER TABLE " + rel + " RENAME COLUMN " + Ident(c.ColumnName) + " TO " + Ident(c.NewName))
		risk("warning", "Queries that use the old column name "+c.ColumnName+" stop working until they use "+c.NewName+".")
		p.Down = append(p.Down, "ALTER TABLE "+rel+" RENAME COLUMN "+Ident(c.NewName)+" TO "+Ident(c.ColumnName))
		p.Slug = "rename_column_" + slugPart(c.Table) + "_" + slugPart(c.ColumnName)

	case DropColumn:
		if err := needTable(); err != nil {
			return p, err
		}
		if err := checkName("column", c.ColumnName); err != nil {
			return p, err
		}
		tx("ALTER TABLE " + rel + " DROP COLUMN " + Ident(c.ColumnName))
		risk("danger", "Deletes the column and its data in every row. This can't be undone (except by restoring a backup).")
		p.Confirm = c.ColumnName
		if st.ColumnType != "" {
			p.DownTODO = append(p.DownTODO, "re-add column "+c.ColumnName+" "+st.ColumnType+" (its data is gone)")
		} else {
			p.DownTODO = append(p.DownTODO, "re-add column "+c.ColumnName+" (its data is gone)")
		}
		p.Slug = "drop_column_" + slugPart(c.Table) + "_" + slugPart(c.ColumnName)

	case AlterColumn:
		if err := needTable(); err != nil {
			return p, err
		}
		if err := checkName("column", c.ColumnName); err != nil {
			return p, err
		}
		col := Ident(c.ColumnName)
		alter := "ALTER TABLE " + rel + " ALTER COLUMN " + col
		n := 0
		if c.Type != nil {
			if err := checkType(*c.Type); err != nil {
				return p, err
			}
			sql := alter + " TYPE " + strings.TrimSpace(*c.Type)
			if c.Using != nil && strings.TrimSpace(*c.Using) != "" {
				if err := checkExpr("USING expression", *c.Using); err != nil {
					return p, err
				}
				sql += " USING " + *c.Using
			}
			tx(sql)
			if st.TypeRewrites {
				risk(st.rewriteLevel(), "Rewrites the table and blocks writes. Estimated size: "+HumanBytes(st.SizeBytes)+".")
			} else {
				risk("info", "This type change doesn't rewrite the table.")
			}
			if st.ColumnType != "" {
				p.Down = append(p.Down, alter+" TYPE "+st.ColumnType)
				p.DownTODO = append(p.DownTODO, "check the data still fits "+st.ColumnType+" before migrating down")
			}
			n++
		}
		if c.DropDefault {
			tx(alter + " DROP DEFAULT")
			if st.ColumnDefault != nil {
				p.Down = append(p.Down, alter+" SET DEFAULT "+*st.ColumnDefault)
			}
			n++
		} else if c.Default != nil {
			if err := checkExpr("default", *c.Default); err != nil {
				return p, err
			}
			tx(alter + " SET DEFAULT " + *c.Default)
			if st.ColumnDefault != nil {
				p.Down = append(p.Down, alter+" SET DEFAULT "+*st.ColumnDefault)
			} else {
				p.Down = append(p.Down, alter+" DROP DEFAULT")
			}
			risk("info", "A new default applies to rows inserted from now on; existing rows keep their values.")
			n++
		}
		if c.Nullable != nil {
			if *c.Nullable {
				tx(alter + " DROP NOT NULL")
				p.Down = append(p.Down, alter+" SET NOT NULL")
			} else {
				tx(alter + " SET NOT NULL")
				level := "info"
				if st.SizeBytes >= bigTable {
					level = "warning"
				}
				risk(level, "Scans the whole table under an exclusive lock. Consider adding a CHECK ("+c.ColumnName+" IS NOT NULL) NOT VALID constraint and validating it separately.")
				p.Down = append(p.Down, alter+" DROP NOT NULL")
			}
			n++
		}
		if n == 0 {
			return p, fmt.Errorf("%w: nothing to change", ErrInvalid)
		}
		slices.Reverse(p.Down)
		p.Slug = "alter_column_" + slugPart(c.Table) + "_" + slugPart(c.ColumnName)

	case AddCheck:
		if err := needTable(); err != nil {
			return p, err
		}
		if err := checkExpr("check", c.Expression); err != nil {
			return p, err
		}
		name := c.Name
		if name == "" {
			sum := sha256.Sum256([]byte(c.Expression))
			name = autoName(c.Table, nil, "check_"+hex.EncodeToString(sum[:])[:6])
		}
		sql := "ALTER TABLE " + rel + " ADD CONSTRAINT " + Ident(name) + " CHECK (" + c.Expression + ")"
		if c.NotValid {
			sql += " NOT VALID"
			risk("info", "NOT VALID: existing rows aren't checked now. Run ALTER TABLE … VALIDATE CONSTRAINT later; it doesn't block writes.")
		} else {
			risk(lockLevel(st), "Checks every existing row while holding a lock that blocks writes. On a big table, add it NOT VALID and validate it separately.")
		}
		tx(sql)
		p.Down = append(p.Down, "ALTER TABLE "+rel+" DROP CONSTRAINT "+Ident(name))
		p.Slug = "add_check_" + slugPart(c.Table)

	case AddUnique:
		if err := needTable(); err != nil {
			return p, err
		}
		if len(c.KeyColumns) == 0 {
			return p, fmt.Errorf("%w: choose the columns", ErrInvalid)
		}
		name := c.Name
		if name == "" {
			name = autoName(c.Table, c.KeyColumns, "key")
		}
		tx("ALTER TABLE " + rel + " ADD CONSTRAINT " + Ident(name) + " UNIQUE (" + idents(c.KeyColumns) + ")")
		risk(lockLevel(st), "Builds a unique index while blocking writes, and fails if existing rows have duplicates. On a big table, create a unique index concurrently first.")
		p.Down = append(p.Down, "ALTER TABLE "+rel+" DROP CONSTRAINT "+Ident(name))
		p.Slug = "add_unique_" + slugPart(c.Table) + "_" + slugPart(strings.Join(c.KeyColumns, "_"))

	case AddForeignKey:
		if err := needTable(); err != nil {
			return p, err
		}
		if len(c.KeyColumns) == 0 || len(c.KeyColumns) != len(c.RefColumns) {
			return p, fmt.Errorf("%w: choose matching columns on both tables", ErrInvalid)
		}
		if err := checkName("referenced table", c.RefTable); err != nil {
			return p, err
		}
		refSchema := c.RefSchema
		if refSchema == "" {
			refSchema = c.Schema
		}
		for _, a := range []string{c.OnDelete, c.OnUpdate} {
			if !slices.Contains(fkActions, strings.ToUpper(a)) {
				return p, fmt.Errorf("%w: unknown action %q", ErrInvalid, a)
			}
		}
		name := c.Name
		if name == "" {
			name = autoName(c.Table, c.KeyColumns, "fkey")
		}
		sql := "ALTER TABLE " + rel + " ADD CONSTRAINT " + Ident(name) + " FOREIGN KEY (" + idents(c.KeyColumns) + ") REFERENCES " +
			qrel(refSchema, c.RefTable) + " (" + idents(c.RefColumns) + ")"
		if c.OnDelete != "" {
			sql += " ON DELETE " + strings.ToUpper(c.OnDelete)
		}
		if c.OnUpdate != "" {
			sql += " ON UPDATE " + strings.ToUpper(c.OnUpdate)
		}
		if c.NotValid {
			sql += " NOT VALID"
			risk("info", "NOT VALID: existing rows aren't checked now. Validate it later without blocking writes.")
		} else {
			risk(lockLevel(st), "Checks every existing row against "+c.RefTable+", blocking writes to both tables while it does. On a big table, add it NOT VALID and validate it separately.")
		}
		tx(sql)
		p.Down = append(p.Down, "ALTER TABLE "+rel+" DROP CONSTRAINT "+Ident(name))
		p.Slug = "add_foreign_key_" + slugPart(c.Table) + "_" + slugPart(c.RefTable)

	case DropConstraint:
		if err := needTable(); err != nil {
			return p, err
		}
		if err := checkName("constraint", c.Name); err != nil {
			return p, err
		}
		tx("ALTER TABLE " + rel + " DROP CONSTRAINT " + Ident(c.Name))
		risk("warning", "Rows that break the constraint can be written from now on.")
		p.Confirm = c.Name
		if st.ObjectDef != "" {
			p.Down = append(p.Down, "ALTER TABLE "+rel+" ADD CONSTRAINT "+Ident(c.Name)+" "+st.ObjectDef)
		} else {
			p.DownTODO = append(p.DownTODO, "re-add constraint "+c.Name)
		}
		p.Slug = "drop_constraint_" + slugPart(c.Name)

	case CreateIndex:
		if err := needTable(); err != nil {
			return p, err
		}
		if len(c.KeyColumns) == 0 {
			return p, fmt.Errorf("%w: choose the columns", ErrInvalid)
		}
		method := strings.ToLower(c.Method)
		if !slices.Contains(indexMethods, method) {
			return p, fmt.Errorf("%w: unknown index method %q", ErrInvalid, c.Method)
		}
		name := c.Name
		if name == "" {
			name = autoName(c.Table, c.KeyColumns, "idx")
		}
		concurrent := c.Concurrent == nil || *c.Concurrent
		sql := "CREATE "
		if c.Unique {
			sql += "UNIQUE "
		}
		sql += "INDEX "
		if concurrent {
			sql += "CONCURRENTLY "
		}
		sql += Ident(name) + " ON " + rel
		if method != "" && method != "btree" {
			sql += " USING " + method
		}
		sql += " (" + idents(c.KeyColumns) + ")"
		if strings.TrimSpace(c.Where) != "" {
			if err := checkExpr("WHERE clause", c.Where); err != nil {
				return p, err
			}
			sql += " WHERE " + c.Where
		}
		p.Statements = append(p.Statements, Statement{SQL: sql, Transactional: !concurrent})
		dropSQL := "DROP INDEX " + qrel(c.Schema, name)
		if concurrent {
			risk("info", "Built CONCURRENTLY, outside a transaction, so writes carry on. It takes longer, and if it fails it leaves an invalid index to drop.")
			dropSQL = "DROP INDEX CONCURRENTLY " + qrel(c.Schema, name)
		} else {
			risk(lockLevel(st), "Builds the index while blocking writes to the table.")
		}
		p.Down = append(p.Down, dropSQL)
		p.Slug = "create_index_" + slugPart(name)

	case DropIndex:
		if err := checkName("index", c.Name); err != nil {
			return p, err
		}
		p.Statements = append(p.Statements, Statement{SQL: "DROP INDEX CONCURRENTLY " + qrel(c.Schema, c.Name), Transactional: false})
		risk("warning", "Queries that used the index may get slower.")
		p.Confirm = c.Name
		if st.ObjectDef != "" {
			p.Down = append(p.Down, st.ObjectDef)
		} else {
			p.DownTODO = append(p.DownTODO, "recreate index "+c.Name)
		}
		p.Slug = "drop_index_" + slugPart(c.Name)

	case CreateEnum:
		if err := checkName("type", c.Name); err != nil {
			return p, err
		}
		if len(c.Values) == 0 {
			return p, fmt.Errorf("%w: an enum needs at least one value", ErrInvalid)
		}
		lits := make([]string, len(c.Values))
		for i, v := range c.Values {
			lits[i] = Literal(v)
		}
		tx("CREATE TYPE " + qrel(c.Schema, c.Name) + " AS ENUM (" + strings.Join(lits, ", ") + ")")
		p.Down = append(p.Down, "DROP TYPE "+qrel(c.Schema, c.Name))
		p.Slug = "create_enum_" + slugPart(c.Name)

	case AddEnumValue:
		if err := checkName("type", c.Name); err != nil {
			return p, err
		}
		if c.Value == "" {
			return p, fmt.Errorf("%w: give the new value", ErrInvalid)
		}
		sql := "ALTER TYPE " + qrel(c.Schema, c.Name) + " ADD VALUE " + Literal(c.Value)
		if c.BeforeValue != "" {
			sql += " BEFORE " + Literal(c.BeforeValue)
		}
		p.Statements = append(p.Statements, Statement{SQL: sql, Transactional: false})
		p.DownTODO = append(p.DownTODO, "Postgres can't remove an enum value; recreate the type without "+c.Value+" if you need to")
		p.Slug = "add_enum_value_" + slugPart(c.Name)

	default:
		return p, fmt.Errorf("%w: unknown kind %q", ErrInvalid, c.Kind)
	}
	p.Hash = hash(p.Statements)
	return p, nil
}

func lockLevel(st State) string {
	if st.SizeBytes >= bigTable {
		return "warning"
	}
	return "info"
}

func columnSQL(col ColumnDef) (string, error) {
	if err := checkType(col.Type); err != nil {
		return "", err
	}
	def := Ident(col.Name) + " " + strings.TrimSpace(col.Type)
	if !col.nullable() || col.PrimaryKey {
		def += " NOT NULL"
	}
	if col.Default != nil && strings.TrimSpace(*col.Default) != "" {
		if err := checkExpr("default", *col.Default); err != nil {
			return "", err
		}
		def += " DEFAULT " + *col.Default
	}
	return def, nil
}

func hash(stmts []Statement) string {
	h := sha256.New()
	for _, s := range stmts {
		h.Write([]byte(s.SQL))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
