package insights

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/israel-duff/pgdock/internal/store"
)

// Ranges for top queries and series.
var ranges = map[string]time.Duration{"1h": time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour}

// Range parses a range ("" is 24h).
func Range(r string) (time.Duration, error) {
	if r == "" {
		r = "24h"
	}
	d, ok := ranges[r]
	if !ok {
		return 0, invalid("range is 1h, 24h, 7d or 30d")
	}
	return d, nil
}

// step is a series' resolution for a range.
func step(d time.Duration) time.Duration {
	switch {
	case d <= 24*time.Hour:
		return bucketSize
	case d <= 7*24*time.Hour:
		return time.Hour
	default:
		return 6 * time.Hour
	}
}

// TopQuery is one query's totals over a range.
type TopQuery struct {
	QueryID    int64
	Query      string
	HasExample bool
	Calls      int64
	TotalMS    float64
	MeanMS     float64
	MaxMS      float64
	Rows       int64
	HitRatio   float64
	// Share is the query's share of the project's total time.
	Share float64
}

// Top lists a project's queries over d by sort (total, mean, calls, rows).
func (s *Service) Top(ctx context.Context, p store.Project, d time.Duration, sort string, limit int) ([]TopQuery, error) {
	if ok, err := s.Available(ctx, p); err != nil || !ok {
		return nil, errors.Join(err, notAvailable(ok))
	}
	switch sort {
	case "":
		sort = "total"
	case "total", "mean", "calls", "rows":
	default:
		return nil, invalid("sort is total, mean, calls or rows")
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	since := s.Now().Add(-d)
	q := store.New(s.db)
	totals, err := q.QueryTotals(ctx, store.QueryTotalsParams{ProjectID: p.ID, Since: since})
	if err != nil {
		return nil, err
	}
	rows, err := q.TopQueries(ctx, store.TopQueriesParams{ProjectID: p.ID, Since: since, Sort: sort, Lim: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]TopQuery, 0, len(rows))
	for _, r := range rows {
		t := TopQuery{QueryID: r.Queryid, Query: r.Query, HasExample: r.HasExample, Calls: r.Calls, TotalMS: r.TotalMs,
			MaxMS: r.MaxMs, Rows: r.Rows, HitRatio: r.HitRatio, MeanMS: r.MeanMs}
		if totals.TotalMs > 0 {
			t.Share = r.TotalMs / totals.TotalMs
		}
		out = append(out, t)
	}
	return out, nil
}

func pgInterval(d time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: d.Microseconds(), Valid: true}
}

func notAvailable(ok bool) error {
	if ok {
		return nil
	}
	return ErrNotAvailable
}

// Point is one step of a query's series.
type Point struct {
	TS      time.Time
	Calls   int64
	TotalMS float64
	MeanMS  float64
	MaxMS   float64
	Rows    int64
}

// Detail is one query: its text, example, totals and series.
type Detail struct {
	TopQuery
	Example   *string
	ExampleAt *time.Time
	FirstSeen time.Time
	LastSeen  time.Time
	Step      time.Duration
	Series    []Point
}

// Query returns one query's detail over d.
func (s *Service) Query(ctx context.Context, p store.Project, queryID int64, d time.Duration) (Detail, error) {
	var out Detail
	if ok, err := s.Available(ctx, p); err != nil || !ok {
		return out, errors.Join(err, notAvailable(ok))
	}
	q := store.New(s.db)
	t, err := q.GetQueryText(ctx, store.GetQueryTextParams{ProjectID: p.ID, Queryid: queryID})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	since := s.Now().Add(-d)
	st := step(d)
	series, err := q.QuerySeries(ctx, store.QuerySeriesParams{ProjectID: p.ID, Queryid: queryID, Since: since, Step: pgInterval(st)})
	if err != nil {
		return out, err
	}
	out = Detail{TopQuery: TopQuery{QueryID: queryID, Query: t.Query, HasExample: t.Example != nil}, Example: t.Example, ExampleAt: t.ExampleAt,
		FirstSeen: t.FirstSeen, LastSeen: t.LastSeen, Step: st, Series: make([]Point, 0, len(series))}
	for _, r := range series {
		pt := Point{TS: r.Ts, Calls: r.Calls, TotalMS: r.TotalMs, MaxMS: r.MaxMs, Rows: r.Rows}
		if r.Calls > 0 {
			pt.MeanMS = r.TotalMs / float64(r.Calls)
		}
		out.Series = append(out.Series, pt)
		out.Calls += r.Calls
		out.TotalMS += r.TotalMs
		out.Rows += r.Rows
		out.MaxMS = max(out.MaxMS, r.MaxMs)
	}
	if out.Calls > 0 {
		out.MeanMS = out.TotalMS / float64(out.Calls)
	}
	return out, nil
}

// SlowQuery is one slow-query log entry.
type SlowQuery struct {
	QueryID    *int64
	Query      string
	DurationMS float64
	Source     string
	Role       string
	SeenAt     time.Time
}

// Slow lists a project's slow statements over d, newest first.
func (s *Service) Slow(ctx context.Context, p store.Project, d time.Duration) ([]SlowQuery, error) {
	if ok, err := s.Available(ctx, p); err != nil || !ok {
		return nil, errors.Join(err, notAvailable(ok))
	}
	rows, err := store.New(s.db).ListSlowQueries(ctx, store.ListSlowQueriesParams{ProjectID: p.ID, Since: s.Now().Add(-d), Lim: 200})
	if err != nil {
		return nil, err
	}
	out := make([]SlowQuery, 0, len(rows))
	for _, r := range rows {
		out = append(out, SlowQuery{QueryID: r.Queryid, Query: r.Query, DurationMS: r.DurationMs, Source: r.Source, Role: r.RoleName, SeenAt: r.SeenAt})
	}
	return out, nil
}

// Plan is an EXPLAIN result: the JSON plan and its estimated total cost.
type Plan struct {
	// Generic: planned from the normalised text with parameters left
	// unknown (no example has been captured, or none was asked for).
	Generic bool
	// Statement is what was explained.
	Statement string
	JSON      json.RawMessage
	TotalCost float64
	// Indexes are the indexes the plan uses.
	Indexes []string
	// SeqScans are the tables the plan reads whole.
	SeqScans []string
}

// Explain runs EXPLAIN (without ANALYZE) on query queryID as the project's
// role in a read-only transaction: on its latest example with literals, or
// as a generic plan.
func (s *Service) Explain(ctx context.Context, p store.Project, queryID int64, generic bool) (Plan, error) {
	var out Plan
	if ok, err := s.Available(ctx, p); err != nil || !ok {
		return out, errors.Join(err, notAvailable(ok))
	}
	t, err := store.New(s.db).GetQueryText(ctx, store.GetQueryTextParams{ProjectID: p.ID, Queryid: queryID})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	out.Statement, out.Generic = t.Query, true
	if !generic && t.Example != nil {
		out.Statement, out.Generic = *t.Example, false
	}
	err = s.console.ReadOnly(ctx, p.ID, func(conn *pgx.Conn) error {
		plan, err := explain(ctx, conn, out.Statement, out.Generic, true)
		if err != nil {
			return err
		}
		out.JSON, out.TotalCost, out.Indexes, out.SeqScans = plan.raw, plan.cost, plan.indexes, plan.seqScans
		return nil
	})
	return out, err
}

type planResult struct {
	raw      json.RawMessage
	cost     float64
	indexes  []string
	seqScans []string
	// filters: per table, the columns a sequential scan filters on.
	filters map[string][]string
}

// explain plans stmt (without ANALYZE: nothing runs). verbose qualifies
// relation names with their schema.
func explain(ctx context.Context, conn *pgx.Conn, stmt string, generic, verbose bool) (planResult, error) {
	stmt = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(stmt), ";"))
	opts := "FORMAT JSON"
	if generic {
		opts = "GENERIC_PLAN, " + opts
	}
	if verbose {
		opts = "VERBOSE, " + opts
	}
	// An example runs over the extended protocol, which refuses more than
	// one statement. A generic plan's $1… would be bind parameters there,
	// so it goes over the simple protocol; pg_stat_statements' text is one
	// statement, with its literals (semicolons among them) normalised away,
	// so a semicolon left means something else.
	var raw []byte
	if generic {
		if strings.Contains(stmt, ";") {
			return planResult{}, invalid("EXPLAIN: the statement is not a single statement")
		}
		// pgx's own simple protocol would fill in $1 client-side.
		res, err := conn.PgConn().Exec(ctx, "EXPLAIN ("+opts+") "+stmt).ReadAll()
		if err != nil {
			return planResult{}, invalid("EXPLAIN: %v", err)
		}
		if len(res) != 1 || len(res[0].Rows) == 0 || len(res[0].Rows[0]) == 0 {
			return planResult{}, invalid("EXPLAIN returned no plan")
		}
		raw = res[0].Rows[0][0]
	} else if err := conn.QueryRow(ctx, "EXPLAIN ("+opts+") "+stmt, pgx.QueryExecModeExec).Scan(&raw); err != nil {
		return planResult{}, invalid("EXPLAIN: %v", err)
	}
	var doc []struct {
		Plan planNode `json:"Plan"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc) == 0 {
		return planResult{}, invalid("EXPLAIN returned no plan")
	}
	r := planResult{raw: raw, cost: doc[0].Plan.TotalCost, filters: map[string][]string{}}
	walk(doc[0].Plan, &r)
	return r, nil
}

type planNode struct {
	NodeType  string     `json:"Node Type"`
	Relation  string     `json:"Relation Name"`
	Schema    string     `json:"Schema"`
	Alias     string     `json:"Alias"`
	Index     string     `json:"Index Name"`
	Filter    string     `json:"Filter"`
	TotalCost float64    `json:"Total Cost"`
	PlanRows  float64    `json:"Plan Rows"`
	Plans     []planNode `json:"Plans"`
}

func walk(n planNode, r *planResult) {
	if n.Index != "" {
		r.indexes = append(r.indexes, n.Index)
	}
	if n.NodeType == "Seq Scan" && n.Relation != "" {
		name := n.Relation
		if n.Schema != "" {
			name = n.Schema + "." + n.Relation
		}
		r.seqScans = append(r.seqScans, name)
		if n.Filter != "" {
			r.filters[name] = append(r.filters[name], filterColumns(n.Filter)...)
		}
	}
	for _, c := range n.Plans {
		walk(c, r)
	}
}
