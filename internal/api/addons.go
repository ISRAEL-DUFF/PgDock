package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/dedicated"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/internal/tenancy"
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
	out := gen.InstanceUpdated{}
	if req.Profile != nil || req.Cpus != nil || req.MemoryMb != nil || req.DiskGb != nil {
		plan, op, ok := s.resizeInstance(w, r, p, req)
		if !ok {
			return
		}
		out.Plan = plan
		out.Operation = op
	}
	sum, ok := s.instanceSummaries(r.Context())[p.InstanceID]
	if !ok {
		s.provisionError(w, "instance", provision.ErrNotFound)
		return
	}
	out.Instance = sum
	writeJSON(w, http.StatusOK, out)
}

// resizeInstance plans and (unless a dry run) queues a resize (V4.1 §5),
// answering a refusal itself; ok false means it did.
func (s *Server) resizeInstance(w http.ResponseWriter, r *http.Request, p store.Project, req gen.InstanceUpdate) (*gen.ResizePlan, *gen.Operation, bool) {
	ds := s.dedicatedSvc(w)
	if ds == nil {
		return nil, nil, false
	}
	var want dedicated.Size
	if req.Profile != nil {
		prof, ok := dedicated.ProfileByName(*req.Profile)
		if !ok {
			writeError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("unknown profile %q", *req.Profile))
			return nil, nil, false
		}
		want.CPUs, want.MemoryMB = prof.CPUs, prof.MemoryMB
	}
	if req.Cpus != nil {
		want.CPUs = float64(*req.Cpus)
	}
	if req.MemoryMb != nil {
		want.MemoryMB = *req.MemoryMb
	}
	if req.DiskGb != nil {
		want.DiskGB = *req.DiskGb
	}
	a := auditFrom(r.Context())
	a.set("size", want)
	plan, err := ds.PlanResize(r.Context(), p, want)
	if err != nil {
		s.resizeError(w, err)
		return nil, nil, false
	}
	if s.tenancy != nil {
		ok, err := s.tenancy.WithinAllowanceChange(r.Context(), p.OrgID,
			tenancy.Dedicated{CPUs: plan.From.CPUs, MemoryMB: plan.From.MemoryMB, DiskGB: plan.From.DiskGB},
			tenancy.Dedicated{CPUs: plan.To.CPUs, MemoryMB: plan.To.MemoryMB, DiskGB: plan.To.DiskGB})
		if err != nil {
			s.internalError(w, "resize", err)
			return nil, nil, false
		}
		if !ok {
			writeJSON(w, http.StatusConflict, gen.Error{Code: "quota_exceeded",
				Message: "this is beyond your organisation's dedicated allowance: ask the platform admin to raise it, or choose a smaller size"})
			return nil, nil, false
		}
		if !s.checkQuota(w, s.tenancy.CheckSpendCap(r.Context(), p.OrgID)) {
			return nil, nil, false
		}
	}
	out := &gen.ResizePlan{From: genSize(plan.From), To: genSize(plan.To), Restart: plan.Restart}
	if plan.Move != nil {
		out.MoveTo = &plan.MoveName
	}
	if req.DryRun != nil && *req.DryRun {
		return out, nil, true
	}
	op, _, err := ds.Resize(r.Context(), dedicated.ResizeParams{ProjectID: p.ID, Size: plan.To, CreatedBy: userID(r.Context())})
	if err != nil {
		s.resizeError(w, err)
		return nil, nil, false
	}
	gop, err := toAPIOperation(op)
	if err != nil {
		s.internalError(w, "resize", err)
		return nil, nil, false
	}
	return out, &gop, true
}

func (s *Server) resizeError(w http.ResponseWriter, err error) {
	if errors.Is(err, dedicated.ErrNoRoom) {
		writeError(w, http.StatusConflict, "no_capacity", err.Error())
		return
	}
	s.provisionError(w, "resize", err)
}

func genSize(sz dedicated.Size) gen.InstanceSize {
	return gen.InstanceSize{Cpus: float32(sz.CPUs), MemoryMb: sz.MemoryMB, DiskGb: sz.DiskGB}
}
