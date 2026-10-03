package api

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/auth"
	"github.com/israel-duff/pgdock/internal/authz"
	"github.com/israel-duff/pgdock/internal/crypto"
	"github.com/israel-duff/pgdock/internal/orgs"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/internal/store/storetest"
)

func allRoutes(t *testing.T) []string {
	t.Helper()
	h := NewHandler(Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), InsecureNoAuth: true,
		UI: fstest.MapFS{"index.html": {Data: []byte("app")}}, UIIndex: "index.html"})
	var out []string
	if err := chi.Walk(h.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		out = append(out, method+" "+route)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// TestEveryRouteDeclaresAnAction is V2 §2.6's CI check: no route without
// an authorization rule (and no rule for a route that is gone).
func TestEveryRouteDeclaresAnAction(t *testing.T) {
	routes := allRoutes(t)
	if len(routes) < 100 {
		t.Fatalf("only %d routes found", len(routes))
	}
	seen := map[string]bool{}
	for _, r := range routes {
		seen[r] = true
		rl, ok := routeRules[r]
		if !ok {
			t.Errorf("%s has no entry in routeRules", r)
			continue
		}
		if rl.scope != scopePublic && rl.action == "" {
			t.Errorf("%s declares no action", r)
		}
		if strings.Contains(r, "{id}") && strings.HasPrefix(strings.SplitN(r, " ", 2)[1], "/api/v1/projects/{id}") && rl.scope != scopeProject {
			t.Errorf("%s is a project route but resolves its resource as scope %d", r, rl.scope)
		}
		if rl.scope == scopeProject && !authz.IsProjectAction(rl.action) {
			t.Errorf("%s: project scope with the non-project action %s", r, rl.action)
		}
	}
	for r := range routeRules {
		if !seen[r] {
			t.Errorf("routeRules has %s, which is not a route", r)
		}
	}
}

// ---- The permission matrix --------------------------------------------------------

// Actors in the matrix world. Everyone but outsider and platform is in org A.
const (
	rOwner     = "org_owner"
	rAdmin     = "org_admin"
	rProjAdmin = "project_admin"
	rDev       = "developer"
	rReadOnly  = "read_only"
	rMember    = "member_without_project"
	rOutsider  = "other_org_owner"
	rPlatform  = "platform_admin"
	rAnonymous = "anonymous"
)

var matrixActors = []string{rOwner, rAdmin, rProjAdmin, rDev, rReadOnly, rMember, rOutsider, rPlatform, rAnonymous}

// expected is the spec's matrix (V2 §2.3, §2.4), written out
// independently of authz: who may perform each action on org A and its
// project P.
var expected = map[authz.Action][]string{
	authz.ProjectView:        {rOwner, rAdmin, rProjAdmin, rDev, rReadOnly},
	authz.ProjectCredentials: {rOwner, rAdmin, rProjAdmin, rDev, rReadOnly},
	authz.ConsoleRead:        {rOwner, rAdmin, rProjAdmin, rDev, rReadOnly},
	authz.ConsoleWrite:       {rOwner, rAdmin, rProjAdmin, rDev},
	authz.TableEdit:          {rOwner, rAdmin, rProjAdmin, rDev},
	authz.BackupCreate:       {rOwner, rAdmin, rProjAdmin, rDev},
	authz.RestoreInPlace:     {rOwner, rAdmin, rProjAdmin},
	authz.BackupStorage:      {rOwner, rAdmin, rProjAdmin},
	authz.BranchManage:       {rOwner, rAdmin, rProjAdmin, rDev},
	authz.AutomationManage:   {rOwner, rAdmin, rProjAdmin, rDev},
	authz.ProjectSettings:    {rOwner, rAdmin, rProjAdmin},
	authz.ProjectMembers:     {rOwner, rAdmin, rProjAdmin},
	authz.ProjectPromote:     {rOwner, rAdmin, rProjAdmin},
	authz.ProjectDelete:      {rOwner, rAdmin, rProjAdmin},
	authz.ProjectAudit:       {rOwner, rAdmin, rProjAdmin},
	authz.OrgView:            {rOwner, rAdmin, rProjAdmin, rDev, rReadOnly, rMember},
	authz.OrgCreateProject:   {rOwner, rAdmin, rProjAdmin, rDev, rReadOnly, rMember},
	authz.OrgManage:          {rOwner, rAdmin},
	authz.OrgAudit:           {rOwner, rAdmin},
	authz.OrgOwnerOnly:       {rOwner},
	authz.ProjectExport:      {rOwner},
	authz.PlatformManage:     {rPlatform},
}

// seesProject is who may know project P exists.
var seesProject = []string{rOwner, rAdmin, rProjAdmin, rDev, rReadOnly}

// seesOrg is who may know org A exists.
var seesOrg = []string{rOwner, rAdmin, rProjAdmin, rDev, rReadOnly, rMember}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

type matrixWorld struct {
	tokens       map[string]string
	users        map[string]uuid.UUID
	orgA, orgB   uuid.UUID
	project      uuid.UUID
	backup       uuid.UUID
	operation    uuid.UUID
	platformOp   uuid.UUID
	invitation   uuid.UUID
	authzHandler http.Handler
	pool         *pgxpool.Pool
	log          *slog.Logger
}

// stubServer answers 501 for every route: a request that reaches it got
// past the guard.
type stubServer struct{ gen.Unimplemented }

func newMatrixWorld(t *testing.T) *matrixWorld {
	t.Helper()
	ctx := context.Background()
	pool := storetest.New(t)
	key, _ := crypto.GenerateKey()
	kr, _ := crypto.NewKeyring(key)
	clk := &fakeClock{t: time.Now()}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := auth.NewService(pool, kr, auth.Config{Now: clk.Now, ReauthWindow: time.Hour}, "code", log)
	if err := svc.EnsureTerms(ctx); err != nil {
		t.Fatal(err)
	}
	orgSvc := orgs.New(pool, svc, nil, nil, "", log)
	svc.SetHooks(orgSvc.Hooks())
	w := &matrixWorld{tokens: map[string]string{}, users: map[string]uuid.UUID{}, pool: pool, log: log}

	// The platform admin, through setup.
	enr, err := svc.BeginSetup(ctx, "code", "platform@example.com", ownerPassword)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := auth.TOTPCode(enr.Secret, clk.Now())
	in, err := svc.CompleteSetup(ctx, enr.Token, code, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	w.tokens[rPlatform], w.users[rPlatform] = in.Token, in.Session.UserID
	clk.step()

	// Everyone else: accounts with a verified address, signed in once.
	for _, r := range []string{rOwner, rAdmin, rProjAdmin, rDev, rReadOnly, rMember, rOutsider} {
		addr := strings.ReplaceAll(r, "_", "-") + "@example.com"
		var u store.User
		if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			var err error
			u, err = svc.CreateVerifiedUser(ctx, tx, addr, ownerPassword, r, 1, nil)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		ch, err := svc.Login(ctx, addr, ownerPassword)
		if err != nil {
			t.Fatal(err)
		}
		code, _ := auth.TOTPCode(ch.Enroll.Secret, clk.Now())
		signed, err := svc.CompleteLogin(ctx, ch.Token, code, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		w.tokens[r], w.users[r] = signed.Token, u.ID
	}

	q := store.New(pool)
	oa, err := orgSvc.Create(ctx, w.users[rOwner], "Org A")
	if err != nil {
		t.Fatal(err)
	}
	ob, err := orgSvc.Create(ctx, w.users[rOutsider], "Org B")
	if err != nil {
		t.Fatal(err)
	}
	w.orgA, w.orgB = oa.ID, ob.ID
	for r, role := range map[string]string{rAdmin: "admin", rProjAdmin: "member", rDev: "member", rReadOnly: "member", rMember: "member"} {
		if err := q.InsertOrgMember(ctx, store.InsertOrgMemberParams{OrgID: w.orgA, UserID: w.users[r], Role: role}); err != nil {
			t.Fatal(err)
		}
	}
	node, inst := uuid.New(), uuid.New()
	for _, stmt := range []string{
		`INSERT INTO nodes (id, name, private_addr, role, agent_cert_fp, capacity) VALUES ('` + node.String() + `', 'n1', '10.0.0.1', 'both', 'fp', '{}')`,
		`INSERT INTO instances (id, node_id, kind, pg_version, port, status) VALUES ('` + inst.String() + `', '` + node.String() + `', 'shared', 18, 5432, 'running')`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	p, err := q.InsertProject(ctx, store.InsertProjectParams{
		ID: uuid.New(), OrgID: w.orgA, Name: "P", Slug: "p", DbName: "p_abcd", OwnerRole: "p_abcd_owner",
		ScramVerifier: "x", Tier: "shared", InstanceID: inst, Settings: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	w.project = p.ID
	for r, role := range map[string]string{rProjAdmin: "admin", rDev: "developer", rReadOnly: "read_only"} {
		if err := q.UpsertProjectMember(ctx, store.UpsertProjectMemberParams{ProjectID: p.ID, UserID: w.users[r], OrgID: w.orgA, Role: role}); err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.QueryRow(ctx, `INSERT INTO backups (project_id, kind, object_key, started_at, status) VALUES ($1, 'logical', 'k', now(), 'succeeded') RETURNING id`, p.ID).Scan(&w.backup); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO operations (kind, project_id) VALUES ('noop', $1) RETURNING id`, p.ID).Scan(&w.operation); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO operations (kind) VALUES ('noop') RETURNING id`).Scan(&w.platformOp); err != nil {
		t.Fatal(err)
	}
	w.invitation = uuid.New()

	// The guard in front of a server that answers 501 everywhere.
	s := &Server{log: log, db: pool, auth: svc}
	r := chi.NewRouter()
	r.Use(s.guard)
	gen.HandlerWithOptions(stubServer{}, gen.ChiServerOptions{BaseRouter: r})
	w.authzHandler = r
	return w
}

// path fills a route pattern's parameters with the world's resources.
func (w *matrixWorld) path(pattern string, platformOp bool) string {
	id := w.project.String()
	switch {
	case strings.HasPrefix(pattern, "/api/v1/backups/{id}"):
		id = w.backup.String()
	case strings.HasPrefix(pattern, "/api/v1/operations/{id}"):
		id = w.operation.String()
		if platformOp {
			id = w.platformOp.String()
		}
	case strings.HasPrefix(pattern, "/api/v1/nodes/{id}"):
		id = uuid.New().String()
	}
	p := strings.NewReplacer(
		"{id}", id, "{org}", w.orgA.String(), "{user}", w.users[rMember].String(),
		"{invitation_id}", w.invitation.String(), "{session_id}", "abc", "{schema}", "public", "{table}", "t",
		"{plan_id}", uuid.NewString(), "{request_id}", uuid.NewString(), "{token_id}", uuid.NewString(), "{target_id}", uuid.NewString(),
		"{webhook_id}", uuid.NewString(), "{job_id}", uuid.NewString(),
		"{user_code}", "BCDF-GHJK",
	).Replace(pattern)
	for _, m := range []string{"GET", "POST"} {
		if rl := routeRules[m+" "+pattern]; rl.scope == scopeOrgQuery {
			p += "?org=" + w.orgA.String()
			break
		}
	}
	return p
}

func (w *matrixWorld) call(t *testing.T, actor, method, path string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	if tok, ok := w.tokens[actor]; ok {
		req.AddCookie(&http.Cookie{Name: sessionName, Value: tok})
	}
	req.AddCookie(&http.Cookie{Name: csrfName, Value: strings.Repeat("c", 40)})
	req.Header.Set(csrfHeader, strings.Repeat("c", 40))
	rec := httptest.NewRecorder()
	w.authzHandler.ServeHTTP(rec, req)
	return rec.Code, rec.Header().Get("X-PGDock-Authz")
}

// TestPermissionMatrix checks every route for every kind of actor against
// the spec's matrix (V2 §2.3, §2.4, §2.6): 404 for resources the actor
// may not know about, 403 for what they see but may not do, and through
// to the handler otherwise.
func TestPermissionMatrix(t *testing.T) {
	w := newMatrixWorld(t)
	for _, route := range allRoutes(t) {
		method, pattern, _ := strings.Cut(route, " ")
		rl := routeRules[route]
		if pattern == "/metrics" {
			continue // its own bearer-token check
		}
		for _, actor := range matrixActors {
			want := "allow"
			switch {
			case rl.scope == scopePublic:
			case actor == rAnonymous:
				want = "unauthenticated"
			case rl.scope == scopeSelf, rl.scope == scopeBody:
				// Body routes check the organisation in the handler.
			case rl.scope == scopePlatform:
				if actor != rPlatform {
					want = "denied"
				}
			case rl.scope == scopeOrgPath || rl.scope == scopeOrgQuery:
				if !has(seesOrg, actor) {
					want = "hidden"
				} else if !has(expected[rl.action], actor) {
					want = "denied"
				}
			default: // project, backup, operation
				if !has(seesProject, actor) {
					want = "hidden"
				} else if !has(expected[rl.action], actor) {
					want = "denied"
				}
			}
			status, hdr := w.call(t, actor, method, w.path(pattern, false))
			got := "allow"
			switch {
			case status == http.StatusUnauthorized:
				got = "unauthenticated"
			case hdr != "":
				got = hdr
			case status == http.StatusForbidden || status == http.StatusNotFound:
				// Refused before the guard decided (CSRF, re-auth, terms)?
				got = fmt.Sprintf("refused-%d", status)
			}
			if got != want {
				t.Errorf("%s as %s: got %s (HTTP %d), want %s", route, actor, got, status, want)
			}
		}
	}

	// Platform operations: the platform admin's alone, and invisible to
	// everyone else.
	for _, actor := range []string{rOwner, rOutsider, rPlatform} {
		_, hdr := w.call(t, actor, "GET", w.path("/api/v1/operations/{id}", true))
		if (actor == rPlatform) != (hdr == "") {
			t.Errorf("platform operation as %s: authz header %q", actor, hdr)
		}
	}
	// Another organisation's ids look like ids that do not exist.
	_, hdrForeign := w.call(t, rOutsider, "GET", "/api/v1/projects/"+w.project.String())
	_, hdrMissing := w.call(t, rOutsider, "GET", "/api/v1/projects/"+uuid.New().String())
	if hdrForeign != "hidden" || hdrMissing != "hidden" {
		t.Errorf("foreign %q vs missing %q", hdrForeign, hdrMissing)
	}
}

// TestMembersCanCreateProjectsSetting covers the one org action that
// depends on a setting.
func TestMembersCanCreateProjectsSetting(t *testing.T) {
	w := newMatrixWorld(t)
	ctx := context.Background()
	check := func(actor string) bool {
		d, err := authz.Can(ctx, store.New(w.pool), authz.Actor{Kind: authz.ActorSession, UserID: w.users[actor]}, authz.OrgCreateProject, authz.Resource{OrgID: w.orgA})
		if err != nil {
			t.Fatal(err)
		}
		return d.Allowed
	}
	if !check(rMember) || !check(rOwner) {
		t.Fatal("members create projects by default")
	}
	off := false
	if _, err := orgs.New(w.pool, nil, nil, nil, "", w.log).Update(ctx, w.orgA, orgs.Patch{MembersCanCreateProjects: &off}); err != nil {
		t.Fatal(err)
	}
	if check(rMember) || !check(rAdmin) {
		t.Fatal("with the setting off, only owners and admins create projects")
	}
	if check(rOutsider) {
		t.Fatal("an outsider created a project in org A")
	}
}

// specMatrix restates V2 §2.3/§2.4 row by row: which routes each action of
// the spec's matrix covers. It is kept apart from routeRules on purpose, so
// a route declared with the wrong action fails here.
var specMatrix = map[authz.Action][]string{
	// "View project, metrics, operations"
	authz.ProjectView: {
		"GET /api/v1/projects/{id}", "GET /api/v1/projects/{id}/metrics", "GET /api/v1/projects/{id}/extensions",
		"GET /api/v1/projects/{id}/members", "GET /api/v1/operations/{id}", "GET /api/v1/operations/{id}/stream",
		// §10.4: storage against the limit, and the reaper's log
		"GET /api/v1/projects/{id}/storage", "GET /api/v1/projects/{id}/reaped",
		// V2 §6: where the project's backups go (choosing is BackupStorage)
		"GET /api/v1/projects/{id}/storage-target",
		// V2 §8 Project → Branches
		"GET /api/v1/projects/{id}/branches",
	},
	// "Get personal DB credentials"
	authz.ProjectCredentials: {"GET /api/v1/projects/{id}/credentials", "POST /api/v1/projects/{id}/credentials"},
	// "SQL console - read" (writes are re-checked in the handler)
	authz.ConsoleRead: {
		"POST /api/v1/projects/{id}/sql", "POST /api/v1/projects/{id}/sql/cancel",
		"GET /api/v1/projects/{id}/schema", "GET /api/v1/projects/{id}/tables/{schema}/{table}/rows",
		// §4.1 the grid's table details and export; previewing a schema
		// change and rendering it as a migration change nothing
		"GET /api/v1/projects/{id}/tables/{schema}/{table}", "GET /api/v1/projects/{id}/tables/{schema}/{table}/export",
		"GET /api/v1/projects/{id}/tables/{schema}/{table}/count", "GET /api/v1/projects/{id}/tables/{schema}/{table}/definition",
		"POST /api/v1/projects/{id}/schema/preview", "POST /api/v1/projects/{id}/schema/migration",
		"GET /api/v1/projects/{id}/editor-preferences",
	},
	// "Table editor — rows and schema"
	authz.TableEdit: {"POST /api/v1/projects/{id}/tables/{schema}/{table}/changes", "POST /api/v1/projects/{id}/schema/apply"},
	// "Create backup, restore into new project" (in place re-checked)
	authz.BackupCreate: {"POST /api/v1/projects/{id}/backups", "POST /api/v1/projects/{id}/pitr", "POST /api/v1/backups/{id}/restore"},
	// "Create / reset / delete branches" (deleting a branch is DELETE
	// /projects/{id} with the branch action; detaching keeps one)
	authz.BranchManage: {"POST /api/v1/projects/{id}/branches", "POST /api/v1/projects/{id}/reset", "POST /api/v1/projects/{id}/detach"},
	// "Manage webhooks and scheduled jobs"
	authz.AutomationManage: {
		"GET /api/v1/projects/{id}/webhooks", "POST /api/v1/projects/{id}/webhooks", "GET /api/v1/projects/{id}/webhooks/{webhook_id}",
		"PATCH /api/v1/projects/{id}/webhooks/{webhook_id}", "DELETE /api/v1/projects/{id}/webhooks/{webhook_id}",
		"POST /api/v1/projects/{id}/webhooks/{webhook_id}/test", "POST /api/v1/projects/{id}/webhooks/{webhook_id}/rotate-secret",
		"GET /api/v1/projects/{id}/webhooks/{webhook_id}/deliveries", "POST /api/v1/projects/{id}/webhooks/{webhook_id}/replay",
		"GET /api/v1/projects/{id}/jobs", "POST /api/v1/projects/{id}/jobs", "GET /api/v1/projects/{id}/jobs/{job_id}",
		"PATCH /api/v1/projects/{id}/jobs/{job_id}", "DELETE /api/v1/projects/{id}/jobs/{job_id}",
		"POST /api/v1/projects/{id}/jobs/{job_id}/run", "GET /api/v1/projects/{id}/jobs/{job_id}/runs",
	},
	// "Choose project's storage target, download backup key" (V2 §6)
	authz.BackupStorage: {
		"PUT /api/v1/projects/{id}/storage-target", "POST /api/v1/projects/{id}/backup-key",
		"GET /api/v1/projects/{id}/backup-key/download",
	},
	// "Rotate app password, settings/guardrails, extensions"
	authz.ProjectSettings: {
		"PATCH /api/v1/projects/{id}/settings", "POST /api/v1/projects/{id}/rotate-password",
		"POST /api/v1/projects/{id}/extensions", "POST /api/v1/projects/{id}/instance",
		// §10.2 switch to opaque credentials (a new app password), §10.4 Reclaim space
		"POST /api/v1/projects/{id}/switch-credentials", "POST /api/v1/projects/{id}/reclaim-space",
	},
	// "Manage project members"
	authz.ProjectMembers: {
		"POST /api/v1/projects/{id}/members", "PATCH /api/v1/projects/{id}/members/{user}", "DELETE /api/v1/projects/{id}/members/{user}",
	},
	// "Promote / demote"
	authz.ProjectPromote: {
		"GET /api/v1/projects/{id}/promote", "POST /api/v1/projects/{id}/promote",
		"POST /api/v1/projects/{id}/demote/preflight", "POST /api/v1/projects/{id}/demote",
	},
	// "Delete project" (transfer also needs owner of both orgs, checked in the handler)
	authz.ProjectDelete: {"DELETE /api/v1/projects/{id}", "POST /api/v1/projects/{id}/transfer"},
	// Project audit log, for project admins (§2.7)
	authz.ProjectAudit: {"GET /api/v1/projects/{id}/audit"},
	// Seeing the organisation and its lists
	authz.OrgView: {
		"GET /api/v1/orgs/{org}", "GET /api/v1/orgs/{org}/members", "POST /api/v1/orgs/{org}/leave",
		"GET /api/v1/projects", "GET /api/v1/operations", "GET /api/v1/backups", "GET /api/v1/backups/overview",
		// §13 "projects list ... with quota usage bars": every member sees the limits
		"GET /api/v1/orgs/{org}/quotas",
	},
	// "Create projects, import"
	authz.OrgCreateProject: {"POST /api/v1/projects", "POST /api/v1/imports"},
	// "Manage org members and invitations", "org settings"
	authz.OrgManage: {
		"PATCH /api/v1/orgs/{org}", "POST /api/v1/orgs/{org}/members", "PATCH /api/v1/orgs/{org}/members/{user}",
		"DELETE /api/v1/orgs/{org}/members/{user}", "GET /api/v1/orgs/{org}/invitations", "DELETE /api/v1/orgs/{org}/invitations/{invitation_id}",
		// §2.3 "See and revoke any token scoped to the org"
		"GET /api/v1/orgs/{org}/tokens", "DELETE /api/v1/orgs/{org}/tokens/{token_id}",
		// V2 §6 "Org targets (managed by org owners and admins)"
		"GET /api/v1/orgs/{org}/storage-targets", "POST /api/v1/orgs/{org}/storage-targets",
		"GET /api/v1/orgs/{org}/storage-targets/{target_id}", "PATCH /api/v1/orgs/{org}/storage-targets/{target_id}",
		"DELETE /api/v1/orgs/{org}/storage-targets/{target_id}", "POST /api/v1/storage-targets/test",
	},
	// "View org usage and quotas, org audit log"
	authz.OrgAudit: {"GET /api/v1/orgs/{org}/audit", "GET /api/v1/orgs/{org}/usage", "GET /api/v1/orgs/{org}/dedicated-requests"},
	// "Add/remove owners, delete org, transfer projects out"
	authz.OrgOwnerOnly: {
		"POST /api/v1/orgs/{org}/transfer-ownership", "DELETE /api/v1/orgs/{org}", "POST /api/v1/orgs/{org}/cancel-deletion",
		// §2.4 "any org owner can end the session early"
		"POST /api/v1/orgs/{org}/break-glass/{session_id}/end",
	},
	// §10.10 "Org owners can export any project as a pg_dump file"
	authz.ProjectExport: {"GET /api/v1/backups/{id}/download"},
	// The signed-in user's own account
	authz.Self: {
		"POST /api/v1/auth/reauth", "POST /api/v1/auth/logout", "GET /api/v1/me", "PATCH /api/v1/me", "POST /api/v1/me/password",
		"GET /api/v1/me/sessions", "DELETE /api/v1/me/sessions/{session_id}", "GET /api/v1/me/recovery-codes",
		"POST /api/v1/me/recovery-codes", "POST /api/v1/me/terms/accept", "GET /api/v1/me/invitations",
		"POST /api/v1/me/invitations/{invitation_id}/accept", "GET /api/v1/orgs", "POST /api/v1/orgs",
		"GET /api/v1/settings/general", "GET /api/v1/profiles", "POST /api/v1/imports/preflight",
		// §7.2 "Users manage their own tokens", §7.1 device-login approval
		"GET /api/v1/tokens", "POST /api/v1/tokens", "DELETE /api/v1/tokens/{token_id}",
		"GET /api/v1/auth/device/requests/{user_code}", "POST /api/v1/auth/device/approve",
	},
	// §2.4: the platform admin's
	authz.PlatformManage: {
		"PUT /api/v1/settings/db-host", "POST /api/v1/settings/db-host/check", "POST /api/v1/dev/operations",
		"POST /api/v1/restore-tests", "GET /api/v1/settings/storage", "PUT /api/v1/settings/storage",
		"POST /api/v1/settings/storage/test", "GET /api/v1/settings/backup-key", "POST /api/v1/settings/backup-key",
		"POST /api/v1/settings/backup-key/export", "POST /api/v1/settings/backup-key/confirm", "GET /api/v1/nodes",
		"POST /api/v1/nodes", "GET /api/v1/nodes/{id}", "PATCH /api/v1/nodes/{id}", "DELETE /api/v1/nodes/{id}",
		"POST /api/v1/nodes/{id}/shared-cluster", "POST /api/v1/nodes/{id}/registration-token", "GET /api/v1/nodes/{id}/metrics",
		"GET /api/v1/security/isolation-checks", "POST /api/v1/security/isolation-checks", "GET /api/v1/alerts",
		"GET /api/v1/settings/alerts", "PUT /api/v1/settings/alerts", "POST /api/v1/settings/alerts/test",
		"GET /api/v1/admin/audit", "GET /api/v1/admin/users", "PATCH /api/v1/admin/users/{user}",
		"POST /api/v1/admin/users/{user}/reset-2fa", "GET /api/v1/admin/invitations", "POST /api/v1/admin/invitations",
		"DELETE /api/v1/admin/invitations/{invitation_id}", "GET /api/v1/admin/settings/signup", "PUT /api/v1/admin/settings/signup",
		"GET /api/v1/admin/settings/mail", "PUT /api/v1/admin/settings/mail", "POST /api/v1/admin/settings/terms",
		// §2.4 "assign quota plans and dedicated allowances; approve dedicated requests", "suspend and reinstate", break-glass
		"GET /api/v1/admin/orgs", "GET /api/v1/admin/orgs/{org}", "PATCH /api/v1/admin/orgs/{org}",
		"POST /api/v1/admin/orgs/{org}/suspend", "POST /api/v1/admin/orgs/{org}/reinstate", "POST /api/v1/admin/orgs/{org}/cluster",
		"GET /api/v1/admin/orgs/{org}/outbound", "PUT /api/v1/admin/orgs/{org}/outbound",
		"POST /api/v1/admin/orgs/{org}/break-glass", "GET /api/v1/admin/plans", "POST /api/v1/admin/plans",
		"PATCH /api/v1/admin/plans/{plan_id}", "GET /api/v1/admin/dedicated-requests",
		"POST /api/v1/admin/dedicated-requests/{request_id}/approve", "POST /api/v1/admin/dedicated-requests/{request_id}/reject",
		"GET /api/v1/admin/usage", "GET /api/v1/admin/shared-clusters",
		// §7.2 "The platform admin can set a platform-wide maximum"
		"GET /api/v1/admin/settings/tokens", "PUT /api/v1/admin/settings/tokens",
		// V2 §6 "Platform targets (managed by the platform admin)"
		"GET /api/v1/admin/storage-targets", "POST /api/v1/admin/storage-targets",
		"GET /api/v1/admin/storage-targets/{target_id}", "PATCH /api/v1/admin/storage-targets/{target_id}",
		"DELETE /api/v1/admin/storage-targets/{target_id}",
	},
}

// TestRoutesMatchTheSpecMatrix checks every route's declared action
// against specMatrix (and that every non-public route is in it once).
func TestRoutesMatchTheSpecMatrix(t *testing.T) {
	want := map[string]authz.Action{}
	for action, routes := range specMatrix {
		for _, r := range routes {
			if prev, dup := want[r]; dup {
				t.Errorf("%s is listed under both %s and %s", r, prev, action)
			}
			want[r] = action
		}
	}
	for route, rl := range routeRules {
		w, listed := want[route]
		switch {
		case rl.scope == scopePublic:
			if listed {
				t.Errorf("%s is public but listed under %s", route, w)
			}
		case !listed:
			t.Errorf("%s (%s) is not in the spec matrix", route, rl.action)
		case w != rl.action:
			t.Errorf("%s declares %s; the spec says %s", route, rl.action, w)
		}
	}
	for r := range want {
		if _, ok := routeRules[r]; !ok {
			t.Errorf("spec matrix lists %s, which has no rule", r)
		}
	}
}
