package integration

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/test/testenv"
)

// writer inserts numbered rows through one URL until stopped, reconnecting
// after errors, and remembers every commit the server acknowledged.
type writer struct {
	name  string
	url   string
	acked []int
	errs  []string
	n     atomic.Int64
}

func (w *writer) run(ctx context.Context, e *testenv.Env, wg *sync.WaitGroup) {
	defer wg.Done()
	var conn *pgx.Conn
	defer func() {
		if conn != nil {
			_ = conn.Close(context.Background())
		}
	}()
	for i := 1; ctx.Err() == nil; i++ {
		if conn == nil {
			c, err := e.Connect(w.url)
			if err != nil {
				w.errs = append(w.errs, "connect: "+err.Error())
				time.Sleep(100 * time.Millisecond)
				continue
			}
			conn = c
		}
		qctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		_, err := conn.Exec(qctx, `INSERT INTO events (writer, n) VALUES ($1, $2)`, w.name, i)
		cancel()
		if err != nil {
			w.errs = append(w.errs, err.Error())
			_ = conn.Close(context.Background())
			conn = nil
			time.Sleep(50 * time.Millisecond)
			continue
		}
		w.acked = append(w.acked, i)
		w.n.Add(1)
		time.Sleep(5 * time.Millisecond)
	}
}

func seedHobby(t *testing.T, e *testenv.Env, url string) {
	t.Helper()
	ctx := context.Background()
	app := e.MustConnect(url)
	for _, stmt := range []string{
		`CREATE TABLE events (id bigserial PRIMARY KEY, writer text NOT NULL, n int NOT NULL, at timestamptz NOT NULL DEFAULT now(), UNIQUE (writer, n))`,
		`CREATE TABLE notes (id serial PRIMARY KEY, body text NOT NULL)`,
		`INSERT INTO notes (body) SELECT 'note ' || g FROM generate_series(1, 2000) g`,
		`CREATE INDEX notes_body ON notes (body)`,
		`CREATE VIEW recent AS SELECT * FROM events ORDER BY id DESC LIMIT 10`,
		`CREATE FUNCTION note_count() RETURNS bigint LANGUAGE sql AS 'SELECT count(*) FROM notes'`,
		`CREATE SCHEMA app`,
		`CREATE TABLE app.settings (k text PRIMARY KEY, v text)`,
		`INSERT INTO app.settings VALUES ('theme', 'dark')`,
	} {
		if _, err := app.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

// TestPromotionLiveWriter is the M5 done-when: a hobby project is promoted
// to a dedicated instance while clients write through both poolers; the
// URL does not change and no acknowledged commit is lost.
func TestPromotionLiveWriter(t *testing.T) {
	needDedicated(t)
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	e.ConfigureBackups()
	ctx := context.Background()

	c := e.CreateProject("Hobby")
	seedHobby(t, e, c.Connection.PooledUrl)
	// The console has been used on the shared tier (its login exists there).
	if got := onlyValue(t, runSQL(t, e, c.Project.Id.String(), "SELECT count(*) FROM notes")); got != "2000" {
		t.Fatalf("console before promotion: %q", got)
	}

	var est gen.PromotionEstimate
	if code := e.Do("GET", "/api/v1/projects/"+c.Project.Id.String()+"/promote", nil, &est); code != http.StatusOK || est.SizeBytes == 0 || est.EstimatedDowntimeSeconds < 5 {
		t.Fatalf("estimate: %d %+v", code, est)
	}

	// Live writers: an app through the transaction pooler, a worker through
	// the session pooler.
	wctx, stop := context.WithCancel(ctx)
	var wg sync.WaitGroup
	tx := &writer{name: "tx", url: c.Connection.PooledUrl}
	session := &writer{name: "session", url: c.Connection.SessionUrl}
	wg.Add(2)
	go tx.run(wctx, e, &wg)
	go session.run(wctx, e, &wg)
	deadline := time.Now().Add(10 * time.Second)
	for tx.n.Load() < 20 || session.n.Load() < 20 {
		if time.Now().After(deadline) {
			t.Fatalf("writers did not start: tx %d session %d (%v %v)", tx.n.Load(), session.n.Load(), tx.errs, session.errs)
		}
		time.Sleep(50 * time.Millisecond)
	}

	profile, vol := "small", 5
	var op gen.Operation
	if code := e.Do("POST", "/api/v1/projects/"+c.Project.Id.String()+"/promote", gen.PromoteRequest{Profile: &profile, VolumeGb: &vol}, &op); code != http.StatusAccepted {
		t.Fatalf("promote: %d", code)
	}
	var p gen.Project
	e.Do("GET", "/api/v1/projects/"+c.Project.Id.String(), nil, &p)
	if p.Status != gen.ProjectStatusPromoting && p.Status != gen.ProjectStatusActive {
		t.Fatalf("status during promotion: %s", p.Status)
	}
	if code := e.Do("POST", "/api/v1/projects/"+c.Project.Id.String()+"/rotate-password", nil, nil); code != http.StatusConflict {
		t.Fatalf("rotate during promotion: %d, want 409", code)
	}
	op = e.WaitOperation(op.Id)
	log := testenv.FormatLog(op)
	if op.Status != gen.OperationStatusSucceeded {
		stop()
		wg.Wait()
		t.Fatalf("promote: %s %s\n%s", op.Status, deref(op.Error), log)
	}
	// Keep writing on the new backend for a moment.
	before := tx.n.Load()
	deadline = time.Now().Add(15 * time.Second)
	for tx.n.Load() < before+20 {
		if time.Now().After(deadline) {
			t.Fatalf("tx writer stalled after promotion: %v", tx.errs)
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop()
	wg.Wait()
	t.Logf("promotion log:\n%s", log)
	t.Logf("tx writer: %d commits, %d errors %v; session writer: %d commits, %d errors %v",
		len(tx.acked), len(tx.errs), tx.errs, len(session.acked), len(session.errs), session.errs)
	for _, want := range []string{"writes frozen", "verified:", "route switched", "writes were frozen for", "base backup base_"} {
		if !strings.Contains(log, want) {
			t.Errorf("promotion log lacks %q", want)
		}
	}

	// Same URL, now served by the dedicated instance.
	app := e.MustConnect(c.Connection.PooledUrl)
	var archive string
	if err := app.QueryRow(ctx, `SELECT current_setting('archive_mode')`).Scan(&archive); err != nil || archive != "on" {
		t.Fatalf("the URL does not reach the dedicated instance: archive_mode %q %v", archive, err)
	}
	// No acknowledged commit is lost, on either pooler.
	for _, w := range []*writer{tx, session} {
		rows, err := app.Query(ctx, `SELECT n FROM events WHERE writer = $1`, w.name)
		if err != nil {
			t.Fatal(err)
		}
		have, err := pgx.CollectRows(rows, pgx.RowTo[int32])
		if err != nil {
			t.Fatal(err)
		}
		set := map[int32]bool{}
		for _, n := range have {
			set[n] = true
		}
		var lost []int
		for _, n := range w.acked {
			if !set[int32(n)] {
				lost = append(lost, n)
			}
		}
		if len(lost) > 0 {
			t.Fatalf("%s writer: %d acknowledged commit(s) lost: %v", w.name, len(lost), lost)
		}
	}
	// App traffic through the transaction pooler waited out the freeze.
	if len(tx.errs) > 0 {
		t.Errorf("the transaction-mode writer saw errors: %v", tx.errs)
	}
	var notes, fn int
	var theme string
	if err := app.QueryRow(ctx, `SELECT (SELECT count(*) FROM notes), note_count(), (SELECT v FROM app.settings WHERE k = 'theme')`).Scan(&notes, &fn, &theme); err != nil ||
		notes != 2000 || fn != 2000 || theme != "dark" {
		t.Fatalf("copied objects: %d %d %q %v", notes, fn, theme, err)
	}
	var owner string
	if err := app.QueryRow(ctx, `SELECT tableowner FROM pg_tables WHERE tablename = 'events'`).Scan(&owner); err != nil || owner != c.Project.OwnerRole {
		t.Fatalf("owner after promotion: %q %v", owner, err)
	}

	e.Do("GET", "/api/v1/projects/"+c.Project.Id.String(), nil, &p)
	if p.Tier != gen.ProjectTierDedicated || p.Status != gen.ProjectStatusActive || p.Instance == nil || p.Instance.Kind != "dedicated" ||
		p.Connection.PooledUrl != c.Project.Connection.PooledUrl || !p.Settings.ConsoleReadOnly || p.Settings.PoolSize != 20 ||
		p.RetiredCopyUntil == nil || time.Until(*p.RetiredCopyUntil) < 47*time.Hour {
		t.Fatalf("project after promotion: %+v", p)
	}

	// The console follows the project to its instance, read-only now.
	r := runSQL(t, e, c.Project.Id.String(), "SELECT count(*) FROM notes")
	if got := onlyValue(t, r); got != "2000" || !r.ReadOnly {
		t.Fatalf("console after promotion: %q read-only %v", got, r.ReadOnly)
	}
	var sc gen.DbSchema
	if code := e.Do("GET", "/api/v1/projects/"+c.Project.Id.String()+"/schema", nil, &sc); code != http.StatusOK || len(sc.Schemas) < 2 {
		t.Fatalf("schema after promotion: %d %+v", code, sc)
	}

	// The shared copy stays, read-only, and the role cannot log in there.
	admin := e.SharedAdmin(c.Project.DbName)
	if _, err := admin.Exec(ctx, `INSERT INTO notes (body) VALUES ('late')`); err == nil {
		t.Fatal("the retired shared copy accepted a write")
	}
	var canLogin bool
	if err := admin.QueryRow(ctx, `SELECT rolcanlogin FROM pg_roles WHERE rolname = $1`, c.Project.OwnerRole).Scan(&canLogin); err != nil || canLogin {
		t.Fatalf("the shared role can still log in: %v %v", canLogin, err)
	}
	_ = admin.Close(ctx)

	// After 48 hours it is dropped.
	if _, err := e.DB.Exec(ctx, `UPDATE retired_databases SET drop_after = now() - interval '1 minute' WHERE project_id = $1`, c.Project.Id); err != nil {
		t.Fatal(err)
	}
	if err := e.Dedicated.DropRetired(ctx); err != nil {
		t.Fatal(err)
	}
	pg := e.SharedAdmin("postgres")
	var exists bool
	if err := pg.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)
		OR EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $2)`, c.Project.DbName, provision.ConsoleRole(c.Project.DbName)).Scan(&exists); err != nil || exists {
		t.Fatalf("retired copy or its console login not dropped: %v %v", exists, err)
	}
	p = gen.Project{}
	e.Do("GET", "/api/v1/projects/"+c.Project.Id.String(), nil, &p)
	if p.RetiredCopyUntil != nil {
		t.Fatal("retired_copy_until still set after the drop")
	}
	// Dedicated projects cannot be promoted again.
	if code := e.Do("POST", "/api/v1/projects/"+c.Project.Id.String()+"/promote", gen.PromoteRequest{}, nil); code != http.StatusBadRequest {
		t.Fatalf("promote a dedicated project: %d", code)
	}
}

// TestPromotionRollback fails a promotion after the freeze: the project
// stays on the shared tier, writable, with nothing lost, and the new
// instance is removed.
func TestPromotionRollback(t *testing.T) {
	needDedicated(t)
	e := testenv.Start(t, testenv.Options{AfterFreeze: func(context.Context) error {
		return errors.New("injected failure after the freeze")
	}})
	e.StartAgent()
	e.ConfigureBackups()
	ctx := context.Background()
	c := e.CreateProject("Hobby rollback")
	seedHobby(t, e, c.Connection.PooledUrl)

	var op gen.Operation
	if code := e.Do("POST", "/api/v1/projects/"+c.Project.Id.String()+"/promote", gen.PromoteRequest{}, &op); code != http.StatusAccepted {
		t.Fatalf("promote: %d", code)
	}
	op = e.WaitOperation(op.Id)
	if op.Status != gen.OperationStatusFailed || !strings.Contains(testenv.FormatLog(op), "promotion rolled back") {
		t.Fatalf("promote: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	var p gen.Project
	e.Do("GET", "/api/v1/projects/"+c.Project.Id.String(), nil, &p)
	if p.Tier != gen.ProjectTierShared || p.Status != gen.ProjectStatusActive {
		t.Fatalf("project after rollback: %s %s", p.Tier, p.Status)
	}
	// Writable again through both poolers, on the shared cluster.
	for _, u := range []string{c.Connection.PooledUrl, c.Connection.SessionUrl} {
		conn, err := e.Connect(u)
		if err != nil {
			t.Fatalf("connect after rollback: %v", err)
		}
		var archive string
		if _, err := conn.Exec(ctx, `INSERT INTO notes (body) VALUES ('after rollback')`); err != nil {
			t.Fatalf("write after rollback: %v", err)
		}
		if err := conn.QueryRow(ctx, `SELECT current_setting('archive_mode')`).Scan(&archive); err != nil || archive == "on" {
			t.Fatalf("not on the shared cluster after rollback: %q %v", archive, err)
		}
		_ = conn.Close(ctx)
	}
	var instances int
	if err := e.DB.QueryRow(ctx, `SELECT count(*) FROM instances WHERE kind = 'dedicated' AND deleted_at IS NULL`).Scan(&instances); err != nil || instances != 0 {
		t.Fatalf("dedicated instances left after rollback: %d %v", instances, err)
	}
}
