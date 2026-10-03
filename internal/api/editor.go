package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/authz"
	"github.com/israel-duff/pgdock/internal/console"
	"github.com/israel-duff/pgdock/internal/schemaedit"
	"github.com/israel-duff/pgdock/internal/store"
)

// The table editor (V2 §4): the grid, row edits, and schema changes.

func gridQuery(filters *[]string, sort *string, desc *bool, order *[]string) (console.GridQuery, error) {
	var q console.GridQuery
	var err error
	if q.Filters, err = gridFilters(filters); err != nil {
		return q, err
	}
	if order != nil && len(*order) > 0 {
		for _, raw := range *order {
			var so console.Sort
			if err := json.Unmarshal([]byte(raw), &so); err != nil {
				return q, fmt.Errorf("%w: an order is a JSON object: %w", console.ErrBadQuery, err)
			}
			q.Sorts = append(q.Sorts, so)
		}
	} else if sort != nil && *sort != "" {
		q.Sorts = []console.Sort{{Column: *sort, Desc: desc != nil && *desc}}
	}
	return q, nil
}

func gridFilters(filters *[]string) ([]console.Filter, error) {
	var out []console.Filter
	if filters != nil {
		for _, raw := range *filters {
			var f console.Filter
			if err := json.Unmarshal([]byte(raw), &f); err != nil {
				return nil, fmt.Errorf("%w: a filter is a JSON object: %w", console.ErrBadQuery, err)
			}
			out = append(out, f)
		}
		if len(out) > 20 {
			return nil, fmt.Errorf("%w: at most 20 filters", console.ErrBadQuery)
		}
	}
	return out, nil
}

// editorError maps the editors' errors to HTTP.
func (s *Server) editorError(w http.ResponseWriter, what string, err error) {
	var qe *console.QueryError
	var de *console.DDLError
	switch {
	case errors.As(err, &qe):
		writeJSON(w, http.StatusBadRequest, gen.Error{Code: "bad_filter", Message: qe.Err.Message, SqlError: sqlError(qe.Err)})
	case errors.As(err, &de):
		writeJSON(w, http.StatusUnprocessableEntity, gen.Error{Code: "ddl_failed", Message: de.Err.Message, SqlError: sqlError(de.Err), Statement: &de.Statement})
	case errors.Is(err, console.ErrBadQuery), errors.Is(err, schemaedit.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
	case errors.Is(err, console.ErrNotEditable):
		writeError(w, http.StatusBadRequest, "not_editable", err.Error())
	case errors.Is(err, console.ErrReadOnly):
		writeError(w, http.StatusForbidden, "console_read_only", err.Error()+": turn the read-only toggle off in the project's settings to edit")
	case errors.Is(err, console.ErrStalePlan):
		writeError(w, http.StatusConflict, "stale_plan", err.Error())
	case errors.Is(err, console.ErrConfirm):
		writeError(w, http.StatusBadRequest, "confirm_required", err.Error())
	default:
		s.consoleError(w, what, err)
	}
}

func sqlError(e *console.Error) *gen.SqlError {
	out := &gen.SqlError{Message: e.Message}
	if e.Code != "" {
		out.Code = &e.Code
	}
	if e.Detail != "" {
		out.Detail = &e.Detail
	}
	if e.Hint != "" {
		out.Hint = &e.Hint
	}
	if e.Position != 0 {
		out.Position = &e.Position
	}
	return out
}

// GetTableRows implements GET /api/v1/projects/{id}/tables/{schema}/{table}/rows.
func (s *Server) GetTableRows(w http.ResponseWriter, r *http.Request, id gen.ProjectID, schema, table string, params gen.GetTableRowsParams) {
	if !s.requireConsole(w) {
		return
	}
	q, err := gridQuery(params.Filter, params.Sort, params.Desc, params.Order)
	if err != nil {
		s.editorError(w, "table rows", err)
		return
	}
	if params.After != nil {
		q.After = *params.After
	}
	if params.Offset != nil {
		if *params.Offset < 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "offset must not be negative")
			return
		}
		q.Offset = params.Offset
	}
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > console.MaxPageSize {
			writeError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("limit must be between 1 and %d", console.MaxPageSize))
			return
		}
		q.Limit = *params.Limit
	}
	page, err := s.console.Rows(r.Context(), id, schema, table, q)
	if err != nil {
		s.editorError(w, "table rows", err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// GetTableInfo implements GET /api/v1/projects/{id}/tables/{schema}/{table}.
func (s *Server) GetTableInfo(w http.ResponseWriter, r *http.Request, id gen.ProjectID, schema, table string) {
	if !s.requireConsole(w) {
		return
	}
	info, err := s.console.Info(r.Context(), id, schema, table)
	if err != nil {
		s.editorError(w, "table info", err)
		return
	}
	if info.Editable {
		ok, err := s.can(r.Context(), authz.TableEdit)
		if err != nil {
			s.internalError(w, "table info", err)
			return
		}
		if !ok {
			info.Editable, info.ReadOnlyReason = false, "Your role on this project is read-only."
		}
	}
	writeJSON(w, http.StatusOK, info)
}

// CountTableRows implements GET /api/v1/projects/{id}/tables/{schema}/{table}/count.
func (s *Server) CountTableRows(w http.ResponseWriter, r *http.Request, id gen.ProjectID, schema, table string, params gen.CountTableRowsParams) {
	if !s.requireConsole(w) {
		return
	}
	filters, err := gridFilters(params.Filter)
	if err != nil {
		s.editorError(w, "count rows", err)
		return
	}
	n, err := s.console.Count(r.Context(), id, schema, table, filters)
	if err != nil {
		s.editorError(w, "count rows", err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

// GetTableDefinition implements GET /api/v1/projects/{id}/tables/{schema}/{table}/definition.
func (s *Server) GetTableDefinition(w http.ResponseWriter, r *http.Request, id gen.ProjectID, schema, table string) {
	if !s.requireConsole(w) {
		return
	}
	sql, err := s.console.Definition(r.Context(), id, schema, table)
	if err != nil {
		s.editorError(w, "table definition", err)
		return
	}
	writeJSON(w, http.StatusOK, gen.TableDefinition{Sql: sql})
}

// ExportTableRows implements GET /api/v1/projects/{id}/tables/{schema}/{table}/export.
func (s *Server) ExportTableRows(w http.ResponseWriter, r *http.Request, id gen.ProjectID, schema, table string, params gen.ExportTableRowsParams) {
	if !s.requireConsole(w) {
		return
	}
	q, err := gridQuery(params.Filter, params.Sort, params.Desc, params.Order)
	if err != nil {
		s.editorError(w, "export", err)
		return
	}
	format := "csv"
	if params.Format != nil {
		format = string(*params.Format)
	}
	if s.tenancy != nil {
		release, err := s.tenancy.AcquireConsole(r.Context(), accessFrom(r.Context()).OrgID)
		if err != nil {
			s.tenancyError(w, "export", err)
			return
		}
		defer release()
	}
	ctype := "text/csv; charset=utf-8"
	if format == "json" {
		ctype = "application/json"
	}
	// Whether rows were left out is only known at the end: a trailer.
	w.Header().Set("Trailer", "X-PGDock-Truncated")
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, safeFilename(table)+"."+format))
	n, more, err := s.console.Export(r.Context(), id, schema, table, q, format, w)
	if err != nil && n == 0 {
		w.Header().Del("Content-Disposition")
		w.Header().Set("Content-Type", "application/json")
		s.editorError(w, "export", err)
		return
	}
	if err != nil {
		s.log.Warn("export ended early", "project_id", id, "err", err)
	}
	w.Header().Set("X-PGDock-Truncated", fmt.Sprint(more))
}

func safeFilename(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, s)
}

// SaveTableChanges implements POST /api/v1/projects/{id}/tables/{schema}/{table}/changes.
func (s *Server) SaveTableChanges(w http.ResponseWriter, r *http.Request, id gen.ProjectID, schema, table string) {
	a := auditFrom(r.Context())
	a.target("table", schema+"."+table)
	if !s.requireConsole(w) {
		return
	}
	var req struct {
		Changes []console.Change `json:"changes"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	a.set("summary", console.Summary(req.Changes))
	if s.tenancy != nil {
		release, err := s.tenancy.AcquireConsole(r.Context(), accessFrom(r.Context()).OrgID)
		if err != nil {
			s.tenancyError(w, "save rows", err)
			return
		}
		defer release()
	}
	res, err := s.console.SaveRows(r.Context(), id, schema, table, req.Changes)
	if err != nil {
		s.editorError(w, "save rows", err)
		return
	}
	switch {
	case res.Conflict != nil:
		a.set("conflict", true)
		writeJSON(w, http.StatusConflict, res)
	case res.Failed != nil:
		writeJSON(w, http.StatusUnprocessableEntity, res)
	default:
		writeJSON(w, http.StatusOK, res)
	}
}

type schemaRequest struct {
	Change  schemaedit.Change `json:"change"`
	Hash    string            `json:"hash"`
	Confirm string            `json:"confirm"`
	Format  string            `json:"format"`
}

// PreviewSchemaChange implements POST /api/v1/projects/{id}/schema/preview.
func (s *Server) PreviewSchemaChange(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	if !s.requireConsole(w) {
		return
	}
	var req schemaRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	plan, err := s.console.PlanSchema(r.Context(), id, req.Change)
	if err != nil {
		s.editorError(w, "preview schema change", err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// ApplySchemaChange implements POST /api/v1/projects/{id}/schema/apply.
func (s *Server) ApplySchemaChange(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	a := auditFrom(r.Context())
	a.target("project", id.String())
	if !s.requireConsole(w) {
		return
	}
	var req schemaRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	a.set("kind", req.Change.Kind)
	sess, _ := sessionFrom(r.Context())
	if sess.Token != nil {
		// Drops with a token need the admin scope (V2 §7.2).
		plan, err := s.console.PlanSchema(r.Context(), id, req.Change)
		if err != nil {
			s.editorError(w, "apply schema change", err)
			return
		}
		if plan.Confirm != "" && !authz.HasScope(sess.Token.Scopes, authz.ScopeAdmin) {
			writeScopeError(w, authz.ScopeAdmin)
			return
		}
	}
	if s.tenancy != nil {
		release, err := s.tenancy.AcquireConsole(r.Context(), accessFrom(r.Context()).OrgID)
		if err != nil {
			s.tenancyError(w, "apply schema change", err)
			return
		}
		defer release()
	}
	applied, err := s.console.ApplySchema(r.Context(), id, req.Change, req.Hash, req.Confirm)
	// Schema history is worth keeping, and rarely secret: the audit log
	// gets the SQL (V2 §4.3).
	if len(applied.Plan.Statements) > 0 {
		sqls := make([]string, len(applied.Plan.Statements))
		for i, st := range applied.Plan.Statements {
			sqls[i] = st.SQL
		}
		a.set("sql", sqls)
	}
	if err != nil {
		s.editorError(w, "apply schema change", err)
		return
	}
	writeJSON(w, http.StatusOK, applied)
}

// SchemaMigration implements POST /api/v1/projects/{id}/schema/migration.
func (s *Server) SchemaMigration(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	if !s.requireConsole(w) {
		return
	}
	var req schemaRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	plan, err := s.console.PlanSchema(r.Context(), id, req.Change)
	if err != nil {
		s.editorError(w, "schema migration", err)
		return
	}
	m, err := schemaedit.Render(plan, req.Format, s.now())
	if err != nil {
		s.editorError(w, "schema migration", err)
		return
	}
	// The project remembers each user's format (V2 §4.3).
	acc := accessFrom(r.Context())
	if err := store.New(s.db).PutEditorPreferences(r.Context(), store.PutEditorPreferencesParams{
		ProjectID: id, UserID: acc.Actor.UserID, OrgID: acc.OrgID, MigrationFormat: req.Format,
	}); err != nil {
		s.log.Warn("save editor preferences", "err", err)
	}
	auditFrom(r.Context()).skip = true
	writeJSON(w, http.StatusOK, m)
}

// GetEditorPreferences implements GET /api/v1/projects/{id}/editor-preferences.
func (s *Server) GetEditorPreferences(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	acc := accessFrom(r.Context())
	f, err := store.New(s.db).GetEditorPreferences(r.Context(), store.GetEditorPreferencesParams{ProjectID: id, UserID: acc.Actor.UserID, OrgID: acc.OrgID})
	if errors.Is(err, pgx.ErrNoRows) {
		f = schemaedit.FormatSQL
	} else if err != nil {
		s.internalError(w, "editor preferences", err)
		return
	}
	writeJSON(w, http.StatusOK, gen.EditorPreferences{MigrationFormat: gen.EditorPreferencesMigrationFormat(f)})
}
