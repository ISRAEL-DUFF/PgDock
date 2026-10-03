package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/settings"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/internal/tenancy"
)

func (s *Server) generalSettings() gen.GeneralSettings {
	out := gen.GeneralSettings{Tls: gen.TlsStatus{Mode: gen.TlsStatusModeOff, State: gen.TlsStatusStateOff}}
	if s.projects != nil {
		c := s.projects.ConnectionFor(store.Project{})
		out.DbHost, out.SessionPort, out.PooledPort, out.Sslmode = c.Host, c.SessionPort, c.PooledPort, c.SSLMode
	}
	if s.tls != nil {
		out.Tls = s.tls()
	}
	return out
}

// GetGeneralSettings implements GET /api/v1/settings/general.
func (s *Server) GetGeneralSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.generalSettings())
}

// PutDbHost implements PUT /api/v1/settings/db-host.
func (s *Server) PutDbHost(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "settings are not configured")
		return
	}
	var req gen.DbHostRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	host, err := s.settings.SetDBHost(r.Context(), req.DbHost)
	if err != nil {
		if strings.Contains(err.Error(), "hostname") {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		s.internalError(w, "set db host", err)
		return
	}
	auditFrom(r.Context()).set("db_host", host)
	writeJSON(w, http.StatusOK, s.generalSettings())
}

// CheckDbHost implements POST /api/v1/settings/db-host/check.
func (s *Server) CheckDbHost(w http.ResponseWriter, r *http.Request) {
	var req gen.DbHostRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	host, err := settings.ValidateHost(req.DbHost)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	c := settings.CheckDNS(r.Context(), host, r.Host, s.publicIPs)
	out := gen.DnsCheck{Host: c.Host, Addresses: c.Addresses, ServerAddresses: c.ServerAddresses, PointsHere: c.PointsHere}
	if out.Addresses == nil {
		out.Addresses = []string{}
	}
	if out.ServerAddresses == nil {
		out.ServerAddresses = []string{}
	}
	if c.Err != nil {
		msg := c.Err.Error()
		out.Error = &msg
	}
	writeJSON(w, http.StatusOK, out)
}

// UpdateProject implements PATCH /api/v1/projects/{id}/settings.
func (s *Server) UpdateProject(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	if !s.requireProjects(w) {
		return
	}
	var req gen.UpdateProjectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	a := auditFrom(r.Context())
	a.target("project", id.String())
	if !s.updateBranchFields(w, r, id, req) {
		return
	}
	p := provisionUpdate(req)
	if s.tenancy != nil && p.Settings != nil && p.Settings.ConnectionLimit != nil {
		// A shared project's connections are capped by the plan (V2 §10.3).
		if cur, err := s.tenantProjectLive(r.Context()); err == nil && cur.Tier == provision.TierShared {
			connMax, err := s.tenancy.MaxConnections(r.Context(), cur.OrgID)
			if err != nil {
				s.internalError(w, "update project", err)
				return
			}
			if connMax > 0 && *p.Settings.ConnectionLimit > connMax {
				writeQuotaError(w, &tenancy.QuotaError{Limit: store.LimitProjectConnections, Used: int64(*p.Settings.ConnectionLimit), Max: int64(connMax)})
				return
			}
		}
	}
	upd, err := s.projects.Update(r.Context(), id, p, userID(r.Context()))
	if err != nil {
		s.provisionError(w, "update project", err)
		return
	}
	if req.Settings != nil {
		b, _ := json.Marshal(req.Settings)
		var changed map[string]any
		_ = json.Unmarshal(b, &changed)
		a.set("settings", changed)
	}
	gp, err := s.toAPIProject(upd.Project)
	if err != nil {
		s.internalError(w, "update project", err)
		return
	}
	out := gen.ProjectUpdated{Project: gp}
	if upd.Operation != nil {
		o, err := toAPIOperation(*upd.Operation)
		if err != nil {
			s.internalError(w, "update project", err)
			return
		}
		out.Operation = &o
	}
	writeJSON(w, http.StatusOK, out)
}
