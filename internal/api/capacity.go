package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/capacity"
	"github.com/israel-duff/pgdock/internal/cloud"
	"github.com/israel-duff/pgdock/internal/costs"
	"github.com/israel-duff/pgdock/internal/store"
)

func (s *Server) capacitySvc(w http.ResponseWriter) *capacity.Service {
	if s.capacity == nil {
		writeError(w, http.StatusNotImplemented, "not_configured", "capacity automation needs nodes and agents on this server")
	}
	return s.capacity
}

func (s *Server) costsSvc(w http.ResponseWriter) *costs.Service {
	if s.costs == nil {
		writeError(w, http.StatusNotImplemented, "not_configured", "cost attribution isn't set up on this server")
	}
	return s.costs
}

func (s *Server) capacityError(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, capacity.ErrInvalid), errors.Is(err, costs.ErrInvalid):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	case errors.Is(err, capacity.ErrNotFound), errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, capacity.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, costs.ErrNoRate):
		writeError(w, http.StatusConflict, "no_rate", err.Error())
	default:
		s.internalError(w, what, err)
	}
}

// recode converts between the API's types and the services' through JSON:
// their fields are the same.
func recode(from, to any) error {
	b, err := json.Marshal(from)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, to)
}

func toAPIProposal(p store.CapacityProposal, nodeName string) gen.CapacityProposal {
	out := gen.CapacityProposal{
		Id: p.ID, Region: p.Region, Tier: gen.CapacityProposalTier(p.Tier), Reason: p.Reason, Provider: p.Provider, ServerType: p.ServerType,
		Location: p.Location, MonthlyCostMinor: p.MonthlyCostMinor, Currency: p.Currency, Status: gen.CapacityProposalStatus(p.Status),
		Auto: p.Auto, NodeId: p.NodeID, OperationId: p.OperationID, Error: p.Error, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
	if nodeName != "" {
		out.NodeName = &nodeName
	}
	return out
}

// AdminCapacity implements GET /api/v1/admin/capacity.
func (s *Server) AdminCapacity(w http.ResponseWriter, r *http.Request) {
	cs := s.capacitySvc(w)
	if cs == nil {
		return
	}
	ctx := r.Context()
	st, err := cs.Settings(ctx)
	if err != nil {
		s.internalError(w, "capacity", err)
		return
	}
	look, _, err := cs.Look(ctx)
	if err != nil {
		s.internalError(w, "capacity", err)
		return
	}
	q := store.New(s.db)
	nodes, err := q.ListNodes(ctx)
	if err != nil {
		s.internalError(w, "capacity", err)
		return
	}
	names := map[uuid.UUID]string{}
	out := gen.Capacity{Provider: cs.Provider().Name(), CanCreate: cs.Provider().Name() != cloud.Manual, Region: cs.Region(),
		Nodes: []gen.Node{}, Proposals: []gen.CapacityProposal{}, Moves: []gen.RebalanceMove{}}
	for _, n := range nodes {
		names[n.ID] = n.Name
		if n.Status == "removed" || n.Role == "pooler" {
			continue
		}
		out.Nodes = append(out.Nodes, s.toAPINode(n))
		if n.MonthlyCostMinor != nil {
			if n.CostCurrency == st.BudgetCurrency {
				out.BudgetUsedMinor += *n.MonthlyCostMinor
			} else if s.costs != nil {
				if v, err := s.costs.Convert(ctx, *n.MonthlyCostMinor, n.CostCurrency, st.BudgetCurrency); err == nil {
					out.BudgetUsedMinor += v
				}
			}
		}
	}
	if err := recode(st, &out.Settings); err != nil {
		s.internalError(w, "capacity", err)
		return
	}
	if err := recode(look.Shared, &out.Shared); err != nil {
		s.internalError(w, "capacity", err)
		return
	}
	if err := recode(look.Dedicated, &out.Dedicated); err != nil {
		s.internalError(w, "capacity", err)
		return
	}
	if out.Shared == nil {
		out.Shared = []gen.RegionShared{}
	}
	if out.Dedicated == nil {
		out.Dedicated = []gen.RegionDedicated{}
	}
	props, err := q.ListCapacityProposals(ctx)
	if err != nil {
		s.internalError(w, "capacity", err)
		return
	}
	for _, p := range props {
		name := ""
		if p.NodeID != nil {
			name = names[*p.NodeID]
		}
		out.Proposals = append(out.Proposals, toAPIProposal(p, name))
	}
	moves, err := q.ListRebalanceMoves(ctx)
	if err != nil {
		s.internalError(w, "capacity", err)
		return
	}
	for _, m := range moves {
		out.Moves = append(out.Moves, gen.RebalanceMove{
			Id: m.ID, Batch: m.Batch, Kind: gen.RebalanceMoveKind(m.Kind), ProjectId: m.ProjectID, ProjectName: m.ProjectName, OrgId: m.OrgID,
			FromNode: m.FromNode, FromName: m.FromName, ToNode: m.ToNode, ToName: m.ToName, Reason: m.Reason,
			Status: gen.RebalanceMoveStatus(m.Status), OperationId: m.OperationID, Error: m.Error, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// PutCapacitySettings implements PUT /api/v1/admin/capacity/settings.
func (s *Server) PutCapacitySettings(w http.ResponseWriter, r *http.Request) {
	cs := s.capacitySvc(w)
	if cs == nil {
		return
	}
	var req gen.CapacitySettings
	if !decodeJSON(w, r, &req) {
		return
	}
	var st capacity.Settings
	if err := recode(req, &st); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.Edge == nil {
		// A client from before edge nodes (V4.1 §11) keeps the edge tier as it is.
		cur, err := cs.Settings(r.Context())
		if err != nil {
			s.internalError(w, "capacity settings", err)
			return
		}
		st.Edge = cur.Edge
	}
	st.BudgetCurrency = strings.ToUpper(st.BudgetCurrency)
	if err := cs.SetSettings(r.Context(), st); err != nil {
		s.capacityError(w, "capacity settings", err)
		return
	}
	a := auditFrom(r.Context())
	a.set("auto_apply", st.AutoApply)
	a.set("monthly_budget_minor", st.MonthlyBudgetMinor)
	a.set("budget_currency", st.BudgetCurrency)
	var out gen.CapacitySettings
	_ = recode(st, &out)
	writeJSON(w, http.StatusOK, out)
}

// EvaluateCapacity implements POST /api/v1/admin/capacity/evaluate.
func (s *Server) EvaluateCapacity(w http.ResponseWriter, r *http.Request) {
	cs := s.capacitySvc(w)
	if cs == nil {
		return
	}
	made, err := cs.Evaluate(r.Context())
	if err != nil && len(made) == 0 {
		s.capacityError(w, "evaluate capacity", err)
		return
	}
	out := gen.CapacityProposalList{Items: []gen.CapacityProposal{}}
	for _, p := range made {
		out.Items = append(out.Items, toAPIProposal(p, ""))
	}
	auditFrom(r.Context()).set("proposals", len(made))
	writeJSON(w, http.StatusOK, out)
}

// ApproveCapacityProposal implements POST /api/v1/admin/capacity/proposals/{proposal_id}/approve.
func (s *Server) ApproveCapacityProposal(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	cs := s.capacitySvc(w)
	if cs == nil {
		return
	}
	p, _, err := cs.Approve(r.Context(), id, userID(r.Context()))
	if err != nil {
		s.capacityError(w, "approve proposal", err)
		return
	}
	a := auditFrom(r.Context())
	a.target("capacity_proposal", id.String())
	a.set("server_type", p.ServerType)
	a.set("monthly_cost_minor", p.MonthlyCostMinor)
	writeJSON(w, http.StatusOK, toAPIProposal(p, ""))
}

// RejectCapacityProposal implements POST /api/v1/admin/capacity/proposals/{proposal_id}/reject.
func (s *Server) RejectCapacityProposal(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	cs := s.capacitySvc(w)
	if cs == nil {
		return
	}
	p, err := cs.Reject(r.Context(), id, userID(r.Context()))
	if err != nil {
		s.capacityError(w, "reject proposal", err)
		return
	}
	auditFrom(r.Context()).target("capacity_proposal", id.String())
	writeJSON(w, http.StatusOK, toAPIProposal(p, ""))
}

// PlanRebalance implements POST /api/v1/admin/capacity/rebalance.
func (s *Server) PlanRebalance(w http.ResponseWriter, r *http.Request) {
	cs := s.capacitySvc(w)
	if cs == nil {
		return
	}
	batch, n, err := cs.Rebalance(r.Context())
	if err != nil {
		s.capacityError(w, "rebalance", err)
		return
	}
	out := gen.RebalancePlan{Moves: int64(n)}
	if batch != uuid.Nil {
		out.Batch = &batch
	}
	auditFrom(r.Context()).set("moves", n)
	writeJSON(w, http.StatusOK, out)
}

// DecideRebalanceBatch implements POST /api/v1/admin/capacity/batches/{batch_id}.
func (s *Server) DecideRebalanceBatch(w http.ResponseWriter, r *http.Request, batch uuid.UUID) {
	cs := s.capacitySvc(w)
	if cs == nil {
		return
	}
	var req gen.BatchDecision
	if !decodeJSON(w, r, &req) {
		return
	}
	n, err := cs.DecideBatch(r.Context(), batch, req.Approve)
	if err != nil {
		s.capacityError(w, "rebalance batch", err)
		return
	}
	a := auditFrom(r.Context())
	a.set("batch", batch.String())
	a.set("approve", req.Approve)
	writeJSON(w, http.StatusOK, gen.RebalancePlan{Batch: &batch, Moves: n})
}

// CloudCatalog implements GET /api/v1/admin/cloud/catalog.
func (s *Server) CloudCatalog(w http.ResponseWriter, r *http.Request) {
	cs := s.capacitySvc(w)
	if cs == nil {
		return
	}
	cat, err := cs.Catalog(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "provider", err.Error())
		return
	}
	out := gen.ServerPriceList{Provider: cs.Provider().Name(), Items: []gen.ServerPrice{}}
	_ = recode(cat, &out.Items)
	if out.Items == nil {
		out.Items = []gen.ServerPrice{}
	}
	writeJSON(w, http.StatusOK, out)
}

// DrainNode implements POST /api/v1/nodes/{id}/drain.
func (s *Server) DrainNode(w http.ResponseWriter, r *http.Request, id gen.NodeID) {
	cs := s.capacitySvc(w)
	if cs == nil {
		return
	}
	n, moves, err := cs.Drain(r.Context(), id)
	if err != nil {
		s.capacityError(w, "drain", err)
		return
	}
	a := auditFrom(r.Context())
	a.target("node", id.String())
	a.set("moves", moves)
	out := gen.DrainResult{Node: s.toAPINode(n), Moves: moves}
	// A node that is leaving takes no etcd member with it (V3.1 §3.2).
	if ds := s.backups; ds != nil && ds.Dedicated != nil && ds.Dedicated.Etcd != nil {
		op, err := ds.Dedicated.Etcd.ReplaceOnDrain(r.Context(), id)
		switch {
		case err != nil:
			msg := "The node holds an etcd member that can't be moved yet: " + err.Error()
			out.Warning = &msg
		case op != nil:
			out.EtcdReplacement = &op.ID
			a.set("etcd_replacement", op.ID.String())
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// StopDrain implements DELETE /api/v1/nodes/{id}/drain.
func (s *Server) StopDrain(w http.ResponseWriter, r *http.Request, id gen.NodeID) {
	cs := s.capacitySvc(w)
	if cs == nil {
		return
	}
	n, err := cs.StopDrain(r.Context(), id)
	if err != nil {
		s.capacityError(w, "stop drain", err)
		return
	}
	auditFrom(r.Context()).target("node", id.String())
	writeJSON(w, http.StatusOK, s.toAPINode(n))
}

// SetNodeCost implements PUT /api/v1/nodes/{id}/cost.
func (s *Server) SetNodeCost(w http.ResponseWriter, r *http.Request, id gen.NodeID) {
	var req gen.NodeCost
	if !decodeJSON(w, r, &req) {
		return
	}
	cur := strings.ToUpper(strings.TrimSpace(req.Currency))
	if len(cur) != 3 || (req.MonthlyCostMinor != nil && *req.MonthlyCostMinor < 0) {
		writeError(w, http.StatusBadRequest, "bad_request", "a currency code (EUR) and a monthly cost of zero or more are required")
		return
	}
	if req.Region != nil && !regionName.MatchString(*req.Region) {
		writeError(w, http.StatusBadRequest, "bad_request", "regions are lowercase letters, digits and dashes")
		return
	}
	n, err := store.New(s.db).SetNodeCost(r.Context(), store.SetNodeCostParams{
		ID: id, MonthlyCostMinor: req.MonthlyCostMinor, CostCurrency: cur, ServerType: req.ServerType, Region: req.Region, Keep: req.Keep,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "node not found")
		return
	}
	if err != nil {
		s.internalError(w, "node cost", err)
		return
	}
	a := auditFrom(r.Context())
	a.target("node", id.String())
	if req.MonthlyCostMinor != nil {
		a.set("monthly_cost_minor", *req.MonthlyCostMinor)
	}
	a.set("currency", cur)
	writeJSON(w, http.StatusOK, s.toAPINode(n))
}

var regionName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

func parseMonth(v *string, now time.Time) (time.Time, bool) {
	if v == nil || *v == "" {
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC), true
	}
	t, err := time.Parse("2006-01", *v)
	return t, err == nil
}

// AdminCosts implements GET /api/v1/admin/costs.
func (s *Server) AdminCosts(w http.ResponseWriter, r *http.Request, params gen.AdminCostsParams) {
	cs := s.costsSvc(w)
	if cs == nil {
		return
	}
	month, ok := parseMonth(params.Month, s.now())
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_request", "month is YYYY-MM")
		return
	}
	m, err := cs.Margins(r.Context(), month)
	if err != nil {
		s.capacityError(w, "costs", err)
		return
	}
	if params.Format != nil && *params.Format == gen.AdminCostsParamsFormatCsv {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="pgdock-margins-`+m.Month+`.csv"`)
		_ = costs.WriteMarginsCSV(w, m)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// AttributeCosts implements POST /api/v1/admin/costs/attribute.
func (s *Server) AttributeCosts(w http.ResponseWriter, r *http.Request) {
	cs := s.costsSvc(w)
	if cs == nil {
		return
	}
	var req gen.AttributeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	from, to := req.From.Time, req.To.Time
	if to.Before(from) || to.Sub(from) > 92*24*time.Hour || !to.Before(s.now().AddDate(0, 0, 1)) {
		writeError(w, http.StatusBadRequest, "bad_request", "from and to are dates, at most 92 days apart, not in the future")
		return
	}
	n := 0
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		if err := cs.Attribute(r.Context(), d); err != nil {
			s.internalError(w, "attribute costs", err)
			return
		}
		n++
	}
	a := auditFrom(r.Context())
	a.set("from", from.Format(time.DateOnly))
	a.set("to", to.Format(time.DateOnly))
	writeJSON(w, http.StatusOK, gen.AttributeResult{Days: n})
}

// GetCostSettings implements GET /api/v1/admin/costs/settings.
func (s *Server) GetCostSettings(w http.ResponseWriter, r *http.Request) {
	cs := s.costsSvc(w)
	if cs == nil {
		return
	}
	st, err := cs.Settings(r.Context())
	if err != nil {
		s.internalError(w, "cost settings", err)
		return
	}
	if st.Overheads == nil {
		st.Overheads = []costs.Overhead{}
	}
	writeJSON(w, http.StatusOK, st)
}

// PutCostSettings implements PUT /api/v1/admin/costs/settings.
func (s *Server) PutCostSettings(w http.ResponseWriter, r *http.Request) {
	cs := s.costsSvc(w)
	if cs == nil {
		return
	}
	var req gen.CostSettings
	if !decodeJSON(w, r, &req) {
		return
	}
	var st costs.Settings
	if err := recode(req, &st); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	st.Currency = strings.ToUpper(st.Currency)
	for i := range st.Overheads {
		st.Overheads[i].Currency = strings.ToUpper(st.Overheads[i].Currency)
	}
	if err := cs.SetSettings(r.Context(), st); err != nil {
		s.capacityError(w, "cost settings", err)
		return
	}
	if st.Overheads == nil {
		st.Overheads = []costs.Overhead{}
	}
	writeJSON(w, http.StatusOK, st)
}

func toAPIFX(f store.FxRate) gen.FXRate {
	v, _ := f.NgnPerUnit.Float64Value()
	return gen.FXRate{Id: f.ID, Currency: f.Currency, NgnPerUnit: v.Float64, EffectiveAt: f.EffectiveAt, Source: f.Source}
}

// ListFXRates implements GET /api/v1/admin/fx-rates.
func (s *Server) ListFXRates(w http.ResponseWriter, r *http.Request) {
	q := store.New(s.db)
	cur, err := q.LatestFXRates(r.Context(), s.now())
	if err != nil {
		s.internalError(w, "fx rates", err)
		return
	}
	hist, err := q.FXRateHistory(r.Context())
	if err != nil {
		s.internalError(w, "fx rates", err)
		return
	}
	out := gen.FXRateList{Current: []gen.FXRate{}, History: []gen.FXRate{}}
	for _, f := range cur {
		out.Current = append(out.Current, toAPIFX(f))
	}
	for _, f := range hist {
		out.History = append(out.History, toAPIFX(f))
	}
	writeJSON(w, http.StatusOK, out)
}

// SetFXRate implements POST /api/v1/admin/fx-rates.
func (s *Server) SetFXRate(w http.ResponseWriter, r *http.Request) {
	cs := s.costsSvc(w)
	if cs == nil {
		return
	}
	var req gen.FXRateInput
	if !decodeJSON(w, r, &req) {
		return
	}
	f, err := cs.SetRate(r.Context(), req.Currency, req.NgnPerUnit, req.EffectiveAt, userID(r.Context()))
	if err != nil {
		s.capacityError(w, "fx rate", err)
		return
	}
	a := auditFrom(r.Context())
	a.set("currency", f.Currency)
	a.set("ngn_per_unit", req.NgnPerUnit)
	writeJSON(w, http.StatusCreated, toAPIFX(f))
}
