package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/test/testenv"
)

// apiClient calls a project's data API as one caller.
type apiClient struct {
	t     *testing.T
	ed    *testenv.Edge
	ref   string
	key   string
	token string
}

type apiResult struct {
	Code       int
	Data       json.RawMessage `json:"data"`
	NextCursor string          `json:"next_cursor"`
	Count      *int64          `json:"count"`
	Error      struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	} `json:"error"`
	Body string
}

func (c apiClient) do(method, path, body string) apiResult {
	c.t.Helper()
	h := []string{"apikey", c.key}
	if c.token != "" {
		h = append(h, "Authorization", "Bearer "+c.token)
	}
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
		h = append(h, "Content-Type", "application/json")
	}
	var code int
	var out string
	if rdr != nil {
		code, _, out = c.ed.Do(c.ref, method, path, rdr, h...)
	} else {
		code, _, out = c.ed.Do(c.ref, method, path, nil, h...)
	}
	r := apiResult{Code: code, Body: out}
	_ = json.Unmarshal([]byte(out), &r)
	return r
}

func (c apiClient) get(path string) apiResult { return c.do("GET", path, "") }

func (r apiResult) rows(t *testing.T) []map[string]any {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal(r.Data, &rows); err != nil {
		t.Fatalf("not a list (%d): %s", r.Code, r.Body)
	}
	return rows
}

func titles(rows []map[string]any, key string) []string {
	var out []string
	for _, r := range rows {
		out = append(out, fmt.Sprint(r[key]))
	}
	sort.Strings(out)
	return out
}

// TestDataAPIReads is V4-M29's done-when (V4 §3.2, §3.6): the RLS test
// suite's read cases pass for anon, two users and service, and a table
// without row-level security returns rls_required. Around it: embeds,
// filters, JSON paths, ordering, cursor pagination, counts, one row by key,
// JSON queries, views, exposed schemas and public tables, a schema change
// picked up without a restart, error codes, the cost guard and the
// project's OpenAPI document.
func TestDataAPIReads(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	creds := e.CreateProject("todo-app")
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
		   done boolean NOT NULL DEFAULT false, meta jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL)`,
		`CREATE TABLE posts (id serial PRIMARY KEY, author_id uuid REFERENCES profiles(id), title text, published boolean NOT NULL DEFAULT false)`,
		`CREATE TABLE comments (id serial PRIMARY KEY, post_id int NOT NULL REFERENCES posts(id), body text)`,
		`CREATE TABLE notes (id serial PRIMARY KEY, body text)`, // no row-level security
		`ALTER TABLE profiles ENABLE ROW LEVEL SECURITY`,
		`CREATE POLICY read_all ON profiles FOR SELECT USING (true)`,
		`ALTER TABLE todos ENABLE ROW LEVEL SECURITY`,
		fmt.Sprintf(`CREATE POLICY own_todos ON todos FOR ALL TO %q USING (owner_id = pgd_auth.uid()) WITH CHECK (owner_id = pgd_auth.uid())`, userRole),
		`ALTER TABLE posts ENABLE ROW LEVEL SECURITY`,
		`CREATE POLICY visible_posts ON posts FOR SELECT USING (published OR author_id = pgd_auth.uid())`,
		`ALTER TABLE comments ENABLE ROW LEVEL SECURITY`,
		`CREATE POLICY comments_of_visible_posts ON comments FOR SELECT USING (EXISTS (SELECT 1 FROM posts p WHERE p.id = post_id))`,
		`CREATE VIEW todo_titles AS SELECT id, title FROM todos`,
		`CREATE VIEW my_todo_titles WITH (security_invoker = true) AS SELECT id, title FROM todos`,
		fmt.Sprintf(`INSERT INTO profiles VALUES ('%s', 'Ada'), ('%s', 'Bola')`, u1, u2),
		fmt.Sprintf(`INSERT INTO todos (owner_id, title, done, meta, created_at) VALUES
		   ('%[1]s', 'a', false, '{"tag":"home"}', '2026-10-01 10:00Z'), ('%[1]s', 'b', true, '{"tag":"work"}', '2026-10-02 10:00Z'),
		   ('%[1]s', 'c', false, '{"tag":"work"}', '2026-10-03 10:00Z'), ('%[2]s', 'd', false, '{}', '2026-10-04 10:00Z'),
		   ('%[2]s', 'e', true, '{}', '2026-10-05 10:00Z')`, u1, u2),
		fmt.Sprintf(`INSERT INTO posts (author_id, title, published) VALUES ('%[1]s', 'p1', true), ('%[1]s', 'p2', false), ('%[2]s', 'p3', false)`, u1, u2),
		`INSERT INTO comments (post_id, body) VALUES (1, 'c1a'), (1, 'c1b'), (2, 'c2'), (3, 'c3')`,
		`INSERT INTO notes (body) VALUES ('secret note')`,
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

	// ---- The RLS suite's read cases ----------------------------------------
	for _, c := range []struct {
		who  string
		cl   apiClient
		path string
		want []string
	}{
		{"anon", anon, "/data/v1/todos", nil},
		{"user 1", user1, "/data/v1/todos", []string{"a", "b", "c"}},
		{"user 2", user2, "/data/v1/todos", []string{"d", "e"}},
		{"service", service, "/data/v1/todos", []string{"a", "b", "c", "d", "e"}},
		{"anon", anon, "/data/v1/posts", []string{"p1"}},
		{"user 1", user1, "/data/v1/posts", []string{"p1", "p2"}},
		{"user 2", user2, "/data/v1/posts", []string{"p1", "p3"}},
		{"service", service, "/data/v1/posts", []string{"p1", "p2", "p3"}},
		{"user 1", user1, "/data/v1/my_todo_titles", []string{"a", "b", "c"}},
		{"user 2", user2, "/data/v1/my_todo_titles", []string{"d", "e"}},
	} {
		r := c.cl.get(c.path)
		if r.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", c.who, c.path, r.Code, r.Body)
		}
		got := titles(r.rows(t), "title")
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Fatalf("%s %s: %v, want %v", c.who, c.path, got, c.want)
		}
	}
	// Embedded rows follow the same policies.
	r := anon.get("/data/v1/posts?select=title,author:profiles(name),comments(body)")
	rows := r.rows(t)
	if r.Code != 200 || len(rows) != 1 || fmt.Sprint(rows[0]["author"]) != "map[name:Ada]" || len(rows[0]["comments"].([]any)) != 2 {
		t.Fatalf("embeds as anon: %d %s", r.Code, r.Body)
	}
	r = user2.get("/data/v1/posts?select=title,comments(body)&order=id")
	if rows = r.rows(t); len(rows) != 2 || rows[1]["title"] != "p3" || fmt.Sprint(rows[1]["comments"]) != "[map[body:c3]]" {
		t.Fatalf("one-to-many embeds as user 2: %s", r.Body)
	}
	// The done-when's other half: no row-level security, no access.
	for _, c := range []apiClient{anon, user1} {
		if r := c.get("/data/v1/notes"); r.Code != http.StatusForbidden || r.Error.Code != "rls_required" {
			t.Fatalf("notes without RLS: %d %s", r.Code, r.Body)
		}
		if r := c.get("/data/v1/todo_titles"); r.Code != http.StatusForbidden || r.Error.Code != "rls_required" {
			t.Fatalf("a view without security_invoker: %d %s", r.Code, r.Body)
		}
		if r := c.get("/data/v1/posts?select=title,notes(body)"); r.Code != http.StatusBadRequest && r.Code != http.StatusForbidden {
			t.Fatalf("embedding a table without RLS: %d %s", r.Code, r.Body)
		}
	}
	if r := service.get("/data/v1/notes"); r.Code != http.StatusOK || len(r.rows(t)) != 1 {
		t.Fatalf("service reads notes: %d %s", r.Code, r.Body)
	}

	// ---- Filters, JSON paths, ordering ---------------------------------------
	for _, c := range []struct {
		q    string
		want string
	}{
		{"where=done:eq:true", "b"},
		{"where=done:eq:false&where=title:neq:a", "c"},
		{"where=meta->tag:eq:work", "b,c"},
		{"where=title:in:a,c", "a,c"},
		{"where=title:like:%25b%25", "b"},
		{"where=created_at:gte:2026-10-02T00:00:00Z", "b,c"},
		{"or=title:eq:a,done:eq:true", "a,b"},
		{"where=meta->tag:is:null", ""},
	} {
		r := user1.get("/data/v1/todos?" + c.q)
		if r.Code != http.StatusOK || strings.Join(titles(r.rows(t), "title"), ",") != c.want {
			t.Fatalf("%s: %d %s (want %s)", c.q, r.Code, r.Body, c.want)
		}
	}
	r = user1.get("/data/v1/todos?select=title,tag:meta->tag&order=created_at:desc")
	if rows = r.rows(t); len(rows) != 3 || rows[0]["title"] != "c" || rows[0]["tag"] != "work" {
		t.Fatalf("order and a JSON path: %s", r.Body)
	}

	// ---- Cursor pagination, offsets and counts --------------------------------
	seen := []string{}
	cursor := ""
	for page := 0; page < 5; page++ {
		path := "/data/v1/todos?select=title&order=created_at:desc&limit=2"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		r := service.get(path)
		if r.Code != http.StatusOK {
			t.Fatalf("page %d: %s", page, r.Body)
		}
		for _, row := range r.rows(t) {
			seen = append(seen, row["title"].(string))
		}
		if r.NextCursor == "" {
			break
		}
		cursor = r.NextCursor
	}
	if strings.Join(seen, ",") != "e,d,c,b,a" {
		t.Fatalf("pages: %v", seen)
	}
	if r := service.get("/data/v1/todos?select=title&order=title&offset=3"); strings.Join(titles(r.rows(t), "title"), ",") != "d,e" {
		t.Fatalf("offset: %s", r.Body)
	}
	if r := service.get("/data/v1/todos?count=exact&limit=1"); r.Count == nil || *r.Count != 5 || len(r.rows(t)) != 1 {
		t.Fatalf("exact count: %s", r.Body)
	}
	if r := user1.get("/data/v1/todos?count=exact&where=done:eq:false"); r.Count == nil || *r.Count != 2 {
		t.Fatalf("count under RLS: %s", r.Body)
	}
	if r := service.get("/data/v1/todos?count=estimated"); r.Code != 200 || r.Count == nil {
		t.Fatalf("estimated count: %s", r.Body)
	}
	if r := service.get("/data/v1/todos?cursor=bogus"); r.Code != 400 || r.Error.Code != "invalid_cursor" {
		t.Fatalf("bad cursor: %s", r.Body)
	}

	// ---- One row, JSON queries -----------------------------------------------
	var u1TodoID int64
	if err := app.QueryRow(ctx, `SELECT id FROM todos WHERE title = 'a'`).Scan(&u1TodoID); err != nil {
		t.Fatal(err)
	}
	one := fmt.Sprintf("/data/v1/todos/%d?select=title,owner:profiles(name)", u1TodoID)
	if r := user1.get(one); r.Code != 200 || !strings.Contains(string(r.Data), `"owner":{"name":"Ada"}`) {
		t.Fatalf("one row: %d %s", r.Code, r.Body)
	}
	if r := user2.get(one); r.Code != http.StatusNotFound {
		t.Fatalf("another user's row: %d %s", r.Code, r.Body)
	}
	r = user1.do("POST", "/data/v1/todos/query", `{"select":"title","where":{"or":[{"column":"done","op":"eq","value":true},
		{"and":[{"column":"meta->tag","op":"eq","value":"home"},{"not":{"column":"title","op":"eq","value":"zzz"}}]}]},"order":"title:desc"}`)
	if r.Code != 200 || strings.Join(titles(r.rows(t), "title"), ",") != "a,b" || r.rows(t)[0]["title"] != "b" {
		t.Fatalf("JSON query: %d %s", r.Code, r.Body)
	}

	// ---- Errors --------------------------------------------------------------
	for _, c := range []struct {
		cl     apiClient
		method string
		path   string
		status int
		code   string
	}{
		{user1, "GET", "/data/v1/todos?where=nope:eq:1", 400, "unknown_column"},
		{user1, "GET", "/data/v1/todos?where=id:eq:abc", 400, "invalid_value"},
		{user1, "GET", "/data/v1/nothing", 404, "unknown_table"},
		{user1, "GET", "/data/v1/todos?limit=5000", 400, "invalid_limit"},
		{user1, "GET", "/data/v1/todos?bogus=1", 400, "unknown_parameter"},
		{user1, "PUT", "/data/v1/todos", 405, "method_not_allowed"},
		{anon, "GET", "/data/v1/pgd_auth.claims", 404, "unknown_table"},
	} {
		r := c.cl.do(c.method, c.path, "")
		if r.Code != c.status || r.Error.Code != c.code {
			t.Fatalf("%s %s: %d %s (want %d %s)", c.method, c.path, r.Code, r.Body, c.status, c.code)
		}
	}

	// ---- Schema changes are picked up -------------------------------------------
	if _, err := app.Exec(ctx, `ALTER TABLE todos ADD COLUMN priority int NOT NULL DEFAULT 3`); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 15*time.Second, "the new column through the API", func() bool {
		r := user1.get("/data/v1/todos?select=title,priority&where=priority:eq:3")
		return r.Code == 200 && len(r.rows(t)) == 3
	})

	// ---- Public tables and exposed schemas ---------------------------------
	for _, stmt := range []string{`CREATE SCHEMA api`, `CREATE TABLE api.items (id serial PRIMARY KEY, name text)`,
		`INSERT INTO api.items (name) VALUES ('from api')`, `ALTER TABLE api.items ENABLE ROW LEVEL SECURITY`,
		`CREATE POLICY anyone ON api.items FOR SELECT USING (true)`} {
		if _, err := app.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	var svc gen.BackendServices
	if code := e.Do("PATCH", "/api/v1/projects/"+pid.String()+"/services", gen.BackendServicesUpdate{
		ExposedSchemas: &[]string{"public", "api"}, PublicTables: &[]string{"notes"}}, &svc); code != http.StatusOK || len(svc.ExposedSchemas) != 2 {
		t.Fatalf("expose: %d %+v", code, svc)
	}
	var apiErr gen.Error
	if code := e.Do("PATCH", "/api/v1/projects/"+pid.String()+"/services", gen.BackendServicesUpdate{
		ExposedSchemas: &[]string{"public", "pgd_auth"}}, &apiErr); code != http.StatusBadRequest {
		t.Fatalf("exposing pgd_auth: %d", code)
	}
	waitFor(t, 15*time.Second, "public notes and the api schema", func() bool {
		a := anon.get("/data/v1/notes")
		b := anon.get("/data/v1/api.items")
		c := anon.get("/data/v1/items") // the first exposed schema that has it
		return a.Code == 200 && b.Code == 200 && c.Code == 200 && len(b.rows(t)) == 1
	})

	// ---- The cost guard ---------------------------------------------------------
	one1 := 1
	if code := e.Do("PATCH", "/api/v1/projects/"+pid.String()+"/services", gen.BackendServicesUpdate{
		Settings: &gen.BackendServicesSettings{MaxQueryCost: &one1}}, &svc); code != http.StatusOK {
		t.Fatalf("cost limit: %d", code)
	}
	waitFor(t, 15*time.Second, "the cost guard", func() bool {
		r := service.get("/data/v1/todos?order=title")
		return r.Code == 400 && r.Error.Code == "query_too_expensive"
	})

	// ---- The project's OpenAPI document ----------------------------------------
	if r := anon.get("/data/v1/openapi.json"); r.Code != http.StatusForbidden {
		t.Fatalf("OpenAPI with the publishable key: %d", r.Code)
	}
	code, _, body := ed.Do(ref, "GET", "/data/v1/openapi.json", nil, "apikey", sec)
	var doc struct {
		Paths      map[string]any `json:"paths"`
		Components struct {
			Schemas map[string]struct {
				Properties map[string]map[string]any `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil || code != 200 || doc.Paths["/data/v1/todos"] == nil ||
		doc.Paths["/data/v1/todos/{id}"] == nil || doc.Components.Schemas["todos"].Properties["done"]["type"] != "boolean" ||
		doc.Paths["/data/v1/api.items"] == nil {
		t.Fatalf("OpenAPI: %d %v %s", code, err, body[:min(len(body), 400)])
	}
}

// TestServicesSurviveRestore (V4 §2.5): a project with backend services is
// backed up and restored, in place and into a new project. The pgd_*
// schemas restore (they hold the auth users from M31), are taken back by
// the platform when the reconciler runs, and the data API answers with the
// same policies afterwards.
func TestServicesSurviveRestore(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	e.ConfigureBackups()
	ctx := context.Background()
	creds := e.CreateProject("Shop")
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
	var pub string
	for _, k := range en.Keys {
		if k.Key.Kind == gen.ApiKeyKindPublishable {
			pub = k.Value
		}
	}
	app := e.MustConnect(creds.Connection.PooledUrl)
	for _, stmt := range []string{
		`CREATE TABLE items (id serial PRIMARY KEY, name text, public boolean NOT NULL)`,
		`ALTER TABLE items ENABLE ROW LEVEL SECURITY`,
		`CREATE POLICY public_items ON items FOR SELECT USING (public)`,
		`INSERT INTO items (name, public) VALUES ('shown', true), ('hidden', false)`,
	} {
		if _, err := app.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	app.Close(ctx)
	ed := e.StartEdge()
	anon := apiClient{t, ed, *en.Services.Ref, pub, ""}
	if r := anon.get("/data/v1/items?select=name"); r.Code != 200 || strings.Join(titles(r.rows(t), "name"), ",") != "shown" {
		t.Fatalf("before: %d %s", r.Code, r.Body)
	}

	var op gen.Operation
	if code := e.Do("POST", "/api/v1/projects/"+pid.String()+"/backups", nil, &op); code != http.StatusAccepted {
		t.Fatalf("backup: %d", code)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("backup: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	var list gen.BackupList
	if code := e.Do("GET", "/api/v1/backups?project_id="+pid.String(), nil, &list); code != http.StatusOK || len(list.Items) != 1 {
		t.Fatalf("backups: %d %+v", code, list)
	}
	b := list.Items[0]

	// Into a new project: the pgd_* schemas come along; the new project
	// has no backend services until it turns them on.
	var rr gen.RestoreResponse
	mode, name := gen.New, "Shop copy"
	if code := e.Do("POST", "/api/v1/backups/"+b.Id.String()+"/restore", gen.RestoreRequest{Mode: &mode, Name: &name}, &rr); code != http.StatusAccepted {
		t.Fatalf("restore to new: %d", code)
	}
	if op = e.WaitOperation(rr.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("restore to new: %s\n%s", op.Status, testenv.FormatLog(op))
	}

	// In place.
	inPlace, confirm := gen.InPlace, "Shop"
	e.Reauth()
	rr = gen.RestoreResponse{}
	if code := e.Do("POST", "/api/v1/backups/"+b.Id.String()+"/restore", gen.RestoreRequest{Mode: &inPlace, Confirm: &confirm}, &rr); code != http.StatusAccepted {
		t.Fatalf("in place: %d", code)
	}
	if op = e.WaitOperation(rr.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("in place: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	if err := e.Services.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	conn, err := e.Service.AdminConn(ctx, p.InstanceID, p.DbName)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var owner string
	if err := conn.QueryRow(ctx, `SELECT nspowner::regrole::text FROM pg_namespace WHERE nspname = 'pgd_auth'`).Scan(&owner); err != nil || owner == p.OwnerRole {
		t.Fatalf("pgd_auth after the restore is owned by %q (%v)", owner, err)
	}
	waitFor(t, 15*time.Second, "the data API after the restore", func() bool {
		r := anon.get("/data/v1/items?select=name")
		return r.Code == 200 && strings.Join(titles(r.rows(t), "name"), ",") == "shown"
	})

	// Promotion to a dedicated instance copies the database keeping owners
	// and grants: the request roles must exist there first, and the pgd_*
	// schemas are copied as the platform's (V4 §2.5). Same API URL after.
	if os.Getenv("PGDOCK_TEST_PG_IMAGE") == "" {
		return
	}
	profile, vol := "small", 5
	if code := e.Do("POST", "/api/v1/projects/"+pid.String()+"/promote", gen.PromoteRequest{Profile: &profile, VolumeGb: &vol}, &op); code != http.StatusAccepted {
		t.Fatalf("promote: %d", code)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("promote: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	if err := e.Services.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile after promotion: %v", err)
	}
	waitFor(t, 30*time.Second, "the data API after the promotion", func() bool {
		r := anon.get("/data/v1/items?select=name")
		return r.Code == 200 && strings.Join(titles(r.rows(t), "name"), ",") == "shown"
	})
}
