package integration

import (
	"context"
	"net/http"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/test/testenv"
)

// trapSQL plants a function a CHECK constraint calls on every row: as an
// ordinary role it does nothing, but called by a session that started as
// the superuser (a restore that only SET ROLE to the owner) it would
// RESET ROLE and create a role of its own.
const trapSQL = `
CREATE FUNCTION trap() RETURNS boolean LANGUAGE plpgsql AS $$
BEGIN
  BEGIN
    EXECUTE 'RESET ROLE';
    EXECUTE 'CREATE ROLE ' || quote_ident('pgdock_esc_' || current_database()) || ' SUPERUSER';
  EXCEPTION WHEN OTHERS THEN NULL;
  END;
  RETURN true;
END $$;
CREATE TABLE trapped (id int PRIMARY KEY, CHECK (trap()));
INSERT INTO trapped VALUES (1), (2);`

func escaped(t *testing.T, conn *pgx.Conn) []string {
	t.Helper()
	rows, err := conn.Query(context.Background(), `SELECT rolname FROM pg_roles WHERE rolname LIKE 'pgdock_esc_%'`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		_, _ = conn.Exec(context.Background(), "DROP ROLE "+pgx.Identifier{n}.Sanitize())
	}
	return names
}

// TestCopiesDoNotRunAsSuperuser: copying a tenant's database (a branch, a
// restore, the restore test, promotion, demotion) runs the tenant's own
// functions, and none of them may run as the superuser (M16 review).
func TestCopiesDoNotRunAsSuperuser(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	e.ConfigureBackups()
	ctx := context.Background()
	shared := e.SharedAdmin("postgres")
	defer shared.Close(ctx)

	c := e.CreateProject("Trap")
	pid := c.Project.Id.String()
	app := e.MustConnect(c.Connection.SessionUrl)
	if _, err := app.Exec(ctx, trapSQL); err != nil {
		t.Fatal(err)
	}
	app.Close(ctx)
	if n := escaped(t, shared); len(n) != 0 {
		t.Fatalf("the trap fired for the owner itself: %v", n)
	}
	check := func(what string, conn *pgx.Conn) {
		t.Helper()
		if n := escaped(t, conn); len(n) != 0 {
			t.Errorf("%s ran the tenant's function as the superuser: created %v", what, n)
		}
	}
	waitOK := func(what string, op gen.Operation) {
		t.Helper()
		if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
			t.Fatalf("%s: %s\n%s", what, op.Status, testenv.FormatLog(op))
		}
	}

	// A branch, copied live.
	live := gen.BranchRequestSourceLive
	ttl := 0
	var bc gen.ProjectCredentials
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/branches", gen.BranchRequest{Name: "trap-branch", Source: &live, TtlHours: &ttl}, &bc); code != http.StatusAccepted {
		t.Fatalf("branch: %d", code)
	}
	waitOK("branch", bc.Operation)
	check("a branch", shared)

	// A backup, restored into a new project, and the restore test.
	op := backupNow(t, e, pid)
	waitOK("backup", op)
	var list gen.BackupList
	e.Do("GET", "/api/v1/backups?project_id="+pid, nil, &list)
	if len(list.Items) == 0 {
		t.Fatal("no backup")
	}
	var rr gen.RestoreResponse
	mode := gen.New
	name := "Trap restored"
	if code := e.Do("POST", "/api/v1/backups/"+list.Items[0].Id.String()+"/restore", gen.RestoreRequest{Mode: &mode, Name: &name}, &rr); code != http.StatusAccepted {
		t.Fatalf("restore: %d", code)
	}
	waitOK("restore", rr.Operation)
	check("a restore into a new project", shared)
	if code := e.Do("POST", "/api/v1/restore-tests?project_id="+pid, nil, &op); code != http.StatusAccepted {
		t.Fatalf("restore test: %d", code)
	}
	waitOK("restore test", op)
	check("the restore test", shared)

	if os.Getenv("PGDOCK_TEST_PG_IMAGE") == "" {
		t.Log("PGDOCK_TEST_PG_IMAGE not set: promotion and demotion not checked")
		return
	}
	// Promotion onto a dedicated instance, and demotion back.
	profile, vol := "small", 5
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/promote", gen.PromoteRequest{Profile: &profile, VolumeGb: &vol}, &op); code != http.StatusAccepted {
		t.Fatalf("promote: %d", code)
	}
	waitOK("promote", op)
	var instanceID uuid.UUID
	if err := e.DB.QueryRow(ctx, `SELECT instance_id FROM projects WHERE id = $1`, c.Project.Id).Scan(&instanceID); err != nil {
		t.Fatal(err)
	}
	inst, err := e.Service.AdminConn(ctx, instanceID, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	check("promotion", inst)
	inst.Close(ctx)
	accept := true
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/demote", gen.DemoteRequest{AcceptWarnings: &accept}, &op); code != http.StatusAccepted {
		t.Fatalf("demote: %d", code)
	}
	waitOK("demote", op)
	check("demotion", shared)
	// The copy kept its owners and its trap, and the restore login is gone.
	var owner string
	var logins int
	if err := shared.QueryRow(ctx, `SELECT (SELECT count(*) FROM pg_roles WHERE rolname LIKE '%\_restore')`).Scan(&logins); err != nil || logins != 0 {
		t.Fatalf("restore logins left: %d %v", logins, err)
	}
	app = e.MustConnect(c.Connection.SessionUrl)
	defer app.Close(ctx)
	if err := app.QueryRow(ctx, `SELECT tableowner FROM pg_tables WHERE tablename = 'trapped'`).Scan(&owner); err != nil || owner != c.Project.OwnerRole {
		t.Fatalf("after the moves trapped is owned by %q (%v)", owner, err)
	}
}
