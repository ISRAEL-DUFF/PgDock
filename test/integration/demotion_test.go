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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/tenancy"
	"github.com/israel-duff/pgdock/test/testenv"
)

// teamWithAllowance is an organisation with its own shared cluster (the
// test node's) and a dedicated allowance of one instance.
func teamWithAllowance(t *testing.T, e *testenv.Env) uuid.UUID {
	t.Helper()
	team := e.CreateOrg("Launch team")
	var clusters gen.SharedClusterList
	e.Do("GET", "/api/v1/admin/shared-clusters", nil, &clusters)
	if len(clusters.Items) != 1 {
		t.Fatalf("shared clusters: %+v", clusters.Items)
	}
	if code := e.Do("POST", "/api/v1/admin/orgs/"+team.String()+"/cluster", map[string]any{"instance_id": clusters.Items[0].Id}, nil); code != http.StatusOK {
		t.Fatalf("give the team its own cluster: %d", code)
	}
	if code := e.Do("PATCH", "/api/v1/admin/orgs/"+team.String(), map[string]any{
		"dedicated_allowance": map[string]any{"instances": 1, "cpus": 2, "memory_mb": 4096, "disk_gb": 20},
	}, nil); code != http.StatusOK {
		t.Fatalf("set the allowance: %d", code)
	}
	return team
}

// promoteSmall promotes a project to a small dedicated instance.
func promoteSmall(t *testing.T, e *testenv.Env, pid string) {
	t.Helper()
	profile, vol := "small", 5
	var op gen.Operation
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/promote", gen.PromoteRequest{Profile: &profile, VolumeGb: &vol}, &op); code != http.StatusAccepted {
		t.Fatalf("promote: %d", code)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("promote: %s %s\n%s", op.Status, deref(op.Error), testenv.FormatLog(op))
	}
}

func dedicatedUse(t *testing.T, e *testenv.Env, org uuid.UUID) int {
	t.Helper()
	var q gen.OrgQuotas
	if code := e.Do("GET", "/api/v1/orgs/"+org.String()+"/quotas", nil, &q); code != http.StatusOK {
		t.Fatalf("quotas: %d", code)
	}
	return q.DedicatedUse.Instances
}

func preflight(t *testing.T, e *testenv.Env, pid string) gen.DemotePreflight {
	t.Helper()
	var pf gen.DemotePreflight
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/demote/preflight", gen.DemoteRequest{}, &pf); code != http.StatusOK {
		t.Fatalf("preflight: %d", code)
	}
	return pf
}

func check(pf gen.DemotePreflight, name gen.DemoteCheckName) gen.DemoteCheck {
	for _, c := range pf.Checks {
		if c.Name == name {
			return c
		}
	}
	return gen.DemoteCheck{}
}

// TestDemotionLiveWriter is the M14 done-when: a promoted project demotes,
// while clients write through both poolers, with its URL and every
// member's credentials still working, and the organisation's dedicated
// allowance is released once the stopped instance is destroyed.
func TestDemotionLiveWriter(t *testing.T) {
	needDedicated(t)
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	e.ConfigureBackups()
	ctx := context.Background()
	team := teamWithAllowance(t, e)

	c := e.CreateProjectIn("Launch", team)
	pid := c.Project.Id.String()
	seedHobby(t, e, c.Connection.PooledUrl)
	member := func(email, role string) gen.PersonalCredentials {
		t.Helper()
		if code := e.Do("POST", "/api/v1/projects/"+pid+"/members", map[string]any{"email": email, "role": role}, nil); code != http.StatusOK {
			t.Fatalf("invite %s: %d", email, code)
		}
		m := e.AcceptInvitation(e.MailToken(email, "invitation"), email)
		var creds gen.PersonalCredentials
		if code := m.Do("POST", "/api/v1/projects/"+pid+"/credentials", nil, &creds); code != http.StatusOK {
			t.Fatalf("credentials for %s: %d", email, code)
		}
		return creds
	}
	reader := member("rita@example.com", "read_only")
	dev := member("walt@example.com", "developer")
	members := func(when string) {
		t.Helper()
		for _, url := range []string{reader.Connection.SessionUrl, reader.Connection.PooledUrl} {
			conn := e.MustConnect(url)
			var n int
			if err := conn.QueryRow(ctx, `SELECT count(*) FROM notes`).Scan(&n); err != nil || n < 2000 {
				t.Fatalf("%s: reader through %s: %d %v", when, url, n, err)
			}
			if _, err := conn.Exec(ctx, `INSERT INTO notes (body) VALUES ('reader')`); err == nil {
				t.Fatalf("%s: the read-only member wrote", when)
			}
			_ = conn.Close(ctx)
		}
		for _, url := range []string{dev.Connection.SessionUrl, dev.Connection.PooledUrl} {
			conn := e.MustConnect(url)
			if _, err := conn.Exec(ctx, `INSERT INTO app.settings VALUES ($1, 'dev') ON CONFLICT (k) DO NOTHING`, when+url); err != nil {
				t.Fatalf("%s: the developer through %s: %v", when, url, err)
			}
			_ = conn.Close(ctx)
		}
	}

	promoteSmall(t, e, pid)
	members("after promotion")
	if n := dedicatedUse(t, e, team); n != 1 {
		t.Fatalf("dedicated use after promotion: %d", n)
	}
	var p gen.Project
	e.Do("GET", "/api/v1/projects/"+pid, nil, &p)
	if p.Instance == nil {
		t.Fatal("no instance")
	}
	dedicatedID := p.Instance.Id

	// The preflight: everything passes, and the organisation's own cluster
	// takes it.
	pf := preflight(t, e, pid)
	if !pf.Eligible || len(pf.Checks) != 7 || pf.Target == nil || !pf.Target.OrgCluster || pf.RetainHours != 48 || pf.SizeBytes == 0 {
		t.Fatalf("preflight: %+v", pf)
	}
	if c := check(pf, gen.DemoteCheckNameAllowance); !strings.Contains(c.Message, "releases a small instance") {
		t.Fatalf("allowance check: %+v", c)
	}
	if !strings.Contains(strings.Join(pf.Resets, "; "), "connection limit 90 → 20") || pf.SettingsAfter.ConnectionLimit != 20 ||
		!pf.SettingsAfter.ConsoleReadOnly || pf.SettingsAfter.StatementTimeout != "60s" {
		t.Fatalf("settings after: %+v %v", pf.SettingsAfter, pf.Resets)
	}

	// An extension or a role the shared tier can't have blocks it.
	admin, err := e.Service.AdminConn(ctx, dedicatedID, c.Project.DbName)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	for _, stmt := range []string{`CREATE EXTENSION postgres_fdw`, `CREATE ROLE reporting NOLOGIN`} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	pf = preflight(t, e, pid)
	if pf.Eligible || check(pf, gen.DemoteCheckNameExtensions).Status != gen.DemoteCheckStatusBlocked || !strings.Contains(check(pf, gen.DemoteCheckNameExtensions).Message, "postgres_fdw") ||
		check(pf, gen.DemoteCheckNameRoles).Status != gen.DemoteCheckStatusBlocked || !strings.Contains(check(pf, gen.DemoteCheckNameRoles).Message, "reporting") {
		t.Fatalf("preflight with postgres_fdw and a custom role: %+v", pf.Checks)
	}
	var apiErr gen.Error
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/demote", gen.DemoteRequest{}, &apiErr); code != http.StatusConflict || !strings.Contains(apiErr.Message, "postgres_fdw") {
		t.Fatalf("demote while blocked: %d %+v", code, apiErr)
	}
	for _, stmt := range []string{`DROP EXTENSION postgres_fdw`, `DROP ROLE reporting`} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	// A database setting the shared tier resets is a warning to accept.
	app := e.MustConnect(c.Connection.SessionUrl)
	if _, err := app.Exec(ctx, `ALTER DATABASE `+pgx.Identifier{c.Project.DbName}.Sanitize()+` SET work_mem = '64MB'`); err != nil {
		t.Fatal(err)
	}
	_ = app.Close(ctx)
	pf = preflight(t, e, pid)
	if !pf.Eligible || check(pf, gen.DemoteCheckNameSettings).Status != gen.DemoteCheckStatusWarning || !strings.Contains(check(pf, gen.DemoteCheckNameSettings).Message, "work_mem=64MB") {
		t.Fatalf("preflight with a database setting: %+v", pf.Checks)
	}
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/demote", gen.DemoteRequest{}, &apiErr); code != http.StatusConflict || !strings.Contains(apiErr.Message, "acknowledge") {
		t.Fatalf("demote without accepting the warnings: %d %+v", code, apiErr)
	}

	// Live writers through both poolers.
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

	accept := true
	var op gen.Operation
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/demote", gen.DemoteRequest{AcceptWarnings: &accept}, &op); code != http.StatusAccepted {
		t.Fatalf("demote: %d", code)
	}
	e.Do("GET", "/api/v1/projects/"+pid, nil, &p)
	if p.Status != gen.ProjectStatusDemoting && p.Status != gen.ProjectStatusActive {
		t.Fatalf("status during demotion: %s", p.Status)
	}
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/rotate-password", nil, nil); code != http.StatusConflict {
		t.Fatalf("rotate during demotion: %d, want 409", code)
	}
	op = e.WaitOperation(op.Id)
	log := testenv.FormatLog(op)
	if op.Status != gen.OperationStatusSucceeded {
		stop()
		wg.Wait()
		t.Fatalf("demote: %s %s\n%s", op.Status, deref(op.Error), log)
	}
	before := tx.n.Load()
	deadline = time.Now().Add(15 * time.Second)
	for tx.n.Load() < before+20 {
		if time.Now().After(deadline) {
			t.Fatalf("tx writer stalled after demotion: %v", tx.errs)
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop()
	wg.Wait()
	t.Logf("demotion log:\n%s", log)
	t.Logf("tx writer: %d commits, %d errors %v; session writer: %d commits, %d errors %v",
		len(tx.acked), len(tx.errs), tx.errs, len(session.acked), len(session.errs), session.errs)
	for _, want := range []string{"checks passed", "writes frozen", "logical replication", "match", "route switched to the shared cluster", "writes were paused for",
		"dedicated instance stopped", "point-in-time recovery ends here"} {
		if !strings.Contains(log, want) {
			t.Errorf("demotion log lacks %q", want)
		}
	}

	// Same URL and password, now served by the shared cluster.
	app = e.MustConnect(c.Connection.PooledUrl)
	defer app.Close(ctx)
	var archive, workMem string
	if err := app.QueryRow(ctx, `SELECT current_setting('archive_mode'), current_setting('work_mem')`).Scan(&archive, &workMem); err != nil || archive == "on" {
		t.Fatalf("the URL does not reach the shared cluster: archive_mode %q %v", archive, err)
	}
	if workMem == "64MB" {
		t.Fatal("the database setting was not reset")
	}
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
	if len(tx.errs) > 0 {
		t.Errorf("the transaction-mode writer saw errors: %v", tx.errs)
	}
	var owner, theme string
	if err := app.QueryRow(ctx, `SELECT tableowner, (SELECT v FROM app.settings WHERE k = 'theme') FROM pg_tables WHERE tablename = 'events'`).Scan(&owner, &theme); err != nil ||
		owner != c.Project.OwnerRole || theme != "dark" {
		t.Fatalf("copied objects: owner %q theme %q %v", owner, theme, err)
	}
	// Every member's credentials still work, read-only still read-only.
	members("after demotion")

	p = gen.Project{}
	e.Do("GET", "/api/v1/projects/"+pid, nil, &p)
	if p.Tier != gen.ProjectTierShared || p.Status != gen.ProjectStatusActive || p.Instance == nil || p.Instance.Kind != "shared" ||
		p.Connection.PooledUrl != c.Project.Connection.PooledUrl || p.Settings.ConnectionLimit != 20 || p.Settings.PoolSize != 5 ||
		!p.Settings.ConsoleReadOnly || p.RetiredCopyUntil == nil || time.Until(*p.RetiredCopyUntil) < 47*time.Hour || p.PitrWindow != nil {
		t.Fatalf("project after demotion: %+v", p)
	}
	// Nightly logical backups from now on, the first already taken; the
	// base backups stay until their retention ends.
	var backups gen.BackupList
	e.Do("GET", "/api/v1/backups?org="+team.String()+"&project_id="+pid, nil, &backups)
	var logical, base int
	for _, b := range backups.Items {
		switch b.Kind {
		case "logical":
			logical++
		case "base":
			base++
		}
	}
	if logical != 1 || base == 0 {
		t.Fatalf("backups after demotion: %d logical, %d base", logical, base)
	}
	var expiring int
	if err := e.DB.QueryRow(ctx, `SELECT count(*) FROM backups WHERE project_id = $1 AND kind = 'base' AND expires_at > now() + interval '6 days'`, c.Project.Id).Scan(&expiring); err != nil || expiring != base {
		t.Fatalf("base backups with an expiry: %d of %d %v", expiring, base, err)
	}

	// The dedicated instance is stopped and still counts toward the
	// allowance until it is destroyed.
	var status string
	if err := e.DB.QueryRow(ctx, `SELECT status FROM instances WHERE id = $1`, dedicatedID).Scan(&status); err != nil || status != "stopped" {
		t.Fatalf("dedicated instance after demotion: %q %v", status, err)
	}
	small := tenancy.Dedicated{CPUs: 1, MemoryMB: 1024, DiskGB: 5}
	if n := dedicatedUse(t, e, team); n != 1 {
		t.Fatalf("dedicated use during the retention: %d", n)
	}
	if ok, err := e.Tenancy.WithinAllowance(ctx, team, small); err != nil || ok {
		t.Fatalf("another dedicated instance fits the allowance during the retention: %v %v", ok, err)
	}
	if n, err := e.Dedicated.ReapOrphans(ctx); err != nil || n != 0 {
		t.Fatalf("the orphan reaper removed the kept instance: %d %v", n, err)
	}

	// After 48 hours it is destroyed, and the allowance is released.
	if _, err := e.DB.Exec(ctx, `UPDATE retired_databases SET drop_after = now() - interval '1 minute' WHERE project_id = $1`, c.Project.Id); err != nil {
		t.Fatal(err)
	}
	if err := e.Dedicated.DropRetired(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.DB.QueryRow(ctx, `SELECT status FROM instances WHERE id = $1`, dedicatedID).Scan(&status); err != nil || status != "deleted" {
		t.Fatalf("dedicated instance after the retention: %q %v", status, err)
	}
	if n := dedicatedUse(t, e, team); n != 0 {
		t.Fatalf("dedicated use after the retention: %d", n)
	}
	if ok, err := e.Tenancy.WithinAllowance(ctx, team, small); err != nil || !ok {
		t.Fatalf("the allowance was not released: %v %v", ok, err)
	}
	p = gen.Project{}
	e.Do("GET", "/api/v1/projects/"+pid, nil, &p)
	if p.RetiredCopyUntil != nil {
		t.Fatal("retired_copy_until still set after the instance was destroyed")
	}
	members("after the retention")
	// Shared projects can't be demoted.
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/demote/preflight", gen.DemoteRequest{}, nil); code != http.StatusBadRequest {
		t.Fatalf("preflight on a shared project: %d", code)
	}
}

// TestDemotionRollback fails a demotion after the freeze: the project stays
// on its dedicated instance, writable, with nothing lost, and the partial
// shared copy is dropped.
func TestDemotionRollback(t *testing.T) {
	needDedicated(t)
	var failDemotion atomic.Bool
	e := testenv.Start(t, testenv.Options{AfterFreeze: func(context.Context) error {
		if failDemotion.Load() {
			return errors.New("injected failure after the freeze")
		}
		return nil
	}})
	e.StartAgent()
	e.ConfigureBackups()
	ctx := context.Background()
	c := e.CreateProject("Demote rollback")
	pid := c.Project.Id.String()
	seedHobby(t, e, c.Connection.PooledUrl)
	promoteSmall(t, e, pid)

	failDemotion.Store(true)
	var op gen.Operation
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/demote", gen.DemoteRequest{}, &op); code != http.StatusAccepted {
		t.Fatalf("demote: %d", code)
	}
	op = e.WaitOperation(op.Id)
	if op.Status != gen.OperationStatusFailed || !strings.Contains(testenv.FormatLog(op), "demotion rolled back") {
		t.Fatalf("demote: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	var p gen.Project
	e.Do("GET", "/api/v1/projects/"+pid, nil, &p)
	if p.Tier != gen.ProjectTierDedicated || p.Status != gen.ProjectStatusActive || p.Instance == nil || p.Instance.Status != "running" {
		t.Fatalf("project after rollback: %s %s %+v", p.Tier, p.Status, p.Instance)
	}
	for _, u := range []string{c.Connection.PooledUrl, c.Connection.SessionUrl} {
		conn, err := e.Connect(u)
		if err != nil {
			t.Fatalf("connect after rollback: %v", err)
		}
		var archive string
		if _, err := conn.Exec(ctx, `INSERT INTO notes (body) VALUES ('after rollback')`); err != nil {
			t.Fatalf("write after rollback: %v", err)
		}
		if err := conn.QueryRow(ctx, `SELECT current_setting('archive_mode')`).Scan(&archive); err != nil || archive != "on" {
			t.Fatalf("not on the dedicated instance after rollback: %q %v", archive, err)
		}
		_ = conn.Close(ctx)
	}
	// The shared copy kept from the promotion was superseded and dropped,
	// and the partial demotion copy is gone too.
	pg := e.SharedAdmin("postgres")
	defer pg.Close(ctx)
	var exists bool
	if err := pg.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, c.Project.DbName).Scan(&exists); err != nil || exists {
		t.Fatalf("shared copy left after the rollback: %v %v", exists, err)
	}

	// It demotes once the fault is gone, here with the CLI and an admin
	// token.
	failDemotion.Store(false)
	bin := buildCLI(t)
	token := e.CreateToken(map[string]any{"name": "ops", "org_id": e.OrgID, "scopes": []string{"write", "admin"}})
	env := []string{"PGDOCK_SERVER=" + e.URL, "PGDOCK_TOKEN=" + token, "PGDOCK_CONFIG_DIR=" + t.TempDir()}
	r := runCLI(t, bin, env, "demote", "Demote rollback", "--check")
	if r.code != 0 || !strings.Contains(r.stdout, "ok      capacity") || !strings.Contains(r.stdout, "connection limit 90 → 20") {
		t.Fatalf("demote --check: exit %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	r = runCLI(t, bin, env, "demote", "Demote rollback")
	if r.code != 0 || !strings.Contains(r.stdout, "Demoted Demote rollback to the shared tier") || !strings.Contains(r.stderr, "route switched to the shared cluster") {
		t.Fatalf("demote: exit %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	e.Do("GET", "/api/v1/projects/"+pid, nil, &p)
	if p.Tier != gen.ProjectTierShared {
		t.Fatalf("tier after the CLI demotion: %s", p.Tier)
	}
	conn := e.MustConnect(c.Connection.PooledUrl)
	defer conn.Close(ctx)
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM notes WHERE body = 'after rollback'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("writes from after the rollback: %d %v", n, err)
	}
}
