package integration

import (
	"bufio"
	"context"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/test/testenv"
)

// TestCLIPolicies is V4.1-M8's CLI done-when (V4.1 §9.4): `policies
// list` shows a table's policies in the request roles' names, `policies
// lint` exits 1 while a table has no row-level security (and 0 once it
// does), and `logs api` filters the request log and follows it. Around
// it, the catalog the API docs are drawn from.
func TestCLIPolicies(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	creds := e.CreateProject("lint-me")
	pid := creds.Project.Id
	p, err := store.New(e.DB).GetProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	var en gen.BackendServicesEnabled
	if code := e.Do("POST", "/api/v1/projects/"+pid.String()+"/services", nil, &en); code != http.StatusAccepted {
		t.Fatalf("enable: %d", code)
	}
	if op := e.WaitOperation(en.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("enable: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	app := e.MustConnect(creds.Connection.PooledUrl)
	defer app.Close(ctx)
	for _, stmt := range []string{
		`CREATE TYPE mood AS ENUM ('happy', 'sad')`,
		`CREATE TABLE lists (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, title text NOT NULL)`,
		`CREATE TABLE todos (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, list_id bigint REFERENCES lists, owner uuid NOT NULL DEFAULT pgd_auth.uid(),
			body text NOT NULL, mood mood, done boolean NOT NULL DEFAULT false)`,
		`ALTER TABLE todos ENABLE ROW LEVEL SECURITY`,
		`CREATE POLICY own_rows ON todos TO ` + store.UserRole(p.DbName) + ` USING (owner = pgd_auth.uid()) WITH CHECK (owner = pgd_auth.uid())`,
		`CREATE TABLE notes (id int PRIMARY KEY, body text)`,
	} {
		if _, err := app.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	// The catalog: columns with defaults and enums, the foreign key both
	// ways, the policy in friendly names, and who may do what.
	var cat gen.ServicesCatalog
	if code := e.Do("GET", "/api/v1/projects/"+pid.String()+"/services/catalog", nil, &cat); code != http.StatusOK || cat.Ref == nil {
		t.Fatalf("catalog: %d %+v", code, cat)
	}
	byName := map[string]gen.CatalogTable{}
	for _, tb := range cat.Tables {
		byName[tb.Name] = tb
	}
	todos, lists := byName["todos"], byName["lists"]
	cols := map[string]gen.CatalogColumn{}
	for _, c := range todos.Columns {
		cols[c.Name] = c
	}
	if !todos.Rls || len(todos.Policies) != 1 || todos.Policies[0].Roles[0] != "user" || todos.Policies[0].Using == nil ||
		cols["owner"].Default == nil || !strings.Contains(*cols["owner"].Default, "uid()") || !cols["id"].Identity ||
		cols["mood"].Enum == nil || len(*cols["mood"].Enum) != 2 ||
		len(todos.ForeignKeys) != 1 || todos.ForeignKeys[0].Embed != "lists" || len(lists.ReferencedBy) != 1 || !lists.ReferencedBy[0].Multiple {
		t.Fatalf("todos in the catalog: %+v\nlists: %+v", todos, lists)
	}
	if a := todos.Access["user"]; !a.Select || !a.Insert {
		t.Fatalf("user's access to todos: %+v", todos.Access)
	}

	bin := buildCLI(t)
	token := e.CreateToken(map[string]any{"name": "ci", "org_id": e.OrgID, "scopes": []string{"read", "write"}})
	env := []string{"PGDOCK_SERVER=" + e.URL, "PGDOCK_TOKEN=" + token, "PGDOCK_CONFIG_DIR=" + t.TempDir()}

	if r := runCLI(t, bin, env, "policies", "list", "lint-me", "--table", "todos"); r.code != 0 ||
		!strings.Contains(r.stdout, "own_rows") || !strings.Contains(r.stdout, "user") || !strings.Contains(r.stdout, "uid()") {
		t.Fatalf("policies list: exit %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	if r := runCLI(t, bin, env, "policies", "list", "lint-me"); r.code != 0 || !strings.Contains(r.stdout, "public.notes (row-level security OFF)") {
		t.Fatalf("policies list all: exit %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	r := runCLI(t, bin, env, "policies", "lint", "lint-me")
	if r.code != 1 || !strings.Contains(r.stdout, "public.notes") || !strings.Contains(r.stdout, "danger") {
		t.Fatalf("policies lint with notes unprotected: exit %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	if r := runCLI(t, bin, env, "--json", "policies", "lint", "lint-me"); r.code != 1 || !strings.HasPrefix(strings.TrimSpace(r.stdout), "[") {
		t.Fatalf("policies lint --json: exit %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	if _, err := app.Exec(ctx, `ALTER TABLE notes ENABLE ROW LEVEL SECURITY; ALTER TABLE lists ENABLE ROW LEVEL SECURITY`); err != nil {
		t.Fatal(err)
	}
	if r := runCLI(t, bin, env, "policies", "lint", "lint-me"); r.code != 0 {
		t.Fatalf("policies lint with every table protected: exit %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}

	// The request log: filtered, then followed.
	logRow := func(status int, path string) {
		t.Helper()
		if _, err := e.DB.Exec(ctx, `INSERT INTO api_request_logs (project_id, at, request_id, method, path, status, latency_ms, role, bytes_out)
			VALUES ($1, now(), gen_random_uuid()::text, 'GET', $2, $3, 12, 'anon', 10)`, pid, path, status); err != nil {
			t.Fatal(err)
		}
	}
	logRow(200, "/data/v1/todos")
	logRow(503, "/data/v1/todos")
	logRow(404, "/storage/v1/object/x")
	if r := runCLI(t, bin, env, "logs", "api", "lint-me", "--status", "5xx"); r.code != 0 || strings.Count(r.stdout, "\n") != 1 || !strings.Contains(r.stdout, "503") {
		t.Fatalf("logs --status 5xx: exit %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	if r := runCLI(t, bin, env, "logs", "api", "lint-me", "--path", "/storage/"); r.code != 0 || strings.Count(r.stdout, "\n") != 1 || !strings.Contains(r.stdout, "404") {
		t.Fatalf("logs --path: exit %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	fctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(fctx, bin, "logs", "api", "lint-me", "--follow", "--path", "/data/v1/lists")
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	lines := make(chan string, 10)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	time.Sleep(2 * time.Second)   // it is waiting on the server
	logRow(201, "/data/v1/todos") // filtered out
	logRow(200, "/data/v1/lists")
	select {
	case l := <-lines:
		if !strings.Contains(l, "/data/v1/lists") || !strings.Contains(l, "200") {
			t.Fatalf("followed line: %q", l)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("logs --follow printed nothing for a new request")
	}
}
