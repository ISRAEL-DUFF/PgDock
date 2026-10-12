package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/pgversions"
	"github.com/israel-duff/pgdock/internal/provision"
)

// The Postgres version lifecycle (V4.1 §6.1).

func toAPIPgVersion(v provision.PGVersion, withProjects bool) gen.PgVersionInfo {
	out := gen.PgVersionInfo{Major: v.Major, Status: gen.PgVersionInfoStatus(v.Status), DeprecatedAt: v.DeprecatedAt, RetiresAt: v.RetiresAt,
		Notes: v.Notes, Installed: v.Installed}
	if withProjects {
		n := v.Projects
		out.Projects = &n
	}
	return out
}

// ListPgVersions implements GET /api/v1/admin/pg-versions.
func (s *Server) ListPgVersions(w http.ResponseWriter, r *http.Request) {
	if !s.requireProjects(w) {
		return
	}
	vs, err := s.projects.Versions(r.Context())
	if err != nil {
		s.internalError(w, "postgres versions", err)
		return
	}
	items := make([]gen.PgVersionInfo, 0, len(vs))
	for _, v := range vs {
		items = append(items, toAPIPgVersion(v, true))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// UpdatePgVersion implements PATCH /api/v1/admin/pg-versions/{major}.
func (s *Server) UpdatePgVersion(w http.ResponseWriter, r *http.Request, major int) {
	if s.pgversions == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the version lifecycle isn't configured")
		return
	}
	var req gen.PgVersionUpdate
	if !decodeJSON(w, r, &req) {
		return
	}
	a := auditFrom(r.Context())
	a.target("pg_version", fmt.Sprint(major))
	a.set("status", req.Status)
	if req.RetiresAt != nil {
		a.set("retires_at", req.RetiresAt)
	}
	v, err := s.pgversions.Change(r.Context(), major, pgversions.Update{Status: string(req.Status), RetiresAt: req.RetiresAt, Notes: req.Notes})
	if errors.Is(err, pgversions.ErrInvalid) {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err != nil {
		s.internalError(w, "postgres version", err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIPgVersion(v, true))
}
