package testenv

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/store"
)

// MakeV1 turns a freshly created shared project into what a V1 install
// left behind: its database and roles named after the project (v1Name and
// v1Name_owner), as before V2 §10.2. The password still works: Postgres
// keeps SCRAM verifiers across a rename.
func (e *Env) MakeV1(projectID uuid.UUID, v1Name string) store.Project {
	e.t.Helper()
	ctx := context.Background()
	q := store.New(e.DB)
	p, err := q.GetProject(ctx, projectID)
	if err != nil {
		e.t.Fatal(err)
	}
	admin := e.SharedAdmin("postgres")
	ro, console := provision.ReadOnlyRole(p.DbName), provision.ConsoleRole(p.DbName)
	for _, stmt := range []string{
		`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '` + p.DbName + `'`,
		"ALTER DATABASE " + pgx.Identifier{p.DbName}.Sanitize() + " RENAME TO " + pgx.Identifier{v1Name}.Sanitize(),
		"ALTER ROLE " + pgx.Identifier{p.OwnerRole}.Sanitize() + " RENAME TO " + pgx.Identifier{v1Name + "_owner"}.Sanitize(),
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			e.t.Fatalf("make V1: %s: %v", stmt, err)
		}
	}
	for _, r := range []string{ro, console} {
		var exists bool
		_ = admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, r).Scan(&exists)
		if exists {
			to := v1Name + strings.TrimPrefix(r, p.DbName)
			if _, err := admin.Exec(ctx, "ALTER ROLE "+pgx.Identifier{r}.Sanitize()+" RENAME TO "+pgx.Identifier{to}.Sanitize()); err != nil {
				e.t.Fatal(err)
			}
		}
	}
	if _, err := e.DB.Exec(ctx, `UPDATE projects SET db_name = $2, owner_role = $3, slug = $4 WHERE id = $1`,
		p.ID, v1Name, v1Name+"_owner", v1Name); err != nil {
		e.t.Fatal(err)
	}
	if err := e.Pooler.Sync(ctx); err != nil {
		e.t.Fatal(err)
	}
	p, err = q.GetProject(ctx, projectID)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

// CreateOrg creates an organisation the owner owns.
func (e *Env) CreateOrg(name string) uuid.UUID {
	e.t.Helper()
	var o gen.Org
	if code := e.Do("POST", "/api/v1/orgs", map[string]string{"name": name}, &o); code != http.StatusCreated {
		e.t.Fatalf("create org %q: %d", name, code)
	}
	return o.Id
}

// CreateProjectIn is CreateProject in another of the owner's organisations.
func (e *Env) CreateProjectIn(name string, org uuid.UUID) gen.ProjectCredentials {
	e.t.Helper()
	var c gen.ProjectCredentials
	if code := e.Do("POST", "/api/v1/projects", map[string]any{"name": name, "org_id": org}, &c); code != http.StatusAccepted {
		e.t.Fatalf("create %q in %s: status %d", name, org, code)
	}
	if op := e.WaitOperation(c.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		e.t.Fatalf("create %q failed:\n%s", name, FormatLog(op))
	}
	return c
}
