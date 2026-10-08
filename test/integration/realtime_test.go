package integration

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/test/testenv"
)

// realtimeProject is a project with backend services, two users, and a
// todos table each user sees only their own rows of, with realtime on.
type realtimeProject struct {
	e          *testenv.Env
	ed         *testenv.Edge
	ref        string
	pid        uuid.UUID
	db         string
	pub, sec   string
	owner      string // the project owner's pooled URL
	alice, bob string
	aliceID    uuid.UUID
	bobID      uuid.UUID
}

func newRealtimeProject(t *testing.T, name string) *realtimeProject {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	creds := e.CreateProject(name)
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
	rp := &realtimeProject{e: e, ref: *en.Services.Ref, pid: pid, db: p.DbName, owner: creds.Connection.PooledUrl}
	for _, k := range en.Keys {
		if k.Key.Kind == gen.ApiKeyKindPublishable {
			rp.pub = k.Value
		} else {
			rp.sec = k.Value
		}
	}
	rp.ed = e.StartEdge()
	waitFor(t, 30*time.Second, "the edge reaches the project's database", func() bool {
		code, _, _ := rp.ed.Do(rp.ref, "GET", "/data/v1/health", nil, "apikey", rp.pub)
		return code == 200
	})
	api := authAPI{t: t, ed: rp.ed, ref: rp.ref, key: rp.sec}
	user := func(email string) (string, uuid.UUID) {
		if r := api.post("/auth/v1/admin/users", fmt.Sprintf(`{"email":%q,"password":"password-123456","email_confirm":true}`, email)); r.Code != 201 {
			t.Fatalf("create %s: %d %s", email, r.Code, r.Body)
		}
		r := authAPI{t: t, ed: rp.ed, ref: rp.ref, key: rp.pub}.post("/auth/v1/signin/password", fmt.Sprintf(`{"email":%q,"password":"password-123456"}`, email))
		if r.Code != 200 {
			t.Fatalf("sign in %s: %d %s", email, r.Code, r.Body)
		}
		return r.Session.AccessToken, r.Session.User.ID
	}
	rp.alice, rp.aliceID = user("alice@example.com")
	rp.bob, rp.bobID = user("bob@example.com")
	app := rp.ownerConn(t)
	defer app.Close(ctx)
	for _, st := range []string{
		`CREATE TABLE todos (id serial PRIMARY KEY, owner uuid NOT NULL, body text NOT NULL, done boolean NOT NULL DEFAULT false)`,
		`ALTER TABLE todos ENABLE ROW LEVEL SECURITY`,
		fmt.Sprintf(`CREATE POLICY own_todos ON todos FOR ALL TO %q USING (owner = pgd_auth.uid()) WITH CHECK (owner = pgd_auth.uid())`,
			store.UserRole(p.DbName)),
		`SELECT pgd_realtime.enable('todos')`,
	} {
		if _, err := app.Exec(ctx, st); err != nil {
			t.Fatalf("%s: %v", st, err)
		}
	}
	return rp
}

func (rp *realtimeProject) ownerConn(t *testing.T) *pgx.Conn {
	t.Helper()
	return rp.e.MustConnect(rp.owner)
}

// TestRealtimeCapture: enabling a table captures its committed changes in
// the outbox, and a rolled-back change leaves nothing.
func TestRealtimeCapture(t *testing.T) {
	rp := newRealtimeProject(t, "rt-capture")
	ctx := context.Background()
	app := rp.ownerConn(t)
	defer app.Close(ctx)

	if _, err := app.Exec(ctx, `SELECT pgd_realtime.enable('pgd_realtime.outbox')`); err == nil {
		t.Fatal("enabled realtime on a platform table")
	}
	if _, err := app.Exec(ctx, `CREATE TABLE nokey (x int)`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Exec(ctx, `SELECT pgd_realtime.enable('nokey')`); err == nil {
		t.Fatal("enabled realtime on a table without a primary key")
	}
	tx, err := app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO todos (owner, body) VALUES ($1, 'rolled back')`, rp.aliceID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Exec(ctx, `INSERT INTO todos (owner, body) VALUES ($1, 'kept')`, rp.aliceID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Exec(ctx, `UPDATE todos SET done = true WHERE body = 'kept'`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Exec(ctx, `DELETE FROM todos WHERE body = 'kept'`); err != nil {
		t.Fatal(err)
	}
	p, err := store.New(rp.e.DB).GetProject(ctx, rp.pid)
	if err != nil {
		t.Fatal(err)
	}
	adm, err := rp.e.Service.AdminConn(ctx, p.InstanceID, p.DbName)
	if err != nil {
		t.Fatal(err)
	}
	defer adm.Close(ctx)
	rows, err := adm.Query(ctx, `SELECT op, coalesce(record ->> 'body', ''), coalesce(old_record::text, '-') FROM pgd_realtime.outbox ORDER BY xid, id`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var op, body, old string
		if err := rows.Scan(&op, &body, &old); err != nil {
			t.Fatal(err)
		}
		got = append(got, op+" "+body+" "+old)
	}
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	want := []string{`INSERT kept -`, `UPDATE kept {"id": 2}`, `DELETE  {"id": 2}`}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("outbox: %q, want %q", got, want)
	}
	if _, err := app.Exec(ctx, `SELECT pgd_realtime.disable('todos')`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Exec(ctx, `INSERT INTO todos (owner, body) VALUES ($1, 'after')`, rp.aliceID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := adm.QueryRow(ctx, `SELECT count(*) FROM pgd_realtime.outbox WHERE record ->> 'body' = 'after'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("captured after disable: %d %v", n, err)
	}
}
