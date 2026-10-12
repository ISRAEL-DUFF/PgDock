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
	draft := in.Status == "draft"
	out.Draft, out.ProposedFor = &draft, in.ProposedFor
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

// ConfirmMaintenanceDraft implements POST
// /api/v1/admin/maintenance/announcements/{incident_id}/confirm.
func (s *Server) ConfirmMaintenanceDraft(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	if !s.requireIncidents(w) {
		return
	}
	auditFrom(r.Context()).target("incident", id.String())
	a, err := s.incidents.Confirm(r.Context(), id)
	if err != nil {
		s.incidentError(w, "confirm maintenance", err)
		return
	}
	out := s.announcementOut(r, a.Incident, a.Scope)
	out.Emailed = &a.Emailed
	writeJSON(w, http.StatusOK, out)
}

// DiscardMaintenanceDraft implements POST
// /api/v1/admin/maintenance/announcements/{incident_id}/discard.
func (s *Server) DiscardMaintenanceDraft(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	if !s.requireIncidents(w) {
		return
	}
	auditFrom(r.Context()).target("incident", id.String())
	in, err := s.incidents.Discard(r.Context(), id)
	if err != nil {
		s.incidentError(w, "discard maintenance", err)
		return
	}
	writeJSON(w, http.StatusOK, s.announcementOut(r, in, nil))
}

// PreviewMaintenanceAnnouncement implements POST
// /api/v1/admin/maintenance/announcements/preview.
func (s *Server) PreviewMaintenanceAnnouncement(w http.ResponseWriter, r *http.Request) {
	if !s.requireIncidents(w) {
		return
	}
	var req gen.MaintenancePreviewRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	var p incidents.Preview
	var err error
	if req.IncidentId != nil {
		p, err = s.incidents.PreviewDraft(r.Context(), *req.IncidentId)
	} else {
		if req.Start == nil || req.End == nil {
			writeError(w, http.StatusBadRequest, "bad_request", "start and end are required")
			return
		}
		m := incidents.Maintenance{Start: *req.Start, End: *req.End}
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
		p, err = s.incidents.PreviewAnnouncement(r.Context(), m)
	}
	if err != nil {
		s.incidentError(w, "preview maintenance", err)
		return
	}
	writeJSON(w, http.StatusOK, gen.MaintenancePreview{Subject: p.Subject, Body: p.Body, Organisations: p.Organisations, Addresses: p.Addresses})
}
