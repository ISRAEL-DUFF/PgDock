package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/test/testenv"
)

// waitMail waits for the n-th message to addr containing substr.
func waitMail(t *testing.T, e *testenv.Env, addr, substr string, n int) {
	t.Helper()
	waitFor(t, 20*time.Second, "mail "+substr, func() bool { return e.SMTP.Count(addr, substr) >= n })
}

// TestPgVersionLifecycle is V4.1-M5's lifecycle done-when (V4.1 §6.3):
// deprecating Postgres 17 needs a retirement date 180 days out; owners of
// a 17 project are told and see it on the project, and reminded as the
// date nears; once it has passed, new 17 projects are refused while the
// existing one keeps running, marked retired.
func TestPgVersionLifecycle(t *testing.T) {
	needDedicated(t) // a Postgres 17 project: dedicated, since the harness's shared cluster runs 18
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	e.ConfigureBackups()
	e.SetNodeRole("test", "both")
	ctx := context.Background()
	v17, tier, profile, vol := 17, gen.ProjectTierDedicated, "small", 5
	var c gen.ProjectCredentials
	if code := e.Do("POST", "/api/v1/projects", gen.CreateProjectRequest{Name: "Old faithful", PgVersion: &v17, Tier: &tier, Profile: &profile, VolumeGb: &vol}, &c); code != http.StatusAccepted {
		t.Fatalf("create on 17: %d", code)
	}
	if op := e.WaitOperation(c.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("create on 17: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	var list struct{ Items []gen.PgVersionInfo }
	if code := e.Do("GET", "/api/v1/admin/pg-versions", nil, &list); code != http.StatusOK || len(list.Items) < 2 {
		t.Fatalf("versions: %d %+v", code, list)
	}
	for _, v := range list.Items {
		if v.Major == 17 && (v.Status != gen.PgVersionInfoStatusSupported || v.Projects == nil || *v.Projects != 1 || !v.Installed) {
			t.Fatalf("17 before: %+v", v)
		}
	}

	path := "/api/v1/admin/pg-versions/17"
	soon := time.Now().Add(100 * 24 * time.Hour)
	var apiErr gen.Error
	if code := e.Do("PATCH", path, gen.PgVersionUpdate{Status: gen.PgVersionUpdateStatusDeprecated, RetiresAt: &soon}, &apiErr); code != http.StatusBadRequest || !strings.Contains(apiErr.Message, "180 days") {
		t.Fatalf("deprecate 100 days out: %d %+v", code, apiErr)
	}
	if code := e.Do("PATCH", path, gen.PgVersionUpdate{Status: gen.PgVersionUpdateStatusRetired}, &apiErr); code != http.StatusBadRequest {
		t.Fatalf("retire with a project on it: %d %+v", code, apiErr)
	}
	retires := time.Now().Add(181 * 24 * time.Hour).UTC().Truncate(time.Second)
	var v gen.PgVersionInfo
	if code := e.Do("PATCH", path, gen.PgVersionUpdate{Status: gen.PgVersionUpdateStatusDeprecated, RetiresAt: &retires}, &v); code != http.StatusOK ||
		v.Status != gen.PgVersionInfoStatusDeprecated || v.RetiresAt == nil || !v.RetiresAt.Equal(retires) {
		t.Fatalf("deprecate 181 days out: %d %+v", code, v)
	}
	// The owner is told, with the project and its upgrade link.
	waitMail(t, e, testenv.OwnerEmail, "Postgres 17 is deprecated", 1)
	if e.SMTP.Count(testenv.OwnerEmail, "Old faithful") == 0 || e.SMTP.Count(testenv.OwnerEmail, "/projects/"+c.Project.Id.String()+"/settings") == 0 {
		t.Error("the notice doesn't list the project and its upgrade link")
	}
	var p gen.Project
	e.Do("GET", "/api/v1/projects/"+c.Project.Id.String(), nil, &p)
	if p.Instance == nil || p.Instance.PgVersionStatus == nil || *p.Instance.PgVersionStatus != gen.InstanceSummaryPgVersionStatusDeprecated ||
		p.Instance.PgVersionRetiresAt == nil {
		t.Fatalf("the project's banner data: %+v", p.Instance)
	}
	// New projects on 17 are still allowed while it is deprecated, but it
	// isn't the default.
	var profiles gen.ProfileList
	e.Do("GET", "/api/v1/profiles", nil, &profiles)
	if profiles.DefaultPgVersion == 17 {
		t.Errorf("profiles: the default is the deprecated 17")
	}

	// Reminders: one at 90 days, however many sweeps; then at 30.
	at := func(d time.Duration) { e.PGVersions.SetNow(func() time.Time { return retires.Add(-d) }) }
	at(89 * 24 * time.Hour)
	for range 2 {
		if err := e.PGVersions.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
	}
	waitMail(t, e, testenv.OwnerEmail, "Postgres 17 retires in 90 days", 1)
	at(29 * 24 * time.Hour)
	if err := e.PGVersions.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	waitMail(t, e, testenv.OwnerEmail, "Postgres 17 retires in 30 days", 1)
	time.Sleep(500 * time.Millisecond)
	if n := e.SMTP.Count(testenv.OwnerEmail, "retires in 90 days"); n != 1 {
		t.Errorf("90-day reminders: %d", n)
	}

	// The date passes: no new 17 projects; the old one keeps running.
	if _, err := e.DB.Exec(ctx, `UPDATE pg_versions SET retires_at = now() - interval '1 minute' WHERE major = 17`); err != nil {
		t.Fatal(err)
	}
	if code := e.Do("POST", "/api/v1/projects", gen.CreateProjectRequest{Name: "Too late", PgVersion: &v17, Tier: &tier, Profile: &profile, VolumeGb: &vol}, &apiErr); code != http.StatusBadRequest || !strings.Contains(apiErr.Message, "retired") {
		t.Fatalf("create on a retired 17: %d %+v", code, apiErr)
	}
	e.Do("GET", "/api/v1/projects/"+c.Project.Id.String(), nil, &p)
	if p.Instance == nil || p.Instance.PgVersionStatus == nil || *p.Instance.PgVersionStatus != gen.InstanceSummaryPgVersionStatusRetired {
		t.Fatalf("the project after retirement: %+v", p.Instance)
	}
	conn := e.MustConnect(c.Connection.PooledUrl)
	defer conn.Close(ctx)
	var one int
	if err := conn.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("the retired project's database: %v", err)
	}
	e.Do("GET", "/api/v1/profiles", nil, &profiles)
	for _, m := range profiles.PgVersions {
		if m == 17 {
			t.Errorf("profiles still offer 17: %v", profiles.PgVersions)
		}
	}
}

// TestDedicatedUpgradePreflight: a dedicated Postgres 17 project with a
// view 18 can't restore is stopped by the preflight, which tried the schema
// on a temporary Postgres 18 instance (gone after) and flagged the removed
// column; the upgrade isn't started.
func TestDedicatedUpgradePreflight(t *testing.T) {
	needDedicated(t)
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	e.ConfigureBackups()
	e.SetNodeRole("test", "both")
	ctx := context.Background()
	tier, profile, vol, v17 := gen.ProjectTierDedicated, "small", 5, 17
	var c gen.ProjectCredentials
	if code := e.Do("POST", "/api/v1/projects", gen.CreateProjectRequest{Name: "Stats", Tier: &tier, Profile: &profile, VolumeGb: &vol, PgVersion: &v17}, &c); code != http.StatusAccepted {
		t.Fatalf("create: %d", code)
	}
	if op := e.WaitOperation(c.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("create: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	app := e.MustConnect(c.Connection.PooledUrl)
	for _, stmt := range []string{
		`CREATE TABLE orders (id int PRIMARY KEY)`,
		// pg_stat_wal.wal_write is gone in 18.
		`CREATE VIEW wal_writes AS SELECT wal_write FROM pg_stat_wal`,
	} {
		if _, err := app.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	_ = app.Close(ctx)
	path := "/api/v1/projects/" + c.Project.Id.String() + "/upgrade"
	var plan gen.UpgradePreflight
	if code := e.Do("POST", path+"/preflight", gen.UpgradeRequest{PgVersion: 18}, &plan); code != http.StatusOK {
		t.Fatalf("preflight: %d", code)
	}
	got := map[string]gen.UpgradeCheck{}
	for _, ch := range plan.Checks {
		got[string(ch.Name)] = ch
		t.Logf("%s %s: %s", ch.Name, ch.Status, ch.Message)
	}
	if ch := got["schema"]; ch.Status != "blocked" || !strings.Contains(ch.Message, "wal_write") {
		t.Fatalf("schema check: %+v", ch)
	}
	if ch := got["deprecated"]; ch.Status != "warning" || !strings.Contains(ch.Message, "view public.wal_writes uses wal_write") {
		t.Fatalf("deprecated check: %+v", ch)
	}
	var apiErr gen.Error
	if code := e.Do("POST", path, gen.UpgradeRequest{PgVersion: 18}, &apiErr); code != http.StatusConflict {
		t.Fatalf("upgrade despite the preflight: %d %+v", code, apiErr)
	}
	var ops int
	if err := e.DB.QueryRow(ctx, `SELECT count(*) FROM operations WHERE project_id = $1 AND kind = 'major_upgrade'`, c.Project.Id).Scan(&ops); err != nil || ops != 0 {
		t.Fatalf("upgrade operations: %d %v", ops, err)
	}
	// The temporary instances (one per preflight) are gone.
	rows, err := e.DB.Query(ctx, `SELECT id::text, deleted_at IS NOT NULL FROM instances WHERE profile = 'scratch'`)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rows.Next() {
		var id string
		var gone bool
		if err := rows.Scan(&id, &gone); err != nil {
			t.Fatal(err)
		}
		n++
		if !gone {
			t.Errorf("scratch instance %s isn't marked deleted", id)
		}
		if _, err := dockerInspect(t, "pgdock-"+id, "{{.Id}}"); err == nil {
			t.Errorf("scratch instance %s's container survived", id)
		}
	}
	rows.Close()
	if n < 2 {
		t.Errorf("%d scratch instances recorded, want one per preflight (2)", n)
	}

	// Without the view, it passes.
	app = e.MustConnect(c.Connection.PooledUrl)
	if _, err := app.Exec(ctx, `DROP VIEW wal_writes`); err != nil {
		t.Fatal(err)
	}
	_ = app.Close(ctx)
	if code := e.Do("POST", path+"/preflight", gen.UpgradeRequest{PgVersion: 18}, &plan); code != http.StatusOK {
		t.Fatalf("preflight: %d", code)
	}
	for _, ch := range plan.Checks {
		if ch.Status == "blocked" {
			t.Fatalf("blocked without the view: %+v", ch)
		}
	}
}
