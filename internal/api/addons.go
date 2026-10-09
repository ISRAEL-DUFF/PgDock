package api

import (
	"fmt"
	"net/http"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/dedicated"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/store"
)

// Billing add-ons (V4.1 §4): a dedicated instance's point-in-time recovery
// window and a project's backup retention, within the plan's limits.

// addonAllowed checks that the request's organisation's plan allows want
// for the limit key, answering 403 plan_required when it doesn't; a spend
// cap refuses new billable add-ons too. True means go on.
func (s *Server) addonAllowed(w http.ResponseWriter, r *http.Request, key string, want int64, what string) bool {
	if s.tenancy == nil {
		return true
	}
	org := accessFrom(r.Context()).OrgID
	l, _, err := s.tenancy.Limits(r.Context(), org)
	if err != nil {
		s.internalError(w, "plan limits", err)
		return false
	}
	if most, ok := l.Get(key); ok && want > most {
		writeError(w, http.StatusForbidden, "plan_required", fmt.Sprintf("%s needs a Pro or Team plan (Organisation → Billing)", what))
		return false
	}
	return s.checkQuota(w, s.tenancy.CheckSpendCap(r.Context(), org))
}

// UpdateProjectInstance implements PATCH /api/v1/projects/{id}/instance.
func (s *Server) UpdateProjectInstance(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	if !s.requireProjects(w) {
		return
	}
	var req gen.InstanceUpdate
	if !decodeJSON(w, r, &req) {
		return
	}
	p, err := s.tenantProjectLive(r.Context())
	if err != nil {
		s.provisionError(w, "instance", err)
		return
	}
	if p.Tier != provision.TierDedicated {
		writeError(w, http.StatusConflict, "conflict", "instance settings are for dedicated projects")
		return
	}
	a := auditFrom(r.Context())
	a.target("project", id.String())
	q := store.New(s.db)
	if req.PitrDays != nil {
		days := int(*req.PitrDays)
		if days != 7 && days != 14 && days != 30 {
			writeError(w, http.StatusBadRequest, "bad_request", "pitr_days must be 7, 14 or 30")
			return
		}
		a.set("pitr_days", days)
		if days > dedicated.DefaultPITRDays && !s.addonAllowed(w, r, store.LimitPITRDaysMax, int64(days), fmt.Sprintf("%d-day point-in-time recovery", days)) {
			return
		}
		if err := q.SetInstancePITRDays(r.Context(), store.SetInstancePITRDaysParams{ID: p.InstanceID, PitrDays: int32(days)}); err != nil {
			s.internalError(w, "instance", err)
			return
		}
	}
	sum, ok := s.instanceSummaries(r.Context())[p.InstanceID]
	if !ok {
		s.provisionError(w, "instance", provision.ErrNotFound)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}
