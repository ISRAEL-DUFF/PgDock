package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/test/testenv"
)

// writeResultBody is a write's response.
type writeResultBody struct {
	Affected int              `json:"affected"`
	Data     []map[string]any `json:"data"`
}

func (r apiResult) written(t *testing.T) writeResultBody {
	t.Helper()
	var w writeResultBody
	if err := json.Unmarshal([]byte(r.Body), &w); err != nil {
		t.Fatalf("not a write result (%d): %s", r.Code, r.Body)
	}
	return w
}

// TestDataAPIWrites is V4-M30's done-when (V4 §3.3, §3.4, §3.7): a sample
// todo app runs end to end with only the publishable key and generated
// types (Go, compiled and run here; TypeScript type-checked). Around it:
// inserts, upserts, updates and deletes under row-level security, the
// filter requirement and max_affected, batches that are all or nothing,
// functions over GET and POST, the security advisor and the request
// explorer.
func TestDataAPIWrites(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	creds := e.CreateProject("todo-writes")
	pid := creds.Project.Id
	p, err := store.New(e.DB).GetProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	userRole := store.UserRole(p.DbName)
	var en gen.BackendServicesEnabled
	if code := e.Do("POST", "/api/v1/projects/"+pid.String()+"/services", nil, &en); code != http.StatusAccepted {
		t.Fatalf("enable: %d", code)
	}
	if op := e.WaitOperation(en.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("enable: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	var pub, sec string
	for _, k := range en.Keys {
		if k.Key.Kind == gen.ApiKeyKindPublishable {
			pub = k.Value
		} else {
			sec = k.Value
		}
	}
	ref := *en.Services.Ref
	u1, u2 := uuid.New(), uuid.New()
	app := e.MustConnect(creds.Connection.PooledUrl)
	defer app.Close(ctx)
	for _, stmt := range []string{
		`CREATE TABLE profiles (id uuid PRIMARY KEY, name text NOT NULL)`,
		`CREATE TABLE todos (id bigserial PRIMARY KEY, owner_id uuid NOT NULL REFERENCES profiles(id), title text NOT NULL,
		   done boolean NOT NULL DEFAULT false, created_at timestamptz NOT NULL DEFAULT now())`,
		`CREATE TABLE notes (id serial PRIMARY KEY, body text)`,
		`ALTER TABLE profiles ENABLE ROW LEVEL SECURITY`,
		`CREATE POLICY read_all ON profiles FOR SELECT USING (true)`,
		`ALTER TABLE todos ENABLE ROW LEVEL SECURITY`,
		fmt.Sprintf(`CREATE POLICY own_todos ON todos FOR ALL TO %q USING (owner_id = pgd_auth.uid()) WITH CHECK (owner_id = pgd_auth.uid())`, userRole),
		`CREATE FUNCTION todo_counts() RETURNS TABLE (done boolean, n bigint) LANGUAGE sql STABLE AS
		   'SELECT done, count(*) FROM todos GROUP BY done ORDER BY done'`,
		`CREATE FUNCTION complete_all(before timestamptz DEFAULT now()) RETURNS int LANGUAGE sql AS
		   'WITH u AS (UPDATE todos SET done = true WHERE NOT done AND created_at <= before RETURNING 1) SELECT count(*)::int FROM u'`,
		`CREATE FUNCTION add(a int, b int) RETURNS int LANGUAGE sql IMMUTABLE AS 'SELECT a + b'`,
		`CREATE FUNCTION wipe() RETURNS void LANGUAGE sql SECURITY DEFINER AS 'DELETE FROM notes'`,
		fmt.Sprintf(`INSERT INTO profiles VALUES ('%s', 'Ada'), ('%s', 'Bola')`, u1, u2),
		fmt.Sprintf(`INSERT INTO todos (owner_id, title) VALUES ('%s', 'u2 first')`, u2),
	} {
		if _, err := app.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	ed := e.StartEdge()
	token := func(u uuid.UUID) string {
		tok, err := e.Services.MintToken(ctx, pid, map[string]any{"sub": u.String(), "role": "user"}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	anon := apiClient{t, ed, ref, pub, ""}
	user1 := apiClient{t, ed, ref, pub, token(u1)}
	user2 := apiClient{t, ed, ref, pub, token(u2)}
	service := apiClient{t, ed, ref, sec, ""}
	count := func(where string) int {
		t.Helper()
		var n int
		if err := app.QueryRow(ctx, "SELECT count(*) FROM todos WHERE "+where).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// ---- Inserts and upserts under row-level security -------------------------
	r := user1.do("POST", "/data/v1/todos", fmt.Sprintf(`{"owner_id":"%s","title":"one"}`, u1))
	w := r.written(t)
	if r.Code != http.StatusCreated || w.Affected != 1 || w.Data[0]["title"] != "one" || w.Data[0]["done"] != false {
		t.Fatalf("insert: %d %s", r.Code, r.Body)
	}
	firstID := int64(w.Data[0]["id"].(float64))
	r = user1.do("POST", "/data/v1/todos?select=title,owner:profiles(name)", fmt.Sprintf(`[{"owner_id":"%[1]s","title":"two"},{"owner_id":"%[1]s","title":"three"}]`, u1))
	if w = r.written(t); r.Code != 201 || w.Affected != 2 || fmt.Sprint(w.Data[0]["owner"]) != "map[name:Ada]" {
		t.Fatalf("insert many with embeds: %d %s", r.Code, r.Body)
	}
	for _, c := range []struct {
		name string
		cl   apiClient
		path string
		body string
		code int
		err  string
	}{
		{"another user's row", user1, "/data/v1/todos", fmt.Sprintf(`{"owner_id":"%s","title":"sneaky"}`, u2), 403, "permission_denied"},
		{"anon", anon, "/data/v1/todos", fmt.Sprintf(`{"owner_id":"%s","title":"anon"}`, u1), 403, "permission_denied"},
		{"no row-level security", user1, "/data/v1/notes", `{"body":"x"}`, 403, "rls_required"},
		{"an unknown column", user1, "/data/v1/todos", `{"nope":1}`, 400, "unknown_column"},
		{"a missing column", user1, "/data/v1/todos", fmt.Sprintf(`{"owner_id":"%s"}`, u1), 422, "not_null_violation"},
		{"a missing reference", service, "/data/v1/todos", fmt.Sprintf(`{"owner_id":"%s","title":"x"}`, uuid.New()), 409, "foreign_key_violation"},
		{"not JSON", user1, "/data/v1/todos", `nope`, 400, "invalid_body"},
	} {
		r := c.cl.do("POST", c.path, c.body)
		if r.Code != c.code || r.Error.Code != c.err {
			t.Fatalf("insert, %s: %d %s (want %d %s)", c.name, r.Code, r.Body, c.code, c.err)
		}
	}
	r = user1.do("POST", "/data/v1/todos?on_conflict=id", fmt.Sprintf(`{"id":%d,"owner_id":"%s","title":"one, renamed"}`, firstID, u1))
	if w = r.written(t); r.Code != 201 || w.Affected != 1 || w.Data[0]["title"] != "one, renamed" {
		t.Fatalf("upsert (merge): %d %s", r.Code, r.Body)
	}
	r = user1.do("POST", "/data/v1/todos?on_conflict=id&resolution=ignore&return=minimal", fmt.Sprintf(`{"id":%d,"owner_id":"%s","title":"ignored"}`, firstID, u1))
	if w = r.written(t); r.Code != 201 || w.Affected != 0 || w.Data != nil {
		t.Fatalf("upsert (ignore, minimal): %d %s", r.Code, r.Body)
	}

	// ---- Updates and deletes ------------------------------------------------------
	r = user1.do("PATCH", fmt.Sprintf("/data/v1/todos/%d", firstID), `{"done":true}`)
	if w = r.written(t); r.Code != 200 || w.Affected != 1 || w.Data[0]["done"] != true {
		t.Fatalf("update by key: %d %s", r.Code, r.Body)
	}
	if r := user2.do("PATCH", fmt.Sprintf("/data/v1/todos/%d", firstID), `{"done":false}`); r.Code != 404 {
		t.Fatalf("update another user's row: %d %s", r.Code, r.Body)
	}
	if r := user1.do("PATCH", "/data/v1/todos", `{"done":true}`); r.Code != 400 || r.Error.Code != "filter_required" {
		t.Fatalf("an unfiltered update: %d %s", r.Code, r.Body)
	}
	if r := user1.do("PATCH", "/data/v1/todos?where=done:eq:false&max_affected=1", `{"done":true}`); r.Code != 400 || r.Error.Code != "too_many_rows" {
		t.Fatalf("max_affected: %d %s", r.Code, r.Body)
	}
	if n := count(fmt.Sprintf("owner_id = '%s' AND NOT done", u1)); n != 2 {
		t.Fatalf("max_affected changed rows anyway: %d not done left", n)
	}
	r = user1.do("PATCH", "/data/v1/todos?where=done:eq:false", `{"title":"bulk"}`)
	if w = r.written(t); r.Code != 200 || w.Affected != 2 {
		t.Fatalf("update by filter (only u1's rows): %d %s", r.Code, r.Body)
	}
	if n := count("title = 'u2 first'"); n != 1 {
		t.Fatal("a user's update reached another user's row")
	}
	if r := user1.do("DELETE", "/data/v1/todos", ""); r.Code != 400 || r.Error.Code != "filter_required" {
		t.Fatalf("an unfiltered delete: %d %s", r.Code, r.Body)
	}
	r = user1.do("DELETE", fmt.Sprintf("/data/v1/todos/%d", firstID), "")
	if w = r.written(t); r.Code != 200 || w.Affected != 1 {
		t.Fatalf("delete by key: %d %s", r.Code, r.Body)
	}
	if r := user2.do("DELETE", "/data/v1/todos?where=title:eq:bulk", ""); r.Code != 200 || r.written(t).Affected != 0 {
		t.Fatalf("a delete under row-level security: %d %s", r.Code, r.Body)
	}

	// ---- Batches: all or nothing ----------------------------------------------
	r = user1.do("POST", "/data/v1/batch", fmt.Sprintf(`{"operations":[
		{"op":"insert","table":"todos","rows":[{"owner_id":"%[1]s","title":"batched"}]},
		{"op":"update","table":"todos","where":{"column":"title","op":"eq","value":"batched"},"set":{"done":true}},
		{"op":"delete","table":"todos","where":{"column":"title","op":"eq","value":"bulk"},"return":"minimal"}]}`, u1))
	var batch struct {
		Results []writeResultBody `json:"results"`
	}
	if err := json.Unmarshal([]byte(r.Body), &batch); err != nil || r.Code != 200 || len(batch.Results) != 3 ||
		batch.Results[1].Affected != 1 || batch.Results[2].Affected != 2 {
		t.Fatalf("batch: %d %s", r.Code, r.Body)
	}
	r = user1.do("POST", "/data/v1/batch", fmt.Sprintf(`{"operations":[
		{"op":"insert","table":"todos","rows":{"owner_id":"%[1]s","title":"rolled back"}},
		{"op":"insert","table":"todos","rows":{"owner_id":"%[2]s","title":"not mine"}}]}`, u1, u2))
	if r.Code != 403 || r.Error.Details["operation"] != float64(1) || count("title = 'rolled back'") != 0 {
		t.Fatalf("a failing batch: %d %s", r.Code, r.Body)
	}

	// ---- Functions -----------------------------------------------------------
	r = user1.get("/data/v1/rpc/todo_counts")
	if rows := r.rows(t); r.Code != 200 || len(rows) != 1 || rows[0]["done"] != true {
		t.Fatalf("a stable set-returning function over GET (under RLS): %d %s", r.Code, r.Body)
	}
	if r := service.get("/data/v1/rpc/todo_counts?where=done:eq:false"); r.Code != 200 || len(r.rows(t)) != 1 || r.rows(t)[0]["n"] != float64(1) {
		t.Fatalf("filtering a function's rows: %d %s", r.Code, r.Body)
	}
	if r := anon.get("/data/v1/rpc/add?a=2&b=40"); r.Code != 200 || string(r.Data) != "42" {
		t.Fatalf("an immutable scalar function: %d %s", r.Code, r.Body)
	}
	if r := user1.get("/data/v1/rpc/complete_all"); r.Code != 405 || r.Error.Code != "volatile_function" {
		t.Fatalf("a volatile function over GET: %d %s", r.Code, r.Body)
	}
	if _, err := app.Exec(ctx, fmt.Sprintf(`INSERT INTO todos (owner_id, title) VALUES ('%s', 'open')`, u1)); err != nil {
		t.Fatal(err)
	}
	if r := user1.do("POST", "/data/v1/rpc/complete_all", `{}`); r.Code != 200 || string(r.Data) != "1" {
		t.Fatalf("a volatile function over POST: %d %s", r.Code, r.Body)
	}
	if r := user1.do("POST", "/data/v1/rpc/add", `{"a":1}`); r.Code != 400 || r.Error.Code != "no_matching_function" {
		t.Fatalf("missing arguments: %d %s", r.Code, r.Body)
	}

	// ---- The security advisor ----------------------------------------------
	var adv gen.AdvisorFindings
	if code := e.Do("GET", "/api/v1/projects/"+pid.String()+"/services/advisor", nil, &adv); code != 200 {
		t.Fatalf("advisor: %d", code)
	}
	codes := map[string]string{}
	for _, f := range adv.Items {
		codes[f.Code+" "+f.Object] = string(f.Level)
	}
	if codes["rls_disabled public.notes"] != "danger" || codes["security_definer_function public.wipe"] != "warn" ||
		codes["public_read_policy public.profiles / read_all"] != "info" {
		t.Fatalf("advisor findings: %v", codes)
	}

	// ---- The request explorer ------------------------------------------------
	var ex gen.ExploreResponse
	if code := e.Do("POST", "/api/v1/projects/"+pid.String()+"/services/explore", gen.ExploreRequest{Method: "GET",
		Path: "/data/v1/todos?select=title&order=title", Role: "user", UserId: &u2}, &ex); code != 200 || ex.Status != 200 ||
		!strings.Contains(ex.Body, "u2 first") || strings.Contains(ex.Body, "batched") {
		t.Fatalf("explorer as user 2: %d %+v", code, ex)
	}
	if code := e.Do("POST", "/api/v1/projects/"+pid.String()+"/services/explore", gen.ExploreRequest{Method: "GET",
		Path: "/data/v1/notes", Role: "anon"}, &ex); code != 200 || ex.Status != 403 {
		t.Fatalf("explorer as anon on notes: %d %+v", code, ex)
	}

	// ---- The done-when: a todo app on the generated types ------------------------
	typesOf := func(lang string, extra string) string {
		t.Helper()
		code, body := e.DoBytes("GET", "/api/v1/projects/"+pid.String()+"/services/types?lang="+lang+extra, "", nil)
		if code != 200 {
			t.Fatalf("types %s: %d %s", lang, code, body)
		}
		return string(body)
	}
	if dart := typesOf("dart", ""); !strings.Contains(dart, "class Todos {") || !strings.Contains(dart, "factory Todos.fromJson") {
		t.Fatalf("dart types:\n%s", dart)
	}
	runGoTodoApp(t, ed, ref, pub, token(u1), u1, typesOf("go", "&package=main"))
	checkTSTodoApp(t, typesOf("ts", ""))
}

// runGoTodoApp builds and runs a program that uses only the generated Go
// types, net/http and the publishable key with a user's token.
func runGoTodoApp(t *testing.T, ed *testenv.Edge, ref, key, token string, user uuid.UUID, types string) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go tool")
	}
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":   "module todoapp\n\ngo 1.22\n",
		"types.go": types,
		"main.go": `package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

func call(method, path string, body any, out any) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, os.Getenv("API")+path, r)
	req.Host = os.Getenv("HOST")
	req.Header.Set("apikey", os.Getenv("KEY"))
	req.Header.Set("Authorization", "Bearer "+os.Getenv("TOKEN"))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		panic(fmt.Sprintf("%s %s: %d %s", method, path, res.StatusCode, raw))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		panic(err)
	}
}

type written[T any] struct {
	Affected int ` + "`json:\"affected\"`" + `
	Data     []T ` + "`json:\"data\"`" + `
}

type list[T any] struct {
	Data []T ` + "`json:\"data\"`" + `
}

func main() {
	var ins written[Todos]
	call("POST", "/data/v1/"+TodosTable, TodosInsert{OwnerID: os.Getenv("USER_ID"), Title: "from the generated types"}, &ins)
	id := ins.Data[0].ID
	done := true
	var upd written[Todos]
	call("PATCH", fmt.Sprintf("/data/v1/%s/%d", TodosTable, id), TodosUpdate{Done: &done}, &upd)
	var mine list[Todos]
	call("GET", "/data/v1/"+TodosTable+"?order=id", nil, &mine)
	var del written[Todos]
	call("DELETE", fmt.Sprintf("/data/v1/%s/%d", TodosTable, id), nil, &del)
	fmt.Printf("inserted %d done=%v listed=%d deleted=%d created=%s\n", id, upd.Data[0].Done, len(mine.Data), del.Affected, ins.Data[0].CreatedAt.Format("2006"))
}
`,
	}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("go", "run", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "API="+ed.URL, "HOST="+ref+"."+testenv.EdgeDomain,
		"KEY="+key, "TOKEN="+token, "USER_ID="+user.String())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the Go todo app: %v\n%s\n--- types.go\n%s", err, out, types)
	}
	if !strings.Contains(string(out), "done=true") || !strings.Contains(string(out), "deleted=1") {
		t.Fatalf("the Go todo app: %s", out)
	}
	t.Logf("Go todo app: %s", strings.TrimSpace(string(out)))
}

// checkTSTodoApp type-checks a TypeScript todo app against the generated
// types with the dashboard's TypeScript compiler, when it is installed.
func checkTSTodoApp(t *testing.T, types string) {
	t.Helper()
	root, _ := filepath.Abs("../../web/node_modules/typescript/bin/tsc")
	node, err := exec.LookPath("node")
	if err != nil || !fileExists(root) {
		t.Log("TypeScript isn't installed (web/node_modules): skipping the TS check")
		return
	}
	dir := t.TempDir()
	app := `import type { Database, Tables, TablesInsert, TablesUpdate } from "./database";
type Todo = Tables<"todos">;
const insert: TablesInsert<"todos"> = { owner_id: "u", title: "t" };
const update: TablesUpdate<"todos"> = { done: true };
const row: Todo = { id: 1, owner_id: "u", title: "t", done: false, created_at: "2026-10-07T00:00:00Z" };
type Counts = Database["public"]["Functions"]["todo_counts"]["Returns"];
const counts: Counts = [{ done: true, n: 1 }];
// @ts-expect-error: title is required on insert
const bad: TablesInsert<"todos"> = { owner_id: "u" };
export { insert, update, row, counts, bad };
`
	for name, src := range map[string]string{"database.ts": types, "app.ts": app} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(node, root, "--noEmit", "--strict", "--target", "es2020", "--moduleResolution", "node", "app.ts")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tsc: %v\n%s\n--- database.ts\n%s", err, out, types)
	}
}

func fileExists(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(f, 1))
	_ = f.Close()
	return true
}
