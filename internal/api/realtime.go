package api

import (
	"encoding/json"
	"net/http"

	"github.com/israel-duff/pgdock/internal/api/gen"
)

// Project → Realtime (V4 §6): which tables' changes are delivered, the
// topics kept as history, and the limits and use.

// GetProjectRealtime implements GET /api/v1/projects/{id}/realtime.
func (s *Server) GetProjectRealtime(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	if !s.requireServices(w) {
		return
	}
	o, err := s.services.Realtime(r.Context(), id)
	if err != nil {
		s.servicesError(w, "realtime", err)
		return
	}
	out := gen.RealtimeOverview{Tables: []gen.RealtimeTable{}, PersistedTopics: o.PersistedTopics, MessagesBlocked: o.MessagesBlocked,
		MessagesThisMonth: float32(o.MessagesMonth), ConnectionMinutesThisMonth: float32(o.ConnMinutesMonth),
		ChangesPerSecond: o.ChangesPerSecond, GroupsPerChange: o.GroupsPerChange}
	if o.MaxConnections != nil {
		v := int(*o.MaxConnections)
		out.MaxConnections = &v
	}
	for _, t := range o.Tables {
		out.Tables = append(out.Tables, gen.RealtimeTable{Schema: t.Schema, Table: t.Table, Enabled: t.Enabled, Rls: t.RLS, HasPrimaryKey: t.HasPK})
	}
	writeJSON(w, http.StatusOK, out)
}

// SetRealtimeTable implements PUT /api/v1/projects/{id}/realtime/tables/{schema}/{table}.
func (s *Server) SetRealtimeTable(w http.ResponseWriter, r *http.Request, id gen.ProjectID, schema, table string) {
	if !s.requireServices(w) {
		return
	}
	var in gen.SetRealtimeTableJSONBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	auditFrom(r.Context()).target("table", schema+"."+table)
	if err := s.services.SetRealtimeTable(r.Context(), id, schema, table, in.Enabled); err != nil {
		s.servicesError(w, "realtime table", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SetRealtimePersistedTopics implements PUT /api/v1/projects/{id}/realtime/persisted-topics.
func (s *Server) SetRealtimePersistedTopics(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	if !s.requireServices(w) {
		return
	}
	var in gen.SetRealtimePersistedTopicsJSONBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil || in.Topics == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "send {\"topics\": [...]}")
		return
	}
	if err := s.services.SetPersistedTopics(r.Context(), id, in.Topics); err != nil {
		s.servicesError(w, "realtime topics", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
