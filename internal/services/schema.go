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
	{2, []string{
		// Auth (V4 §4.2): users live in the project's database, so they move
		// with it on promotion, branching and restore. Only pgdock-edge's
		// login reads and writes these tables; the request roles see the
		// user_profiles view.
		`CREATE TABLE IF NOT EXISTS pgd_auth.users (
		  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
		  email              text,
		  phone              text,
		  encrypted_password text,
		  email_confirmed_at timestamptz,
		  phone_confirmed_at timestamptz,
		  invited_at         timestamptz,
		  is_anonymous       boolean NOT NULL DEFAULT false,
		  app_metadata       jsonb NOT NULL DEFAULT '{}',
		  user_metadata      jsonb NOT NULL DEFAULT '{}',
		  banned_until       timestamptz,
		  failed_sign_ins    int NOT NULL DEFAULT 0,
		  locked_until       timestamptz,
		  created_at         timestamptz NOT NULL DEFAULT now(),
		  updated_at         timestamptz NOT NULL DEFAULT now(),
		  last_sign_in_at    timestamptz
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS users_email ON pgd_auth.users (email) WHERE email IS NOT NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS users_phone ON pgd_auth.users (phone) WHERE phone IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS users_created ON pgd_auth.users (created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS pgd_auth.identities (
		  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
		  user_id         uuid NOT NULL REFERENCES pgd_auth.users (id) ON DELETE CASCADE,
		  provider        text NOT NULL,
		  provider_id     text NOT NULL,
		  identity_data   jsonb NOT NULL DEFAULT '{}',
		  created_at      timestamptz NOT NULL DEFAULT now(),
		  last_sign_in_at timestamptz,
		  UNIQUE (provider, provider_id)
		)`,
		`CREATE INDEX IF NOT EXISTS identities_user ON pgd_auth.identities (user_id)`,
		`CREATE TABLE IF NOT EXISTS pgd_auth.sessions (
		  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
		  user_id      uuid NOT NULL REFERENCES pgd_auth.users (id) ON DELETE CASCADE,
		  aal          text NOT NULL DEFAULT 'aal1',
		  amr          jsonb NOT NULL DEFAULT '[]',
		  user_agent   text,
		  ip           text,
		  created_at   timestamptz NOT NULL DEFAULT now(),
		  refreshed_at timestamptz NOT NULL DEFAULT now(),
		  not_after    timestamptz
		)`,
		`CREATE INDEX IF NOT EXISTS sessions_user ON pgd_auth.sessions (user_id)`,
		// Refresh tokens are single-use: each refresh revokes the token and
		// makes its child. Presenting a revoked one again ends the session.
		`CREATE TABLE IF NOT EXISTS pgd_auth.refresh_tokens (
		  id         bigserial PRIMARY KEY,
		  token_hash text NOT NULL UNIQUE,
		  session_id uuid NOT NULL REFERENCES pgd_auth.sessions (id) ON DELETE CASCADE,
		  parent     bigint,
		  revoked    boolean NOT NULL DEFAULT false,
		  created_at timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS refresh_tokens_session ON pgd_auth.refresh_tokens (session_id)`,
		// Codes and link tokens for confirmation, magic links, recovery,
		// invitations and email changes; only their hashes are kept.
		`CREATE TABLE IF NOT EXISTS pgd_auth.one_time_codes (
		  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
		  user_id    uuid NOT NULL REFERENCES pgd_auth.users (id) ON DELETE CASCADE,
		  kind       text NOT NULL,
		  target     text NOT NULL,
		  code_hash  text NOT NULL,
		  token_hash text NOT NULL UNIQUE,
		  attempts   int NOT NULL DEFAULT 0,
		  created_at timestamptz NOT NULL DEFAULT now(),
		  expires_at timestamptz NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS one_time_codes_target ON pgd_auth.one_time_codes (target, kind)`,
		`CREATE INDEX IF NOT EXISTS one_time_codes_user ON pgd_auth.one_time_codes (user_id)`,
		`CREATE TABLE IF NOT EXISTS pgd_auth.audit_log (
		  id      bigserial PRIMARY KEY,
		  at      timestamptz NOT NULL DEFAULT now(),
		  user_id uuid,
		  action  text NOT NULL,
		  ip      text,
		  details jsonb NOT NULL DEFAULT '{}'
		)`,
		`CREATE INDEX IF NOT EXISTS audit_log_user ON pgd_auth.audit_log (user_id, at DESC)`,
		`CREATE INDEX IF NOT EXISTS audit_log_at ON pgd_auth.audit_log (at DESC)`,
		// The safe view: no secrets. A signed-in user sees their own row,
		// the service role and the owner's own SQL see everyone; anon has no
		// grant.
		`CREATE OR REPLACE VIEW pgd_auth.user_profiles AS
		  SELECT id, email, phone, email_confirmed_at, phone_confirmed_at, is_anonymous, app_metadata, user_metadata,
		         banned_until, created_at, updated_at, last_sign_in_at
		  FROM pgd_auth.users
		  WHERE pgd_auth.role() IS DISTINCT FROM 'user' OR id = pgd_auth.uid()`,
		`REVOKE ALL ON ALL TABLES IN SCHEMA pgd_auth FROM PUBLIC`,
		`GRANT USAGE ON SCHEMA pgd_auth TO {{edge}}`,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON pgd_auth.users, pgd_auth.identities, pgd_auth.sessions,
		  pgd_auth.refresh_tokens, pgd_auth.one_time_codes, pgd_auth.audit_log TO {{edge}}`,
		`GRANT USAGE ON ALL SEQUENCES IN SCHEMA pgd_auth TO {{edge}}`,
		`GRANT SELECT ON pgd_auth.user_profiles TO {{owner}}, {{user}}, {{service}}`,
		// The owner's tables may reference a user (ON DELETE CASCADE works:
		// referential actions run as the referencing table's owner).
		`GRANT REFERENCES (id) ON pgd_auth.users TO {{owner}}`,
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
    WHERE n.nspname IN ('pgd_auth', 'pgd_storage', 'pgd_realtime') AND c.relkind IN ('r', 'v', 'm', 'S', 'p', 'f')
      -- A serial's or identity's sequence moves with its table.
      AND NOT (c.relkind = 'S' AND EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass
        AND d.objid = c.oid AND d.refclassid = 'pg_class'::regclass AND d.deptype IN ('a', 'i'))) LOOP
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
