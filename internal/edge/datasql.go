package edge

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// The data API's reads (V4 §3.2): a request is parsed into a Query, which
// is checked against the catalog and turned into one parameterised
// statement. Names only ever come from the catalog (quoted); values only
// ever travel as parameters.

// Limits (V4 §3.6).
const (
	defaultLimit   = 100
	maxLimit       = 1000
	maxOffset      = 10000
	maxFilters     = 30
	maxEmbedDepth  = 3
	maxEmbedRows   = 1000
	maxSelectItems = 200
)

// apiError is a client error with its code and HTTP status.
type apiError struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
}

func (e *apiError) Error() string { return e.Message }

func badRequest(code, format string, args ...any) *apiError {
	return &apiError{Status: 400, Code: code, Message: fmt.Sprintf(format, args...)}
}

// SelectItem is one entry of select=: a column, a JSON path, or an
// embedded relation.
type SelectItem struct {
	Alias  string
	Column string   // a column, or the root of a JSON path
	Path   []string // JSON path keys after the column
	Star   bool
	Embed  *Embed
}

// Embed is a related table reached through a foreign key.
type Embed struct {
	Name   string // as written: a table name or a foreign key column without _id
	Hint   string // name!hint: the foreign key column or constraint
	Select []SelectItem
}

// Filter is a tree of conditions.
type Filter struct {
	And    []Filter
	Or     []Filter
	Not    *Filter
	Column string
	Path   []string
	Op     string
	Value  string
	Values []string // in
	Null   bool     // the value is JSON null
}

// Order is one sort key.
type Order struct {
	Column string
	Desc   bool
}

// Query is a read.
type Query struct {
	Select  []SelectItem
	Where   Filter // an And of the conditions
	Order   []Order
	Limit   int
	Offset  int
	Cursor  string
	Count   string // "", exact, estimated
	PKValue *string
}

var (
	identRe = regexp.MustCompile(`^[^\s,():!>'"\\-][^\s,():!>'"\\]*$`)
	ops     = map[string]bool{"eq": true, "neq": true, "lt": true, "lte": true, "gt": true, "gte": true, "in": true,
		"like": true, "ilike": true, "is": true, "contains": true, "contained_by": true, "search": true}
)

// splitTop splits s at commas outside parentheses.
func splitTop(s string) ([]string, error) {
	var out []string
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return nil, badRequest("invalid_select", "unbalanced parentheses in select")
			}
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	if depth != 0 {
		return nil, badRequest("invalid_select", "unbalanced parentheses in select")
	}
	return append(out, s[start:]), nil
}

// parsePath splits col->a->b.
func parsePath(s string) (string, []string, error) {
	parts := strings.Split(s, "->")
	for _, p := range parts {
		if !identRe.MatchString(p) {
			return "", nil, badRequest("invalid_name", "%q is not a column or JSON path", s)
		}
	}
	return parts[0], parts[1:], nil
}

// ParseSelect parses a select= list.
func ParseSelect(s string, depth int) ([]SelectItem, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return []SelectItem{{Star: true}}, nil
	}
	parts, err := splitTop(s)
	if err != nil {
		return nil, err
	}
	if len(parts) > maxSelectItems {
		return nil, badRequest("invalid_select", "at most %d items in select", maxSelectItems)
	}
	var out []SelectItem
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, badRequest("invalid_select", "an empty item in select")
		}
		var it SelectItem
		// alias:item (an alias before any parenthesis).
		if i := strings.IndexByte(part, ':'); i > 0 && (strings.IndexByte(part, '(') < 0 || i < strings.IndexByte(part, '(')) {
			it.Alias, part = strings.TrimSpace(part[:i]), strings.TrimSpace(part[i+1:])
			if !identRe.MatchString(it.Alias) {
				return nil, badRequest("invalid_select", "%q is not an alias", it.Alias)
			}
		}
		switch {
		case part == "*":
			if it.Alias != "" {
				return nil, badRequest("invalid_select", "* can't have an alias")
			}
			it.Star = true
		case strings.HasSuffix(part, ")"):
			open := strings.IndexByte(part, '(')
			if open <= 0 {
				return nil, badRequest("invalid_select", "%q is not a relation", part)
			}
			if depth >= maxEmbedDepth {
				return nil, badRequest("embed_too_deep", "relations nest at most %d levels", maxEmbedDepth)
			}
			name, hint, _ := strings.Cut(part[:open], "!")
			if !identRe.MatchString(name) || (hint != "" && !identRe.MatchString(hint)) {
				return nil, badRequest("invalid_select", "%q is not a relation", part[:open])
			}
			sub, err := ParseSelect(part[open+1:len(part)-1], depth+1)
			if err != nil {
				return nil, err
			}
			it.Embed = &Embed{Name: name, Hint: hint, Select: sub}
		default:
			col, path, err := parsePath(part)
			if err != nil {
				return nil, err
			}
			it.Column, it.Path = col, path
		}
		out = append(out, it)
	}
	return out, nil
}

// ParseCondition parses column:operator:value.
func ParseCondition(s string) (Filter, error) {
	col, rest, ok := strings.Cut(s, ":")
	if !ok {
		return Filter{}, badRequest("invalid_filter", "%q is not column:operator:value", s)
	}
	op, val, ok := strings.Cut(rest, ":")
	if !ok {
		return Filter{}, badRequest("invalid_filter", "%q is not column:operator:value", s)
	}
	if !ops[op] {
		return Filter{}, badRequest("invalid_filter", "unknown operator %q", op)
	}
	c, path, err := parsePath(col)
	if err != nil {
		return Filter{}, err
	}
	f := Filter{Column: c, Path: path, Op: op, Value: val}
	if op == "in" {
		f.Values = splitList(val)
	}
	if op == "is" && val != "null" && val != "true" && val != "false" {
		return Filter{}, badRequest("invalid_filter", "is takes null, true or false")
	}
	return f, nil
}

// splitList splits an in= list at commas; an item in double quotes may
// hold commas.
func splitList(s string) []string {
	var out []string
	var b strings.Builder
	quoted := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			quoted = !quoted
		case c == '\\' && quoted && i+1 < len(s):
			i++
			b.WriteByte(s[i])
		case c == ',' && !quoted:
			out = append(out, b.String())
			b.Reset()
		default:
			b.WriteByte(c)
		}
	}
	return append(out, b.String())
}

var orStartRe = regexp.MustCompile(`,([^\s,():!'"\\]+):(eq|neq|lt|lte|gt|gte|in|like|ilike|is|contains|contained_by|search):`)

// ParseOr parses an or= group: conditions separated by commas, where a new
// condition starts at ",column:operator:".
func ParseOr(s string) (Filter, error) {
	var parts []string
	start := 0
	for _, m := range orStartRe.FindAllStringIndex(s, -1) {
		parts = append(parts, s[start:m[0]])
		start = m[0] + 1
	}
	parts = append(parts, s[start:])
	f := Filter{}
	for _, p := range parts {
		c, err := ParseCondition(p)
		if err != nil {
			return Filter{}, err
		}
		f.Or = append(f.Or, c)
	}
	return f, nil
}

// ParseOrder parses order=col:asc,col2:desc.
func ParseOrder(s string) ([]Order, error) {
	var out []Order
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		col, dir, _ := strings.Cut(part, ":")
		if !identRe.MatchString(col) {
			return nil, badRequest("invalid_order", "%q is not a column", col)
		}
		o := Order{Column: col}
		switch dir {
		case "", "asc":
		case "desc":
			o.Desc = true
		default:
			return nil, badRequest("invalid_order", "order direction is asc or desc, not %q", dir)
		}
		out = append(out, o)
	}
	return out, nil
}

func countFilters(f Filter) int {
	n := 0
	if f.Column != "" {
		n++
	}
	for _, x := range f.And {
		n += countFilters(x)
	}
	for _, x := range f.Or {
		n += countFilters(x)
	}
	if f.Not != nil {
		n += countFilters(*f.Not)
	}
	return n
}

// ---- SQL ---------------------------------------------------------------------

// gate decides whether the request's role may read a relation (V4 §3.6).
type gate func(t *Table) *apiError

// builder accumulates a statement's parameters.
type builder struct {
	cat   *Catalog
	args  []any
	gate  gate
	alias int
	// tables are the relations a read reaches ("schema.table"), for the
	// cache's invalidation.
	tables []string
}

func (b *builder) param(v any) string {
	b.args = append(b.args, v)
	return "$" + strconv.Itoa(len(b.args))
}

func (b *builder) nextAlias() string {
	b.alias++
	return "t" + strconv.Itoa(b.alias)
}

func qi(s string) string { return pgx.Identifier{s}.Sanitize() }

func lit(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func (b *builder) column(t *Table, name string) (*Column, error) {
	c := t.Col(name)
	if c == nil {
		return nil, &apiError{Status: 400, Code: "unknown_column", Message: fmt.Sprintf("%s has no column %q", t.Name, name)}
	}
	return c, nil
}

// pathExpr is alias.col->'a'->'b' (JSON) or, with text, ->> for the last
// key.
func pathExpr(alias string, c *Column, path []string, text bool) string {
	e := alias + "." + qi(c.Name)
	for i, k := range path {
		op := "->"
		if text && i == len(path)-1 {
			op = "->>"
		}
		e += op + lit(k)
	}
	return e
}

// selectList is the "expr AS name" list of a relation's select items.
func (b *builder) selectList(t *Table, alias string, items []SelectItem, depth int) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	add := func(name, expr string) error {
		if seen[name] {
			return badRequest("invalid_select", "%q is selected twice", name)
		}
		seen[name] = true
		out = append(out, expr+" AS "+qi(name))
		return nil
	}
	for _, it := range items {
		switch {
		case it.Star:
			for _, c := range t.Columns {
				if err := add(c.Name, alias+"."+qi(c.Name)); err != nil {
					return nil, err
				}
			}
		case it.Embed != nil:
			expr, name, err := b.embed(t, alias, it.Embed, depth+1)
			if err != nil {
				return nil, err
			}
			if it.Alias != "" {
				name = it.Alias
			}
			if err := add(name, expr); err != nil {
				return nil, err
			}
		default:
			c, err := b.column(t, it.Column)
			if err != nil {
				return nil, err
			}
			if len(it.Path) > 0 && !c.JSON() {
				return nil, badRequest("invalid_select", "%s isn't json or jsonb", c.Name)
			}
			name := c.Name
			if len(it.Path) > 0 {
				name = it.Path[len(it.Path)-1]
			}
			if it.Alias != "" {
				name = it.Alias
			}
			if err := add(name, pathExpr(alias, c, it.Path, false)); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// resolveEmbed finds the foreign key an embed names.
func (b *builder) resolveEmbed(t *Table, e *Embed) (fk *ForeignKey, toOne bool, target *Table, err error) {
	type cand struct {
		fk    *ForeignKey
		toOne bool
	}
	var cands []cand
	matchesHint := func(fk *ForeignKey) bool {
		if e.Hint == "" {
			return true
		}
		return fk.Name == e.Hint || (len(fk.Cols) == 1 && fk.Cols[0] == e.Hint)
	}
	for _, fk := range t.Out {
		single := len(fk.Cols) == 1
		if (fk.Ref.Name == e.Name || (single && (fk.Cols[0] == e.Name || fk.Cols[0] == e.Name+"_id"))) && matchesHint(fk) {
			cands = append(cands, cand{fk, true})
		}
	}
	for _, fk := range t.In {
		if fk.Table.Name == e.Name && matchesHint(fk) {
			cands = append(cands, cand{fk, false})
		}
	}
	switch len(cands) {
	case 0:
		return nil, false, nil, &apiError{Status: 400, Code: "unknown_relation",
			Message: fmt.Sprintf("%s has no foreign key to or from %q", t.Name, e.Name)}
	case 1:
		c := cands[0]
		if c.toOne {
			return c.fk, true, c.fk.Ref, nil
		}
		return c.fk, false, c.fk.Table, nil
	}
	return nil, false, nil, &apiError{Status: 400, Code: "ambiguous_relation",
		Message: fmt.Sprintf("%q matches more than one foreign key of %s: name one, %s!<column or constraint>(…)", e.Name, t.Name, e.Name)}
}

// embed is the subquery for an embedded relation: an object (or null)
// through this table's foreign key, or an array through one that
// references this table.
func (b *builder) embed(t *Table, alias string, e *Embed, depth int) (string, string, error) {
	fk, toOne, target, err := b.resolveEmbed(t, e)
	if err != nil {
		return "", "", err
	}
	if b.gate != nil {
		if err := b.gate(target); err != nil {
			return "", "", err
		}
	}
	b.tables = append(b.tables, target.Schema+"."+target.Name)
	ea := b.nextAlias()
	cols, err := b.selectList(target, ea, e.Select, depth)
	if err != nil {
		return "", "", err
	}
	var on []string
	for i := range fk.Cols {
		if toOne {
			on = append(on, ea+"."+qi(fk.RefCols[i])+" = "+alias+"."+qi(fk.Cols[i]))
		} else {
			on = append(on, ea+"."+qi(fk.Cols[i])+" = "+alias+"."+qi(fk.RefCols[i]))
		}
	}
	inner := "SELECT " + strings.Join(cols, ", ") + " FROM " + target.Qualified() + " " + ea + " WHERE " + strings.Join(on, " AND ")
	name := e.Name
	if toOne {
		return "(SELECT row_to_json(r) FROM (" + inner + " LIMIT 1) r)", name, nil
	}
	if len(target.PK) > 0 {
		var ks []string
		for _, k := range target.PK {
			ks = append(ks, ea+"."+qi(k))
		}
		inner += " ORDER BY " + strings.Join(ks, ", ")
	}
	return "(SELECT coalesce(json_agg(row_to_json(r)), '[]'::json) FROM (" + inner + " LIMIT " + strconv.Itoa(maxEmbedRows) + ") r)", name, nil
}

// where turns a filter into SQL.
func (b *builder) where(t *Table, alias string, f Filter) (string, error) {
	switch {
	case len(f.And) > 0 || len(f.Or) > 0:
		list, join := f.And, " AND "
		if len(f.Or) > 0 {
			list, join = f.Or, " OR "
		}
		var parts []string
		for _, x := range list {
			s, err := b.where(t, alias, x)
			if err != nil {
				return "", err
			}
			if s != "" {
				parts = append(parts, s)
			}
		}
		if len(parts) == 0 {
			return "", nil
		}
		return "(" + strings.Join(parts, join) + ")", nil
	case f.Not != nil:
		s, err := b.where(t, alias, *f.Not)
		if err != nil || s == "" {
			return s, err
		}
		return "NOT " + s, nil
	case f.Column == "":
		return "", nil
	}
	c, err := b.column(t, f.Column)
	if err != nil {
		return "", err
	}
	if len(f.Path) > 0 && !c.JSON() {
		return "", badRequest("invalid_filter", "%s isn't json or jsonb", c.Name)
	}
	// A JSON path compares as text; a column as its own type.
	lhs := alias + "." + qi(c.Name)
	cast := "::" + c.Type
	if len(f.Path) > 0 {
		lhs, cast = pathExpr(alias, c, f.Path, true), ""
	}
	if f.Null && f.Op != "is" {
		return "", badRequest("invalid_filter", "compare with null using is")
	}
	switch f.Op {
	case "eq", "neq", "lt", "lte", "gt", "gte":
		sym := map[string]string{"eq": "=", "neq": "<>", "lt": "<", "lte": "<=", "gt": ">", "gte": ">="}[f.Op]
		return lhs + " " + sym + " " + b.param(f.Value) + cast, nil
	case "in":
		arr := "::text[]"
		if cast != "" {
			arr = "::text[]" + cast + "[]"
		}
		return lhs + " = ANY(" + b.param(f.Values) + arr + ")", nil
	case "like", "ilike":
		return lhs + "::text " + strings.ToUpper(f.Op) + " " + b.param(f.Value), nil
	case "is":
		switch f.Value {
		case "null":
			return lhs + " IS NULL", nil
		case "true":
			return lhs + " IS TRUE", nil
		default:
			return lhs + " IS FALSE", nil
		}
	case "contains", "contained_by":
		if len(f.Path) > 0 {
			lhs, cast = pathExpr(alias, c, f.Path, false), "::jsonb"
			lhs = "(" + lhs + ")::jsonb"
		}
		sym := "@>"
		if f.Op == "contained_by" {
			sym = "<@"
		}
		return lhs + " " + sym + " " + b.param(f.Value) + cast, nil
	case "search":
		if c.TypName == "tsvector" && len(f.Path) == 0 {
			return lhs + " @@ websearch_to_tsquery(" + b.param(f.Value) + ")", nil
		}
		return "to_tsvector(" + lhs + "::text) @@ websearch_to_tsquery(" + b.param(f.Value) + ")", nil
	}
	return "", badRequest("invalid_filter", "unknown operator %q", f.Op)
}

// orderKeys are the request's order plus the primary key, so pages are
// stable.
func orderKeys(t *Table, order []Order) ([]Order, error) {
	out := append([]Order(nil), order...)
	seen := map[string]bool{}
	for _, o := range order {
		if t.Col(o.Column) == nil {
			return nil, &apiError{Status: 400, Code: "unknown_column", Message: fmt.Sprintf("%s has no column %q", t.Name, o.Column)}
		}
		seen[o.Column] = true
	}
	for _, k := range t.PK {
		if !seen[k] {
			out = append(out, Order{Column: k})
		}
	}
	return out, nil
}

// cursorPayload is what a cursor carries: the last row's order key values
// (as text, nil for NULL) and a hash of the order they belong to.
type cursorPayload struct {
	K []*string `json:"k"`
	S string    `json:"s"`
}

func orderSig(t *Table, keys []Order) string {
	h := sha256.New()
	h.Write([]byte(t.Schema + "." + t.Name))
	for _, k := range keys {
		fmt.Fprintf(h, "|%s:%v", k.Column, k.Desc)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func encodeCursor(t *Table, keys []Order, vals []*string) string {
	b, _ := json.Marshal(cursorPayload{K: vals, S: orderSig(t, keys)})
	return base64.RawURLEncoding.EncodeToString(b)
}

// after is the keyset condition "rows after the cursor's row" under keys,
// with Postgres's default NULL placement (last ascending, first
// descending).
func (b *builder) after(t *Table, alias string, keys []Order, cursor string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", badRequest("invalid_cursor", "the cursor is malformed")
	}
	var c cursorPayload
	if json.Unmarshal(raw, &c) != nil || len(c.K) != len(keys) || c.S != orderSig(t, keys) {
		return "", badRequest("invalid_cursor", "the cursor belongs to another query or order")
	}
	var ors []string
	for i, k := range keys {
		col := t.Col(k.Column)
		ref := alias + "." + qi(col.Name)
		var ands []string
		for j := 0; j < i; j++ {
			cj := t.Col(keys[j].Column)
			rj := alias + "." + qi(cj.Name)
			if c.K[j] == nil {
				ands = append(ands, rj+" IS NULL")
			} else {
				ands = append(ands, rj+" = "+b.param(*c.K[j])+"::"+cj.Type)
			}
		}
		var strict string
		switch {
		case !k.Desc && c.K[i] == nil:
			strict = "" // nothing sorts after NULL ascending
		case !k.Desc:
			strict = "(" + ref + " > " + b.param(*c.K[i]) + "::" + col.Type + " OR " + ref + " IS NULL)"
		case c.K[i] == nil:
			strict = ref + " IS NOT NULL"
		default:
			strict = ref + " < " + b.param(*c.K[i]) + "::" + col.Type
		}
		if strict == "" {
			continue
		}
		ors = append(ors, "("+strings.Join(append(ands, strict), " AND ")+")")
	}
	if len(ors) == 0 {
		return "false", nil
	}
	return "(" + strings.Join(ors, " OR ") + ")", nil
}

// statement is a read's SQL: each row is (json, order key texts...).
type statement struct {
	SQL       string
	Args      []any
	CountSQL  string // the filtered relation, for counts
	CountArgs []any
	Keys      []Order
	Limit     int
	Tables    []string // every relation it reads
}

func (b *builder) read(t *Table, q Query) (statement, error) {
	if countFilters(q.Where) > maxFilters {
		return statement{}, badRequest("too_many_filters", "at most %d filters", maxFilters)
	}
	alias := b.nextAlias()
	cols, err := b.selectList(t, alias, q.Select, 0)
	if err != nil {
		return statement{}, err
	}
	conds := []string{}
	if q.PKValue != nil {
		if len(t.PK) != 1 {
			return statement{}, badRequest("no_single_key", "%s has no single-column primary key", t.Name)
		}
		c := t.Col(t.PK[0])
		conds = append(conds, alias+"."+qi(c.Name)+" = "+b.param(*q.PKValue)+"::"+c.Type)
	}
	w, err := b.where(t, alias, q.Where)
	if err != nil {
		return statement{}, err
	}
	if w != "" {
		conds = append(conds, w)
	}
	// The filtered relation, without the cursor, for counts.
	countWhere := strings.Join(conds, " AND ")
	countArgs := append([]any(nil), b.args...)
	keys, err := orderKeys(t, q.Order)
	if err != nil {
		return statement{}, err
	}
	if q.Cursor != "" {
		if q.Offset > 0 {
			return statement{}, badRequest("invalid_cursor", "use cursor or offset, not both")
		}
		a, err := b.after(t, alias, keys, q.Cursor)
		if err != nil {
			return statement{}, err
		}
		conds = append(conds, a)
	}
	limit := q.Limit
	if limit == 0 {
		limit = defaultLimit
	}
	if limit < 1 || limit > maxLimit {
		return statement{}, badRequest("invalid_limit", "limit is 1 to %d", maxLimit)
	}
	if q.Offset < 0 || q.Offset > maxOffset {
		return statement{}, badRequest("invalid_offset", "offset is 0 to %d; use cursor beyond", maxOffset)
	}
	var sb strings.Builder
	sb.WriteString("SELECT (SELECT row_to_json(r) FROM (SELECT ")
	sb.WriteString(strings.Join(cols, ", "))
	sb.WriteString(") r)")
	var ord []string
	for _, k := range keys {
		ref := alias + "." + qi(k.Column)
		sb.WriteString(", " + ref + "::text")
		if k.Desc {
			ref += " DESC"
		}
		ord = append(ord, ref)
	}
	sb.WriteString(" FROM " + t.Qualified() + " " + alias)
	if len(conds) > 0 {
		sb.WriteString(" WHERE " + strings.Join(conds, " AND "))
	}
	if len(ord) > 0 {
		sb.WriteString(" ORDER BY " + strings.Join(ord, ", "))
	}
	sb.WriteString(" LIMIT " + strconv.Itoa(limit))
	if q.Offset > 0 {
		sb.WriteString(" OFFSET " + strconv.Itoa(q.Offset))
	}
	countSQL := "SELECT count(*) FROM " + t.Qualified() + " " + alias
	if countWhere != "" {
		countSQL += " WHERE " + countWhere
	}
	return statement{SQL: sb.String(), Args: b.args, CountSQL: countSQL, CountArgs: countArgs, Keys: keys, Limit: limit,
		Tables: append(b.tables, t.Schema+"."+t.Name)}, nil
}
