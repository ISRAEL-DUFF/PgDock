package pgdock

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Data is the data API: c.Data.From("todos").
type Data struct {
	c      *Client
	schema string
}

// Schema is the data API on another exposed schema.
func (d *Data) Schema(name string) *Data { return &Data{c: d.c, schema: name} }

// From is a table or view ("schema.table" for another exposed schema).
// The generated Go types (pgdock gen types --lang go) give each table a
// …Table constant to pass here and structs to decode into.
func (d *Data) From(table string) *Table {
	schema := d.schema
	if s, t, ok := strings.Cut(table, "."); ok {
		schema, table = s, t
	}
	return &Table{c: d.c, schema: schema, name: table}
}

// Table is one table or view.
type Table struct {
	c            *Client
	schema, name string
}

func (t *Table) path() string {
	n := t.name
	if t.schema != "public" {
		n = t.schema + "." + t.name
	}
	return "/data/v1/" + url.PathEscape(n)
}

// Op is a filter operator.
type Op string

// Operators.
const (
	OpEq          Op = "eq"
	OpNeq         Op = "neq"
	OpLt          Op = "lt"
	OpLte         Op = "lte"
	OpGt          Op = "gt"
	OpGte         Op = "gte"
	OpIn          Op = "in"
	OpLike        Op = "like"
	OpIlike       Op = "ilike"
	OpIs          Op = "is"
	OpContains    Op = "contains"
	OpContainedBy Op = "contained_by"
	OpSearch      Op = "search"
)

// Filter is a condition or a group, the JSON query form.
type Filter struct {
	Column string   `json:"column,omitempty"`
	Op     Op       `json:"op,omitempty"`
	Value  any      `json:"value,omitempty"`
	And    []Filter `json:"and,omitempty"`
	Or     []Filter `json:"or,omitempty"`
	Not    *Filter  `json:"not,omitempty"`
	isLeaf bool
}

// MarshalJSON keeps a leaf's value even when it is null or false.
func (f Filter) MarshalJSON() ([]byte, error) {
	switch {
	case f.And != nil:
		return json.Marshal(map[string]any{"and": f.And})
	case f.Or != nil:
		return json.Marshal(map[string]any{"or": f.Or})
	case f.Not != nil:
		return json.Marshal(map[string]any{"not": f.Not})
	}
	return json.Marshal(map[string]any{"column": f.Column, "op": f.Op, "value": jsonValue(f.Op, f.Value)})
}

// Cond is a condition for Or, Not and And.
func Cond(column string, op Op, value any) Filter {
	return Filter{Column: column, Op: op, Value: value, isLeaf: true}
}

// And holds when all of fs do.
func And(fs ...Filter) Filter { return Filter{And: fs} }

// Or holds when any of fs does.
func Or(fs ...Filter) Filter { return Filter{Or: fs} }

// Not holds when f doesn't.
func Not(f Filter) Filter { return Filter{Not: &f} }

func jsonValue(op Op, v any) any {
	if t, ok := v.(time.Time); ok {
		return t.UTC().Format(time.RFC3339Nano)
	}
	if op == OpIn {
		rv := reflect.ValueOf(v)
		if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
			return []any{v}
		}
	}
	return v
}

func scalar(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano)
	case fmt.Stringer:
		return x.String()
	case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return fmt.Sprint(x)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func listItem(v any) string {
	s := scalar(v)
	if strings.ContainsAny(s, `",\`) {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
	}
	return s
}

// EncodeCondition is the URL form of a condition, column:op:value.
func EncodeCondition(column string, op Op, value any) string {
	if op == OpIn {
		rv := reflect.ValueOf(value)
		var items []string
		if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
			for i := 0; i < rv.Len(); i++ {
				items = append(items, listItem(rv.Index(i).Interface()))
			}
		} else {
			items = []string{listItem(value)}
		}
		return column + ":in:" + strings.Join(items, ",")
	}
	return column + ":" + string(op) + ":" + scalar(value)
}

// filters are the conditions reads, updates and deletes share.
type filters struct {
	conds  []Filter
	groups []Filter
}

func (f *filters) simple() bool {
	for _, g := range f.groups {
		if g.Or == nil {
			return false
		}
		for _, x := range g.Or {
			if !x.isLeaf || strings.Contains(scalar(x.Value), ",") {
				return false
			}
		}
	}
	return true
}

func (f *filters) url(q url.Values) {
	for _, c := range f.conds {
		q.Add("where", EncodeCondition(c.Column, c.Op, c.Value))
	}
	for _, g := range f.groups {
		parts := make([]string, len(g.Or))
		for i, c := range g.Or {
			parts[i] = EncodeCondition(c.Column, c.Op, c.Value)
		}
		q.Add("or", strings.Join(parts, ","))
	}
}

func (f *filters) json() any {
	all := append(append([]Filter{}, f.conds...), f.groups...)
	if len(all) == 0 {
		return nil
	}
	return And(all...)
}

// Page is what a read says besides its rows.
type Page struct {
	Count      *int64
	NextCursor string
}

// SelectQuery is a read.
type SelectQuery struct {
	t       *Table
	columns string
	filters
	order   []string
	limit   int
	offset  int
	cursor  string
	count   string
	replica bool
}

// Select reads columns ("*" when empty; related rows as author(name)).
func (t *Table) Select(columns string) *SelectQuery {
	if columns == "" {
		columns = "*"
	}
	return &SelectQuery{t: t, columns: columns}
}

// Where adds a condition (all must hold).
func (q *SelectQuery) Where(column string, op Op, value any) *SelectQuery {
	q.conds = append(q.conds, Cond(column, op, value))
	return q
}

// Eq is Where(column, OpEq, value).
func (q *SelectQuery) Eq(column string, value any) *SelectQuery { return q.Where(column, OpEq, value) }

// In is Where(column, OpIn, values).
func (q *SelectQuery) In(column string, values any) *SelectQuery {
	return q.Where(column, OpIn, values)
}

// Or adds a group of which any condition may hold.
func (q *SelectQuery) Or(any ...Filter) *SelectQuery {
	q.groups = append(q.groups, Or(any...))
	return q
}

// Not adds a condition that must not hold.
func (q *SelectQuery) Not(f Filter) *SelectQuery {
	q.groups = append(q.groups, Not(f))
	return q
}

// Order sorts by column; call again for more.
func (q *SelectQuery) Order(column string, desc bool) *SelectQuery {
	dir := "asc"
	if desc {
		dir = "desc"
	}
	q.order = append(q.order, column+":"+dir)
	return q
}

// Limit is at most n rows (100 by default, 1,000 at most).
func (q *SelectQuery) Limit(n int) *SelectQuery { q.limit = n; return q }

// Offset skips n rows (keyset Cursor is better for deep pages).
func (q *SelectQuery) Offset(n int) *SelectQuery { q.offset = n; return q }

// Cursor reads the page after a previous Page's NextCursor.
func (q *SelectQuery) Cursor(c string) *SelectQuery { q.cursor = c; return q }

// Count counts the matching rows ("exact" or "estimated").
func (q *SelectQuery) Count(kind string) *SelectQuery { q.count = kind; return q }

// Replica lets a read replica serve the read.
func (q *SelectQuery) Replica() *SelectQuery { q.replica = true; return q }

// Into runs the read and decodes the rows into dest (a pointer to a slice).
func (q *SelectQuery) Into(ctx context.Context, dest any) (Page, error) {
	var out struct {
		Data       json.RawMessage `json:"data"`
		Count      *int64          `json:"count"`
		NextCursor string          `json:"next_cursor"`
	}
	h := http.Header{}
	if q.replica {
		h.Set("Read-Replica", "allowed")
	}
	var err error
	if q.simple() {
		v := url.Values{"select": {q.columns}}
		q.url(v)
		if len(q.order) > 0 {
			v.Set("order", strings.Join(q.order, ","))
		}
		if q.limit > 0 {
			v.Set("limit", strconv.Itoa(q.limit))
		}
		if q.offset > 0 {
			v.Set("offset", strconv.Itoa(q.offset))
		}
		if q.cursor != "" {
			v.Set("cursor", q.cursor)
		}
		if q.count != "" {
			v.Set("count", q.count)
		}
		err = q.t.c.doJSON(ctx, request{path: q.t.path(), query: v, header: h}, &out)
	} else {
		body := map[string]any{"select": q.columns}
		if w := q.json(); w != nil {
			body["where"] = w
		}
		if len(q.order) > 0 {
			body["order"] = strings.Join(q.order, ",")
		}
		if q.limit > 0 {
			body["limit"] = q.limit
		}
		if q.offset > 0 {
			body["offset"] = q.offset
		}
		if q.cursor != "" {
			body["cursor"] = q.cursor
		}
		if q.count != "" {
			body["count"] = q.count
		}
		err = q.t.c.doJSON(ctx, request{path: q.t.path() + "/query", body: body, header: h}, &out)
	}
	if err != nil {
		return Page{}, err
	}
	if dest != nil {
		if err := json.Unmarshal(out.Data, dest); err != nil {
			return Page{}, fmt.Errorf("pgdock: decoding rows: %w", err)
		}
	}
	return Page{Count: out.Count, NextCursor: out.NextCursor}, nil
}

// List runs a read into a slice of T.
func List[T any](ctx context.Context, q *SelectQuery) ([]T, Page, error) {
	var rows []T
	p, err := q.Into(ctx, &rows)
	return rows, p, err
}

// Get reads one row by its primary key into dest; a 404 not_found error
// when it doesn't exist or the caller can't see it.
func (t *Table) Get(ctx context.Context, key any, columns string, dest any) error {
	if columns == "" {
		columns = "*"
	}
	var out struct {
		Data json.RawMessage `json:"data"`
	}
	if err := t.c.doJSON(ctx, request{path: t.path() + "/" + url.PathEscape(scalar(key)), query: url.Values{"select": {columns}}}, &out); err != nil {
		return err
	}
	return json.Unmarshal(out.Data, dest)
}

// WriteQuery is an insert, upsert, update or delete.
type WriteQuery struct {
	t      *Table
	method string
	body   any
	filters
	key         *string
	returning   string
	columns     string
	onConflict  []string
	ignore      bool
	maxAffected int
}

// Insert adds one row or a slice of them (up to 1,000).
func (t *Table) Insert(rows any) *WriteQuery {
	return &WriteQuery{t: t, method: http.MethodPost, body: rows}
}

// Upsert inserts, updating rows that conflict on onConflict's columns.
func (t *Table) Upsert(rows any, onConflict ...string) *WriteQuery {
	return &WriteQuery{t: t, method: http.MethodPost, body: rows, onConflict: onConflict}
}

// Update changes the rows a filter (or Key) picks.
func (t *Table) Update(values any) *WriteQuery {
	return &WriteQuery{t: t, method: http.MethodPatch, body: values}
}

// Delete removes the rows a filter (or Key) picks.
func (t *Table) Delete() *WriteQuery { return &WriteQuery{t: t, method: http.MethodDelete} }

// Where adds a condition.
func (w *WriteQuery) Where(column string, op Op, value any) *WriteQuery {
	w.conds = append(w.conds, Cond(column, op, value))
	return w
}

// Eq is Where(column, OpEq, value).
func (w *WriteQuery) Eq(column string, value any) *WriteQuery { return w.Where(column, OpEq, value) }

// Or adds a group of which any condition may hold.
func (w *WriteQuery) Or(any ...Filter) *WriteQuery {
	w.groups = append(w.groups, Or(any...))
	return w
}

// Key picks one row by primary key.
func (w *WriteQuery) Key(key any) *WriteQuery { w.key = ptr(scalar(key)); return w }

// Columns are the written rows' columns to return.
func (w *WriteQuery) Columns(c string) *WriteQuery { w.columns = c; return w }

// Minimal returns only the count.
func (w *WriteQuery) Minimal() *WriteQuery { w.returning = "minimal"; return w }

// IgnoreConflicts leaves conflicting rows alone (with Upsert).
func (w *WriteQuery) IgnoreConflicts() *WriteQuery { w.ignore = true; return w }

// AtMost refuses (changing nothing) when more than n rows would change.
func (w *WriteQuery) AtMost(n int) *WriteQuery { w.maxAffected = n; return w }

// Exec runs the write and decodes the written rows into dest (a pointer
// to a slice, or nil); it returns how many rows changed.
func (w *WriteQuery) Exec(ctx context.Context, dest any) (int64, error) {
	if !w.simple() {
		return 0, &Error{Code: "invalid_filter", Message: "updates and deletes take Where and Or groups of conditions; use Data.Batch for more"}
	}
	v := url.Values{}
	w.url(v)
	if w.columns != "" {
		v.Set("select", w.columns)
	}
	if w.returning != "" {
		v.Set("return", w.returning)
	}
	if len(w.onConflict) > 0 {
		v.Set("on_conflict", strings.Join(w.onConflict, ","))
	}
	if w.ignore {
		v.Set("resolution", "ignore")
	}
	if w.maxAffected > 0 {
		v.Set("max_affected", strconv.Itoa(w.maxAffected))
	}
	path := w.t.path()
	if w.key != nil {
		path += "/" + url.PathEscape(*w.key)
	}
	var out struct {
		Affected int64           `json:"affected"`
		Data     json.RawMessage `json:"data"`
	}
	if err := w.t.c.doJSON(ctx, request{method: w.method, path: path, query: v, body: w.body}, &out); err != nil {
		return 0, err
	}
	if dest != nil && len(out.Data) > 0 {
		if err := json.Unmarshal(out.Data, dest); err != nil {
			return out.Affected, fmt.Errorf("pgdock: decoding rows: %w", err)
		}
	}
	return out.Affected, nil
}

// RPC calls a function with named arguments and decodes its result
// (a scalar, or rows) into dest. get calls a STABLE or IMMUTABLE function
// with GET.
func (d *Data) RPC(ctx context.Context, fn string, args map[string]any, dest any, get bool) error {
	name := fn
	if d.schema != "public" {
		name = d.schema + "." + fn
	}
	path := "/data/v1/rpc/" + url.PathEscape(name)
	var out struct {
		Data json.RawMessage `json:"data"`
	}
	var err error
	if get {
		v := url.Values{}
		for k, a := range args {
			v.Set(k, scalar(a))
		}
		err = d.c.doJSON(ctx, request{path: path, query: v}, &out)
	} else {
		if args == nil {
			args = map[string]any{}
		}
		err = d.c.doJSON(ctx, request{path: path, body: args}, &out)
	}
	if err != nil || dest == nil {
		return err
	}
	return json.Unmarshal(out.Data, dest)
}

// BatchOp is one write of a batch.
type BatchOp struct {
	Op         string         `json:"op"` // insert, upsert, update, delete
	Table      string         `json:"table"`
	Rows       any            `json:"rows,omitempty"`
	OnConflict []string       `json:"on_conflict,omitempty"`
	Set        map[string]any `json:"set,omitempty"`
	Where      *Filter        `json:"where,omitempty"`
	Key        any            `json:"key,omitempty"`
	Return     string         `json:"return,omitempty"`
}

// Batch runs up to 50 writes in one transaction: all happen or none do.
func (d *Data) Batch(ctx context.Context, ops []BatchOp) ([]json.RawMessage, error) {
	var out struct {
		Results []json.RawMessage `json:"results"`
	}
	err := d.c.doJSON(ctx, request{path: "/data/v1/batch", body: map[string]any{"operations": ops}}, &out)
	return out.Results, err
}
