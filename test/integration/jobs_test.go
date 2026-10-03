package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/outbound"
	"github.com/israel-duff/pgdock/test/testenv"
)

func jobRuns(t *testing.T, e *testenv.Env, pid string, id string) []gen.JobRun {
	t.Helper()
	var runs gen.JobRunList
	if code := e.Do("GET", "/api/v1/projects/"+pid+"/jobs/"+id+"/runs", nil, &runs); code != http.StatusOK {
		t.Fatalf("runs: %d", code)
	}
	return runs.Items
}

func waitRun(t *testing.T, e *testenv.Env, pid, id string, done func([]gen.JobRun) bool) []gen.JobRun {
	t.Helper()
	var runs []gen.JobRun
	waitFor(t, 20*time.Second, "job runs", func() bool {
		runs = jobRuns(t, e, pid, id)
		return done(runs)
	})
	return runs
}

func finished(r gen.JobRun) bool {
	return r.Status != gen.JobRunStatusQueued && r.Status != gen.JobRunStatusRunning
}

// TestScheduledJobs covers V2 §9.2: SQL as the project owner on a
// schedule, HTTP calls signed through the outbound rules, timeouts,
// overlap, and the plan's limits.
func TestScheduledJobs(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	c := e.CreateProject("Cron app")
	pid := c.Project.Id.String()
	app := e.MustConnect(c.Connection.SessionUrl)
	defer app.Close(ctx)
	if _, err := app.Exec(ctx, `CREATE TABLE ticks (at timestamptz NOT NULL DEFAULT now(), who text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	sql := "INSERT INTO ticks (who) SELECT current_user; DELETE FROM ticks WHERE at < now() - interval '1 day'"
	var cj gen.JobCreated
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/jobs", gen.JobRequest{Name: "tick", Cron: "* * * * *", Kind: gen.JobRequestKindSql, Sql: &sql}, &cj); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	if len(cj.Job.Upcoming) != 5 || cj.Job.NextRunAt == nil || cj.Job.Upcoming[1].Sub(cj.Job.Upcoming[0]) != time.Minute || cj.Secret != nil {
		t.Fatalf("job: %+v", cj.Job)
	}
	jid := cj.Job.Id.String()

	// The next minute comes: it runs, as the project owner.
	e.AutomationAdvance(time.Minute)
	runs := waitRun(t, e, pid, jid, func(r []gen.JobRun) bool { return len(r) == 1 && finished(r[0]) })
	if runs[0].Status != gen.JobRunStatusSucceeded || runs[0].RowsAffected == nil || *runs[0].RowsAffected != 1 || runs[0].Trigger != gen.Schedule {
		t.Fatalf("scheduled run: %+v %v", runs[0], deref(runs[0].Error))
	}
	var who string
	if err := app.QueryRow(ctx, `SELECT who FROM ticks`).Scan(&who); err != nil || who != c.Project.OwnerRole {
		t.Fatalf("the job ran as %q, want %s (%v)", who, c.Project.OwnerRole, err)
	}
	// Missed runs are not caught up: an hour later, one run.
	e.AutomationAdvance(time.Hour)
	waitRun(t, e, pid, jid, func(r []gen.JobRun) bool { return len(r) == 2 && finished(r[0]) })
	time.Sleep(time.Second)
	if n := len(jobRuns(t, e, pid, jid)); n != 2 {
		t.Fatalf("after an hour of downtime: %d runs, want 2 (no catch-up)", n)
	}
	// Paused, it doesn't run; run now still does.
	off := false
	if code := e.Do("PATCH", "/api/v1/projects/"+pid+"/jobs/"+jid, gen.JobUpdate{Enabled: &off}, nil); code != http.StatusOK {
		t.Fatalf("pause: %d", code)
	}
	e.AutomationAdvance(2 * time.Minute)
	time.Sleep(time.Second)
	if n := len(jobRuns(t, e, pid, jid)); n != 2 {
		t.Fatalf("a paused job ran: %d runs", n)
	}
	var manual gen.JobRun
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/jobs/"+jid+"/run", nil, &manual); code != http.StatusAccepted || manual.Trigger != gen.Manual {
		t.Fatalf("run now: %d %+v", code, manual)
	}
	waitRun(t, e, pid, jid, func(r []gen.JobRun) bool {
		return len(r) == 3 && finished(r[0]) && r[0].Status == gen.JobRunStatusSucceeded
	})

	// A slow job times out; a second run while it's going is skipped.
	slow := "SELECT pg_sleep(3)"
	timeout := 1
	var sj gen.JobCreated
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/jobs", gen.JobRequest{Name: "slow", Cron: "0 3 * * *", Kind: gen.JobRequestKindSql, Sql: &slow, TimeoutSeconds: &timeout}, &sj); code != http.StatusCreated {
		t.Fatalf("create slow: %d", code)
	}
	e.Do("POST", "/api/v1/projects/"+pid+"/jobs/"+sj.Job.Id.String()+"/run", nil, nil)
	runs = waitRun(t, e, pid, sj.Job.Id.String(), func(r []gen.JobRun) bool { return len(r) == 1 && finished(r[0]) })
	if runs[0].Status != gen.JobRunStatusTimedOut {
		t.Fatalf("slow job: %+v %v", runs[0], deref(runs[0].Error))
	}
	ten := 10
	if code := e.Do("PATCH", "/api/v1/projects/"+pid+"/jobs/"+sj.Job.Id.String(), gen.JobUpdate{TimeoutSeconds: &ten}, nil); code != http.StatusOK {
		t.Fatalf("raise the timeout: %d", code)
	}
	var r1, r2 gen.JobRun
	e.Do("POST", "/api/v1/projects/"+pid+"/jobs/"+sj.Job.Id.String()+"/run", nil, &r1)
	e.Do("POST", "/api/v1/projects/"+pid+"/jobs/"+sj.Job.Id.String()+"/run", nil, &r2)
	if r1.Status != gen.JobRunStatusRunning || r2.Status != gen.JobRunStatusSkipped || !strings.Contains(deref(r2.Error), "still going") {
		t.Fatalf("overlap: %+v / %+v", r1, r2)
	}
	waitRun(t, e, pid, sj.Job.Id.String(), func(r []gen.JobRun) bool {
		for _, x := range r {
			if x.Id == r1.Id {
				return x.Status == gen.JobRunStatusSucceeded
			}
		}
		return false
	})

	// An HTTP job is signed and goes through the outbound rules.
	rc := newReceiver(t)
	body := `{"digest":"daily"}`
	hreq := gen.JobRequest{Name: "digest", Cron: "@daily", Kind: gen.JobRequestKindHttp, Http: &gen.HttpJobSpec{Url: rc.URL + "/digest", Body: &body}}
	var apiErr gen.Error
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/jobs", hreq, &apiErr); code != http.StatusBadRequest {
		t.Fatalf("an HTTP job to loopback without the allow-list: %d %+v", code, apiErr)
	}
	meta := gen.JobRequest{Name: "meta", Cron: "@daily", Kind: gen.JobRequestKindHttp, Http: &gen.HttpJobSpec{Url: "http://169.254.169.254/latest/meta-data"}}
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/jobs", meta, &apiErr); code != http.StatusBadRequest {
		t.Fatalf("an HTTP job to the metadata address: %d", code)
	}
	allowLocal(t, e, e.OrgID)
	var hj gen.JobCreated
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/jobs", hreq, &hj); code != http.StatusCreated || hj.Secret == nil {
		t.Fatalf("create the HTTP job: %d", code)
	}
	e.Do("POST", "/api/v1/projects/"+pid+"/jobs/"+hj.Job.Id.String()+"/run", nil, nil)
	got := rc.waitN(t, 1, 10*time.Second)
	if got[0].Header.Get("PGDock-Job") != hj.Job.Id.String() || string(got[0].Body) != body {
		t.Fatalf("HTTP job request: %v %s", got[0].Header, got[0].Body)
	}
	if err := outbound.Verify(*hj.Secret, got[0].Header.Get("PGDock-Signature"), got[0].Body, time.Now(), 5*time.Minute); err != nil {
		t.Fatalf("HTTP job signature: %v", err)
	}
	runs = waitRun(t, e, pid, hj.Job.Id.String(), func(r []gen.JobRun) bool { return len(r) == 1 && finished(r[0]) })
	if runs[0].Status != gen.JobRunStatusSucceeded || runs[0].StatusCode == nil || *runs[0].StatusCode != 200 {
		t.Fatalf("HTTP run: %+v", runs[0])
	}
	// Three failures in a row email the project's admins.
	rc.status.Store(500)
	for i := range 3 {
		e.Do("POST", "/api/v1/projects/"+pid+"/jobs/"+hj.Job.Id.String()+"/run", nil, nil)
		waitRun(t, e, pid, hj.Job.Id.String(), func(r []gen.JobRun) bool { return len(r) == i+2 && finished(r[0]) })
	}
	waitFor(t, 5*time.Second, "the failure email", func() bool { return e.SMTP.Count(testenv.OwnerEmail, "failed 3 times in a row") == 1 })

	// The Personal plan's limits: at most every 5 minutes, at most 20 jobs.
	team := e.CreateOrg("Small team")
	tp := e.CreateProjectIn("Small app", team)
	every := gen.JobRequest{Name: "often", Cron: "* * * * *", Kind: gen.JobRequestKindSql, Sql: &sql}
	if code := e.Do("POST", "/api/v1/projects/"+tp.Project.Id.String()+"/jobs", every, &apiErr); code != http.StatusConflict ||
		apiErr.Code != "quota_exceeded" || apiErr.Quota == nil || apiErr.Quota.Limit != "job_min_interval_s" {
		t.Fatalf("every minute on Personal: %d %+v", code, apiErr)
	}
	setOverride(t, e, team, "scheduled_jobs", 1)
	five := gen.JobRequest{Name: "five", Cron: "*/5 * * * *", Kind: gen.JobRequestKindSql, Sql: &sql}
	if code := e.Do("POST", "/api/v1/projects/"+tp.Project.Id.String()+"/jobs", five, nil); code != http.StatusCreated {
		t.Fatalf("every 5 minutes on Personal: %d", code)
	}
	five.Name = "six"
	if code := e.Do("POST", "/api/v1/projects/"+tp.Project.Id.String()+"/jobs", five, &apiErr); code != http.StatusConflict || apiErr.Code != "quota_exceeded" {
		t.Fatalf("a job beyond the plan: %d %+v", code, apiErr)
	}
}

// TestAutomationCLI drives webhooks and jobs with the pgdock binary and a
// project-restricted write token, as a CI job would.
func TestAutomationCLI(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	bin := buildCLI(t)
	c := e.CreateProject("Cli app")
	app := e.MustConnect(c.Connection.SessionUrl)
	defer app.Close(ctx)
	if _, err := app.Exec(ctx, `CREATE TABLE orders (id serial PRIMARY KEY, item text NOT NULL, status text)`); err != nil {
		t.Fatal(err)
	}
	rc := newReceiver(t)
	allowLocal(t, e, e.OrgID)
	token := e.CreateToken(map[string]any{"name": "automation", "org_id": e.OrgID, "scopes": []string{"write"}, "project_ids": []string{c.Project.Id.String()}})
	env := []string{"PGDOCK_SERVER=" + e.URL, "PGDOCK_TOKEN=" + token, "PGDOCK_CONFIG_DIR=" + t.TempDir()}

	r := runCLI(t, bin, env, "webhooks", "create", "Cli app", "orders-hook", "--tables", "orders", "--url", rc.URL, "--events", "INSERT,UPDATE",
		"--columns", "status", "--header", "Authorization=Bearer x")
	if r.code != 0 || !strings.Contains(r.stdout, "whsec_") {
		t.Fatalf("webhooks create: %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	if _, err := app.Exec(ctx, `INSERT INTO orders (item) VALUES ('cli')`); err != nil {
		t.Fatal(err)
	}
	got := rc.waitN(t, 1, 5*time.Second)
	if got[0].Header.Get("Authorization") != "Bearer x" {
		t.Fatalf("header: %v", got[0].Header)
	}
	r = runCLI(t, bin, env, "webhooks", "list", "Cli app")
	if r.code != 0 || !strings.Contains(r.stdout, "orders-hook") || !strings.Contains(r.stdout, "healthy") {
		t.Fatalf("webhooks list: %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	r = runCLI(t, bin, env, "webhooks", "deliveries", "Cli app", "orders-hook")
	if r.code != 0 || !strings.Contains(r.stdout, "ok (HTTP 200)") {
		t.Fatalf("webhooks deliveries: %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	r = runCLI(t, bin, env, "webhooks", "replay", "Cli app", "orders-hook", "--all")
	if r.code != 0 || !strings.Contains(r.stdout, "Queued 0") {
		t.Fatalf("webhooks replay: %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	// SSRF refusals reach the CLI as errors.
	r = runCLI(t, bin, env, "webhooks", "create", "Cli app", "meta", "--tables", "orders", "--url", "http://169.254.169.254/")
	if r.code == 0 || !strings.Contains(r.stderr, "metadata") {
		t.Fatalf("a metadata webhook: %d\n%s", r.code, r.stderr)
	}

	r = runCLI(t, bin, env, "jobs", "create", "Cli app", "cleanup", "--cron", "0 4 * * *", "--tz", "Europe/Berlin", "--sql", "DELETE FROM orders WHERE status = 'void'")
	if r.code != 0 || !strings.Contains(r.stdout, "Next runs:") {
		t.Fatalf("jobs create: %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	r = runCLI(t, bin, env, "jobs", "run", "Cli app", "cleanup")
	if r.code != 0 || !strings.Contains(r.stdout, "running") {
		t.Fatalf("jobs run: %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	waitFor(t, 10*time.Second, "the run", func() bool {
		r = runCLI(t, bin, env, "jobs", "history", "Cli app", "cleanup")
		return strings.Contains(r.stdout, "succeeded")
	})
	r = runCLI(t, bin, env, "jobs", "pause", "Cli app", "cleanup")
	if r.code != 0 {
		t.Fatalf("jobs pause: %d\n%s", r.code, r.stderr)
	}
	r = runCLI(t, bin, env, "jobs", "list", "Cli app")
	if r.code != 0 || !strings.Contains(r.stdout, "paused") || !strings.Contains(r.stdout, "Europe/Berlin") {
		t.Fatalf("jobs list: %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	r = runCLI(t, bin, env, "jobs", "resume", "Cli app", "cleanup")
	if r.code != 0 || !strings.Contains(r.stdout, "Resumed cleanup") {
		t.Fatalf("jobs resume: %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
}

// TestJobSQLCannotEscalate: a SQL job runs as the project owner and can't
// leave that role for the superuser that runs the cluster (M16 review).
func TestJobSQLCannotEscalate(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	c := e.CreateProject("Escalation")
	other := e.CreateProject("Neighbour")
	pid := c.Project.Id.String()
	app := e.MustConnect(c.Connection.SessionUrl)
	defer app.Close(ctx)
	if _, err := app.Exec(ctx, `CREATE TABLE who (name text)`); err != nil {
		t.Fatal(err)
	}
	for i, sql := range []string{
		"RESET ROLE; CREATE ROLE pgdock_escalated SUPERUSER LOGIN",
		"RESET ROLE; ALTER ROLE " + c.Project.OwnerRole + " SUPERUSER",
		"RESET ROLE; INSERT INTO who SELECT current_user",
		"RESET ROLE; SELECT pg_read_file('PG_VERSION')",
		"SET ROLE " + other.Project.OwnerRole,
		"SET SESSION AUTHORIZATION DEFAULT; CREATE ROLE pgdock_escalated SUPERUSER",
		"COMMIT; RESET ROLE; CREATE ROLE pgdock_escalated SUPERUSER",
	} {
		var cj gen.JobCreated
		if code := e.Do("POST", "/api/v1/projects/"+pid+"/jobs", gen.JobRequest{Name: "escape-" + string(rune('a'+i)), Cron: "0 3 * * *", Kind: gen.JobRequestKindSql, Sql: &sql}, &cj); code != http.StatusCreated {
			t.Fatalf("create: %d", code)
		}
		jid := cj.Job.Id.String()
		e.Do("POST", "/api/v1/projects/"+pid+"/jobs/"+jid+"/run", nil, nil)
		runs := waitRun(t, e, pid, jid, func(r []gen.JobRun) bool { return len(r) == 1 && finished(r[0]) })
		if runs[0].Status != gen.JobRunStatusFailed {
			t.Errorf("%q: %s (%v), want failed", sql, runs[0].Status, deref(runs[0].Error))
		}
	}
	var escalated, super bool
	admin := e.MustConnect(e.SharedAdminURL)
	defer admin.Close(ctx)
	if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pgdock_escalated'),
		(SELECT rolsuper FROM pg_roles WHERE rolname = $1)`, c.Project.OwnerRole).Scan(&escalated, &super); err != nil {
		t.Fatal(err)
	}
	if escalated || super {
		_, _ = admin.Exec(ctx, `DROP ROLE IF EXISTS pgdock_escalated`)
		_, _ = admin.Exec(ctx, `ALTER ROLE `+c.Project.OwnerRole+` NOSUPERUSER`)
		t.Fatalf("a job became the superuser: created role %v, owner superuser %v", escalated, super)
	}
	var n int
	if err := app.QueryRow(ctx, `SELECT count(*) FROM who`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("who: %d %v", n, err)
	}

	// A soft storage lock (the database read-only by default) applies to
	// jobs: their writes fail (V2 §10.4).
	db := pgx.Identifier{c.Project.DbName}.Sanitize()
	if _, err := admin.Exec(ctx, "ALTER DATABASE "+db+" SET default_transaction_read_only = on"); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "ALTER DATABASE "+db+" RESET default_transaction_read_only") //nolint:errcheck
	write := "INSERT INTO who VALUES ('locked')"
	var wj gen.JobCreated
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/jobs", gen.JobRequest{Name: "locked-write", Cron: "0 3 * * *", Kind: gen.JobRequestKindSql, Sql: &write}, &wj); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	e.Do("POST", "/api/v1/projects/"+pid+"/jobs/"+wj.Job.Id.String()+"/run", nil, nil)
	runs := waitRun(t, e, pid, wj.Job.Id.String(), func(r []gen.JobRun) bool { return len(r) == 1 && finished(r[0]) })
	if runs[0].Status != gen.JobRunStatusFailed || !strings.Contains(deref(runs[0].Error), "read-only") {
		t.Fatalf("a write under a soft lock: %s %v", runs[0].Status, deref(runs[0].Error))
	}
}
