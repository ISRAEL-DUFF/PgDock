package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/incidents"
	"github.com/israel-duff/pgdock/internal/store"
)

func (s *Server) requireIncidents(w http.ResponseWriter) bool {
	if s.incidents == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "incidents are not configured")
		return false
	}
	return true
}

func incidentOut(in incidents.Incident) gen.Incident {
	out := gen.Incident{
		Id: in.ID, Title: in.Title, Components: in.Components, Region: in.RegionID,
		Severity: gen.IncidentSeverity(in.Severity), Status: gen.IncidentStatus(in.Status),
		StartedAt: in.StartedAt, ResolvedAt: in.ResolvedAt, UpdatedAt: in.UpdatedAt,
		PushedAt: in.PushedAt, PushError: in.PushError, Updates: make([]gen.IncidentUpdate, 0, len(in.Updates)),
	}
	for _, u := range in.Updates {
		out.Updates = append(out.Updates, gen.IncidentUpdate{
			Id: u.ID, Status: gen.IncidentStatus(u.Status), Body: u.Body, PostedAt: u.PostedAt, PostedBy: u.PostedByEmail,
		})
	}
	return out
}

func (s *Server) incidentError(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, incidents.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, incidents.ErrInvalid):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	default:
		s.internalError(w, what, err)
	}
}

// ListIncidents implements GET /api/v1/incidents.
func (s *Server) ListIncidents(w http.ResponseWriter, r *http.Request) {
	if !s.requireIncidents(w) {
		return
	}
	list, err := s.incidents.List(r.Context(), 200)
	if err != nil {
		s.internalError(w, "list incidents", err)
		return
	}
	out := gen.IncidentList{Items: make([]gen.Incident, 0, len(list)), Components: s.incidents.Components(),
		StatusPageConfigured: s.incidents.StatusURL() != ""}
	if u := s.incidents.StatusURL(); u != "" {
		out.StatusPageUrl = &u
	}
	for _, in := range list {
		out.Items = append(out.Items, incidentOut(in))
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateIncident implements POST /api/v1/incidents.
func (s *Server) CreateIncident(w http.ResponseWriter, r *http.Request) {
	if !s.requireIncidents(w) {
		return
	}
	var req gen.CreateIncidentRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	in := incidents.Input{Title: req.Title, Components: req.Components, Severity: string(req.Severity),
		Status: string(req.Status), Body: req.Body, By: userID(r.Context())}
	if req.Region != nil {
		in.Region = *req.Region
	}
	inc, err := s.incidents.Create(r.Context(), in)
	if err != nil {
		s.incidentError(w, "create incident", err)
		return
	}
	a := auditFrom(r.Context())
	a.target("incident", inc.ID.String())
	a.set("title", inc.Title)
	a.set("severity", inc.Severity)
	a.set("components", inc.Components)
	writeJSON(w, http.StatusCreated, incidentOut(inc))
}

// GetIncident implements GET /api/v1/incidents/{id}.
func (s *Server) GetIncident(w http.ResponseWriter, r *http.Request, id gen.IncidentID) {
	if !s.requireIncidents(w) {
		return
	}
	inc, err := s.incidents.Get(r.Context(), id)
	if err != nil {
		s.incidentError(w, "get incident", err)
		return
	}
	writeJSON(w, http.StatusOK, incidentOut(inc))
}

// UpdateIncident implements PATCH /api/v1/incidents/{id}.
func (s *Server) UpdateIncident(w http.ResponseWriter, r *http.Request, id gen.IncidentID) {
	if !s.requireIncidents(w) {
		return
	}
	var req gen.UpdateIncidentRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	c := incidents.Change{Title: req.Title}
	if req.Components != nil {
		c.Components = *req.Components
	}
	if req.Severity != nil {
		sev := string(*req.Severity)
		c.Severity = &sev
	}
	inc, err := s.incidents.Edit(r.Context(), id, c)
	if err != nil {
		s.incidentError(w, "update incident", err)
		return
	}
	a := auditFrom(r.Context())
	a.target("incident", inc.ID.String())
	a.set("title", inc.Title)
	a.set("severity", inc.Severity)
	a.set("components", inc.Components)
	writeJSON(w, http.StatusOK, incidentOut(inc))
}

// PostIncidentUpdate implements POST /api/v1/incidents/{id}/updates.
func (s *Server) PostIncidentUpdate(w http.ResponseWriter, r *http.Request, id gen.IncidentID) {
	if !s.requireIncidents(w) {
		return
	}
	var req gen.IncidentUpdateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	inc, err := s.incidents.Post(r.Context(), id, string(req.Status), req.Body, userID(r.Context()))
	if err != nil {
		s.incidentError(w, "post incident update", err)
		return
	}
	a := auditFrom(r.Context())
	a.target("incident", inc.ID.String())
	a.set("status", inc.Status)
	writeJSON(w, http.StatusCreated, incidentOut(inc))
}

// GetPoolerHosts implements GET /api/v1/pooler-hosts.
func (s *Server) GetPoolerHosts(w http.ResponseWriter, r *http.Request) {
	out := gen.PoolerHosts{Hosts: []gen.PoolerHost{}, Events: []gen.PoolerEvent{}}
	if s.arbiter != nil {
		snap := s.arbiter.Snapshot()
		out.Enabled = true
		out.Generation, out.ManagesIp, out.SplitBrain, out.NoHealthy = snap.Generation, snap.ManagesIP, snap.SplitBrain, snap.NoHealthy
		if !snap.CheckedAt.IsZero() {
			out.CheckedAt = &snap.CheckedAt
		}
		out.HolderServerId, out.HolderName, out.HolderError = optional(snap.HolderServerID), optional(snap.HolderName), optional(snap.HolderError)
		for _, h := range snap.Hosts {
			out.Hosts = append(out.Hosts, gen.PoolerHost{
				Id: h.ID, Name: h.Name, ServerId: h.ServerID, Reachable: h.Reachable, Ready: h.Ready, Stale: h.Stale,
				VrrpState: h.VRRP, Generation: h.Generation, Reason: optional(h.Reason),
				Holder: snap.HolderServerID != "" && h.ServerID == snap.HolderServerID,
			})
		}
	}
	events, err := store.New(s.db).ListPoolerEvents(r.Context(), 50)
	if err != nil {
		s.internalError(w, "pooler events", err)
		return
	}
	for _, e := range events {
		detail := map[string]any{}
		_ = json.Unmarshal(e.Detail, &detail)
		out.Events = append(out.Events, gen.PoolerEvent{Id: e.ID, Kind: gen.PoolerEventKind(e.Kind), Host: e.NodeName, CreatedAt: e.OccurredAt, Detail: detail})
	}
	writeJSON(w, http.StatusOK, out)
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
