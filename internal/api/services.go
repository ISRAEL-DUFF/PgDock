package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/edgeapi"
	"github.com/israel-duff/pgdock/internal/services"
	"github.com/israel-duff/pgdock/internal/store"
)

// Backend services (V4 §2): the dashboard's management endpoints, and the
// signed feed pgdock-edge follows.

func (s *Server) requireServices(w http.ResponseWriter) bool {
	if s.services == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "backend services aren't available on this install")
		return false
	}
	return true
}

func (s *Server) servicesError(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, services.ErrNotFound), errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, services.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, services.ErrInvalid):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	default:
		s.internalError(w, what, err)
	}
}

func apiKeyOut(k store.ProjectApiKey) gen.ApiKey {
	return gen.ApiKey{Id: k.ID, Kind: gen.ApiKeyKind(k.Kind), Name: k.Name, Prefix: k.Prefix, Key: k.Display,
		LastUsedAt: k.LastUsedAt, RevokedAt: k.RevokedAt, CreatedAt: k.CreatedAt}
}

func (s *Server) backendServicesOut(r *http.Request, p store.Project, svc *store.ProjectService) (gen.BackendServices, error) {
	out := gen.BackendServices{Keys: []gen.ApiKey{}, CorsOrigins: []string{}, FeedConfigured: s.services.FeedEnabled()}
	if svc == nil {
		return out, nil
	}
	st, err := services.DecodeSettings(svc.Settings)
	if err != nil {
		return out, err
	}
	ref, url := svc.Ref, s.services.URL(svc.Ref, p.Region)
	out.Enabled, out.Ref, out.Url, out.EnabledAt, out.CorsOrigins = svc.Enabled, &ref, &url, svc.EnabledAt, svc.CorsOrigins
	out.Settings = gen.BackendServicesSettings{StatementTimeoutMs: &st.StatementTimeoutMs, RatePerIp: &st.RatePerIP,
		RatePerKey: &st.RatePerKey, AllowSecretInBrowser: &st.AllowSecretInBrowser}
	keys, err := store.New(s.db).ListAPIKeys(r.Context(), p.ID)
	if err != nil {
		return out, err
	}
	for _, k := range keys {
		out.Keys = append(out.Keys, apiKeyOut(k))
	}
	return out, nil
}

// GetBackendServices implements GET /api/v1/projects/{id}/services.
func (s *Server) GetBackendServices(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	if !s.requireServices(w) {
		return
	}
	q := store.New(s.db)
	p, err := q.GetProject(r.Context(), id)
	if err != nil {
		s.provisionError(w, "backend services", err)
		return
	}
	var svc *store.ProjectService
	if row, err := q.GetProjectServices(r.Context(), id); err == nil {
		svc = &row
	} else if !errors.Is(err, pgx.ErrNoRows) {
		s.internalError(w, "backend services", err)
		return
	}
	out, err := s.backendServicesOut(r, p, svc)
	if err != nil {
		s.internalError(w, "backend services", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// EnableBackendServices implements POST /api/v1/projects/{id}/services.
func (s *Server) EnableBackendServices(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	if !s.requireServices(w) {
		return
	}
	auditFrom(r.Context()).target("project", id.String())
	en, err := s.services.Enable(r.Context(), id, userID(r.Context()))
	if err != nil {
		s.servicesError(w, "enable backend services", err)
		return
	}
	p, err := store.New(s.db).GetProject(r.Context(), id)
	if err != nil {
		s.internalError(w, "enable backend services", err)
		return
	}
	svc, err := s.backendServicesOut(r, p, &en.Services)
	if err != nil {
		s.internalError(w, "enable backend services", err)
		return
	}
	op, err := toAPIOperation(en.Operation)
	if err != nil {
		s.internalError(w, "enable backend services", err)
		return
	}
	out := gen.BackendServicesEnabled{Services: svc, Operation: op, Keys: []gen.CreatedApiKey{}}
	for _, k := range en.Keys {
		out.Keys = append(out.Keys, gen.CreatedApiKey{Key: apiKeyOut(k.ProjectApiKey), Value: k.Key})
	}
	w.Header().Set("Location", "/api/v1/operations/"+en.Operation.ID.String())
	writeJSON(w, http.StatusAccepted, out)
}

// UpdateBackendServices implements PATCH /api/v1/projects/{id}/services.
func (s *Server) UpdateBackendServices(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	if !s.requireServices(w) {
		return
	}
	var req gen.BackendServicesUpdate
	if !decodeJSON(w, r, &req) {
		return
	}
	q := store.New(s.db)
	cur, err := q.GetProjectServices(r.Context(), id)
	if err != nil {
		s.servicesError(w, "backend services", err)
		return
	}
	st, err := services.DecodeSettings(cur.Settings)
	if err != nil {
		s.internalError(w, "backend services", err)
		return
	}
	origins := cur.CorsOrigins
	if req.CorsOrigins != nil {
		origins = *req.CorsOrigins
	}
	if v := req.Settings; v != nil {
		if v.StatementTimeoutMs != nil {
			st.StatementTimeoutMs = *v.StatementTimeoutMs
		}
		if v.RatePerIp != nil {
			st.RatePerIP = *v.RatePerIp
		}
		if v.RatePerKey != nil {
			st.RatePerKey = *v.RatePerKey
		}
		if v.AllowSecretInBrowser != nil {
			st.AllowSecretInBrowser = *v.AllowSecretInBrowser
		}
	}
	a := auditFrom(r.Context())
	a.target("project", id.String())
	a.set("cors_origins", origins)
	a.set("settings", st)
	if _, err := s.services.UpdateSettings(r.Context(), id, origins, st); err != nil {
		s.servicesError(w, "backend services", err)
		return
	}
	s.GetBackendServices(w, r, id)
}

// DisableBackendServices implements DELETE /api/v1/projects/{id}/services.
func (s *Server) DisableBackendServices(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	if !s.requireServices(w) {
		return
	}
	auditFrom(r.Context()).target("project", id.String())
	op, err := s.services.Disable(r.Context(), id, userID(r.Context()))
	if err != nil {
		s.servicesError(w, "disable backend services", err)
		return
	}
	s.writeOperation(w, "disable backend services", op)
}

// CreateAPIKey implements POST /api/v1/projects/{id}/services/keys.
func (s *Server) CreateAPIKey(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	if !s.requireServices(w) {
		return
	}
	var req gen.CreateApiKeyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	a := auditFrom(r.Context())
	a.target("project", id.String())
	a.set("kind", string(req.Kind))
	a.set("name", req.Name)
	k, err := s.services.CreateKey(r.Context(), id, string(req.Kind), req.Name, userID(r.Context()))
	if err != nil {
		s.servicesError(w, "create API key", err)
		return
	}
	a.set("key_id", k.ID.String())
	writeJSON(w, http.StatusCreated, gen.CreatedApiKey{Key: apiKeyOut(k.ProjectApiKey), Value: k.Key})
}

// RevokeAPIKey implements DELETE /api/v1/projects/{id}/services/keys/{key_id}.
func (s *Server) RevokeAPIKey(w http.ResponseWriter, r *http.Request, id gen.ProjectID, keyID openapi_types.UUID) {
	if !s.requireServices(w) {
		return
	}
	a := auditFrom(r.Context())
	a.target("project", id.String())
	a.set("key_id", keyID.String())
	k, err := s.services.RevokeKey(r.Context(), id, keyID)
	if err != nil {
		s.servicesError(w, "revoke API key", err)
		return
	}
	writeJSON(w, http.StatusOK, apiKeyOut(k))
}

// ListAPIRequestLogs implements GET /api/v1/projects/{id}/services/logs.
func (s *Server) ListAPIRequestLogs(w http.ResponseWriter, r *http.Request, id gen.ProjectID, params gen.ListAPIRequestLogsParams) {
	lim := 100
	if params.Limit != nil {
		lim = *params.Limit
	}
	if lim < 1 || lim > 500 {
		writeError(w, http.StatusBadRequest, "bad_request", "limit must be 1 to 500")
		return
	}
	rows, err := store.New(s.db).ProjectRequestLogs(r.Context(), store.ProjectRequestLogsParams{ProjectID: id, Before: params.Before, Lim: int32(lim)})
	if err != nil {
		s.internalError(w, "API request logs", err)
		return
	}
	out := gen.ApiRequestLogList{Items: []gen.ApiRequestLog{}}
	for _, l := range rows {
		out.Items = append(out.Items, gen.ApiRequestLog{Id: l.ID, At: l.At, RequestId: l.RequestID, Method: l.Method, Path: l.Path,
			Status: int(l.Status), LatencyMs: int(l.LatencyMs), Role: l.Role, UserId: l.UserID, KeyId: l.KeyID, Ip: l.Ip, BytesOut: l.BytesOut})
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- pgdock-edge's feed ------------------------------------------------------

// edgeAuth reads the body and checks the edge signature.
func (s *Server) edgeAuth(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if s.services == nil || !s.services.FeedEnabled() {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "unreadable body")
		return nil, false
	}
	if err := s.services.VerifyEdge(r, body); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "bad edge signature")
		return nil, false
	}
	return body, true
}

// EdgeConfig implements GET /api/v1/edge/config.
func (s *Server) EdgeConfig(w http.ResponseWriter, r *http.Request, params gen.EdgeConfigParams) {
	if _, ok := s.edgeAuth(w, r); !ok {
		return
	}
	var since int64
	if params.Since != nil {
		since = *params.Since
	}
	wait := 0
	if params.Wait != nil {
		wait = min(max(*params.Wait, 0), 30)
	}
	region := ""
	if params.Region != nil {
		region = *params.Region
	}
	c, err := s.services.Config(r.Context(), region, since, time.Duration(wait)*time.Second)
	if err != nil {
		s.internalError(w, "edge config", err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// EdgeReport implements POST /api/v1/edge/report.
func (s *Server) EdgeReport(w http.ResponseWriter, r *http.Request) {
	body, ok := s.edgeAuth(w, r)
	if !ok {
		return
	}
	var rep edgeapi.Report
	if err := json.Unmarshal(body, &rep); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "not a report")
		return
	}
	if err := s.services.Report(r.Context(), rep); err != nil {
		s.servicesError(w, "edge report", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// EdgeWake implements POST /api/v1/edge/wake.
func (s *Server) EdgeWake(w http.ResponseWriter, r *http.Request) {
	body, ok := s.edgeAuth(w, r)
	if !ok {
		return
	}
	var req edgeapi.Wake
	if err := json.Unmarshal(body, &req); err != nil || req.Ref == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "not a wake request")
		return
	}
	if err := s.services.Wake(r.Context(), req.Ref); err != nil {
		s.servicesError(w, "edge wake", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
