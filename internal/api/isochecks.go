package api

import (
	"net/http"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/isocheck"
)

func (s *Server) requireIsoChecks(w http.ResponseWriter) bool {
	if s.isochecks == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "isolation checks are not configured")
		return false
	}
	return true
}

// ListIsolationChecks implements GET /api/v1/security/isolation-checks.
func (s *Server) ListIsolationChecks(w http.ResponseWriter, r *http.Request) {
	if !s.requireIsoChecks(w) {
		return
	}
	insts, latest, err := s.isochecks.Latest(r.Context())
	if err != nil {
		s.internalError(w, "isolation checks", err)
		return
	}
	out := gen.IsolationCheckList{Items: make([]gen.IsolationCheck, 0, len(insts)), EveryDays: int(isocheck.Every.Hours() / 24)}
	for _, i := range insts {
		c := gen.IsolationCheck{InstanceId: i.ID, NodeId: i.NodeID, NodeName: i.NodeName, InstanceStatus: i.Status}
		if l, ok := latest[i.ID]; ok {
			c.Last = &gen.IsolationCheckRun{OperationId: l.ID, Status: l.Status, Error: l.Error, CreatedAt: l.CreatedAt, FinishedAt: l.FinishedAt}
		}
		out.Items = append(out.Items, c)
	}
	writeJSON(w, http.StatusOK, out)
}

// RunIsolationChecks implements POST /api/v1/security/isolation-checks.
func (s *Server) RunIsolationChecks(w http.ResponseWriter, r *http.Request) {
	if !s.requireIsoChecks(w) {
		return
	}
	ops, err := s.isochecks.EnqueueAll(r.Context(), userID(r.Context()))
	if err != nil {
		s.internalError(w, "run isolation checks", err)
		return
	}
	out := gen.OperationList{Items: make([]gen.Operation, 0, len(ops))}
	for _, op := range ops {
		g, err := toAPIOperation(op)
		if err != nil {
			s.internalError(w, "run isolation checks", err)
			return
		}
		out.Items = append(out.Items, g)
	}
	writeJSON(w, http.StatusAccepted, out)
}
