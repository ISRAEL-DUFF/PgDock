package api

import (
	"net/http"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/incidents"
	"github.com/israel-duff/pgdock/internal/store"
)

func (s *Server) announcementOut(r *http.Request, in incidents.Incident, scope []store.IncidentScope) gen.MaintenanceAnnouncement {
	out := gen.MaintenanceAnnouncement{Incident: incidentOut(in), ScopeProjects: []openapi_types.UUID{}, ScopeNodes: []openapi_types.UUID{},
		ScheduledStart: in.ScheduledStart, ScheduledEnd: in.ScheduledEnd, AnnouncedAt: in.AnnouncedAt, CancelledAt: in.CancelledAt}
	if scope == nil {
		scope, _ = store.New(s.db).IncidentScope(r.Context(), in.ID)
	}
	for _, sc := range scope {
		if sc.ProjectID != nil {
			out.ScopeProjects = append(out.ScopeProjects, *sc.ProjectID)
		}
		if sc.NodeID != nil {
			out.ScopeNodes = append(out.ScopeNodes, *sc.NodeID)
		}
	}
	if in.AnnouncedAt != nil && in.ScheduledStart != nil {
		from := in.AnnouncedAt.Add(incidents.NoticeHours * time.Hour).Truncate(time.Minute)
		short := from.After(*in.ScheduledStart)
		if !short {
			from = *in.ScheduledStart
		}
		out.ExcludedFrom, out.ShortNotice = &from, &short
	}
	return out
}

// ListMaintenanceAnnouncements implements GET /api/v1/admin/maintenance/announcements.
func (s *Server) ListMaintenanceAnnouncements(w http.ResponseWriter, r *http.Request) {
	if !s.requireIncidents(w) {
		return
	}
	list, err := s.incidents.ListMaintenance(r.Context(), time.Now().AddDate(0, -1, 0))
	if err != nil {
		s.internalError(w, "list maintenance", err)
		return
	}
	out := gen.MaintenanceAnnouncementList{Items: []gen.MaintenanceAnnouncement{}}
	for _, in := range list {
		out.Items = append(out.Items, s.announcementOut(r, in, nil))
	}
	writeJSON(w, http.StatusOK, out)
}

// AnnounceMaintenance implements POST /api/v1/admin/maintenance/announcements.
func (s *Server) AnnounceMaintenance(w http.ResponseWriter, r *http.Request) {
	if !s.requireIncidents(w) {
		return
	}
	var req gen.MaintenanceAnnouncementRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	m := incidents.Maintenance{Start: req.Start, End: req.End, By: userID(r.Context()), Replaces: req.Replaces}
	if req.Title != nil {
		m.Title = *req.Title
	}
	if req.Body != nil {
		m.Body = *req.Body
	}
	if req.Region != nil {
		m.Region = *req.Region
	}
	if req.ProjectIds != nil {
		m.Projects = *req.ProjectIds
	}
	if req.NodeIds != nil {
		m.Nodes = *req.NodeIds
	}
	a, err := s.incidents.Announce(r.Context(), m)
	if err != nil {
		s.incidentError(w, "announce maintenance", err)
		return
	}
	au := auditFrom(r.Context())
	au.target("incident", a.ID.String())
	au.set("start", req.Start)
	au.set("end", req.End)
	au.set("region", m.Region)
	out := s.announcementOut(r, a.Incident, a.Scope)
	out.Emailed = &a.Emailed
	writeJSON(w, http.StatusCreated, out)
}

// CancelMaintenance implements DELETE /api/v1/admin/maintenance/announcements/{incident_id}.
func (s *Server) CancelMaintenance(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	if !s.requireIncidents(w) {
		return
	}
	auditFrom(r.Context()).target("incident", id.String())
	in, err := s.incidents.CancelMaintenance(r.Context(), id, userID(r.Context()))
	if err != nil {
		s.incidentError(w, "cancel maintenance", err)
		return
	}
	writeJSON(w, http.StatusOK, s.announcementOut(r, in, nil))
}
