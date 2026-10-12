package api

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/auth"
	"github.com/israel-duff/pgdock/internal/authz"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/store"
)

// access is what the guard resolved for a request: who acts, on which
// organisation and project, with which roles.
type access struct {
	Actor       authz.Actor
	OrgID       uuid.UUID
	ProjectID   uuid.UUID
	ParentID    uuid.UUID // a branch's parent
	OrgRole     string
	ProjectRole string
	// BreakGlass: a platform admin acting through break-glass (V2 §2.4).
	BreakGlass bool
	// Frozen: refused because the organisation is suspended or deleting.
	Frozen bool
	// NeedScope: an API token was refused for lacking this scope ("session":
	// the route takes no tokens; "unrestricted": a project-restricted token
	// tried an organisation action).
	NeedScope string
}

func accessFrom(ctx context.Context) access {
	if a, ok := ctx.Value(keyAccess).(access); ok {
		return a
	}
	return access{}
}

func actorFor(sess auth.Session) authz.Actor {
	a := authz.Actor{Kind: authz.ActorSession, UserID: sess.UserID, PlatformAdmin: sess.PlatformAdmin(),
		Support: sess.Token == nil && sess.PlatformRole == auth.RoleSupport}
	if t := sess.Token; t != nil {
		a.Kind = authz.ActorToken
		a.TokenID, a.TokenOrg = &t.ID, &t.OrgID
		a.TokenScopes, a.TokenProjects = t.Scopes, t.Projects
	}
	return a
}

// authorize resolves rl's resource and checks the action. A non-zero
// status refuses the request: 404 when the actor may not know the resource
// exists, 403 when they may but cannot do this.
func (s *Server) authorize(ctx context.Context, sess auth.Session, rl rule, params map[string]string, r *http.Request) (access, int, error) {
	acc := access{Actor: actorFor(sess)}
	q := store.New(s.db)
	res := authz.Resource{}
	action := rl.action
	switch rl.scope {
	case scopeSelf:
		if sess.Token != nil {
			switch {
			case rl.token == "":
				acc.NeedScope = "session"
				return acc, http.StatusForbidden, nil
			case !authz.HasScope(sess.Token.Scopes, rl.token):
				acc.NeedScope = rl.token
				return acc, http.StatusForbidden, nil
			}
			return acc, 0, nil
		}
	case scopePlatform:
		if sess.Token != nil {
			acc.NeedScope = "session"
			return acc, http.StatusForbidden, nil
		}
	case scopeBody:
		// The handler names the organisation (authorizeOrg).
		return acc, 0, nil
	case scopeOrgPath:
		id, err := uuid.Parse(params["org"])
		if err != nil {
			return acc, http.StatusNotFound, nil
		}
		res.OrgID = id
	case scopeOrgQuery:
		switch v := r.URL.Query().Get("org"); {
		case v != "":
			id, err := uuid.Parse(v)
			if err != nil {
				return acc, http.StatusNotFound, nil
			}
			res.OrgID = id
		case r.URL.Query().Get("platform") == "true":
			action = authz.PlatformManage
		case sess.Token != nil:
			res.OrgID = sess.Token.OrgID // a token's organisation
		default:
			o, err := q.GetPersonalOrg(ctx, &sess.UserID)
			if errors.Is(err, pgx.ErrNoRows) {
				return acc, http.StatusNotFound, nil
			}
			if err != nil {
				return acc, 0, err
			}
			res.OrgID = o.ID
		}
	case scopeProject:
		id, err := uuid.Parse(params["id"])
		if err != nil {
			return acc, http.StatusNotFound, nil
		}
		rp, err := q.ResolveProject(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return acc, http.StatusNotFound, nil
		}
		if err != nil {
			return acc, 0, err
		}
		res = authz.Resource{OrgID: rp.OrgID, ProjectID: id}
		if rp.ParentProjectID != nil {
			res.ParentID = *rp.ParentProjectID
			if rl.branchAction != "" {
				action = rl.branchAction
			}
		}
	case scopeBackup, scopeOperation:
		id, err := uuid.Parse(params["id"])
		if err != nil {
			return acc, http.StatusNotFound, nil
		}
		var project *uuid.UUID
		if rl.scope == scopeBackup {
			b, err := q.GetBackup(ctx, id)
			if errors.Is(err, pgx.ErrNoRows) {
				return acc, http.StatusNotFound, nil
			}
			if err != nil {
				return acc, 0, err
			}
			project = b.ProjectID
		} else {
			op, err := q.GetOperation(ctx, id)
			if errors.Is(err, pgx.ErrNoRows) {
				return acc, http.StatusNotFound, nil
			}
			if err != nil {
				return acc, 0, err
			}
			project = op.ProjectID
		}
		if project == nil {
			// Platform work (node, metadata backup, restore test): the
			// platform admin's, and invisible to everyone else.
			if !sess.PlatformAdmin() { // never a token
				return acc, http.StatusNotFound, nil
			}
			return acc, 0, nil
		}
		rp, err := q.ResolveProject(ctx, *project)
		if errors.Is(err, pgx.ErrNoRows) {
			return acc, http.StatusNotFound, nil
		}
		if err != nil {
			return acc, 0, err
		}
		res = authz.Resource{OrgID: rp.OrgID, ProjectID: *project}
		if rp.ParentProjectID != nil {
			res.ParentID = *rp.ParentProjectID
		}
	}
	d, err := authz.Can(ctx, q, acc.Actor, action, res)
	if err != nil {
		return acc, 0, err
	}
	acc.OrgID, acc.ProjectID, acc.ParentID, acc.OrgRole, acc.ProjectRole = res.OrgID, res.ProjectID, res.ParentID, d.OrgRole, d.ProjectRole
	acc.BreakGlass, acc.Frozen, acc.NeedScope = d.BreakGlass, d.Frozen, d.NeedScope
	switch {
	case !d.Visible:
		return acc, http.StatusNotFound, nil
	case !d.Allowed:
		return acc, http.StatusForbidden, nil
	}
	return acc, 0, nil
}

// can re-checks a further action on the request's resolved resource (e.g.
// writes in the console, restoring in place).
func (s *Server) can(ctx context.Context, action authz.Action) (bool, error) {
	acc := accessFrom(ctx)
	d, err := authz.Can(ctx, store.New(s.db), acc.Actor, action, authz.Resource{OrgID: acc.OrgID, ProjectID: acc.ProjectID, ParentID: acc.ParentID})
	return d.Allowed, err
}

// authorizeOrg checks action on orgID for a scopeBody route (the
// organisation came in the request body; nil means the user's personal
// organisation). It writes the refusal and returns false when denied.
func (s *Server) authorizeOrg(w http.ResponseWriter, r *http.Request, orgID *uuid.UUID, action authz.Action) (access, bool) {
	ctx := r.Context()
	sess, _ := sessionFrom(ctx)
	acc := access{Actor: actorFor(sess)}
	q := store.New(s.db)
	if orgID == nil && sess.Token != nil {
		orgID = &sess.Token.OrgID
	}
	if orgID == nil {
		o, err := q.GetPersonalOrg(ctx, &sess.UserID)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "not found")
			return acc, false
		}
		if err != nil {
			s.internalError(w, "authorize", err)
			return acc, false
		}
		orgID = &o.ID
	}
	d, err := authz.Can(ctx, q, acc.Actor, action, authz.Resource{OrgID: *orgID})
	if err != nil {
		s.internalError(w, "authorize", err)
		return acc, false
	}
	acc.OrgID, acc.OrgRole, acc.BreakGlass = *orgID, d.OrgRole, d.BreakGlass
	if !d.Visible {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return acc, false
	}
	if d.Frozen {
		writeError(w, http.StatusForbidden, "org_suspended", "the organisation is suspended or being deleted; only viewing works")
		return acc, false
	}
	if d.NeedScope != "" {
		writeScopeError(w, d.NeedScope)
		return acc, false
	}
	if !d.Allowed {
		writeError(w, http.StatusForbidden, "forbidden", "you don't have permission to do this")
		return acc, false
	}
	a := auditFrom(ctx)
	a.orgID, a.breakGlass = *orgID, d.BreakGlass
	return acc, true
}

// creatorRole is the project role a new project's creator is given: none
// for org owners and admins (they are admins of every project already),
// admin for members (V2 §2.2).
func creatorRole(acc access) string {
	if acc.OrgRole == authz.OrgOwner || acc.OrgRole == authz.OrgAdmin {
		return ""
	}
	return authz.ProjectAdmin
}

// tenantProject loads the request's project, scoped to its organisation.
func (s *Server) tenantProject(ctx context.Context) (store.Project, error) {
	acc := accessFrom(ctx)
	return store.New(s.db).GetOrgProject(ctx, store.GetOrgProjectParams{ID: acc.ProjectID, OrgID: acc.OrgID})
}

// visibleProjects says which of acc's organisation's projects the actor
// sees: all of them for owners and admins, otherwise their memberships.
func (s *Server) visibleProjects(ctx context.Context, acc access) (bool, []uuid.UUID) {
	if acc.Actor.Restricted() {
		// A project-restricted token sees its projects (those its user
		// still can).
		all, ids := s.visibleProjects(ctx, access{Actor: authz.Actor{Kind: authz.ActorSession, UserID: acc.Actor.UserID}, OrgID: acc.OrgID, OrgRole: acc.OrgRole})
		out := []uuid.UUID{}
		for _, id := range acc.Actor.TokenProjects {
			if all || slices.Contains(ids, id) {
				out = append(out, id)
			}
		}
		return false, out
	}
	if acc.OrgRole == authz.OrgOwner || acc.OrgRole == authz.OrgAdmin {
		return true, []uuid.UUID{}
	}
	ids := []uuid.UUID{}
	rows, err := store.New(s.db).ListOrgProjectMemberships(ctx, acc.OrgID)
	if err != nil {
		return false, ids
	}
	for _, m := range rows {
		if m.UserID == acc.Actor.UserID {
			ids = append(ids, m.ProjectID)
		}
	}
	return false, ids
}

// tenantProjectLive is tenantProject for a project that must not be
// deleted; it answers provision.ErrNotFound otherwise.
func (s *Server) tenantProjectLive(ctx context.Context) (store.Project, error) {
	p, err := s.tenantProject(ctx)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && p.DeletedAt != nil) {
		return store.Project{}, provision.ErrNotFound
	}
	return p, err
}
