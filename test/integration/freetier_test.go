package integration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/freetier"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/internal/waker"
	"github.com/israel-duff/pgdock/test/testenv"
)

// lifecycle reads a project's lifecycle.
func lifecycle(t *testing.T, e *testenv.Env, p gen.Project) store.Project {
	t.Helper()
	row, err := store.New(e.DB).GetProject(context.Background(), p.Id)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

// untilConnects retries the pooled URL as a client would, returning the
// errors seen before the connection worked.
func untilConnects(t *testing.T, e *testenv.Env, url string, within time.Duration) (*pgx.Conn, []string) {
	t.Helper()
	var seen []string
	deadline := time.Now().Add(within)
	for {
		// In transaction mode the pooler completes the login itself; the
		// backend (here, the waker) answers the first query.
		c, err := e.Connect(url)
		if err == nil {
			if _, err = c.Exec(context.Background(), "SELECT 1"); err == nil {
				t.Cleanup(func() { _ = c.Close(context.Background()) })
				return c, seen
			}
			_ = c.Close(context.Background())
		}
		msg := err.Error()
		var pe *pgconn.PgError
		if errors.As(err, &pe) {
			msg = pe.Message
		}
		if len(seen) == 0 || seen[len(seen)-1] != msg {
			seen = append(seen, msg)
		}
		if time.Now().After(deadline) {
			t.Fatalf("no connection within %s; saw %q", within, seen)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// TestFreeProjectPausesAndArchives is M22's done-when: a paused Free
// project resumes by itself on the first connection attempt, which gets
// the waker's message through the real poolers; an archived one restores
// from its archive the same way.
func TestFreeProjectPausesAndArchives(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	e.ConfigureBackups()
	ctx := context.Background()
	q := store.New(e.DB)

	c := e.CreateProject("Hobby")
	app := e.MustConnect(c.Connection.PooledUrl)
	if _, err := app.Exec(ctx, `CREATE TABLE notes (id serial PRIMARY KEY, body text); INSERT INTO notes (body) SELECT 'note ' || g FROM generate_series(1, 250) g`); err != nil {
		t.Fatal(err)
	}
	_ = app.Close(ctx)

	// A week without clients: warned, then (24 hours on) paused.
	if _, err := e.DB.Exec(ctx, `UPDATE projects SET last_active_at = now() - interval '8 days' WHERE id = $1`, c.Project.Id); err != nil {
		t.Fatal(err)
	}
	if sw, err := e.FreeTier.Sweep(ctx); err != nil || sw.Warned != 1 || sw.Paused != 0 {
		t.Fatalf("first sweep: %+v %v", sw, err)
	}
	if n := e.SMTP.Count(testenv.OwnerEmail, "will be paused"); n != 1 {
		t.Errorf("pause warnings: %d", n)
	}
	if _, err := e.DB.Exec(ctx, `UPDATE projects SET pause_warned_at = now() - interval '25 hours' WHERE id = $1`, c.Project.Id); err != nil {
		t.Fatal(err)
	}
	sw, err := e.FreeTier.Sweep(ctx)
	if err != nil || sw.Paused != 1 {
		t.Fatalf("second sweep: %+v %v", sw, err)
	}
	awaitPay(t, "the pause", func() bool { return lifecycle(t, e, c.Project).Lifecycle == freetier.Paused && !busy(t, e, c.Project) })
	var allowed bool
	if err := e.SharedAdmin("postgres").QueryRow(ctx, `SELECT datallowconn FROM pg_database WHERE datname = $1`, c.Project.DbName).Scan(&allowed); err != nil || allowed {
		t.Fatalf("a paused database allows connections: %v %v", allowed, err)
	}

	// The first connection gets the waker's message and wakes the project;
	// a retry gets in, with the data there.
	conn, seen := untilConnects(t, e, c.Connection.PooledUrl, 60*time.Second)
	if len(seen) == 0 || !strings.Contains(seen[0], waker.MsgResuming) {
		t.Fatalf("the first attempt's error: %q", seen)
	}
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM notes`).Scan(&n); err != nil || n != 250 {
		t.Fatalf("after resuming: %d %v", n, err)
	}
	if p := lifecycle(t, e, c.Project); p.Lifecycle != freetier.Active || p.PausedAt != nil {
		t.Errorf("after resuming: %s %v", p.Lifecycle, p.PausedAt)
	}
	_ = conn.Close(ctx)

	// Paused again, then 90 days on: archived (a verified backup, then the
	// database is dropped).
	if _, err := e.DB.Exec(ctx, `UPDATE projects SET last_active_at = now() - interval '8 days', pause_warned_at = now() - interval '2 days' WHERE id = $1`, c.Project.Id); err != nil {
		t.Fatal(err)
	}
	if sw, err := e.FreeTier.Sweep(ctx); err != nil || sw.Paused != 1 {
		t.Fatalf("pause again: %+v %v", sw, err)
	}
	awaitPay(t, "the second pause", func() bool { return lifecycle(t, e, c.Project).Lifecycle == freetier.Paused && !busy(t, e, c.Project) })
	if _, err := e.DB.Exec(ctx, `UPDATE projects SET paused_at = now() - interval '91 days' WHERE id = $1`, c.Project.Id); err != nil {
		t.Fatal(err)
	}
	if sw, err := e.FreeTier.Sweep(ctx); err != nil || sw.Archived != 1 {
		t.Fatalf("archive sweep: %+v %v", sw, err)
	}
	awaitArchive := time.Now().Add(2 * time.Minute)
	for lifecycle(t, e, c.Project).Lifecycle != freetier.Archived || busy(t, e, c.Project) {
		if time.Now().After(awaitArchive) {
			t.Fatalf("not archived: %+v", lifecycle(t, e, c.Project))
		}
		time.Sleep(200 * time.Millisecond)
	}
	p := lifecycle(t, e, c.Project)
	if p.ArchiveBackupID == nil {
		t.Fatal("no archive backup")
	}
	if b, err := q.GetBackup(ctx, *p.ArchiveBackupID); err != nil || b.Kind != "archive" || b.Status != "succeeded" {
		t.Fatalf("archive backup: %+v %v", b, err)
	}
	var exists bool
	if err := e.SharedAdmin("postgres").QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, c.Project.DbName).Scan(&exists); err != nil || exists {
		t.Fatalf("the archived database is still there: %v %v", exists, err)
	}
	if n := e.SMTP.Count(testenv.OwnerEmail, "[PGDock] Hobby is archived"); n != 1 {
		t.Errorf("archive notices: %d", n)
	}

	// The same connection string restores it from the archive.
	conn, seen = untilConnects(t, e, c.Connection.PooledUrl, 3*time.Minute)
	if len(seen) == 0 || !strings.Contains(seen[0], "restored from its archive") {
		t.Fatalf("the first attempt's error: %q", seen)
	}
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM notes`).Scan(&n); err != nil || n != 250 {
		t.Fatalf("after restoring: %d %v", n, err)
	}
	if p := lifecycle(t, e, c.Project); p.Lifecycle != freetier.Active || p.ArchivedAt != nil {
		t.Errorf("after restoring: %s %v", p.Lifecycle, p.ArchivedAt)
	}
}

func busy(t *testing.T, e *testenv.Env, p gen.Project) bool {
	t.Helper()
	b, err := store.New(e.DB).ProjectHasActiveOperation(context.Background(), &p.Id)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
