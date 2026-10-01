// Package integration tests shared-tier provisioning end to end against
// real Postgres and PgBouncer: create, connect through the poolers, rotate,
// delete, and rollback (spec §13).
package integration

import (
	"context"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/test/testenv"
)

func TestCreateConnectRotateDelete(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})

	c := e.CreateProject("Integration Blog")
	p := c.Project
	if !strings.HasPrefix(p.DbName, "integration_blog_") || p.OwnerRole != p.DbName+"_owner" {
		t.Fatalf("unexpected names: %s %s", p.DbName, p.OwnerRole)
	}
	if strings.Contains(c.Project.Connection.PooledUrl, c.Password) {
		t.Fatal("project detail URL must not carry the password")
	}
	if !strings.Contains(c.Connection.PooledUrl, ":"+c.Password+"@") {
		t.Fatal("credentials URL must carry the password")
	}

	var got gen.Project
	if code := e.Do("GET", "/api/v1/projects/"+p.Id.String(), nil, &got); code != http.StatusOK || got.Status != gen.ProjectStatusActive {
		t.Fatalf("get project: %d %+v", code, got)
	}

	ctx := context.Background()
	// Both URLs work, and data written through one is visible through the other.
	pooled := e.MustConnect(c.Connection.PooledUrl)
	if _, err := pooled.Exec(ctx, `CREATE TABLE notes (id serial PRIMARY KEY, body text)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pooled.Exec(ctx, `INSERT INTO notes (body) VALUES ('hello')`); err != nil {
		t.Fatal(err)
	}
	session := e.MustConnect(c.Connection.SessionUrl)
	var n int
	if err := session.QueryRow(ctx, `SELECT count(*) FROM notes`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("session read: %d %v", n, err)
	}

	// Protocol-level prepared statements across transactions in transaction
	// mode (max_prepared_statements), as ORMs use them.
	for i := range 20 {
		if err := pooled.QueryRow(ctx, `SELECT count(*) + $1::int FROM notes`, i).Scan(&n); err != nil || n != 1+i {
			t.Fatalf("prepared statement round %d: %d %v", i, n, err)
		}
	}

	// Guardrails from spec §4.3.
	var timeout string
	if err := session.QueryRow(ctx, `SHOW statement_timeout`).Scan(&timeout); err != nil || timeout != "1min" {
		t.Fatalf("statement_timeout = %q, %v", timeout, err)
	}

	// Rotate: the new password works, the old one stops working.
	var rot gen.ProjectCredentials
	if code := e.Do("POST", "/api/v1/projects/"+p.Id.String()+"/rotate-password", nil, &rot); code != http.StatusAccepted {
		t.Fatalf("rotate: %d", code)
	}
	if rot.Password == c.Password {
		t.Fatal("rotation returned the same password")
	}
	if op := e.WaitOperation(rot.Operation.Id); op.Status != gen.Succeeded {
		t.Fatalf("rotate operation %s\n%s", op.Status, testenv.FormatLog(op))
	}
	if conn, err := e.Connect(c.Connection.PooledUrl); err == nil {
		_ = conn.Close(ctx)
		t.Fatal("old password still works after rotation")
	}
	if conn, err := e.Connect(e.DirectURL(c.Connection.SessionUrl, "")); err == nil {
		_ = conn.Close(ctx)
		t.Fatal("old password still works on the backend after rotation")
	}
	rotated := e.MustConnect(rot.Connection.SessionUrl)
	if err := rotated.QueryRow(ctx, `SELECT count(*) FROM notes`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("read after rotation: %d %v", n, err)
	}

	// Delete needs the exact name.
	if code := e.Do("DELETE", "/api/v1/projects/"+p.Id.String()+"?confirm=nope", nil, nil); code != http.StatusBadRequest {
		t.Fatalf("delete with wrong confirm: %d", code)
	}
	var del gen.Operation
	if code := e.Do("DELETE", "/api/v1/projects/"+p.Id.String()+"?confirm=Integration%20Blog", nil, &del); code != http.StatusAccepted {
		t.Fatalf("delete: %d", code)
	}
	// An open client connection must not block the delete.
	if op := e.WaitOperation(del.Id); op.Status != gen.Succeeded {
		t.Fatalf("delete operation %s\n%s", op.Status, testenv.FormatLog(op))
	}
	if code := e.Do("GET", "/api/v1/projects/"+p.Id.String(), nil, nil); code != http.StatusNotFound {
		t.Fatalf("get after delete: %d", code)
	}
	if conn, err := e.Connect(rot.Connection.PooledUrl); err == nil {
		_ = conn.Close(ctx)
		t.Fatal("URL still works after delete")
	}
	assertGone(t, e, p.DbName, p.OwnerRole)
}

func TestPsqlCanConnect(t *testing.T) {
	psql, err := exec.LookPath("psql")
	if err != nil {
		t.Skip("psql not installed")
	}
	e := testenv.Start(t, testenv.Options{})
	c := e.CreateProject("psql check")

	for _, url := range []string{c.Connection.PooledUrl, c.Connection.SessionUrl} {
		out, err := exec.Command(psql, url, "-Atc", "SELECT current_user || '@' || current_database()").CombinedOutput()
		if err != nil {
			t.Fatalf("psql %s: %v\n%s", testenv.RedactURL(url), err, out)
		}
		if got := strings.TrimSpace(string(out)); got != c.Project.OwnerRole+"@"+c.Project.DbName {
			t.Fatalf("psql printed %q", got)
		}
	}
}

func TestFailedCreateRollsBack(t *testing.T) {
	// The transaction-pooler smoke test hits a closed port, so every attempt
	// fails after the database, role, and route exist.
	e := testenv.Start(t, testenv.Options{SmokePooledAddr: "127.0.0.1:1", MaxAttempts: 2})

	var c gen.ProjectCredentials
	if code := e.Do("POST", "/api/v1/projects", map[string]string{"name": "doomed"}, &c); code != http.StatusAccepted {
		t.Fatalf("create: %d", code)
	}
	op := e.WaitOperation(c.Operation.Id)
	if op.Status != gen.Failed || op.Attempts != 2 {
		t.Fatalf("operation %s after %d attempts\n%s", op.Status, op.Attempts, testenv.FormatLog(op))
	}
	if log := testenv.FormatLog(op); !strings.Contains(log, "rollback complete") {
		t.Fatalf("no rollback in log:\n%s", log)
	}
	if code := e.Do("GET", "/api/v1/projects/"+c.Project.Id.String(), nil, nil); code != http.StatusNotFound {
		t.Fatalf("failed project still visible: %d", code)
	}
	if conn, err := e.Connect(c.Connection.SessionUrl); err == nil {
		_ = conn.Close(context.Background())
		t.Fatal("failed project is still reachable through the pooler")
	}
	assertGone(t, e, c.Project.DbName, c.Project.OwnerRole)
}

func TestPauseResume(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	c := e.CreateProject("pausable")
	ctx := context.Background()

	conn := e.MustConnect(c.Connection.PooledUrl)
	if _, err := conn.Exec(ctx, "SELECT 1"); err != nil {
		t.Fatal(err)
	}
	if err := e.Pooler.Pause(ctx, c.Project.DbName); err != nil {
		t.Fatal(err)
	}

	// Queries wait while paused rather than failing...
	done := make(chan error, 1)
	go func() {
		_, err := conn.Exec(ctx, "SELECT 1")
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("query ran while paused (err=%v)", err)
	case <-time.After(500 * time.Millisecond):
	}
	// ...and complete once resumed.
	if err := e.Pooler.Resume(ctx, c.Project.DbName); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("query after resume: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("query still blocked after resume")
	}
}

func TestConcurrentCreates(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	var wg sync.WaitGroup
	creds := make([]gen.ProjectCredentials, 6)
	for i := range creds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var c gen.ProjectCredentials
			if code := e.Do("POST", "/api/v1/projects", map[string]string{"name": "parallel"}, &c); code != http.StatusAccepted {
				t.Errorf("create %d: status %d", i, code)
				return
			}
			creds[i] = c
		}()
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	seen := map[string]bool{}
	for _, c := range creds {
		if op := e.WaitOperation(c.Operation.Id); op.Status != gen.Succeeded {
			t.Fatalf("%s: %s\n%s", c.Project.DbName, op.Status, testenv.FormatLog(op))
		}
		if seen[c.Project.DbName] {
			t.Fatalf("duplicate database name %s", c.Project.DbName)
		}
		seen[c.Project.DbName] = true
	}
	// Every project is routable after all the concurrent syncs.
	for _, c := range creds {
		conn := e.MustConnect(c.Connection.PooledUrl)
		var db string
		if err := conn.QueryRow(context.Background(), "SELECT current_database()").Scan(&db); err != nil || db != c.Project.DbName {
			t.Fatalf("connected to %q, %v; want %s", db, err, c.Project.DbName)
		}
	}
	var list gen.ProjectList
	if code := e.Do("GET", "/api/v1/projects?status=active", nil, &list); code != http.StatusOK || len(list.Items) != len(creds) {
		t.Fatalf("list: %d, %d items", code, len(list.Items))
	}
}

func TestAPIErrors(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	for _, body := range []map[string]string{{"name": ""}, {"name": "!!!"}, {"name": strings.Repeat("x", 65)}} {
		if code := e.Do("POST", "/api/v1/projects", body, nil); code != http.StatusBadRequest {
			t.Errorf("create %v: %d", body, code)
		}
	}
	if code := e.Do("GET", "/api/v1/projects/00000000-0000-0000-0000-000000000000", nil, nil); code != http.StatusNotFound {
		t.Errorf("missing project: %d", code)
	}

	// One operation per project at a time.
	var c gen.ProjectCredentials
	if code := e.Do("POST", "/api/v1/projects", map[string]string{"name": "busy"}, &c); code != http.StatusAccepted {
		t.Fatalf("create: %d", code)
	}
	if code := e.Do("POST", "/api/v1/projects/"+c.Project.Id.String()+"/rotate-password", nil, nil); code != http.StatusConflict {
		t.Errorf("rotate during create: %d", code)
	}
	e.WaitOperation(c.Operation.Id)

	// Operation responses never expose the encrypted password handoff.
	var op gen.Operation
	e.Do("GET", "/api/v1/operations/"+c.Operation.Id.String(), nil, &op)
	if _, ok := op.Params["secrets"]; ok {
		t.Error("operation params expose secrets")
	}
	var raw []byte
	if err := e.DB.QueryRow(context.Background(), `SELECT params::text::bytea FROM operations WHERE id = $1`, c.Operation.Id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secrets") {
		t.Errorf("secrets not scrubbed from finished operation: %s", raw)
	}
}

func assertGone(t *testing.T, e *testenv.Env, db, role string) {
	t.Helper()
	admin := e.SharedAdmin("postgres")
	var dbs, roles int
	if err := admin.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM pg_database WHERE datname = $1),
		(SELECT count(*) FROM pg_roles WHERE rolname = $2)`, db, role).Scan(&dbs, &roles); err != nil {
		t.Fatal(err)
	}
	if dbs != 0 || roles != 0 {
		t.Fatalf("leftovers on the shared cluster: %d database(s), %d role(s)", dbs, roles)
	}
}
