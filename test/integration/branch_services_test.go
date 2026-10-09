package integration

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/test/testenv"
)

// TestBranchServices is V4.1-M8's branch done-when (V4.1 §9.5): a branch
// of a project with backend services answers at its own ref with its own
// keys, refuses the parent's keys and tokens, serves the users its copy
// brought, keeps the parent's exposed schemas and public tables, has none
// of the parent's auth secrets, and with copy_files its files download.
func TestBranchServices(t *testing.T) {
	sp := newStorageProject(t, "shop", false)
	e, ctx := sp.e, context.Background()
	e.StartAgent()

	// The parent: a file, settings to copy, and a secret not to.
	if r := sp.admin.json("POST", "/storage/v1/bucket", `{"id":"docs"}`, ""); r.Code != 201 {
		t.Fatalf("bucket: %s", r)
	}
	if r := sp.admin.do("POST", "/storage/v1/object/docs/readme.txt", strings.NewReader("hello from the parent"), "", "Content-Type", "text/plain"); r.Code != 200 {
		t.Fatalf("upload: %s", r)
	}
	if _, err := e.DB.Exec(ctx, `UPDATE project_services SET public_tables = '{public.notices}', cors_origins = '{https://shop.example}' WHERE project_id = $1`, sp.pid); err != nil {
		t.Fatal(err)
	}
	if code := e.Do("PATCH", "/api/v1/projects/"+sp.pid.String()+"/auth/config", gen.AuthConfigUpdate{
		Settings: &gen.AuthSettings{SiteUrl: ptr("https://shop.example")}, CaptchaSecret: ptr("turnstile-secret")}, nil); code != 200 {
		t.Fatalf("the parent's auth settings: %d", code)
	}
	parentAuth := authAPI{t: t, ed: sp.ed, ref: sp.ref, key: sp.anon.key}
	signIn := func(a authAPI) authResult {
		return a.post("/auth/v1/signin/password", `{"email":"alice@example.com","password":"password-123456"}`)
	}
	parentSession := signIn(parentAuth)
	if parentSession.Code != 200 {
		t.Fatalf("sign in on the parent: %d %s", parentSession.Code, parentSession.Body)
	}

	// The branch, with its files.
	yes, live := true, gen.BranchRequestSourceLive
	var cr gen.ProjectCredentials
	if code := e.Do("POST", "/api/v1/projects/"+sp.pid.String()+"/branches", gen.BranchRequest{Name: "pr-7", Source: &live, CopyFiles: &yes}, &cr); code != http.StatusAccepted {
		t.Fatalf("branch: %d", code)
	}
	if cr.Api == nil || cr.Api.Ref == sp.ref || cr.Api.PublishableKey == "" || cr.Api.SecretKey == "" || cr.Api.Url == nil {
		t.Fatalf("the branch's API: %+v", cr.Api)
	}
	if op := e.WaitOperation(cr.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("branch: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	bid, ref := cr.Project.Id, cr.Api.Ref
	waitFor(t, 30*time.Second, "the files copy", func() bool {
		var status string
		_ = e.DB.QueryRow(ctx, `SELECT status FROM operations WHERE project_id = $1 AND kind = 'copy_branch_files' ORDER BY created_at DESC LIMIT 1`, bid).Scan(&status)
		return status == "succeeded"
	})

	// Its own ref and keys.
	branchAnon := files{t: t, ed: sp.ed, ref: ref, key: cr.Api.PublishableKey}
	branchAdmin := files{t: t, ed: sp.ed, ref: ref, key: cr.Api.SecretKey}
	waitFor(t, 30*time.Second, "the edge serves the branch", func() bool {
		return branchAnon.do("GET", "/data/v1/health", nil, "").Code == 200
	})
	if r := (files{t: t, ed: sp.ed, ref: ref, key: sp.anon.key}).do("GET", "/data/v1/health", nil, ""); r.Code != 401 {
		t.Fatalf("the parent's publishable key on the branch: %d", r.Code)
	}
	if r := (files{t: t, ed: sp.ed, ref: sp.ref, key: cr.Api.PublishableKey}).do("GET", "/data/v1/health", nil, ""); r.Code != 401 {
		t.Fatalf("the branch's key on the parent: %d", r.Code)
	}

	// The parent's users came with the copy; its tokens didn't.
	branchAuth := authAPI{t: t, ed: sp.ed, ref: ref, key: cr.Api.PublishableKey}
	if r := branchAuth.call("GET", "/auth/v1/user", "", parentSession.Session.AccessToken); r.Code != 401 {
		t.Fatalf("the parent's access token on the branch: %d %s", r.Code, r.Body)
	}
	if r := branchAuth.post("/auth/v1/token?grant_type=refresh_token", `{"refresh_token":"`+parentSession.Session.RefreshToken+`"}`); r.Code == 200 {
		t.Fatalf("the parent's refresh token worked on the branch: %s", r.Body)
	}
	branchSession := signIn(branchAuth)
	if branchSession.Code != 200 || branchSession.Session.User.ID != parentSession.Session.User.ID {
		t.Fatalf("alice on the branch: %d %s", branchSession.Code, branchSession.Body)
	}
	if r := parentAuth.call("GET", "/auth/v1/user", "", branchSession.Session.AccessToken); r.Code != 401 {
		t.Fatalf("the branch's token on the parent: %d", r.Code)
	}
	// The parent's session still works there.
	if r := parentAuth.call("GET", "/auth/v1/user", "", parentSession.Session.AccessToken); r.Code != 200 {
		t.Fatalf("the parent's own token after branching: %d", r.Code)
	}

	// Settings, not secrets.
	var tables, origins []string
	if err := e.DB.QueryRow(ctx, `SELECT public_tables, cors_origins FROM project_services WHERE project_id = $1`, bid).Scan(&tables, &origins); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tables, []string{"public.notices"}) || !slices.Equal(origins, []string{"https://shop.example"}) {
		t.Fatalf("the branch's settings: %v %v", tables, origins)
	}
	var secrets *string
	var site string
	if err := e.DB.QueryRow(ctx, `SELECT coalesce(config->>'site_url', config->'settings'->>'site_url', ''), providers_enc::text FROM project_auth_config WHERE project_id = $1`, bid).Scan(&site, &secrets); err != nil {
		t.Fatal(err)
	}
	if site != "https://shop.example" || secrets != nil {
		t.Fatalf("the branch's auth config: site %q, secrets %v", site, secrets)
	}

	// Its own copy of the files.
	r := branchAdmin.do("GET", "/storage/v1/object/docs/readme.txt", nil, "")
	if r.Code != 200 || string(r.Body) != "hello from the parent" {
		t.Fatalf("the branch's file: %d %q", r.Code, r.Body)
	}
	// Deleting it on the branch leaves the parent's.
	if r := branchAdmin.do("DELETE", "/storage/v1/object/docs/readme.txt", nil, ""); r.Code != 200 {
		t.Fatalf("delete on the branch: %s", r)
	}
	if r := sp.admin.do("GET", "/storage/v1/object/docs/readme.txt", nil, ""); r.Code != 200 {
		t.Fatalf("the parent's file after the branch's was deleted: %d", r.Code)
	}
}
