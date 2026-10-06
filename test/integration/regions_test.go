package integration

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/backup"
	"github.com/israel-duff/pgdock/internal/dedicated"
	"github.com/israel-duff/pgdock/internal/jobs"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/regions"
	"github.com/israel-duff/pgdock/internal/storage"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/test/testenv"
)

// regionBucket starts a fake S3 bucket the agent can reach and saves it
// as a platform storage target.
func regionBucket(t *testing.T, e *testenv.Env, name string) (*storage.Fake, store.StorageTarget) {
	t.Helper()
	addr := ""
	if gw := os.Getenv("PGDOCK_TEST_DOCKER_GATEWAY"); gw != "" {
		addr = net.JoinHostPort(gw, "0")
	}
	f, err := storage.NewFakeAt(name, addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	tgt := f.Target(name)
	row, _, err := e.Backups.SaveTarget(context.Background(), nil, nil, backup.TargetInput{
		Name: name, Endpoint: tgt.Endpoint, Region: tgt.Region, Bucket: tgt.Bucket, Prefix: "pgdock",
		AccessKey: tgt.AccessKey, SecretKey: tgt.SecretKey, PathStyle: true,
	}, nil)
	if err != nil {
		t.Fatalf("save target %s: %v", name, err)
	}
	return f, row
}

// has reports whether bucket holds an object of project id's.
func has(t *testing.T, f *storage.Fake, bucket string, id uuid.UUID) bool {
	t.Helper()
	keys, err := f.Objects(bucket)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if strings.Contains(k, id.String()) {
			return true
		}
	}
	return false
}

// TestRegionBackupsAndCopies covers M25's storage side (V3 §2.5, §6.3): a
// region's projects back up to the region's target; backups on platform
// targets get a checksum-verified copy on the region's copy target, which
// the restore test can read; a data-residency project's backups stay in
// the region (no copy outside it, existing ones removed) and it can't be
// moved out of it.
func TestRegionBackupsAndCopies(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	e.ConfigureBackups()
	ctx := context.Background()
	q := store.New(e.DB)

	euCopy, euCopyT := regionBucket(t, e, "eu-copy")
	lagos, lagosT := regionBucket(t, e, "ng-lagos")
	if _, err := e.Regions.Save(ctx, regions.Input{ID: "eu-central", Name: "eu-central", CopyTargetID: &euCopyT.ID}); err != nil {
		t.Fatal(err)
	}
	// Lagos's copies would go to the EU bucket: fine for ordinary Lagos
	// projects, not for residency ones.
	if _, err := e.Regions.Save(ctx, regions.Input{
		ID: "ng-lagos", Name: "Lagos", Country: "NG", PoolerHost: "db.ng.pgdock.test",
		StorageTargetID: &lagosT.ID, CopyTargetID: &euCopyT.ID, Residency: true,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = e.DB.Exec(bg, `UPDATE projects SET region = 'eu-central', data_residency = false, forward_region = NULL, forward_until = NULL`)
		_, _ = e.DB.Exec(bg, `UPDATE regions SET copy_target_id = NULL, storage_target_id = NULL`)
		_, _ = e.DB.Exec(bg, `DELETE FROM regions WHERE id = 'ng-lagos'`)
	})

	mk := func(name string) gen.Project {
		c := e.CreateProject(name)
		app := e.MustConnect(c.Connection.PooledUrl)
		defer app.Close(ctx)
		if _, err := app.Exec(ctx, `CREATE TABLE t AS SELECT g AS id FROM generate_series(1, 100) g`); err != nil {
			t.Fatal(err)
		}
		return c.Project
	}
	backupNow := func(p gen.Project) store.Backup {
		t.Helper()
		var op gen.Operation
		if code := e.Do("POST", "/api/v1/projects/"+p.Id.String()+"/backups", nil, &op); code != http.StatusAccepted {
			t.Fatalf("backup %s: %d", p.Name, code)
		}
		if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
			t.Fatalf("backup %s: %s\n%s", p.Name, op.Status, testenv.FormatLog(op))
		}
		b, err := q.LatestSucceededBackup(ctx, &p.Id)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	eu, ng, ngRes := mk("eu-app"), mk("lagos-app"), mk("lagos-resident")
	if _, err := e.DB.Exec(ctx, `UPDATE projects SET region = 'ng-lagos' WHERE id = ANY($1)`, []uuid.UUID{ng.Id, ngRes.Id}); err != nil {
		t.Fatal(err)
	}
	pRes, err := q.GetProject(ctx, ngRes.Id)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.Backups.SetResidency(ctx, pRes, true); err != nil {
		t.Fatalf("residency on: %v", err)
	}
	pEU, _ := q.GetProject(ctx, eu.Id)
	if _, _, err := e.Backups.SetResidency(ctx, pEU, true); !errors.Is(err, backup.ErrInvalid) {
		t.Fatalf("residency in eu-central (no residency offered): %v", err)
	}

	// 1. Lagos projects back up to the Lagos bucket; the EU one to the default.
	bEU, bNG, bRes := backupNow(eu), backupNow(ng), backupNow(ngRes)
	if bNG.StorageTargetID == nil || *bNG.StorageTargetID != lagosT.ID || *bRes.StorageTargetID != lagosT.ID {
		t.Fatalf("Lagos backups on %v and %v, want the Lagos target", bNG.StorageTargetID, bRes.StorageTargetID)
	}
	if bEU.StorageTargetID != nil && *bEU.StorageTargetID == lagosT.ID {
		t.Fatal("the EU project backed up to Lagos")
	}
	if !has(t, lagos, "ng-lagos", ngRes.Id) || !has(t, lagos, "ng-lagos", ng.Id) || has(t, lagos, "ng-lagos", eu.Id) {
		t.Fatal("the Lagos bucket doesn't hold exactly the Lagos projects' backups")
	}

	// 2. Copies: the EU and ordinary Lagos backups are copied, the
	// residency one is skipped.
	res, err := e.Backups.CopyRegionBackups(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 0 || res.Copied < 2 || res.Skipped < 1 {
		t.Fatalf("copy pass: %+v", res)
	}
	status := func(id uuid.UUID) string {
		b, err := q.GetBackup(ctx, id)
		if err != nil || b.CopyStatus == nil {
			return "none"
		}
		return *b.CopyStatus
	}
	if s := status(bEU.ID); s != backup.CopyCopied {
		t.Fatalf("EU backup copy: %s", s)
	}
	if s := status(bNG.ID); s != backup.CopyCopied {
		t.Fatalf("Lagos backup copy: %s", s)
	}
	if s := status(bRes.ID); s != backup.CopySkipped {
		t.Fatalf("residency backup copy: %s", s)
	}
	if !has(t, euCopy, "eu-copy", eu.Id) || !has(t, euCopy, "eu-copy", ng.Id) || has(t, euCopy, "eu-copy", ngRes.Id) {
		t.Fatal("the copy bucket doesn't hold exactly the copied backups")
	}
	// A second pass has nothing to do.
	if res, err := e.Backups.CopyRegionBackups(ctx, 100); err != nil || res.Copied+res.Failed+res.Skipped != 0 {
		t.Fatalf("second pass: %+v %v", res, err)
	}

	// 3. The restore test reads the copy: it passes with the primary gone.
	primary, err := e.Backups.TargetByID(ctx, *bEU.StorageTargetID)
	if err != nil {
		t.Fatal(err)
	}
	pc, err := storage.New(primary)
	if err != nil {
		t.Fatal(err)
	}
	if err := pc.Delete(ctx, bEU.ObjectKey); err != nil {
		t.Fatal(err)
	}
	op, err := jobs.Enqueue(ctx, e.DB, jobs.EnqueueParams{Kind: backup.KindRestoreTest, ProjectID: &eu.Id, Params: backup.RestoreTestParams{FromCopy: true}})
	if err != nil {
		t.Fatal(err)
	}
	if got := e.WaitOperation(op.ID); got.Status != gen.OperationStatusSucceeded || !strings.Contains(testenv.FormatLog(got), "cross-region copy") {
		t.Fatalf("restore test from the copy: %s\n%s", got.Status, testenv.FormatLog(got))
	}

	// 4. Turning residency on for the ordinary Lagos project removes its
	// copy outside the region.
	pNG, _ := q.GetProject(ctx, ng.Id)
	if _, removed, err := e.Backups.SetResidency(ctx, pNG, true); err != nil || removed != 1 {
		t.Fatalf("residency on with a copy: removed %d, %v", removed, err)
	}
	if has(t, euCopy, "eu-copy", ng.Id) || status(bNG.ID) != backup.CopySkipped {
		t.Fatal("the copy outside the region was kept")
	}

	// 5. A residency project can't be pointed at a target outside its
	// region, nor moved out of it.
	pRes, _ = q.GetProject(ctx, ngRes.Id)
	if _, err := e.Backups.SwitchTarget(ctx, pRes, backup.SwitchParams{TargetID: &euCopyT.ID}); !errors.Is(err, backup.ErrInvalid) {
		t.Fatalf("switch a residency project to the EU target: %v", err)
	}
	other, err := q.InsertNode(ctx, store.InsertNodeParams{Name: "eu-spare", PrivateAddr: "10.9.9.9", Role: "shared", Region: "eu-central"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.DB.Exec(context.Background(), `DELETE FROM nodes WHERE id = $1`, other.ID) })
	if _, err := e.Dedicated.Move(ctx, dedicated.MoveParams{ProjectID: ngRes.Id, NodeID: other.ID}); !errors.Is(err, provision.ErrConflict) || !strings.Contains(err.Error(), "must stay in ng-lagos") {
		t.Fatalf("move a residency project out of its region: %v", err)
	}
}
