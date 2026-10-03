package console

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/store"
)

// TableInfo is what the editors need to know about a table (V2 §4.2–4.3).
type TableInfo struct {
	Schema      string     `json:"schema"`
	Name        string     `json:"name"`
	Kind        string     `json:"kind"`
	PrimaryKey  []string   `json:"primary_key"`
	Columns     []EditCol  `json:"columns"`
	ForeignKeys []FKey     `json:"foreign_keys"`
	Constraints []Constr   `json:"constraints"`
	Indexes     []TableIdx `json:"indexes"`
	SizeBytes   int64      `json:"size_bytes"`
	RowEstimate *int64     `json:"row_estimate"`
	Comment     *string    `json:"comment"`
	// Editable says whether rows can be edited; ReadOnlyReason says why
	// not.
	Editable       bool   `json:"editable"`
	ReadOnlyReason string `json:"read_only_reason,omitempty"`
}

// EditCol is a column with what its editor needs.
type EditCol struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Nullable bool    `json:"nullable"`
	Default  *string `json:"default"`
	// Category is pg_type.typcategory: B boolean, N numeric, S string,
	// D date/time, U user (uuid, json…), E enum, A array.
	Category   string   `json:"category"`
	BaseType   string   `json:"base_type"` // the type without modifiers, e.g. uuid, jsonb, int4
	EnumValues []string `json:"enum_values,omitempty"`
	// Generated (a stored generated column) and Identity ("always" or
	// "by_default") are read-only in the editor.
	Generated bool    `json:"generated"`
	Identity  string  `json:"identity,omitempty"`
	Comment   *string `json:"comment"`
}

// FKey is a foreign key.
type FKey struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns"`
	RefSchema  string   `json:"ref_schema"`
	RefTable   string   `json:"ref_table"`
	RefColumns []string `json:"ref_columns"`
	OnDelete   string   `json:"on_delete"`
	OnUpdate   string   `json:"on_update"`
}

// Constr is a check or unique constraint.
type Constr struct {
	Name       string   `json:"name"`
	Kind       string   `json:"kind"` // check, unique, primary_key, foreign_key, exclusion
	Definition string   `json:"definition"`
	Columns    []string `json:"columns"`
}

var fkActions = map[string]string{"a": "NO ACTION", "r": "RESTRICT", "c": "CASCADE", "n": "SET NULL", "d": "SET DEFAULT"}
var conKinds = map[string]string{"c": "check", "u": "unique", "p": "primary_key", "f": "foreign_key", "x": "exclusion"}

// Info describes a table for the row and schema editors.
func (s *Service) Info(ctx context.Context, projectID uuid.UUID, schema, table string) (TableInfo, error) {
	p, err := s.active(ctx, projectID)
	if err != nil {
		return TableInfo{}, err
	}
	set, err := store.DecodeProjectSettings(p.Settings)
	if err != nil {
		return TableInfo{}, err
	}
	var out TableInfo
	err = s.readOnly(ctx, projectID, func(conn *pgx.Conn) error {
		var err error
		out, err = tableInfo(ctx, conn, schema, table)
		return err
	})
	if err == nil && out.Editable && set.ConsoleReadOnly {
		out.Editable, out.ReadOnlyReason = false, "The project's console is read-only (Settings)."
	}
	return out, err
}

func tableInfo(ctx context.Context, conn *pgx.Conn, schema, table string) (TableInfo, error) {
	out := TableInfo{Schema: schema, Name: table, Columns: []EditCol{}, ForeignKeys: []FKey{}, Constraints: []Constr{}, Indexes: []TableIdx{}}
	r, err := lookupRelation(ctx, conn, schema, table)
	if err != nil {
		return out, err
	}
	out.Kind, out.PrimaryKey = relKinds[r.kind], r.pk
	var oid uint32
	if err := conn.QueryRow(ctx, `
		SELECT c.oid, pg_total_relation_size(c.oid), CASE WHEN c.reltuples < 0 THEN NULL ELSE c.reltuples::int8 END,
		       obj_description(c.oid, 'pg_class')
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relname = $2`,
		schema, table).Scan(&oid, &out.SizeBytes, &out.RowEstimate, &out.Comment); err != nil {
		return out, err
	}
	switch {
	case r.kind == "v" || r.kind == "m":
		out.ReadOnlyReason = "Views can't be edited here; change the tables they read."
	case r.kind == "p":
		out.ReadOnlyReason = "This is a partitioned table: edit its partitions."
	case r.kind == "f":
		out.ReadOnlyReason = "Foreign tables can't be edited here."
	case len(r.pk) == 0:
		out.ReadOnlyReason = "The table has no primary key, so rows can't be told apart safely. Add one in the schema editor."
	default:
		out.Editable = true
	}

	rows, err := conn.Query(ctx, `
		SELECT a.attname, format_type(a.atttypid, a.atttypmod), NOT a.attnotnull, pg_get_expr(d.adbin, d.adrelid),
		       CASE WHEN t.typelem <> 0 AND t.typcategory = 'A' THEN 'A' ELSE COALESCE(bt.typcategory, t.typcategory)::text END,
		       COALESCE(bt.typname, t.typname)::text, a.attgenerated <> '', a.attidentity::text,
		       CASE WHEN COALESCE(bt.typtype, t.typtype) = 'e'
		            THEN (SELECT array_agg(e.enumlabel ORDER BY e.enumsortorder) FROM pg_enum e WHERE e.enumtypid = COALESCE(bt.oid, t.oid)) END,
		       col_description(a.attrelid, a.attnum)
		FROM pg_attribute a
		JOIN pg_type t ON t.oid = a.atttypid
		LEFT JOIN pg_type bt ON t.typtype = 'd' AND bt.oid = t.typbasetype
		LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum`, oid)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var c EditCol
		var ident string
		if err := rows.Scan(&c.Name, &c.Type, &c.Nullable, &c.Default, &c.Category, &c.BaseType, &c.Generated, &ident, &c.EnumValues, &c.Comment); err != nil {
			rows.Close()
			return out, err
		}
		switch ident {
		case "a":
			c.Identity = "always"
		case "d":
			c.Identity = "by_default"
		}
		out.Columns = append(out.Columns, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	rows, err = conn.Query(ctx, `
		SELECT con.conname, con.contype::text, pg_get_constraintdef(con.oid),
		       COALESCE((SELECT array_agg(a.attname ORDER BY k.ord) FROM unnest(con.conkey) WITH ORDINALITY k(attnum, ord)
		                 JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum), '{}'),
		       COALESCE(rn.nspname, '')::text, COALESCE(rc.relname, '')::text,
		       COALESCE((SELECT array_agg(a.attname ORDER BY k.ord) FROM unnest(con.confkey) WITH ORDINALITY k(attnum, ord)
		                 JOIN pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.attnum), '{}'),
		       con.confdeltype::text, con.confupdtype::text
		FROM pg_constraint con
		LEFT JOIN pg_class rc ON rc.oid = con.confrelid
		LEFT JOIN pg_namespace rn ON rn.oid = rc.relnamespace
		WHERE con.conrelid = $1
		ORDER BY con.contype, con.conname`, oid)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var name, kind, def, refSchema, refTable, del, upd string
		var cols, refCols []string
		if err := rows.Scan(&name, &kind, &def, &cols, &refSchema, &refTable, &refCols, &del, &upd); err != nil {
			rows.Close()
			return out, err
		}
		out.Constraints = append(out.Constraints, Constr{Name: name, Kind: conKinds[kind], Definition: def, Columns: cols})
		if kind == "f" {
			out.ForeignKeys = append(out.ForeignKeys, FKey{Name: name, Columns: cols, RefSchema: refSchema, RefTable: refTable,
				RefColumns: refCols, OnDelete: fkActions[del], OnUpdate: fkActions[upd]})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	rows, err = conn.Query(ctx, `
		SELECT i.relname, pg_get_indexdef(ix.indexrelid), ix.indisprimary, ix.indisunique
		FROM pg_index ix JOIN pg_class i ON i.oid = ix.indexrelid
		WHERE ix.indrelid = $1 ORDER BY NOT ix.indisprimary, i.relname`, oid)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var ix TableIdx
		if err := rows.Scan(&ix.Name, &ix.Definition, &ix.Primary, &ix.Unique); err != nil {
			rows.Close()
			return out, err
		}
		out.Indexes = append(out.Indexes, ix)
	}
	rows.Close()
	return out, rows.Err()
}
