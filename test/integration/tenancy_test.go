package integration

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/test/testenv"
)

// waitKind waits for the latest operation of kind on a project to finish.
func waitKind(t *testing.T, e *testenv.Env, projectID uuid.UUID, kind string) store.Operation {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		var id uuid.UUID
		err := e.DB.QueryRow(context.Background(), `SELECT id FROM operations WHERE project_id = $1 AND kind = $2
			ORDER BY created_at DESC LIMIT 1`, projectID, kind).Scan(&id)
		if err == nil {
			op := e.WaitOperation(id)
			o, err := store.New(e.DB).GetOperation(context.Background(), op.Id)
			if err != nil {
				t.Fatal(err)
			}
			if o.Status != "succeeded" {
				t.Fatalf("%s failed:\n%s", kind, testenv.FormatLog(op))
			}
			return o
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no %s operation for %s", kind, projectID)
	return store.Operation{}
}

// names lists one catalog column as a tenant sees it.
func names(t *testing.T, c *pgx.Conn, sql string) []string {
	t.Helper()
	rows, err := c.Query(context.Background(), sql)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func containsAny(list []string, words ...string) []string {
	var hit []string
	for _, n := range list {
		for _, w := range words {
			if strings.Contains(n, w) {
				hit = append(hit, n)
			}
		}
	}
	return hit
}

// TestOtherTenantsCannotDiscoverProjectNames is M9's third done-when (V2
// §10.2): another tenant can't discover the name of any project created in
// V2, or of a V1 project once it switched to opaque credentials, while V1
// connection strings keep working through the rename.
func TestOtherTenantsCannotDiscoverProjectNames(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	payroll := e.CreateProject("Acme Payroll")
	if !provision.IsOpaque(payroll.Project.DbName) {
		t.Fatalf("a V2 project's database is %s", payroll.Project.DbName)
	}
	v1 := e.CreateProject("Old Blog")
	legacy := e.MakeV1(v1.Project.Id, "oldblog_k2f9")
	v1URL := strings.ReplaceAll(v1.Connection.PooledUrl, v1.Project.DbName, legacy.DbName)
	app := e.MustConnect(v1URL)
	if _, err := app.Exec(ctx, `CREATE TABLE posts (id int); INSERT INTO posts VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	app.Close(ctx)

	bob := e.InviteUser("bob@example.com")
	bobApp := bob.CreateProject("Bob app", bob.OrgID)
	tenant := e.MustConnect(bobApp.Connection.PooledUrl)

	// The sweep renames the V1 database; its old name keeps routing.
	if err := e.Tenancy.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	waitKind(t, e, v1.Project.Id, provision.KindRenameOpaque)
	renamed, err := store.New(e.DB).GetProject(ctx, v1.Project.Id)
	if err != nil {
		t.Fatal(err)
	}
	if !provision.IsOpaque(renamed.DbName) || renamed.AliasDbName == nil || *renamed.AliasDbName != "oldblog_k2f9" {
		t.Fatalf("after the rename: db %s alias %v", renamed.DbName, renamed.AliasDbName)
	}
	app = e.MustConnect(v1URL)
	var n int
	if err := app.QueryRow(ctx, `SELECT count(*) FROM posts`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the V1 URL after the rename: %v (%d rows)", err, n)
	}
	app.Close(ctx)
	var got gen.Project
	e.Do("GET", "/api/v1/projects/"+v1.Project.Id.String(), nil, &got)
	if got.DbName != "oldblog_k2f9" || got.CanSwitchCredentials == nil || !*got.CanSwitchCredentials {
		t.Fatalf("the project shows %s, can switch %v", got.DbName, got.CanSwitchCredentials)
	}

	// What another tenant sees: no project or org name in any catalog, and
	// no other sessions at all. The V1 owner role is the one exception
	// until its project switches.
	dbs := names(t, tenant, `SELECT datname FROM pg_database`)
	if hit := containsAny(dbs, "acme", "payroll", "oldblog", "bob"); len(hit) > 0 {
		t.Fatalf("pg_database shows %v", hit)
	}
	roles := names(t, tenant, `SELECT rolname FROM pg_roles`)
	if hit := containsAny(roles, "acme", "payroll", "bob"); len(hit) > 0 {
		t.Fatalf("pg_roles shows %v", hit)
	}
	if hit := containsAny(roles, "oldblog"); len(hit) != 1 || hit[0] != "oldblog_k2f9_owner" {
		t.Fatalf("before switching, only the V1 owner role is visible: %v", hit)
	}
	for _, q := range []string{`SELECT count(*) FROM pg_stat_activity`, `SELECT count(*) FROM pg_stat_get_activity(NULL)`} {
		var pe *pgconn.PgError
		if err := tenant.QueryRow(ctx, q).Scan(&n); !errors.As(err, &pe) || pe.Code != "42501" {
			t.Fatalf("%s as a tenant: %v", q, err)
		}
	}

	// Switch to opaque credentials: the new URL works at once, the old one
	// until the grace period ends.
	var sw gen.SwitchedCredentials
	if code := e.Do("POST", "/api/v1/projects/"+v1.Project.Id.String()+"/switch-credentials", map[string]int{"grace_days": 7}, &sw); code != http.StatusAccepted {
		t.Fatalf("switch credentials: %d", code)
	}
	waitKind(t, e, v1.Project.Id, provision.KindSwitchCredentials)
	if sw.Connection.Database != renamed.DbName || sw.Connection.User != renamed.DbName+"_owner" {
		t.Fatalf("new credentials: %+v", sw.Connection)
	}
	fresh := e.MustConnect(sw.Connection.PooledUrl)
	if err := fresh.QueryRow(ctx, `SELECT count(*) FROM posts`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the new URL: %v", err)
	}
	if _, err := fresh.Exec(ctx, `CREATE TABLE after_switch (id int)`); err != nil {
		t.Fatal(err)
	}
	fresh.Close(ctx)
	old := e.MustConnect(v1URL)
	if _, err := old.Exec(ctx, `INSERT INTO posts VALUES (2); CREATE TABLE by_v1_role (id int)`); err != nil {
		t.Fatalf("the V1 URL during the grace period: %v", err)
	}
	old.Close(ctx)

	e.TenancyAdvance(8 * 24 * time.Hour)
	if err := e.Tenancy.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	waitKind(t, e, v1.Project.Id, provision.KindExpireLegacy)
	if c, err := e.Connect(v1URL); err == nil {
		c.Close(ctx)
		t.Fatal("the V1 URL still works after the grace period")
	}
	fresh = e.MustConnect(sw.Connection.PooledUrl)
	if err := fresh.QueryRow(ctx, `SELECT count(*) FROM posts`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("the data the V1 role wrote: %v (%d)", err, n)
	}
	var owner string
	if err := fresh.QueryRow(ctx, `SELECT tableowner FROM pg_tables WHERE tablename = 'by_v1_role'`).Scan(&owner); err != nil || owner != sw.Connection.User {
		t.Fatalf("a table the V1 role made is owned by %q (%v)", owner, err)
	}
	fresh.Close(ctx)
	roles = names(t, tenant, `SELECT rolname FROM pg_roles`)
	if hit := containsAny(roles, "oldblog"); len(hit) > 0 {
		t.Fatalf("after switching, pg_roles still shows %v", hit)
	}
	// The old name no longer routes either: guessing it gets nothing.
	guess := strings.Replace(bobApp.Connection.PooledUrl, "/"+bobApp.Project.DbName+"?", "/oldblog_k2f9?", 1)
	if c, err := e.Connect(guess); err == nil {
		c.Close(ctx)
		t.Fatal("the V1 database name still routes")
	}
}

// setOverride sets one quota override on an organisation as the platform admin.
func setOverride(t *testing.T, e *testenv.Env, org uuid.UUID, limit string, value int64) {
	t.Helper()
	if code := e.Do("PATCH", "/api/v1/admin/orgs/"+org.String(), map[string]any{"limit_overrides": map[string]int64{limit: value}}, nil); code != http.StatusOK {
		t.Fatalf("set %s override: %d", limit, code)
	}
}

// measure samples sizes and applies storage limits, as the 5-minute loops do.
func measure(t *testing.T, e *testenv.Env) {
	t.Helper()
	if err := e.Metrics.CollectSizes(context.Background()); err != nil {
		t.Logf("collect: %v", err) // node metrics may be missing in tests
	}
	if err := e.Tenancy.EnforceStorage(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func storageState(t *testing.T, e *testenv.Env, id uuid.UUID) string {
	t.Helper()
	var st gen.ProjectStorage
	if code := e.Do("GET", "/api/v1/projects/"+id.String()+"/storage", nil, &st); code != http.StatusOK {
		t.Fatalf("storage: %d", code)
	}
	return string(st.State)
}

func consoleSQL(t *testing.T, e *testenv.Env, id uuid.UUID, sql string) (int, gen.SqlResult) {
	t.Helper()
	var out gen.SqlResult
	code := e.Do("POST", "/api/v1/projects/"+id.String()+"/sql", map[string]any{"query": sql, "query_id": uuid.New()}, &out)
	return code, out
}

// TestStorageLimitLocks is M9's first done-when (V2 §10.4): a tenant that
// fills its database is soft-locked at 100% and hard-locked at 120% while
// its SQL console still works, and the locks lift once it is back under.
func TestStorageLimitLocks(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	c := e.CreateProject("Filler")
	id := c.Project.Id
	app := e.MustConnect(c.Connection.SessionUrl)
	if _, err := app.Exec(ctx, `CREATE TABLE fill (id serial PRIMARY KEY, pad text)`); err != nil {
		t.Fatal(err)
	}
	admin := e.SharedAdmin("postgres")
	size := func() int64 {
		var n int64
		if err := admin.QueryRow(ctx, `SELECT pg_database_size($1)`, c.Project.DbName).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// A limit 16 MB above the empty database.
	limitMB := size()>>20 + 16
	limit := limitMB << 20
	setOverride(t, e, e.OrgID, "project_storage_mb", limitMB)
	fillTo := func(target int64) {
		t.Helper()
		for size() < target {
			// About 100 kB a step, so a target is not overshot by much.
			if _, err := app.Exec(ctx, `INSERT INTO fill (pad) SELECT repeat(md5(g::text), 4) FROM generate_series(1, 400) g`); err != nil {
				t.Fatalf("fill: %v", err)
			}
		}
	}

	measure(t, e)
	if st := storageState(t, e, id); st != "none" {
		t.Fatalf("an empty project is %s", st)
	}
	fillTo(limit * 92 / 100)
	measure(t, e)
	if st := storageState(t, e, id); st != "warn" {
		t.Fatalf("at 92%%: %s", st)
	}
	if e.SMTP.Count("owner@example.com", "of its storage limit") == 0 {
		t.Error("no 90% warning email")
	}

	// 100%: read-only by default. Writes fail; deletes in a read-write
	// transaction and the console still work.
	fillTo(limit + limit/50)
	measure(t, e)
	if st := storageState(t, e, id); st != "soft" {
		t.Fatalf("at 102%%: %s", st)
	}
	soft := e.MustConnect(c.Connection.PooledUrl)
	if _, err := soft.Exec(ctx, `INSERT INTO fill (pad) VALUES ('x')`); err == nil || !strings.Contains(err.Error(), "read-only transaction") {
		t.Fatalf("a write while soft-locked: %v", err)
	}
	tx, err := soft.Begin(ctx)
	if err == nil {
		_, err = tx.Exec(ctx, `SET TRANSACTION READ WRITE; DELETE FROM fill WHERE id = 1`)
		_ = tx.Rollback(ctx)
	}
	if err != nil {
		t.Fatalf("an explicit read-write transaction while soft-locked: %v", err)
	}
	if code, out := consoleSQL(t, e, id, `INSERT INTO fill (pad) VALUES ('console')`); code != http.StatusOK || out.Error != nil {
		t.Fatalf("the console while soft-locked: %d %+v", code, out)
	}

	// 120%: logins are off; the console still works, so the team can
	// delete data and reclaim the space.
	app.Close(ctx)
	// A soft lock is soft: a tenant can still write explicitly, and keeps
	// filling the database.
	writer := e.MustConnect(c.Connection.SessionUrl)
	if _, err := writer.Exec(ctx, `SET default_transaction_read_only = off`); err != nil {
		t.Fatal(err)
	}
	for size() < limit*122/100 {
		if _, err := writer.Exec(ctx, `INSERT INTO fill (pad) SELECT repeat(md5(g::text), 16) FROM generate_series(1, 2000) g`); err != nil {
			t.Fatalf("fill past 120%%: %v", err)
		}
	}
	measure(t, e)
	if st := storageState(t, e, id); st != "hard" {
		t.Fatalf("at 122%%: %s", st)
	}
	if conn, err := e.Connect(c.Connection.PooledUrl); err == nil {
		err = conn.Ping(ctx)
		conn.Close(ctx)
		if err == nil {
			t.Fatal("an app connected while hard-locked")
		}
	}
	if err := writer.Ping(ctx); err == nil {
		t.Fatal("an existing session survived the hard lock")
	}
	if e.SMTP.Count("owner@example.com", "offline: storage limit exceeded") == 0 {
		t.Error("no hard-lock email")
	}
	if code, out := consoleSQL(t, e, id, `DELETE FROM fill`); code != http.StatusOK || out.Error != nil {
		t.Fatalf("the console while hard-locked: %d %+v", code, out)
	}
	var st gen.ProjectStorage
	e.Do("GET", "/api/v1/projects/"+id.String()+"/storage", nil, &st)
	if len(st.Tables) == 0 || st.Tables[0].Table != "fill" || st.LimitBytes == nil || *st.LimitBytes != limit {
		t.Fatalf("storage view: %+v", st)
	}
	var op gen.Operation
	if code := e.Do("POST", "/api/v1/projects/"+id.String()+"/reclaim-space", map[string]string{"schema": "public", "table": "fill"}, &op); code != http.StatusAccepted {
		t.Fatalf("reclaim space: %d", code)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("reclaim space:\n%s", testenv.FormatLog(op))
	}
	measure(t, e)
	if st := storageState(t, e, id); st != "none" {
		t.Fatalf("after reclaiming: %s (%d of %d bytes)", st, size(), limit)
	}
	// The poolers retry a login that failed within server_login_retry (2 s).
	deadline := time.Now().Add(10 * time.Second)
	for {
		back, err := e.Connect(c.Connection.PooledUrl)
		if err == nil {
			_, err = back.Exec(ctx, `INSERT INTO fill (pad) VALUES ('writable again')`)
			back.Close(ctx)
		}
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a write after the locks lifted: %v", err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}
