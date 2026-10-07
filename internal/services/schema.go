package services

import (
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/store"
)

// The pgd_* schemas are applied to each project database in versions, by
// the control plane (V4 §11.2). A version's statements run in one
// transaction as the platform's admin, and are written to be safe to run
// again.
type schemaVersion struct {
	n     int
	stmts []string
}

// SchemaVersion is the newest version.
var SchemaVersion = schemaVersions[len(schemaVersions)-1].n

// Placeholders the statements use: {{anon}}, {{user}}, {{service}},
// {{edge}}, {{owner}} (quoted identifiers).
var schemaVersions = []schemaVersion{
	{1, []string{
		// The schemas, owned by the platform's admin: the project's owner can
		// read through what they're granted but not change them.
		`CREATE SCHEMA IF NOT EXISTS pgd_auth`,
		`CREATE SCHEMA IF NOT EXISTS pgd_storage`,
		`CREATE SCHEMA IF NOT EXISTS pgd_realtime`,
		`REVOKE ALL ON SCHEMA pgd_auth, pgd_storage, pgd_realtime FROM PUBLIC`,
		// The request's claims, set by pgdock-edge with set_config(…, true)
		// for its transaction (V4 §2.3). STABLE and SECURITY INVOKER.
		`CREATE OR REPLACE FUNCTION pgd_auth.claims() RETURNS jsonb LANGUAGE sql STABLE SECURITY INVOKER AS
		$$ SELECT coalesce(nullif(current_setting('pgd.claims', true), ''), '{}')::jsonb $$`,
		`CREATE OR REPLACE FUNCTION pgd_auth.claim(name text) RETURNS text LANGUAGE sql STABLE SECURITY INVOKER AS
		$$ SELECT pgd_auth.claims() ->> name $$`,
		`CREATE OR REPLACE FUNCTION pgd_auth.role() RETURNS text LANGUAGE sql STABLE SECURITY INVOKER AS
		$$ SELECT pgd_auth.claims() ->> 'role' $$`,
		`CREATE OR REPLACE FUNCTION pgd_auth.uid() RETURNS uuid LANGUAGE sql STABLE SECURITY INVOKER AS
		$$ SELECT CASE WHEN pgd_auth.claims() ->> 'sub' ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
		     THEN (pgd_auth.claims() ->> 'sub')::uuid END $$`,
		// The owner writes policies with these; the request roles call them.
		`GRANT USAGE ON SCHEMA pgd_auth TO {{owner}}, {{anon}}, {{user}}, {{service}}`,
		`GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA pgd_auth TO {{owner}}, {{anon}}, {{user}}, {{service}}`,
		// The exposed schema: the request roles may use what the owner
		// creates, and row-level security decides which rows (the edge
		// refuses anon and user on a table without it, V4 §3.6).
		`GRANT USAGE ON SCHEMA public TO {{anon}}, {{user}}, {{service}}`,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO {{anon}}, {{user}}, {{service}}`,
		`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO {{anon}}, {{user}}, {{service}}`,
		`GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA public TO {{anon}}, {{user}}, {{service}}`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE {{owner}} IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO {{anon}}, {{user}}, {{service}}`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE {{owner}} IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO {{anon}}, {{user}}, {{service}}`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE {{owner}} IN SCHEMA public GRANT EXECUTE ON FUNCTIONS TO {{anon}}, {{user}}, {{service}}`,
	}},
}

// exposureStmts let the request roles use what the owner makes in an
// exposed schema other than public (public's are in version 1).
var exposureStmts = []string{
	`GRANT USAGE ON SCHEMA {{schema}} TO {{anon}}, {{user}}, {{service}}`,
	`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA {{schema}} TO {{anon}}, {{user}}, {{service}}`,
	`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA {{schema}} TO {{anon}}, {{user}}, {{service}}`,
	`GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA {{schema}} TO {{anon}}, {{user}}, {{service}}`,
	`ALTER DEFAULT PRIVILEGES FOR ROLE {{owner}} IN SCHEMA {{schema}} GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO {{anon}}, {{user}}, {{service}}`,
	`ALTER DEFAULT PRIVILEGES FOR ROLE {{owner}} IN SCHEMA {{schema}} GRANT USAGE, SELECT ON SEQUENCES TO {{anon}}, {{user}}, {{service}}`,
	`ALTER DEFAULT PRIVILEGES FOR ROLE {{owner}} IN SCHEMA {{schema}} GRANT EXECUTE ON FUNCTIONS TO {{anon}}, {{user}}, {{service}}`,
}

// reownSQL gives the platform's admin (the session's user) the pgd_*
// schemas and everything in them.
const reownSQL = `DO $$
DECLARE r record;
BEGIN
  FOR r IN SELECT nspname FROM pg_namespace WHERE nspname IN ('pgd_auth', 'pgd_storage', 'pgd_realtime') LOOP
    EXECUTE format('ALTER SCHEMA %I OWNER TO CURRENT_USER', r.nspname);
  END LOOP;
  FOR r IN SELECT c.oid::regclass AS rel, c.relkind FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname IN ('pgd_auth', 'pgd_storage', 'pgd_realtime') AND c.relkind IN ('r', 'v', 'm', 'S', 'p', 'f') LOOP
    EXECUTE format(CASE r.relkind WHEN 'v' THEN 'ALTER VIEW %s OWNER TO CURRENT_USER' WHEN 'm' THEN 'ALTER MATERIALIZED VIEW %s OWNER TO CURRENT_USER'
      WHEN 'S' THEN 'ALTER SEQUENCE %s OWNER TO CURRENT_USER' WHEN 'f' THEN 'ALTER FOREIGN TABLE %s OWNER TO CURRENT_USER'
      ELSE 'ALTER TABLE %s OWNER TO CURRENT_USER' END, r.rel);
  END LOOP;
  FOR r IN SELECT p.oid::regprocedure AS fn, p.prokind FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
    WHERE n.nspname IN ('pgd_auth', 'pgd_storage', 'pgd_realtime') LOOP
    EXECUTE format(CASE r.prokind WHEN 'p' THEN 'ALTER PROCEDURE %s OWNER TO CURRENT_USER' ELSE 'ALTER FUNCTION %s OWNER TO CURRENT_USER' END, r.fn);
  END LOOP;
  FOR r IN SELECT t.oid::regtype AS typ FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace
    WHERE n.nspname IN ('pgd_auth', 'pgd_storage', 'pgd_realtime') AND t.typtype IN ('e', 'd', 'c') AND t.typrelid = 0 LOOP
    EXECUTE format('ALTER TYPE %s OWNER TO CURRENT_USER', r.typ);
  END LOOP;
END $$`

func expand(stmt string, p store.Project) string {
	q := func(s string) string { return pgx.Identifier{s}.Sanitize() }
	return strings.NewReplacer(
		"{{anon}}", q(store.AnonRole(p.DbName)),
		"{{user}}", q(store.UserRole(p.DbName)),
		"{{service}}", q(store.ServiceRole(p.DbName)),
		"{{edge}}", q(store.EdgeRole(p.DbName)),
		"{{owner}}", q(p.OwnerRole),
	).Replace(stmt)
}
