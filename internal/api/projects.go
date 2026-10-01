package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/store"
)

// ListProjects implements GET /api/v1/projects.
func (s *Server) ListProjects(w http.ResponseWriter, r *http.Request, params gen.ListProjectsParams) {
	if !s.requireProjects(w) {
		return
	}
	limit := 100
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > 500 {
			writeError(w, http.StatusBadRequest, "bad_request", "limit must be between 1 and 500")
			return
		}
		limit = *params.Limit
	}
	var status *string
	if params.Status != nil {
		if !params.Status.Valid() {
			writeError(w, http.StatusBadRequest, "bad_request", "unknown status")
			return
		}
		st := string(*params.Status)
		status = &st
	}
	ps, err := s.projects.List(r.Context(), status, limit)
	if err != nil {
		s.internalError(w, "list projects", err)
		return
	}
	out := gen.ProjectList{Items: make([]gen.Project, 0, len(ps))}
	for _, p := range ps {
		gp, err := s.toAPIProject(p)
		if err != nil {
			s.internalError(w, "list projects", err)
			return
		}
		out.Items = append(out.Items, gp)
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateProject implements POST /api/v1/projects.
func (s *Server) CreateProject(w http.ResponseWriter, r *http.Request) {
	if !s.requireProjects(w) {
		return
	}
	var req gen.CreateProjectRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	a := auditFrom(r.Context())
	a.set("name", req.Name)
	c, err := s.projects.Create(r.Context(), provision.CreateParams{Name: req.Name, Description: req.Description, CreatedBy: operatorID(r.Context())})
	if err != nil {
		s.provisionError(w, "create project", err)
		return
	}
	a.target("project", c.Project.ID.String())
	a.set("db_name", c.Project.DbName)
	s.writeCredentials(w, c.Project, c.Operation, c.Password)
}

// GetProject implements GET /api/v1/projects/{id}.
func (s *Server) GetProject(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	if !s.requireProjects(w) {
		return
	}
	p, err := s.projects.Get(r.Context(), id)
	if err != nil {
		s.provisionError(w, "get project", err)
		return
	}
	gp, err := s.toAPIProject(p)
	if err != nil {
		s.internalError(w, "get project", err)
		return
	}
	writeJSON(w, http.StatusOK, gp)
}

// DeleteProject implements DELETE /api/v1/projects/{id}.
func (s *Server) DeleteProject(w http.ResponseWriter, r *http.Request, id gen.ProjectID, params gen.DeleteProjectParams) {
	if !s.requireProjects(w) {
		return
	}
	auditFrom(r.Context()).target("project", id.String())
	op, err := s.projects.Delete(r.Context(), id, params.Confirm, operatorID(r.Context()))
	if err != nil {
		s.provisionError(w, "delete project", err)
		return
	}
	o, err := toAPIOperation(op)
	if err != nil {
		s.internalError(w, "delete project", err)
		return
	}
	w.Header().Set("Location", "/api/v1/operations/"+op.ID.String())
	writeJSON(w, http.StatusAccepted, o)
}

// RotateProjectPassword implements POST /api/v1/projects/{id}/rotate-password.
func (s *Server) RotateProjectPassword(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	if !s.requireProjects(w) {
		return
	}
	auditFrom(r.Context()).target("project", id.String())
	rot, err := s.projects.Rotate(r.Context(), id, operatorID(r.Context()))
	if err != nil {
		s.provisionError(w, "rotate password", err)
		return
	}
	s.writeCredentials(w, rot.Project, rot.Operation, rot.Password)
}

func (s *Server) writeCredentials(w http.ResponseWriter, p store.Project, op store.Operation, password string) {
	gp, err := s.toAPIProject(p)
	if err != nil {
		s.internalError(w, "project credentials", err)
		return
	}
	o, err := toAPIOperation(op)
	if err != nil {
		s.internalError(w, "project credentials", err)
		return
	}
	conn := s.projects.ConnectionFor(p)
	w.Header().Set("Location", "/api/v1/operations/"+op.ID.String())
	writeJSON(w, http.StatusAccepted, gen.ProjectCredentials{
		Project:    gp,
		Operation:  o,
		Password:   password,
		Connection: toAPIConnection(conn, password),
	})
}

func (s *Server) toAPIProject(p store.Project) (gen.Project, error) {
	set, err := store.DecodeProjectSettings(p.Settings)
	if err != nil {
		return gen.Project{}, err
	}
	return gen.Project{
		Id:          p.ID,
		Name:        p.Name,
		Slug:        p.Slug,
		DbName:      p.DbName,
		OwnerRole:   p.OwnerRole,
		Tier:        gen.ProjectTier(p.Tier),
		Status:      gen.ProjectStatus(p.Status),
		Description: p.Description,
		CreatedAt:   p.CreatedAt,
		Settings: gen.ProjectSettings{
			ConnectionLimit:                 set.ConnectionLimit,
			PoolSize:                        set.PoolSize,
			StatementTimeout:                set.StatementTimeout,
			IdleInTransactionSessionTimeout: set.IdleInTransactionTimeout,
			DiskWarnBytes:                   set.DiskWarnBytes,
			ConsoleReadOnly:                 set.ConsoleReadOnly,
		},
		Connection: toAPIConnection(s.projects.ConnectionFor(p), ""),
	}, nil
}

func toAPIConnection(c provision.Connection, password string) gen.ConnectionInfo {
	return gen.ConnectionInfo{
		Host:        c.Host,
		SessionPort: c.SessionPort,
		PooledPort:  c.PooledPort,
		Database:    c.Database,
		User:        c.User,
		Sslmode:     c.SSLMode,
		PooledUrl:   c.PooledURL(password),
		SessionUrl:  c.SessionURL(password),
	}
}

func (s *Server) requireProjects(w http.ResponseWriter) bool {
	if s.projects == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable",
			"project provisioning is not configured (set PGDOCK_POOLER_CONFIG_DIR and the pooler settings)")
		return false
	}
	return true
}

func (s *Server) provisionError(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, provision.ErrInvalid):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	case errors.Is(err, provision.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "project not found")
	case errors.Is(err, provision.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, provision.ErrNoCapacity):
		writeError(w, http.StatusServiceUnavailable, "no_capacity", "no shared cluster is available for new projects")
	default:
		s.internalError(w, what, err)
	}
}

func provisionUpdate(req gen.UpdateProjectRequest) provision.UpdateParams {
	p := provision.UpdateParams{Name: req.Name, Description: req.Description}
	if st := req.Settings; st != nil {
		p.Settings = &provision.SettingsPatch{
			ConnectionLimit:          st.ConnectionLimit,
			PoolSize:                 st.PoolSize,
			StatementTimeout:         st.StatementTimeout,
			IdleInTransactionTimeout: st.IdleInTransactionSessionTimeout,
			DiskWarnBytes:            st.DiskWarnBytes,
			ConsoleReadOnly:          st.ConsoleReadOnly,
		}
	}
	return p
}
