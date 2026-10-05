package dedicated

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/agentapi"
	"github.com/israel-duff/pgdock/internal/logical"
	"github.com/israel-duff/pgdock/internal/nodes"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/store"
)

// copyKeepingOwners copies p's database from its instance to the same
// database on target, owners and grants as they are (promotion, demotion).
//
// The tenant's objects are restored through a short-lived login that can
// become each owning role but is no superuser (provision.WithRestoreLogin):
// the dump's own functions run during the restore (CHECK constraints,
// generated columns, index expressions), and must not run as the
// superuser. Its extensions, which only the superuser can create and
// comment on, are created on the target first and left out of the dump.
// PGDock's own webhook schema (superuser-owned, V2 §9.1) is copied first,
// as the superuser: it holds only PGDock's objects and queued events.
//
// schemaOnly copies the definitions without rows: a logical move's
// subscription copies the data.
func (s *Service) copyKeepingOwners(ctx context.Context, agent *nodes.Agent, p store.Project, src *pgx.Conn, target uuid.UUID, schemaOnly bool) (time.Duration, error) {
	from, err := s.projects.AgentConn(ctx, p.InstanceID, p.DbName)
	if err != nil {
		return 0, err
	}
	to, err := s.projects.AgentConn(ctx, target, p.DbName)
	if err != nil {
		return 0, err
	}
	var hooks bool
	if err := src.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'pgdock')`).Scan(&hooks); err != nil {
		return 0, err
	}
	rows, err := src.Query(ctx, `SELECT extname, n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace
		WHERE extname <> 'plpgsql' ORDER BY extname`)
	if err != nil {
		return 0, err
	}
	type ext struct{ name, schema string }
	exts, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ext, error) {
		var e ext
		return e, r.Scan(&e.name, &e.schema)
	})
	if err != nil {
		return 0, err
	}
	// Every role that owns something in the database.
	rows, err = src.Query(ctx, `SELECT DISTINCT r.rolname FROM pg_shdepend d JOIN pg_roles r ON r.oid = d.refobjid
		WHERE d.deptype = 'o' AND d.refclassid = 'pg_authid'::regclass AND NOT r.rolsuper
		  AND d.dbid = (SELECT oid FROM pg_database WHERE datname = current_database())`)
	if err != nil {
		return 0, err
	}
	owners, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}

	dst, err := s.projects.AdminConn(ctx, target, p.DbName)
	if err != nil {
		return 0, err
	}
	defer dst.Close(context.Background())
	var names []string
	for _, e := range exts {
		names = append(names, e.name)
		stmt := "CREATE EXTENSION IF NOT EXISTS " + provision.Ident(e.name)
		if e.schema == "public" {
			stmt += " WITH SCHEMA public"
		}
		if _, err := dst.Exec(ctx, stmt); err != nil {
			return 0, fmt.Errorf("extension %s: %w", e.name, err)
		}
	}

	var took time.Duration
	if hooks {
		res, err := agent.Copy(ctx, agentapi.CopyRequest{
			Source: from, Dump: agentapi.DumpOptions{Schemas: []string{"pgdock"}, SchemaOnly: schemaOnly},
			Target: to, Restore: agentapi.RestoreOptions{KeepOwners: true},
		})
		if err != nil {
			return 0, fmt.Errorf("webhook schema: %w", err)
		}
		took += time.Duration(res.DurationMS) * time.Millisecond
	}
	err = s.projects.WithRestoreLogin(ctx, target, p.DbName, p.OwnerRole, owners, func(c agentapi.PGConn) error {
		if hooks {
			// The tables' webhook triggers name PGDock's function; the login
			// may for this copy only (dropping it revokes this).
			login := provision.Ident(c.User)
			if _, err := dst.Exec(ctx, "GRANT USAGE ON SCHEMA pgdock TO "+login+"; GRANT EXECUTE ON FUNCTION pgdock.webhook_enqueue() TO "+login); err != nil {
				return fmt.Errorf("webhook schema: %w", err)
			}
		}
		res, err := agent.Copy(ctx, agentapi.CopyRequest{
			Source: from, Dump: agentapi.DumpOptions{ExcludeSchemas: []string{"pgdock", logical.Schema}, ExcludeExtensions: names, SchemaOnly: schemaOnly},
			Target: c, Restore: agentapi.RestoreOptions{KeepOwners: true},
		})
		took += time.Duration(res.DurationMS) * time.Millisecond
		return err
	})
	return took, err
}
