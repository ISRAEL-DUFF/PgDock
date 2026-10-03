package integration

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/test/testenv"
)

// workflowStep is the run script of a step in docs/examples/
// github-actions-branch.yml, with the workflow's env and the given
// expressions substituted as GitHub Actions would.
func workflowStep(t *testing.T, job, id string, exprs map[string]string) (string, map[string]string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "examples", "github-actions-branch.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Env  map[string]string `yaml:"env"`
		Jobs map[string]struct {
			Steps []struct {
				ID  string `yaml:"id"`
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatal(err)
	}
	sub := func(s string) string {
		return regexp.MustCompile(`\$\{\{\s*([^}]+?)\s*\}\}`).ReplaceAllStringFunc(s, func(m string) string {
			k := strings.TrimSpace(m[3 : len(m)-2])
			v, ok := exprs[k]
			if !ok {
				t.Fatalf("the workflow uses %s, which the test does not provide", k)
			}
			return v
		})
	}
	env := map[string]string{}
	for k, v := range wf.Env {
		env[k] = sub(v)
	}
	for _, st := range wf.Jobs[job].Steps {
		if st.ID == id {
			return sub(st.Run), env
		}
	}
	t.Fatalf("no step %s in job %s", id, job)
	return "", nil
}

// runStep runs a step's script with bash -e, as a GitHub-hosted runner does,
// with pgdock on the PATH.
func runStep(t *testing.T, script string, env map[string]string, cliBin string, extra ...string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Symlink(cliBin, filepath.Join(dir, "pgdock")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-e", "-c", script)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Env = append(cmd.Env, extra...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("workflow step: %v\n%s", err, out)
	}
	return string(out)
}

func readEnvFile(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			out[k] = v
		}
	}
	return out
}

func listBranches(t *testing.T, e *testenv.Env, parent uuid.UUID) []gen.Project {
	t.Helper()
	var l gen.ProjectList
	if code := e.Do("GET", "/api/v1/projects/"+parent.String()+"/branches", nil, &l); code != http.StatusOK {
		t.Fatalf("list branches: %d", code)
	}
	return l.Items
}

// waitDeleted waits for the project's delete operation (started by the
// sweep or the workflow) to finish.
func waitGone(t *testing.T, e *testenv.Env, id uuid.UUID) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		p, err := store.New(e.DB).GetProject(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if p.DeletedAt != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("project %s still %s", id, p.Status)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestBranchPerPullRequestWorkflow is the first part of the M13 done-when:
// the example GitHub Actions workflow, run step by step with the real CLI
// and a project-restricted write token, creates a branch per pull request
// whose DATABASE_URL works, replaces it on the next push, and the branch
// is gone after its TTL (the closed-PR job deletes another).
func TestBranchPerPullRequestWorkflow(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	e.ConfigureBackups()
	ctx := context.Background()
	bin := buildCLI(t)

	app := e.CreateProject("my-app")
	conn := e.MustConnect(app.Connection.PooledUrl)
	if _, err := conn.Exec(ctx, `CREATE TABLE todos (id serial PRIMARY KEY, title text NOT NULL); INSERT INTO todos (title) SELECT 'todo ' || g FROM generate_series(1, 25) g`); err != nil {
		t.Fatal(err)
	}
	var op gen.Operation
	if code := e.Do("POST", "/api/v1/projects/"+app.Project.Id.String()+"/backups", nil, &op); code != http.StatusAccepted {
		t.Fatalf("backup: %d", code)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("backup: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	token := e.CreateToken(map[string]any{
		"name": "GitHub Actions", "org_id": e.OrgID, "scopes": []string{"write"}, "project_ids": []uuid.UUID{app.Project.Id},
	})
	exprs := map[string]string{"vars.PGDOCK_SERVER": e.URL, "secrets.PGDOCK_TOKEN": token, "github.event.number": "42"}
	ghEnv := filepath.Join(t.TempDir(), "github_env")
	cfg := "PGDOCK_CONFIG_DIR=" + t.TempDir()

	// The pull request opens: a branch with its own DATABASE_URL.
	script, env := workflowStep(t, "test", "branch", exprs)
	out := runStep(t, script, env, bin, "GITHUB_ENV="+ghEnv, cfg)
	if !strings.Contains(out, "::add-mask::") {
		t.Fatalf("the step does not mask the URLs:\n%s", out)
	}
	vars := readEnvFile(t, ghEnv)
	url := vars["DATABASE_URL"]
	if url == "" || vars["DATABASE_URL_SESSION"] == "" || vars["PGDOCK_BRANCH"] != "pr-42" {
		t.Fatalf("GITHUB_ENV: %v", vars)
	}
	if strings.Contains(out, testenv.RedactURL(url)) && strings.Contains(out, url) {
		t.Fatal("the step printed DATABASE_URL")
	}
	branchDB := e.MustConnect(url)
	var n int
	if err := branchDB.QueryRow(ctx, `SELECT count(*) FROM todos`).Scan(&n); err != nil || n != 25 {
		t.Fatalf("the branch has %d todos (%v)", n, err)
	}
	branchDB.Close(ctx)
	bs := listBranches(t, e, app.Project.Id)
	if len(bs) != 1 || bs[0].Name != "pr-42" || bs[0].Branch == nil || bs[0].Branch.ExpiresAt == nil ||
		time.Until(*bs[0].Branch.ExpiresAt) < 71*time.Hour || bs[0].Branch.Source != gen.BranchInfoSourceBackup {
		t.Fatalf("branches: %+v", bs)
	}
	first := bs[0].Id

	// A push to the pull request replaces the branch.
	if err := os.WriteFile(ghEnv, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	runStep(t, script, env, bin, "GITHUB_ENV="+ghEnv, cfg)
	bs = listBranches(t, e, app.Project.Id)
	if len(bs) != 1 || bs[0].Id == first {
		t.Fatalf("after the second push: %+v", bs)
	}
	waitGone(t, e, first)
	second := bs[0].Id

	// 24 hours before expiry the creator hears about it; after the TTL the
	// hourly sweep deletes the branch.
	e.TenancyAdvance(49 * time.Hour)
	if err := e.Branches.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.SMTP.Count(testenv.OwnerEmail, "Branch pr-42 expires in less than a day"); got != 1 {
		t.Fatalf("expiry emails: %d", got)
	}
	if bs = listBranches(t, e, app.Project.Id); len(bs) != 1 {
		t.Fatalf("branch deleted early: %+v", bs)
	}
	e.TenancyAdvance(24 * time.Hour)
	if err := e.Branches.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	waitGone(t, e, second)
	if bs = listBranches(t, e, app.Project.Id); len(bs) != 0 {
		t.Fatalf("branches after the TTL: %+v", bs)
	}
	if c, err := e.Connect(url); err == nil {
		c.Close(ctx)
		t.Fatal("the expired branch's URL still connects")
	}

	// Another pull request, closed: the cleanup job deletes its branch.
	exprs["github.event.number"] = "43"
	script, env = workflowStep(t, "test", "branch", exprs)
	runStep(t, script, env, bin, "GITHUB_ENV="+filepath.Join(t.TempDir(), "env"), cfg)
	bs = listBranches(t, e, app.Project.Id)
	if len(bs) != 1 || bs[0].Name != "pr-43" {
		t.Fatalf("pr-43 branch: %+v", bs)
	}
	script, env = workflowStep(t, "cleanup", "delete", exprs)
	runStep(t, script, env, bin, cfg)
	waitGone(t, e, bs[0].Id)
	// Branch deletes take no final backup.
	var finals gen.BackupList
	e.Do("GET", "/api/v1/backups?kind=final", nil, &finals)
	if len(finals.Items) != 0 {
		t.Fatalf("final backups of branches: %+v", finals.Items)
	}
	// Branch-hours are recorded as usage.
	if err := e.Tenancy.RecordUsage(ctx); err != nil {
		t.Fatal(err)
	}
	var hours float64
	if err := e.DB.QueryRow(ctx, `SELECT COALESCE(sum(quantity), 0)::float8 FROM usage_records WHERE metric = 'branch_hours' AND org_id = $1`, e.OrgID).Scan(&hours); err != nil || hours <= 0 {
		t.Fatalf("branch-hours usage: %v %v", hours, err)
	}
}

// TestBranchResetKeepsURL is the second part: resetting a branch from its
// parent replaces its data but keeps its DATABASE_URL (database, role,
// password) and members' personal credentials working.
func TestBranchResetKeepsURL(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	e.ConfigureBackups()
	ctx := context.Background()

	parent := e.CreateProject("Shop")
	pc := e.MustConnect(parent.Connection.PooledUrl)
	if _, err := pc.Exec(ctx, `CREATE TABLE items (id serial PRIMARY KEY, name text); INSERT INTO items (name) VALUES ('a'), ('b'), ('c')`); err != nil {
		t.Fatal(err)
	}
	live := gen.BranchRequestSourceLive
	ttl := 0
	var bc gen.ProjectCredentials
	if code := e.Do("POST", "/api/v1/projects/"+parent.Project.Id.String()+"/branches", gen.BranchRequest{Name: "feature-x", Source: &live, TtlHours: &ttl}, &bc); code != http.StatusAccepted {
		t.Fatalf("create branch: %d", code)
	}
	if op := e.WaitOperation(bc.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("create branch: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	url := bc.Connection.PooledUrl
	if bc.Project.ParentProjectId == nil || *bc.Project.ParentProjectId != parent.Project.Id || bc.Project.Branch.ExpiresAt != nil {
		t.Fatalf("branch: %+v", bc.Project)
	}
	// A personal login on the branch, before the reset.
	var personal gen.PersonalCredentials
	if code := e.Do("POST", "/api/v1/projects/"+bc.Project.Id.String()+"/credentials", nil, &personal); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("personal credentials: %d", code)
	}

	// The branch diverges; so does the parent.
	b := e.MustConnect(url)
	if _, err := b.Exec(ctx, `DELETE FROM items; CREATE TABLE scratch (x int)`); err != nil {
		t.Fatal(err)
	}
	b.Close(ctx)
	if _, err := pc.Exec(ctx, `INSERT INTO items (name) VALUES ('d')`); err != nil {
		t.Fatal(err)
	}

	var op gen.Operation
	if code := e.Do("POST", "/api/v1/projects/"+bc.Project.Id.String()+"/reset", gen.BranchResetRequest{}, &op); code != http.StatusAccepted {
		t.Fatalf("reset: %d", code)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("reset: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	var after gen.Project
	e.Do("GET", "/api/v1/projects/"+bc.Project.Id.String(), nil, &after)
	if after.Connection.PooledUrl != bc.Project.Connection.PooledUrl || after.DbName != bc.Project.DbName || after.Status != gen.ProjectStatusActive {
		t.Fatalf("the branch's URL changed: %s → %s (%s)", bc.Project.Connection.PooledUrl, after.Connection.PooledUrl, after.Status)
	}
	// The same DATABASE_URL, password included, sees the parent's data now.
	b = e.MustConnect(url)
	var n int
	var scratch bool
	if err := b.QueryRow(ctx, `SELECT count(*), to_regclass('scratch') IS NOT NULL FROM items`).Scan(&n, &scratch); err != nil || n != 4 || scratch {
		t.Fatalf("after reset: %d items, scratch %v (%v)", n, scratch, err)
	}
	b.Close(ctx)
	pu := e.MustConnect(personal.Connection.PooledUrl)
	if err := pu.QueryRow(ctx, `SELECT count(*) FROM items`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("personal login after reset: %d %v", n, err)
	}
	pu.Close(ctx)

	// The parent can't go while it has a branch; detaching keeps the
	// branch as a standalone project.
	var apiErr gen.Error
	if code := e.Do("DELETE", "/api/v1/projects/"+parent.Project.Id.String()+"?confirm=Shop", nil, &apiErr); code != http.StatusConflict || !strings.Contains(apiErr.Message, "branch") {
		t.Fatalf("delete a parent: %d %+v", code, apiErr)
	}
	apiErr = gen.Error{}
	if code := e.Do("POST", "/api/v1/projects/"+bc.Project.Id.String()+"/promote", map[string]any{}, &apiErr); code != http.StatusBadRequest || !strings.Contains(apiErr.Message, "detach") {
		t.Fatalf("promote a branch: %d %+v", code, apiErr)
	}
	var detached gen.Project
	if code := e.Do("POST", "/api/v1/projects/"+bc.Project.Id.String()+"/detach", nil, &detached); code != http.StatusOK || detached.ParentProjectId != nil || detached.Branch != nil {
		t.Fatalf("detach: %d %+v", code, detached)
	}
	if bs := listBranches(t, e, parent.Project.Id); len(bs) != 0 {
		t.Fatalf("branches after detach: %+v", bs)
	}
}

// TestBranchQuotaOnPersonalPlan is the third part: on the Personal plan
// (10 branches) the 11th branch is refused with quota_exceeded, and
// branches don't count as projects. It also covers the sensitive-data
// defaults (V2 §8.5).
func TestBranchQuotaOnPersonalPlan(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	bob := e.InviteUser("bob@example.com")
	ctx := context.Background()
	var org gen.Org
	if code := bob.Do("GET", "/api/v1/orgs/"+bob.OrgID.String(), nil, &org); code != http.StatusOK || org.Plan != "Personal" {
		t.Fatalf("bob's org: %d %+v", code, org)
	}
	app := bob.CreateProject("app", bob.OrgID)
	live := gen.BranchRequestSourceLive
	schemaOnly := true
	for i := 1; i <= 10; i++ {
		var bc gen.ProjectCredentials
		if code := bob.Do("POST", "/api/v1/projects/"+app.Project.Id.String()+"/branches",
			gen.BranchRequest{Name: fmt.Sprintf("b%d", i), Source: &live, SchemaOnly: &schemaOnly}, &bc); code != http.StatusAccepted {
			t.Fatalf("branch %d: %d", i, code)
		}
		if op := bob.WaitOperation(bc.Operation.Id); op.Status != gen.OperationStatusSucceeded {
			t.Fatalf("branch %d: %s\n%s", i, op.Status, testenv.FormatLog(op))
		}
	}
	var apiErr gen.Error
	if code := bob.Do("POST", "/api/v1/projects/"+app.Project.Id.String()+"/branches",
		gen.BranchRequest{Name: "b11", Source: &live, SchemaOnly: &schemaOnly}, &apiErr); code != http.StatusConflict || apiErr.Code != "quota_exceeded" ||
		apiErr.Quota == nil || apiErr.Quota.Limit != store.LimitBranches || apiErr.Quota.Used != 10 {
		t.Fatalf("11th branch: %d %+v %+v", code, apiErr, apiErr.Quota)
	}
	var q gen.OrgQuotas
	bob.Do("GET", "/api/v1/orgs/"+bob.OrgID.String()+"/quotas", nil, &q)
	for _, it := range q.Items {
		if it.Limit == store.LimitProjects && it.Used != 1 {
			t.Fatalf("branches counted as projects: %+v", it)
		}
		if it.Limit == store.LimitBranches && it.Used != 10 {
			t.Fatalf("branches quota use: %+v", it)
		}
	}
	// Branches don't take a project slot.
	bob.CreateProject("second app", bob.OrgID)
	// A branch can't have branches.
	bs := listBranchesAs(t, bob, app.Project.Id)
	apiErr = gen.Error{}
	if code := bob.Do("POST", "/api/v1/projects/"+bs[0].Id.String()+"/branches", gen.BranchRequest{Name: "deeper", Source: &live}, &apiErr); code != http.StatusBadRequest {
		t.Fatalf("branch of a branch: %d %+v", code, apiErr)
	}

	// Sensitive data: schema only by default, and a full copy needs the
	// project admin role.
	yes := true
	if code := bob.Do("PATCH", "/api/v1/projects/"+app.Project.Id.String()+"/settings", gen.UpdateProjectRequest{SensitiveData: &yes}, nil); code != http.StatusOK {
		t.Fatalf("mark sensitive: %d", code)
	}
	conn := e.MustConnect(app.Connection.PooledUrl)
	if _, err := conn.Exec(ctx, `CREATE TABLE people (email text); INSERT INTO people VALUES ('a@example.com')`); err != nil {
		t.Fatal(err)
	}
	// Free a slot first.
	var op gen.Operation
	if code := bob.Do("DELETE", "/api/v1/projects/"+bs[0].Id.String()+"?confirm="+bs[0].Name, nil, &op); code != http.StatusAccepted {
		t.Fatalf("delete a branch: %d", code)
	}
	bob.WaitOperation(op.Id)
	var bc gen.ProjectCredentials
	if code := bob.Do("POST", "/api/v1/projects/"+app.Project.Id.String()+"/branches", gen.BranchRequest{Name: "safe", Source: &live}, &bc); code != http.StatusAccepted {
		t.Fatalf("branch of a sensitive project: %d", code)
	}
	if op := bob.WaitOperation(bc.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("branch: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	if bc.Project.Branch == nil || !bc.Project.Branch.SchemaOnly || bc.Project.SensitiveData == nil || !*bc.Project.SensitiveData {
		t.Fatalf("sensitive branch: %+v %+v", bc.Project.Branch, bc.Project.SensitiveData)
	}
	sc := e.MustConnect(bc.Connection.PooledUrl)
	if err := sc.QueryRow(ctx, `SELECT count(*) FROM people`).Scan(new(int)); err != nil {
		t.Fatalf("the schema-only branch lacks the table: %v", err)
	}
	var rows int
	_ = sc.QueryRow(ctx, `SELECT count(*) FROM people`).Scan(&rows)
	if rows != 0 {
		t.Fatalf("the schema-only branch has %d rows", rows)
	}
	sc.Close(ctx)
	// A developer can't take a full copy of sensitive data.
	if code := bob.Do("POST", "/api/v1/orgs/"+bob.OrgID.String()+"/members", map[string]string{"email": "carol@example.com", "role": "member"}, nil); code != http.StatusCreated {
		t.Fatalf("invite carol: %d", code)
	}
	carolIn := e.AcceptInvitation(e.MailToken("carol@example.com", "invitation"), "carol@example.com")
	if code := bob.Do("POST", "/api/v1/projects/"+app.Project.Id.String()+"/members", map[string]any{"email": "carol@example.com", "role": "developer"}, nil); code != http.StatusOK {
		t.Fatalf("add carol to the project: %d", code)
	}
	no := false
	apiErr = gen.Error{}
	if code := carolIn.Do("POST", "/api/v1/projects/"+app.Project.Id.String()+"/branches", gen.BranchRequest{Name: "full", Source: &live, SchemaOnly: &no}, &apiErr); code != http.StatusForbidden {
		t.Fatalf("developer full branch of sensitive data: %d %+v", code, apiErr)
	}
}

func listBranchesAs(t *testing.T, c *testenv.Client, parent uuid.UUID) []gen.Project {
	t.Helper()
	var l gen.ProjectList
	if code := c.Do("GET", "/api/v1/projects/"+parent.String()+"/branches", nil, &l); code != http.StatusOK || len(l.Items) == 0 {
		t.Fatalf("list branches: %d %+v", code, l)
	}
	return l.Items
}
