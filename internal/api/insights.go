package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/insights"
	"github.com/israel-duff/pgdock/internal/store"
)

// insightsProject returns the project when query insights are set up.
func (s *Server) insightsProject(w http.ResponseWriter, r *http.Request, id gen.ProjectID) (store.Project, bool) {
	if s.insights == nil {
		writeError(w, http.StatusNotImplemented, "not_configured", "query insights need the SQL console on this server")
		return store.Project{}, false
	}
	p, err := s.projects.Get(r.Context(), id)
	if err != nil {
		s.provisionError(w, "query insights", err)
		return p, false
	}
	return p, true
}

func (s *Server) insightsError(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, insights.ErrNotAvailable):
		writeError(w, http.StatusForbidden, "plan_required", err.Error())
	case errors.Is(err, insights.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, insights.ErrInvalid):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	default:
		s.consoleError(w, what, err)
	}
}

func queryID(w http.ResponseWriter, v string) (int64, bool) {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "query_id is pg_stat_statements' queryid")
		return 0, false
	}
	return n, true
}

func toAPIInsightQuery(t insights.TopQuery) gen.InsightQuery {
	return gen.InsightQuery{
		QueryId: strconv.FormatInt(t.QueryID, 10), Query: t.Query, HasExample: t.HasExample, Calls: t.Calls, TotalMs: t.TotalMS,
		MeanMs: t.MeanMS, MaxMs: t.MaxMS, Rows: t.Rows, HitRatio: t.HitRatio, Share: t.Share,
	}
}

func rangeOf(v *string) string {
	if v == nil || *v == "" {
		return "24h"
	}
	return *v
}

// ListInsightQueries implements GET /api/v1/projects/{id}/insights/queries.
func (s *Server) ListInsightQueries(w http.ResponseWriter, r *http.Request, id gen.ProjectID, params gen.ListInsightQueriesParams) {
	p, ok := s.insightsProject(w, r, id)
	if !ok {
		return
	}
	rng := rangeOf((*string)(params.Range))
	d, err := insights.Range(rng)
	if err != nil {
		s.insightsError(w, "top queries", err)
		return
	}
	sort, limit := "", 20
	if params.Sort != nil {
		sort = string(*params.Sort)
	}
	if params.Limit != nil {
		limit = *params.Limit
	}
	top, err := s.insights.Top(r.Context(), p, d, sort, limit)
	if err != nil {
		s.insightsError(w, "top queries", err)
		return
	}
	out := gen.InsightQueryList{Range: rng, Items: make([]gen.InsightQuery, 0, len(top))}
	for _, t := range top {
		out.Items = append(out.Items, toAPIInsightQuery(t))
	}
	writeJSON(w, http.StatusOK, out)
}

// GetInsightQuery implements GET /api/v1/projects/{id}/insights/queries/{query_id}.
func (s *Server) GetInsightQuery(w http.ResponseWriter, r *http.Request, id gen.ProjectID, qid gen.InsightQueryID, params gen.GetInsightQueryParams) {
	p, ok := s.insightsProject(w, r, id)
	if !ok {
		return
	}
	n, ok := queryID(w, qid)
	if !ok {
		return
	}
	d, err := insights.Range(rangeOf((*string)(params.Range)))
	if err != nil {
		s.insightsError(w, "query", err)
		return
	}
	det, err := s.insights.Query(r.Context(), p, n, d)
	if err != nil {
		s.insightsError(w, "query", err)
		return
	}
	out := gen.InsightQueryDetail{Query: toAPIInsightQuery(det.TopQuery), Example: det.Example, ExampleAt: det.ExampleAt,
		FirstSeen: det.FirstSeen, LastSeen: det.LastSeen, StepSeconds: int(det.Step.Seconds()), Series: make([]gen.InsightPoint, 0, len(det.Series))}
	for _, pt := range det.Series {
		out.Series = append(out.Series, gen.InsightPoint{Ts: pt.TS, Calls: pt.Calls, TotalMs: pt.TotalMS, MeanMs: pt.MeanMS, MaxMs: pt.MaxMS, Rows: pt.Rows})
	}
	writeJSON(w, http.StatusOK, out)
}

// ExplainInsightQuery implements POST /api/v1/projects/{id}/insights/explain.
func (s *Server) ExplainInsightQuery(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	p, ok := s.insightsProject(w, r, id)
	if !ok {
		return
	}
	var req gen.InsightExplainRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	n, ok := queryID(w, req.QueryId)
	if !ok {
		return
	}
	plan, err := s.insights.Explain(r.Context(), p, n, req.Generic != nil && *req.Generic)
	if err != nil {
		s.insightsError(w, "explain", err)
		return
	}
	out := gen.InsightPlan{Generic: plan.Generic, Statement: plan.Statement, Plan: plan.JSON, TotalCost: plan.TotalCost,
		Indexes: nonNil(plan.Indexes), SeqScans: nonNil(plan.SeqScans)}
	writeJSON(w, http.StatusOK, out)
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// ListSlowQueries implements GET /api/v1/projects/{id}/insights/slow.
func (s *Server) ListSlowQueries(w http.ResponseWriter, r *http.Request, id gen.ProjectID, params gen.ListSlowQueriesParams) {
	p, ok := s.insightsProject(w, r, id)
	if !ok {
		return
	}
	d, err := insights.Range(rangeOf((*string)(params.Range)))
	if err != nil {
		s.insightsError(w, "slow queries", err)
		return
	}
	list, err := s.insights.Slow(r.Context(), p, d)
	if err != nil {
		s.insightsError(w, "slow queries", err)
		return
	}
	out := gen.SlowQueryList{ThresholdMs: int(s.insights.SlowThreshold().Milliseconds()), Items: make([]gen.SlowQuery, 0, len(list))}
	for _, q := range list {
		sq := gen.SlowQuery{Query: q.Query, DurationMs: q.DurationMS, Source: gen.SlowQuerySource(q.Source), Role: q.Role, SeenAt: q.SeenAt}
		if q.QueryID != nil {
			v := strconv.FormatInt(*q.QueryID, 10)
			sq.QueryId = &v
		}
		out.Items = append(out.Items, sq)
	}
	writeJSON(w, http.StatusOK, out)
}

func toAPIIndexInfo(i insights.IndexInfo) gen.IndexInfo {
	return gen.IndexInfo{Schema: i.Schema, Table: i.Table, Name: i.Name, Definition: i.Definition, Bytes: i.Bytes, Scans: i.Scans}
}

// GetInsightIndexes implements GET /api/v1/projects/{id}/insights/indexes.
func (s *Server) GetInsightIndexes(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	p, ok := s.insightsProject(w, r, id)
	if !ok {
		return
	}
	rep, err := s.insights.Indexes(r.Context(), p)
	if err != nil {
		s.insightsError(w, "indexes", err)
		return
	}
	out := gen.IndexReport{Hypopg: gen.IndexReportHypopg(rep.Hypopg), StatsSince: rep.StatsSince,
		Suggestions: make([]gen.IndexSuggestion, 0, len(rep.Suggestions)), Unused: make([]gen.IndexInfo, 0, len(rep.Unused)),
		Duplicates: make([]gen.DuplicateIndex, 0, len(rep.Duplicates)), HeavySeqScans: make([]gen.TableScans, 0, len(rep.HeavySeqScans))}
	for _, sg := range rep.Suggestions {
		g := gen.IndexSuggestion{Schema: sg.Schema, Table: sg.Table, Columns: sg.Columns, Statement: sg.Statement, TableRows: sg.TableRows,
			SeqScans: sg.SeqScans, QueryIds: make([]string, 0, len(sg.QueryIDs)), Reasons: make([]gen.IndexSuggestionReasons, 0, len(sg.Reasons))}
		for _, q := range sg.QueryIDs {
			g.QueryIds = append(g.QueryIds, strconv.FormatInt(q, 10))
		}
		for _, rs := range sg.Reasons {
			g.Reasons = append(g.Reasons, gen.IndexSuggestionReasons(rs))
		}
		if err := recode(sg.Change, &g.Change); err != nil {
			s.internalError(w, "indexes", err)
			return
		}
		if e := sg.Estimate; e != nil {
			g.Estimate = &gen.IndexEstimate{CostBefore: e.CostBefore, CostAfter: e.CostAfter, Improvement: e.Improvement, UsesIndex: e.UsesIndex}
		}
		out.Suggestions = append(out.Suggestions, g)
	}
	for _, i := range rep.Unused {
		out.Unused = append(out.Unused, toAPIIndexInfo(i))
	}
	for _, d := range rep.Duplicates {
		out.Duplicates = append(out.Duplicates, gen.DuplicateIndex{Schema: d.Schema, Table: d.Table, Name: d.Name, Definition: d.Definition,
			Bytes: d.Bytes, Scans: d.Scans, Of: d.Of, Exact: d.Exact})
	}
	for _, t := range rep.HeavySeqScans {
		out.HeavySeqScans = append(out.HeavySeqScans, gen.TableScans{Schema: t.Schema, Table: t.Table, Rows: t.Rows, SeqScans: t.SeqScans,
			SeqTupRead: t.SeqTupRead, IdxScans: t.IdxScans})
	}
	writeJSON(w, http.StatusOK, out)
}

// GetInsightBloat implements GET /api/v1/projects/{id}/insights/bloat.
func (s *Server) GetInsightBloat(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	p, ok := s.insightsProject(w, r, id)
	if !ok {
		return
	}
	list, err := s.insights.Bloat(r.Context(), p)
	if err != nil {
		s.insightsError(w, "bloat", err)
		return
	}
	out := gen.BloatList{Items: make([]gen.TableBloat, 0, len(list))}
	for _, b := range list {
		out.Items = append(out.Items, gen.TableBloat{Schema: b.Schema, Table: b.Table, Bytes: b.Bytes, ExpectedBytes: b.ExpectedBytes,
			BloatBytes: b.BloatBytes, BloatRatio: b.BloatRatio, LiveRows: b.LiveRows, DeadRows: b.DeadRows, LastVacuum: b.LastVacuum,
			LastAutovacuum: b.LastAutovacuum})
	}
	writeJSON(w, http.StatusOK, out)
}

func toAPISession(x insights.Session) gen.DBSession {
	return gen.DBSession{Pid: int(x.PID), Role: x.Role, State: x.State, WaitEvent: x.WaitEvent, Query: x.Query,
		RunningMs: x.Running.Milliseconds(), InTransactionMs: x.InXact.Milliseconds(), Platform: x.Platform}
}

// GetInsightLocks implements GET /api/v1/projects/{id}/insights/locks.
func (s *Server) GetInsightLocks(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	p, ok := s.insightsProject(w, r, id)
	if !ok {
		return
	}
	list, err := s.insights.Locks(r.Context(), p)
	if err != nil {
		s.insightsError(w, "locks", err)
		return
	}
	out := gen.LockList{Items: make([]gen.LockBlock, 0, len(list))}
	for _, b := range list {
		lb := gen.LockBlock{Blocked: toAPISession(b.Blocked), Lock: b.Lock, Blockers: make([]gen.DBSession, 0, len(b.Blockers))}
		for _, x := range b.Blockers {
			lb.Blockers = append(lb.Blockers, toAPISession(x))
		}
		out.Items = append(out.Items, lb)
	}
	writeJSON(w, http.StatusOK, out)
}
