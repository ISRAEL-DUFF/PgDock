package api

import (
	"errors"
	"net/http"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/freetier"
)

// ResumeProject implements POST /api/v1/projects/{id}/resume (V3 §4.2).
func (s *Server) ResumeProject(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	if s.freetier == nil {
		writeError(w, http.StatusNotImplemented, "not_configured", "pausing Free projects needs backup storage")
		return
	}
	auditFrom(r.Context()).target("project", id.String())
	op, err := s.freetier.Resume(r.Context(), id, userID(r.Context()))
	switch {
	case errors.Is(err, freetier.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "project not found")
	case errors.Is(err, freetier.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case err != nil:
		s.internalError(w, "resume project", err)
	default:
		s.writeOperation(w, "resume", op)
	}
}
