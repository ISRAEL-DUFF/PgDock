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
	"time"

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

// TestLagosHAResidency is M25's done-when (V3 §6): a Lagos HA dedicated
// project with data residency on runs, backs up in-country only (its
// base backups and WAL archive in the Lagos bucket, nothing on the
// platform default, no cross-region copy), and survives a node failure
// with its standby in Lagos.
func TestLagosHAResidency(t *testing.T) {
	needDedicated(t)
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	e.ConfigureBackups()
	e.SetNodeRole("test", "dedicated")
	ns := threeNodes(t, e, gen.CreateNodeRequestRoleDedicated)
	setupEtcd(t, e, ns)
	ctx := context.Background()
	q := store.New(e.DB)

	// Lagos: three local-provider nodes, an in-country bucket, and the EU
	// bucket as its copy target (which residency projects must not use).
	_, euCopyT := regionBucket(t, e, "eu-copy")
	lagos, lagosT := regionBucket(t, e, "ng-lagos")
	if _, err := e.Regions.Save(ctx, regions.Input{
		ID: "ng-lagos", Name: "Lagos", Country: "NG", Provider: "manual",
		StorageTargetID: &lagosT.ID, CopyTargetID: &euCopyT.ID, Residency: true,
	}); err != nil {
		t.Fatal(err)
	}
	ids := []uuid.UUID{ns[0].Id, ns[1].Id, ns[2].Id}
	if _, err := e.DB.Exec(ctx, `UPDATE nodes SET region = 'ng-lagos' WHERE id = ANY($1)`, ids); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = e.DB.Exec(bg, `UPDATE nodes SET region = 'eu-central' WHERE region = 'ng-lagos'`)
		_, _ = e.DB.Exec(bg, `UPDATE projects SET region = 'eu-central', data_residency = false`)
		_, _ = e.DB.Exec(bg, `UPDATE regions SET copy_target_id = NULL, storage_target_id = NULL`)
		_, _ = e.DB.Exec(bg, `DELETE FROM regions WHERE id = 'ng-lagos'`)
	})

	var regionList gen.RegionList
	if code := e.Do("GET", "/api/v1/regions", nil, &regionList); code != http.StatusOK {
		t.Fatalf("regions: %d", code)
	}
	offered := false
	for _, r := range regionList.Items {
		offered = offered || (r.Id == "ng-lagos" && r.Residency)
	}
	if !offered {
		t.Fatalf("Lagos with residency not listed: %+v", regionList.Items)
	}

	tier, profile, vol, region, residency := gen.ProjectTierDedicated, "small", 5, "ng-lagos", true
	var c gen.ProjectCredentials
	if code := e.Do("POST", "/api/v1/projects", gen.CreateProjectRequest{
		Name: "Lagos ledger", Tier: &tier, Profile: &profile, VolumeGb: &vol, Region: &region, DataResidency: &residency,
	}, &c); code != http.StatusAccepted {
		t.Fatalf("create: %d", code)
	}
	if op := e.WaitOperation(c.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("create: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	if c.Project.Region == nil || *c.Project.Region != "ng-lagos" || c.Project.DataResidency == nil || !*c.Project.DataResidency {
		t.Fatalf("project region %v residency %v", c.Project.Region, c.Project.DataResidency)
	}
	p, err := q.GetProject(ctx, c.Project.Id)
	if err != nil {
		t.Fatal(err)
	}
	inst, err := q.GetInstance(ctx, p.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if inst.WalgTargetID == nil || *inst.WalgTargetID != lagosT.ID {
		t.Fatalf("WAL archive on %v, want the Lagos target", inst.WalgTargetID)
	}

	// HA: the standby goes on another Lagos node.
	ctxW, cancel := context.WithCancel(ctx)
	defer cancel()
	go e.Dedicated.RunHAWatcher(ctxW, time.Second)
	var op gen.Operation
	if code := e.Do("POST", "/api/v1/projects/"+c.Project.Id.String()+"/ha", gen.HAEnableRequest{}, &op); code != http.StatusAccepted {
		t.Fatalf("enable HA: %d", code)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("enable HA: %s %s\n%s", op.Status, deref(op.Error), testenv.FormatLog(op))
	}
	st := haStatus(t, e, c.Project.Id)
	primary, ok := leaderOf(st)
	if !ok || primary.NodeId != ns[0].Id {
		t.Fatalf("members: %+v", st.Members)
	}

	app := e.MustConnect(c.Connection.PooledUrl)
	if _, err := app.Exec(ctx, `CREATE TABLE balances (id bigserial PRIMARY KEY, amount_kobo bigint NOT NULL);
		INSERT INTO balances (amount_kobo) SELECT g * 100 FROM generate_series(1, 1000) g`); err != nil {
		t.Fatal(err)
	}
	app.Close(ctx)

	// Backups: in Lagos only.
	if code := e.Do("POST", "/api/v1/projects/"+c.Project.Id.String()+"/backups", nil, &op); code != http.StatusAccepted {
		t.Fatalf("backup: %d", code)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("backup: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	var outside int
	if err := e.DB.QueryRow(ctx, `SELECT count(*) FROM backups WHERE project_id = $1 AND (storage_target_id IS DISTINCT FROM $2)`, c.Project.Id, lagosT.ID).Scan(&outside); err != nil || outside != 0 {
		t.Fatalf("backups outside the Lagos target: %d (%v)", outside, err)
	}
	if !has(t, lagos, "ng-lagos", inst.ID) {
		t.Fatal("no WAL-G archive in the Lagos bucket")
	}
	if has(t, e.S3, "pgdock-test", inst.ID) || has(t, e.S3, "pgdock-test", c.Project.Id) {
		t.Fatal("the project's data reached the platform default bucket")
	}
	if _, err := e.Backups.CopyRegionBackups(ctx, 100); err != nil {
		t.Fatal(err)
	}
	var copied int
	if err := e.DB.QueryRow(ctx, `SELECT count(*) FROM backups WHERE project_id = $1 AND copy_status = 'copied'`, c.Project.Id).Scan(&copied); err != nil || copied != 0 {
		t.Fatalf("cross-region copies of a residency project: %d (%v)", copied, err)
	}

	// The owner can confirm the setting (step-up); it's audited.
	e.Reauth()
	var rr gen.ProjectResidencyResult
	if code := e.Do("PUT", "/api/v1/projects/"+c.Project.Id.String()+"/residency", gen.ProjectResidencyRequest{Enabled: true}, &rr); code != http.StatusOK || !*rr.Project.DataResidency {
		t.Fatalf("residency on again: %d %+v", code, rr)
	}

	// The primary's node dies: writes come back through the same URL on
	// the Lagos standby.
	w := startWriter(t, e, c.Connection.PooledUrl)
	time.Sleep(5 * time.Second)
	e.KillAgent("test")
	killed := time.Now()
	for _, name := range []string{"pgdock-" + primary.Id.String(), "pgdock-etcd-" + ns[0].Id.String()} {
		if out, err := exec.Command("docker", "kill", name).CombinedOutput(); err != nil {
			t.Fatalf("kill %s: %v %s", name, err, out)
		}
	}
	before := w.acked.Load()
	deadline := time.Now().Add(90 * time.Second)
	for w.acked.Load() < before+20 {
		if time.Now().After(deadline) {
			t.Fatalf("no writes after the primary's node died (%d errors)", w.errs.Load())
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("writes back after %s", time.Since(killed).Round(100*time.Millisecond))
	w.Stop()
	deadline = time.Now().Add(30 * time.Second)
	for {
		st = haStatus(t, e, c.Project.Id)
		l, ok := leaderOf(st)
		if ok && l.NodeId != ns[0].Id {
			n, err := q.GetNode(ctx, l.NodeId)
			if err != nil || n.Region != "ng-lagos" {
				t.Fatalf("new leader's node %v region %q", err, n.Region)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no new leader: %+v", st.Members)
		}
		time.Sleep(time.Second)
	}
}
