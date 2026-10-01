package integration

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/backup"
	"github.com/israel-duff/pgdock/internal/jobs"
	"github.com/israel-duff/pgdock/internal/storage"
	"github.com/israel-duff/pgdock/test/testenv"
)

// TestBackupDeleteRestoreVerify is the M3 done-when, minus the Supabase
// half (see imports_test.go): back up, delete data, restore into a new
// project and verify it, then restore in place.
func TestBackupDeleteRestoreVerify(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	e.ConfigureBackups()
	ctx := context.Background()

	c := e.CreateProject("Shop")
	p := c.Project
	app := e.MustConnect(c.Connection.PooledUrl)
	for _, stmt := range []string{
		`CREATE TABLE items (id bigserial PRIMARY KEY, name text NOT NULL, price numeric(10,2))`,
		`INSERT INTO items (name, price) SELECT 'item ' || g, g * 1.5 FROM generate_series(1, 500) g`,
		`CREATE VIEW cheap AS SELECT * FROM items WHERE price < 10`,
		`CREATE FUNCTION item_count() RETURNS bigint LANGUAGE sql AS 'SELECT count(*) FROM items'`,
	} {
		if _, err := app.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	// Back up now.
	var op gen.Operation
	if code := e.Do("POST", "/api/v1/projects/"+p.Id.String()+"/backups", nil, &op); code != http.StatusAccepted {
		t.Fatalf("backup now: status %d", code)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("backup: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	var list gen.BackupList
	if code := e.Do("GET", "/api/v1/backups?project_id="+p.Id.String(), nil, &list); code != http.StatusOK || len(list.Items) != 1 {
		t.Fatalf("list backups: %d %+v", code, list)
	}
	b := list.Items[0]
	if b.Status != gen.BackupStatusSucceeded || b.Kind != gen.Logical || b.SizeBytes == nil || *b.SizeBytes == 0 || b.Checksum == nil {
		t.Fatalf("backup row: %+v", b)
	}
	// The object is in the bucket under projects/<id>/logical/, encrypted.
	keys, err := e.S3.Objects("pgdock-test")
	if err != nil || len(keys) != 1 || !strings.HasPrefix(keys[0], "pgdock/projects/"+p.Id.String()+"/logical/") {
		t.Fatalf("bucket objects: %v %v", keys, err)
	}
	raw := downloadRaw(t, e, keys[0])
	if !strings.HasPrefix(raw, "PGDKBK1\n") || strings.Contains(raw, "PGDMP") || strings.Contains(raw, "item 42") {
		t.Fatal("backup object is not encrypted")
	}
	var proj gen.Project
	e.Do("GET", "/api/v1/projects/"+p.Id.String(), nil, &proj)
	if proj.LastBackupAt == nil || time.Since(*proj.LastBackupAt) > time.Minute {
		t.Fatalf("last_backup_at: %v", proj.LastBackupAt)
	}

	// Delete data.
	if _, err := app.Exec(ctx, `DELETE FROM items WHERE id > 10`); err != nil {
		t.Fatal(err)
	}

	// Restore into a new project and verify it.
	var rr gen.RestoreResponse
	mode := gen.New
	name := "Shop restored"
	if code := e.Do("POST", "/api/v1/backups/"+b.Id.String()+"/restore", gen.RestoreRequest{Mode: &mode, Name: &name}, &rr); code != http.StatusAccepted || rr.Credentials == nil {
		t.Fatalf("restore to new: status %d", code)
	}
	if op = e.WaitOperation(rr.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("restore to new: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	restored := e.MustConnect(rr.Credentials.Connection.PooledUrl)
	verifyShop(t, restored, 500)
	var owner string
	if err := restored.QueryRow(ctx, `SELECT tableowner FROM pg_tables WHERE tablename = 'items'`).Scan(&owner); err != nil || owner != rr.Credentials.Project.OwnerRole {
		t.Fatalf("restored objects owned by %q, want %s (%v)", owner, rr.Credentials.Project.OwnerRole, err)
	}
	// The new project works like any other: writes continue the sequence.
	var id int64
	if err := restored.QueryRow(ctx, `INSERT INTO items (name) VALUES ('new') RETURNING id`).Scan(&id); err != nil || id != 501 {
		t.Fatalf("insert after restore: id %d %v", id, err)
	}
	// The original project is untouched.
	var n int
	if err := app.QueryRow(ctx, `SELECT count(*) FROM items`).Scan(&n); err != nil || n != 10 {
		t.Fatalf("original after restore-to-new: %d rows %v", n, err)
	}

	// In place: needs re-auth and the typed name.
	inPlace := gen.InPlace
	confirm := "Shop"
	var e2 gen.Error
	e.Advance(11 * time.Minute) // sign-in counts as a fresh authentication
	if code := e.Do("POST", "/api/v1/backups/"+b.Id.String()+"/restore", gen.RestoreRequest{Mode: &inPlace, Confirm: &confirm}, &e2); code != http.StatusForbidden || e2.Code != "reauth_required" {
		t.Fatalf("in-place without reauth: %d %+v", code, e2)
	}
	e.Reauth()
	wrong := "shop"
	if code := e.Do("POST", "/api/v1/backups/"+b.Id.String()+"/restore", gen.RestoreRequest{Mode: &inPlace, Confirm: &wrong}, nil); code != http.StatusBadRequest {
		t.Fatalf("in-place with the wrong name: %d", code)
	}
	restoredProject := rr.Credentials.Project.Id
	rr = gen.RestoreResponse{}
	if code := e.Do("POST", "/api/v1/backups/"+b.Id.String()+"/restore", gen.RestoreRequest{Mode: &inPlace, Confirm: &confirm}, &rr); code != http.StatusAccepted || rr.Credentials != nil {
		t.Fatalf("in-place restore: status %d", code)
	}
	if op = e.WaitOperation(rr.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("in-place restore: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	// Same password, same URL, data back.
	again := e.MustConnect(c.Connection.PooledUrl)
	verifyShop(t, again, 500)
	e.Do("GET", "/api/v1/projects/"+p.Id.String(), nil, &proj)
	if proj.Status != gen.ProjectStatusActive {
		t.Fatalf("project after in-place restore: %s", proj.Status)
	}
	// A safety backup of the pre-restore state (10 rows) was taken first.
	safety := gen.Safety
	e.Do("GET", "/api/v1/backups?kind="+string(safety), nil, &list)
	if len(list.Items) != 1 || list.Items[0].ExpiresAt == nil || list.Items[0].ProjectId == nil || *list.Items[0].ProjectId != p.Id {
		t.Fatalf("safety backups: %+v", list.Items)
	}

	// The restore test restores into a scratch database and counts rows.
	if code := e.Do("POST", "/api/v1/restore-tests?project_id="+p.Id.String(), nil, &op); code != http.StatusAccepted {
		t.Fatalf("restore test: %d", code)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded || !strings.Contains(testenv.FormatLog(op), "restore test passed") {
		t.Fatalf("restore test: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	admin := e.SharedAdmin("postgres")
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM pg_database WHERE datname LIKE 'pgdock_restore_test_%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("scratch databases left behind: %d %v", n, err)
	}

	// Metadata self-backup.
	mop, err := jobs.Enqueue(ctx, e.DB, jobs.EnqueueParams{Kind: backup.KindMetadataBackup})
	if err != nil {
		t.Fatal(err)
	}
	if op = e.WaitOperation(mop.ID); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("metadata backup: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	var ov gen.BackupOverview
	if code := e.Do("GET", "/api/v1/backups/overview", nil, &ov); code != http.StatusOK || !ov.StorageConfigured || !ov.AgentAvailable ||
		ov.LastMetadataBackup == nil || ov.LastRestoreTest == nil || ov.Key.ConfirmedAt == nil {
		t.Fatalf("overview: %d %+v", code, ov)
	}

	// Deleting takes a final backup, kept 30 days, restorable after the
	// project is gone.
	e.Reauth()
	var del gen.Operation
	if code := e.Do("DELETE", "/api/v1/projects/"+restoredProject.String()+"?confirm="+url.QueryEscape("Shop restored"), nil, &del); code != http.StatusAccepted {
		t.Fatalf("delete: %d", code)
	}
	if op = e.WaitOperation(del.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("delete: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	final := gen.Final
	e.Do("GET", "/api/v1/backups?kind="+string(final), nil, &list)
	if len(list.Items) != 1 || list.Items[0].ExpiresAt == nil || time.Until(*list.Items[0].ExpiresAt) < 29*24*time.Hour ||
		list.Items[0].ProjectDeleted == nil || !*list.Items[0].ProjectDeleted {
		t.Fatalf("final backups: %+v", list.Items)
	}
	name = "Shop from final"
	rr = gen.RestoreResponse{}
	if code := e.Do("POST", "/api/v1/backups/"+list.Items[0].Id.String()+"/restore", gen.RestoreRequest{Mode: &mode, Name: &name}, &rr); code != http.StatusAccepted {
		t.Fatalf("restore final backup: %d", code)
	}
	if op = e.WaitOperation(rr.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("restore final backup: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	// "Shop restored" had one more row written after its restore.
	verifyShop(t, e.MustConnect(rr.Credentials.Connection.PooledUrl), 501)
}

// verifyShop checks the restored shop: rows, the sequence, a view, and a
// function.
func verifyShop(t *testing.T, c *pgx.Conn, want int) {
	t.Helper()
	ctx := context.Background()
	var n, cheap, fn int
	var last int64
	if err := c.QueryRow(ctx, `SELECT count(*), (SELECT count(*) FROM cheap), item_count(), (SELECT last_value FROM items_id_seq) FROM items`).Scan(&n, &cheap, &fn, &last); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if n != want || fn != want || cheap != 6 || last != int64(want) {
		t.Fatalf("verify: %d rows (function %d, view %d), sequence at %d; want %d", n, fn, cheap, last, want)
	}
}

// downloadRaw fetches an object's bytes without decrypting.
func downloadRaw(t *testing.T, e *testenv.Env, key string) string {
	t.Helper()
	cl, err := storage.New(e.S3.Target("pgdock-test"))
	if err != nil {
		t.Fatal(err)
	}
	rc, err := cl.Download(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestScheduler checks the nightly window: a project's backup is due at its
// jittered time, enqueued once, and the metadata backup and restore test
// follow.
func TestScheduler(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	e.ConfigureBackups()
	ctx := context.Background()
	c := e.CreateProject("Nightly")
	id := c.Project.Id

	at := e.Backups.DueAt(id, time.Now()).Add(time.Minute)
	if at.Before(time.Now()) {
		at = time.Now()
	}
	if err := e.Backups.Schedule(ctx, at.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	countOps := func(kind string) int {
		var n int
		if err := e.DB.QueryRow(ctx, `SELECT count(*) FROM operations WHERE kind = $1`, kind).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// Two minutes early it is not due (unless the window opened so long ago
	// that the jittered time had passed already).
	if early := at.Add(-2 * time.Minute); !early.After(e.Backups.DueAt(id, at)) {
		if n := countOps(backup.KindBackup); n != 0 {
			t.Fatalf("backup enqueued %d time(s) before its jittered time", n)
		}
	}
	for range 2 {
		if err := e.Backups.Schedule(ctx, at); err != nil {
			t.Fatal(err)
		}
	}
	if n := countOps(backup.KindBackup); n != 1 {
		t.Fatalf("nightly backups enqueued: %d, want 1", n)
	}
	if n := countOps(backup.KindMetadataBackup); n != 1 {
		t.Fatalf("metadata backups enqueued: %d, want 1", n)
	}
	var opID uuid.UUID
	if err := e.DB.QueryRow(ctx, `SELECT id FROM operations WHERE kind = 'backup'`).Scan(&opID); err != nil {
		t.Fatal(err)
	}
	if op := e.WaitOperation(opID); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("nightly backup: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	// With a backup to test, the weekly restore test is due.
	if err := e.Backups.Schedule(ctx, at); err != nil {
		t.Fatal(err)
	}
	if err := e.DB.QueryRow(ctx, `SELECT id FROM operations WHERE kind = 'restore_test'`).Scan(&opID); err != nil {
		t.Fatalf("restore test not enqueued: %v", err)
	}
	if op := e.WaitOperation(opID); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("restore test: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	if err := e.Backups.Schedule(ctx, at); err != nil {
		t.Fatal(err)
	}
	if n := countOps(backup.KindRestoreTest); n != 1 {
		t.Fatalf("restore tests enqueued: %d, want 1 a week", n)
	}
}
