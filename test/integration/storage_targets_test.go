package integration

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/storage"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/test/testenv"
)

func quotaUsed(t *testing.T, e *testenv.Env, org string, limit string) (float32, *int64) {
	t.Helper()
	var q gen.OrgQuotas
	if code := e.Do("GET", "/api/v1/orgs/"+org+"/quotas", nil, &q); code != http.StatusOK {
		t.Fatalf("quotas: %d", code)
	}
	for _, it := range q.Items {
		if it.Limit == limit {
			return it.Used, it.Max
		}
	}
	t.Fatalf("no %s quota in %+v", limit, q.Items)
	return 0, nil
}

// TestOrgTargetRestoreWithStandardTools is the M12 done-when: an org's
// project backs up to the org's own bucket, can be restored with standard
// tools (an S3 client, gpg, pg_restore) using only the downloaded key, and
// that storage doesn't count toward the org's backup quota.
func TestOrgTargetRestoreWithStandardTools(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	e.ConfigureBackups() // the platform default target
	ctx := context.Background()

	acme := e.CreateOrg("Acme")
	org := acme.String()
	c := e.CreateProjectIn("Shop", acme)
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

	// A first backup on the platform default counts toward the quota.
	var op gen.Operation
	if code := e.Do("POST", "/api/v1/projects/"+p.Id.String()+"/backups", nil, &op); code != http.StatusAccepted {
		t.Fatalf("backup now: %d", code)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("backup: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	if used, _ := quotaUsed(t, e, org, store.LimitBackupStorageMB); used <= 0 {
		t.Fatalf("platform backup storage used %v, want > 0", used)
	}

	// The org's own bucket: a second S3 the agent can reach.
	gw := os.Getenv("PGDOCK_TEST_DOCKER_GATEWAY")
	addr := ""
	if gw != "" {
		addr = net.JoinHostPort(gw, "0")
	}
	own, err := storage.NewFakeAt("acme-backups", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(own.Close)
	tgt := own.Target("acme-backups")
	region, prefix, pathStyle := tgt.Region, "pgdock/", true
	req := gen.StorageTargetRequest{
		Name: "Acme bucket", Endpoint: tgt.Endpoint, Region: &region, Bucket: tgt.Bucket, Prefix: &prefix,
		AccessKey: &tgt.AccessKey, SecretKey: &tgt.SecretKey, PathStyle: &pathStyle,
	}

	// Saving is blocked until the live test passes.
	bad := req
	nope := "not-the-secret"
	bad.SecretKey = &nope
	wrongBucket := "no-such-bucket"
	bad.Bucket = wrongBucket
	var res gen.StorageTargetSaveResult
	if code := e.Do("POST", "/api/v1/orgs/"+org+"/storage-targets", bad, &res); code != http.StatusOK || res.Saved || res.Test.Ok {
		t.Fatalf("failing test saved? %d %+v", code, res)
	}
	var list gen.StorageTargetList
	if e.Do("GET", "/api/v1/orgs/"+org+"/storage-targets", nil, &list); len(list.Items) != 0 {
		t.Fatalf("a target was saved despite the failed test: %+v", list.Items)
	}
	res = gen.StorageTargetSaveResult{}
	if code := e.Do("POST", "/api/v1/orgs/"+org+"/storage-targets", req, &res); code != http.StatusCreated || !res.Saved || res.Target == nil {
		t.Fatalf("save org target: %d %+v", code, res)
	}
	var steps []string
	for _, s := range res.Test.Steps {
		steps = append(steps, s.Step)
	}
	if strings.Join(steps, ",") != "write,read,list,delete" {
		t.Fatalf("live test steps: %v", steps)
	}
	target := *res.Target
	if target.Kind != gen.StorageTargetKindOrg || target.IsDefault {
		t.Fatalf("org target: %+v", target)
	}
	// Credentials are never shown again.
	_, body := e.GetText("/api/v1/orgs/"+org+"/storage-targets/"+target.Id.String(), nil, true)
	if strings.Contains(body, tgt.SecretKey) || strings.Contains(body, tgt.AccessKey) {
		t.Fatalf("the target's credentials came back: %s", body)
	}
	// Invisible to the platform admin's list and to other organisations.
	var platform gen.StorageTargetList
	e.Do("GET", "/api/v1/admin/storage-targets", nil, &platform)
	for _, pt := range platform.Items {
		if pt.Id == target.Id {
			t.Fatal("the org target is in the platform admin's list")
		}
	}
	bob := e.InviteUser("bob@example.com")
	if code := bob.Do("GET", "/api/v1/orgs/"+org+"/storage-targets/"+target.Id.String(), nil, nil); code != http.StatusNotFound {
		t.Fatalf("another org's user sees the target: %d", code)
	}

	// The project gets its own key...
	var key gen.ProjectBackupKey
	if code := e.Do("POST", "/api/v1/projects/"+p.Id.String()+"/backup-key", nil, &key); code != http.StatusOK || !key.Enabled || key.Fingerprint == nil {
		t.Fatalf("enable key: %d %+v", code, key)
	}
	// ...switches to the org's bucket, copying the existing backup there and
	// deleting the original from platform storage.
	yes := true
	var sw gen.ProjectStorageTargetResult
	if code := e.Do("PUT", "/api/v1/projects/"+p.Id.String()+"/storage-target", gen.ProjectStorageTargetRequest{
		TargetId: &target.Id, CopyExisting: &yes, DeleteOriginals: &yes,
	}, &sw); code != http.StatusOK || sw.Operation == nil {
		t.Fatalf("switch target: %d %+v", code, sw)
	}
	if op = e.WaitOperation(sw.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("switch: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	if !strings.Contains(testenv.FormatLog(op), "verified by checksum") {
		t.Fatalf("copy log:\n%s", testenv.FormatLog(op))
	}
	if sw.Storage.Target.Id == nil || *sw.Storage.Target.Id != target.Id || sw.Storage.CountsTowardQuota {
		t.Fatalf("project storage after switch: %+v", sw.Storage)
	}
	if objs, _ := e.S3.Objects("pgdock-test"); hasPrefix(objs, "pgdock/projects/"+p.Id.String()) {
		t.Fatalf("the original stayed on the platform target: %v", objs)
	}
	if used, _ := quotaUsed(t, e, org, store.LimitBackupStorageMB); used != 0 {
		t.Fatalf("platform backup storage used %v after moving to the org target, want 0", used)
	}

	// With no backup quota left on platform storage, backups to the org's
	// own bucket still go ahead: they don't count.
	if code := e.Do("PATCH", "/api/v1/admin/orgs/"+org, map[string]any{"limit_overrides": map[string]int64{store.LimitBackupStorageMB: 0}}, nil); code != http.StatusOK {
		t.Fatalf("set backup quota: %d", code)
	}
	op = gen.Operation{}
	if code := e.Do("POST", "/api/v1/projects/"+p.Id.String()+"/backups", nil, &op); code != http.StatusAccepted {
		t.Fatalf("backup to the org target with a zero quota: %d", code)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("backup: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	if used, _ := quotaUsed(t, e, org, store.LimitBackupStorageMB); used != 0 {
		t.Fatalf("org-target backup counted toward the quota: %v", used)
	}
	var backups gen.BackupList
	e.Do("GET", "/api/v1/backups?org="+org+"&project_id="+p.Id.String(), nil, &backups)
	var latest *gen.Backup
	for i, b := range backups.Items {
		if b.Status == gen.BackupStatusSucceeded && b.Encryption != nil && *b.Encryption == gen.BackupEncryptionProject {
			latest = &backups.Items[i]
			break
		}
	}
	if latest == nil || latest.StorageTarget == nil || *latest.StorageTarget != "Acme bucket" || latest.KeyFingerprint == nil || *latest.KeyFingerprint != *key.Fingerprint {
		t.Fatalf("backups: %+v", backups.Items)
	}
	objs, err := own.Objects("acme-backups")
	if err != nil {
		t.Fatal(err)
	}
	var gpgObject string
	for _, o := range objs {
		if strings.HasSuffix(o, ".dump.gpg") {
			gpgObject = o
		}
	}
	if gpgObject == "" || !hasPrefix(objs, "pgdock/projects/"+p.Id.String()+"/logical/") {
		t.Fatalf("org bucket objects: %v", objs)
	}

	// Downloading the key needs a fresh re-authentication, and is audited.
	e.Advance(11 * time.Minute)
	if code, body := e.GetText("/api/v1/projects/"+p.Id.String()+"/backup-key/download", nil, true); code != http.StatusForbidden || !strings.Contains(body, "reauth_required") {
		t.Fatalf("download without reauth: %d %s", code, body)
	}
	e.Reauth()
	code, keyFile := e.GetText("/api/v1/projects/"+p.Id.String()+"/backup-key/download", nil, true)
	if code != http.StatusOK || !strings.Contains(keyFile, "-----BEGIN PGP PRIVATE KEY BLOCK-----") || !strings.Contains(keyFile, "pg_restore") {
		t.Fatalf("download key: %d\n%s", code, keyFile)
	}
	var audit gen.AuditList
	e.Do("GET", "/api/v1/projects/"+p.Id.String()+"/audit", nil, &audit)
	downloads := 0
	for _, a := range audit.Items {
		if a.Action == "project.backup_key.download" && a.Outcome == "success" {
			downloads++
		}
	}
	if downloads != 1 {
		t.Fatalf("audited key downloads: %d", downloads)
	}

	// Restore with standard tools only: fetch the object from the org's
	// bucket with an S3 client, then gpg and pg_restore from the stock
	// postgres image, given nothing but the downloaded key file.
	cl, err := storage.New(storage.Target{Endpoint: tgt.Endpoint, Region: tgt.Region, Bucket: tgt.Bucket, AccessKey: tgt.AccessKey, SecretKey: tgt.SecretKey, PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	rc, err := cl.Download(ctx, gpgObject)
	if err != nil {
		t.Fatal(err)
	}
	obj, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(obj, []byte("item 42")) || bytes.Contains(obj, []byte("PGDMP")) {
		t.Fatal("the backup object is not encrypted")
	}
	dir := t.TempDir()
	_ = os.Chmod(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "key.asc"), []byte(keyFile), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "backup.dump.gpg"), obj, 0o644); err != nil {
		t.Fatal(err)
	}
	admin := e.SharedAdmin("postgres")
	if _, err := admin.Exec(ctx, `DROP DATABASE IF EXISTS standard_tools_restore`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `CREATE DATABASE standard_tools_restore`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `DROP DATABASE IF EXISTS standard_tools_restore WITH (FORCE)`)
	})
	network := os.Getenv("PGDOCK_TEST_DOCKER_NETWORK")
	if network == "" {
		network = "pgdock-dev_default"
	}
	script := `set -e
export GNUPGHOME=$(mktemp -d)
gpg --batch --quiet --import /work/key.asc
gpg --batch --quiet --decrypt /work/backup.dump.gpg > /tmp/backup.dump
pg_restore --no-owner --no-acl -d "postgres://pgdock_admin:pgdock_admin@shared-pg:5432/standard_tools_restore" /tmp/backup.dump`
	cmd := exec.Command("docker", "run", "--rm", "--network", network, "-v", dir+":/work:ro", "postgres:18", "sh", "-c", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("restore with gpg and pg_restore: %v\n%s", err, out)
	}
	restored := e.SharedAdmin("standard_tools_restore")
	verifyShop(t, restored, 500)
	restored.Close(ctx)

	// The target can't be deleted while the project uses it.
	var apiErr gen.Error
	if code := e.Do("DELETE", "/api/v1/orgs/"+org+"/storage-targets/"+target.Id.String(), nil, &apiErr); code != http.StatusConflict || apiErr.Code != "target_in_use" {
		t.Fatalf("delete a target in use: %d %+v", code, apiErr)
	}
	// Back on the platform default, the zero quota refuses manual backups
	// there. The target still holds unexpired backups, so deleting it
	// needs explicit consent that they become unrestorable.
	sw = gen.ProjectStorageTargetResult{}
	if code := e.Do("PUT", "/api/v1/projects/"+p.Id.String()+"/storage-target", gen.ProjectStorageTargetRequest{}, &sw); code != http.StatusOK || sw.Operation != nil || !sw.Storage.CountsTowardQuota {
		t.Fatalf("switch back to the platform default: %d %+v", code, sw)
	}
	apiErr = gen.Error{}
	if code := e.Do("POST", "/api/v1/projects/"+p.Id.String()+"/backups", nil, &apiErr); code != http.StatusConflict || apiErr.Code != "quota_exceeded" {
		t.Fatalf("backup to the platform with a zero quota: %d %+v", code, apiErr)
	}
	apiErr = gen.Error{}
	if code := e.Do("DELETE", "/api/v1/orgs/"+org+"/storage-targets/"+target.Id.String(), nil, &apiErr); code != http.StatusConflict || !strings.Contains(apiErr.Message, "unrestorable") {
		t.Fatalf("delete a target holding backups: %d %+v", code, apiErr)
	}
	if code := e.Do("DELETE", "/api/v1/orgs/"+org+"/storage-targets/"+target.Id.String()+"?accept_unrestorable=true", nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete with consent: %d", code)
	}
	backups = gen.BackupList{}
	e.Do("GET", "/api/v1/backups?org="+org+"&project_id="+p.Id.String(), nil, &backups)
	for _, b := range backups.Items {
		if b.StorageKind != nil && *b.StorageKind == gen.StorageTargetKindOrg {
			t.Fatalf("a backup on the deleted target is still listed: %+v", b)
		}
	}
}

func hasPrefix(keys []string, prefix string) bool {
	for _, k := range keys {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

// TestDedicatedStorageSwitch covers V2 §6 for the dedicated tier: switching
// target (with the project's own key) reconfigures WAL-G and takes a fresh
// base backup there at once, and point-in-time recovery works from the new
// archive.
func TestDedicatedStorageSwitch(t *testing.T) {
	needDedicated(t)
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	e.ConfigureBackups()
	e.SetNodeRole("test", "both")
	ctx := context.Background()

	tier := gen.ProjectTierDedicated
	profile, vol := "small", 5
	var c gen.ProjectCredentials
	if code := e.Do("POST", "/api/v1/projects", gen.CreateProjectRequest{Name: "Ledger", Tier: &tier, Profile: &profile, VolumeGb: &vol}, &c); code != http.StatusAccepted {
		t.Fatalf("create dedicated: %d", code)
	}
	if op := e.WaitOperation(c.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("create dedicated: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	pid := c.Project.Id.String()
	app := e.MustConnect(c.Connection.PooledUrl)
	if _, err := app.Exec(ctx, `CREATE TABLE entries (id bigserial PRIMARY KEY, memo text); INSERT INTO entries (memo) SELECT 'e' || g FROM generate_series(1, 50) g`); err != nil {
		t.Fatal(err)
	}

	gw := os.Getenv("PGDOCK_TEST_DOCKER_GATEWAY")
	addr := ""
	if gw != "" {
		addr = net.JoinHostPort(gw, "0")
	}
	own, err := storage.NewFakeAt("ledger-backups", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(own.Close)
	tgt := own.Target("ledger-backups")
	region, prefix, pathStyle := tgt.Region, "pgdock", true
	var res gen.StorageTargetSaveResult
	org := c.Project.OrgId.String()
	if code := e.Do("POST", "/api/v1/orgs/"+org+"/storage-targets", gen.StorageTargetRequest{
		Name: "Own bucket", Endpoint: tgt.Endpoint, Region: &region, Bucket: tgt.Bucket, Prefix: &prefix,
		AccessKey: &tgt.AccessKey, SecretKey: &tgt.SecretKey, PathStyle: &pathStyle,
	}, &res); code != http.StatusCreated || res.Target == nil {
		t.Fatalf("save org target: %d %+v", code, res)
	}
	var key gen.ProjectBackupKey
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/backup-key", nil, &key); code != http.StatusOK || !key.Enabled {
		t.Fatalf("enable key: %d", code)
	}
	var sw gen.ProjectStorageTargetResult
	if code := e.Do("PUT", "/api/v1/projects/"+pid+"/storage-target", gen.ProjectStorageTargetRequest{TargetId: &res.Target.Id}, &sw); code != http.StatusOK || sw.Operation == nil {
		t.Fatalf("switch: %d %+v", code, sw)
	}
	op := e.WaitOperation(sw.Operation.Id)
	if op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("switch: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	for _, want := range []string{"archiving to instances/", "restarting the instance", "base backup base_"} {
		if !strings.Contains(testenv.FormatLog(op), want) {
			t.Errorf("switch log lacks %q:\n%s", want, testenv.FormatLog(op))
		}
	}
	objs, err := own.Objects("ledger-backups")
	if err != nil || !hasPrefix(objs, "pgdock/instances/") {
		t.Fatalf("org bucket after the switch: %v %v", objs, err)
	}
	var backups gen.BackupList
	e.Do("GET", "/api/v1/backups?project_id="+pid+"&kind=base", nil, &backups)
	var onOrg, onPlatform int
	for _, b := range backups.Items {
		switch {
		case b.StorageKind != nil && *b.StorageKind == gen.StorageTargetKindOrg && b.Encryption != nil && *b.Encryption == gen.BackupEncryptionProject:
			onOrg++
		case b.StorageKind != nil && *b.StorageKind == gen.StorageTargetKindPlatform:
			onPlatform++
			// The earlier archive stays restorable for the PITR window.
			if b.ExpiresAt == nil || time.Until(*b.ExpiresAt) < 6*24*time.Hour {
				t.Errorf("earlier base backup expiry: %v", b.ExpiresAt)
			}
		}
	}
	if onOrg != 1 || onPlatform == 0 {
		t.Fatalf("base backups: %d on the org target, %d on the platform: %+v", onOrg, onPlatform, backups.Items)
	}

	// The restarted instance archives to the new place: recover to now.
	app = e.MustConnect(c.Connection.PooledUrl)
	if _, err := app.Exec(ctx, `INSERT INTO entries (memo) VALUES ('after the switch')`); err != nil {
		t.Fatal(err)
	}
	var rc gen.ProjectCredentials
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/pitr", gen.PitrRequest{Name: "Ledger restored"}, &rc); code != http.StatusAccepted {
		t.Fatalf("pitr: %d", code)
	}
	if op = e.WaitOperation(rc.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("pitr: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	restored := e.MustConnect(rc.Connection.PooledUrl)
	var n int
	if err := restored.QueryRow(ctx, `SELECT count(*) FROM entries`).Scan(&n); err != nil || n != 51 {
		t.Fatalf("restored from the org archive: %d rows %v", n, err)
	}
}
