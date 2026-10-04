package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/authz"
	"github.com/israel-duff/pgdock/internal/store"
)

// The SQL Editor's saved queries (docs/ui-redesign.md, phase 3). Everyone
// who can use a project's console keeps their own queries and sees the
// shared ones; owners edit theirs, and project admins may also delete
// shared ones.

// maxSavedQueries caps one person's queries in one project.
const maxSavedQueries = 500

func toAPISavedQuery(q store.SavedQuery, favorite bool, me uuid.UUID) gen.SavedQuery {
	return gen.SavedQuery{
		Id: q.ID, ProjectId: q.ProjectID, Name: q.Name, Sql: q.Sql, Visibility: gen.SavedQueryVisibility(q.Visibility),
		Favorite: favorite, Mine: q.OwnerID == me, CreatedAt: q.CreatedAt, UpdatedAt: q.UpdatedAt,
	}
}

func validVisibility(v string) bool { return v == "private" || v == "shared" }

// savedQuery loads a query the caller may see.
func (s *Server) savedQuery(r *http.Request, id uuid.UUID) (store.SavedQuery, bool, error) {
	acc := accessFrom(r.Context())
	row, err := store.New(s.db).GetSavedQuery(r.Context(), store.GetSavedQueryParams{ID: id, OrgID: acc.OrgID, ProjectID: acc.ProjectID, UserID: acc.Actor.UserID})
	q := store.SavedQuery{ID: row.ID, OrgID: row.OrgID, ProjectID: row.ProjectID, OwnerID: row.OwnerID, Name: row.Name, Sql: row.Sql,
		Visibility: row.Visibility, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	return q, row.Favorite, err
}

// ListSavedQueries implements GET /api/v1/projects/{id}/queries.
func (s *Server) ListSavedQueries(w http.ResponseWriter, r *http.Request, _ gen.ProjectID) {
	acc := accessFrom(r.Context())
	rows, err := store.New(s.db).ListSavedQueries(r.Context(), store.ListSavedQueriesParams{OrgID: acc.OrgID, ProjectID: acc.ProjectID, UserID: acc.Actor.UserID})
	if err != nil {
		s.internalError(w, "list saved queries", err)
		return
	}
	out := gen.SavedQueryList{Items: make([]gen.SavedQuery, 0, len(rows))}
	for _, q := range rows {
		out.Items = append(out.Items, toAPISavedQuery(store.SavedQuery{ID: q.ID, OrgID: q.OrgID, ProjectID: q.ProjectID, OwnerID: q.OwnerID,
			Name: q.Name, Sql: q.Sql, Visibility: q.Visibility, CreatedAt: q.CreatedAt, UpdatedAt: q.UpdatedAt}, q.Favorite, acc.Actor.UserID))
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateSavedQuery implements POST /api/v1/projects/{id}/queries.
func (s *Server) CreateSavedQuery(w http.ResponseWriter, r *http.Request, _ gen.ProjectID) {
	acc := accessFrom(r.Context())
	var req gen.SavedQueryRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	vis := "private"
	if req.Visibility != nil {
		vis = string(*req.Visibility)
	}
	sql := ""
	if req.Sql != nil {
		sql = *req.Sql
	}
	if name == "" || len(name) > 200 || len(sql) > 200_000 || !validVisibility(vis) {
		writeError(w, http.StatusBadRequest, "invalid", "give the query a name of at most 200 characters, SQL of at most 200,000, and private or shared")
		return
	}
	q := store.New(s.db)
	n, err := q.CountUserSavedQueries(r.Context(), store.CountUserSavedQueriesParams{OrgID: acc.OrgID, ProjectID: acc.ProjectID, OwnerID: acc.Actor.UserID})
	if err != nil {
		s.internalError(w, "save query", err)
		return
	}
	if n >= maxSavedQueries {
		writeError(w, http.StatusConflict, "quota_exceeded", "you have 500 saved queries in this project; delete some first")
		return
	}
	row, err := q.InsertSavedQuery(r.Context(), store.InsertSavedQueryParams{OrgID: acc.OrgID, ProjectID: acc.ProjectID, OwnerID: acc.Actor.UserID,
		Name: name, Sql: sql, Visibility: vis})
	if err != nil {
		s.internalError(w, "save query", err)
		return
	}
	writeJSON(w, http.StatusCreated, toAPISavedQuery(row, false, acc.Actor.UserID))
}

// GetSavedQuery implements GET /api/v1/projects/{id}/queries/{query_id}.
func (s *Server) GetSavedQuery(w http.ResponseWriter, r *http.Request, _ gen.ProjectID, queryID gen.SavedQueryID) {
	q, fav, err := s.savedQuery(r, queryID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "no such query")
		return
	}
	if err != nil {
		s.internalError(w, "saved query", err)
		return
	}
	writeJSON(w, http.StatusOK, toAPISavedQuery(q, fav, accessFrom(r.Context()).Actor.UserID))
}

// UpdateSavedQuery implements PATCH /api/v1/projects/{id}/queries/{query_id}.
func (s *Server) UpdateSavedQuery(w http.ResponseWriter, r *http.Request, _ gen.ProjectID, queryID gen.SavedQueryID) {
	acc := accessFrom(r.Context())
	var req gen.SavedQueryPatch
	if !decodeJSON(w, r, &req) {
		return
	}
	cur, fav, err := s.savedQuery(r, queryID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "no such query")
		return
	}
	if err != nil {
		s.internalError(w, "update saved query", err)
		return
	}
	if cur.OwnerID != acc.Actor.UserID {
		writeError(w, http.StatusForbidden, "forbidden", "only its owner can change a query; save a copy of your own instead")
		return
	}
	if req.Name != nil {
		cur.Name = strings.TrimSpace(*req.Name)
	}
	if req.Sql != nil {
		cur.Sql = *req.Sql
	}
	if req.Visibility != nil {
		cur.Visibility = string(*req.Visibility)
	}
	if cur.Name == "" || len(cur.Name) > 200 || len(cur.Sql) > 200_000 || !validVisibility(cur.Visibility) {
		writeError(w, http.StatusBadRequest, "invalid", "give the query a name of at most 200 characters, SQL of at most 200,000, and private or shared")
		return
	}
	row, err := store.New(s.db).UpdateSavedQuery(r.Context(), store.UpdateSavedQueryParams{ID: cur.ID, OrgID: acc.OrgID, ProjectID: acc.ProjectID,
		OwnerID: acc.Actor.UserID, Name: cur.Name, Sql: cur.Sql, Visibility: cur.Visibility})
	if err != nil {
		s.internalError(w, "update saved query", err)
		return
	}
	writeJSON(w, http.StatusOK, toAPISavedQuery(row, fav, acc.Actor.UserID))
}

// DeleteSavedQuery implements DELETE /api/v1/projects/{id}/queries/{query_id}.
func (s *Server) DeleteSavedQuery(w http.ResponseWriter, r *http.Request, _ gen.ProjectID, queryID gen.SavedQueryID) {
	acc := accessFrom(r.Context())
	a := auditFrom(r.Context())
	a.target("saved_query", queryID.String())
	cur, _, err := s.savedQuery(r, queryID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "no such query")
		return
	}
	if err != nil {
		s.internalError(w, "delete saved query", err)
		return
	}
	if cur.OwnerID != acc.Actor.UserID {
		admin, err := s.can(r.Context(), authz.ProjectSettings)
		if err != nil {
			s.internalError(w, "delete saved query", err)
			return
		}
		if !admin {
			writeError(w, http.StatusForbidden, "forbidden", "only its owner or a project admin can delete a shared query")
			return
		}
	}
	if _, err := store.New(s.db).DeleteSavedQuery(r.Context(), store.DeleteSavedQueryParams{ID: cur.ID, OrgID: acc.OrgID, ProjectID: acc.ProjectID}); err != nil {
		s.internalError(w, "delete saved query", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SetSavedQueryFavorite implements PUT /api/v1/projects/{id}/queries/{query_id}/favorite.
func (s *Server) SetSavedQueryFavorite(w http.ResponseWriter, r *http.Request, _ gen.ProjectID, queryID gen.SavedQueryID) {
	acc := accessFrom(r.Context())
	var req gen.SetSavedQueryFavoriteJSONBody
	if !decodeJSON(w, r, &req) {
		return
	}
	cur, _, err := s.savedQuery(r, queryID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "no such query")
		return
	}
	if err != nil {
		s.internalError(w, "favourite query", err)
		return
	}
	q := store.New(s.db)
	if req.Favorite {
		err = q.SetSavedQueryFavorite(r.Context(), store.SetSavedQueryFavoriteParams{QueryID: cur.ID, UserID: acc.Actor.UserID, OrgID: acc.OrgID})
	} else {
		err = q.UnsetSavedQueryFavorite(r.Context(), store.UnsetSavedQueryFavoriteParams{QueryID: cur.ID, UserID: acc.Actor.UserID, OrgID: acc.OrgID})
	}
	if err != nil {
		s.internalError(w, "favourite query", err)
		return
	}
	writeJSON(w, http.StatusOK, toAPISavedQuery(cur, req.Favorite, acc.Actor.UserID))
}
