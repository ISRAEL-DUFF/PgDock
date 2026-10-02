-- Organisations, users, and access (V2 §2, §3, §11; milestone M8).
--
-- V1 operators become users; the V1 owner becomes the platform admin and
-- owns a personal organisation that every existing project moves into.

-- +goose Up
ALTER TABLE operators RENAME TO users;
ALTER TABLE users RENAME COLUMN role TO platform_role;
ALTER TABLE users DROP CONSTRAINT operators_role_check;
UPDATE users SET platform_role = CASE platform_role WHEN 'owner' THEN 'platform_admin' ELSE 'user' END;
ALTER TABLE users
  ALTER COLUMN platform_role SET DEFAULT 'user',
  ADD CONSTRAINT users_platform_role_check CHECK (platform_role IN ('platform_admin', 'user')),
  ADD COLUMN name              text,
  ADD COLUMN email_verified_at timestamptz,
  ADD COLUMN approved_at       timestamptz,      -- 'approval required' signup mode
  ADD COLUMN recovery_codes    bytea,            -- sealed JSON of SHA-256 code hashes
  ADD COLUMN last_active_at    timestamptz;
-- V1 operators proved their email by running the platform.
UPDATE users SET email_verified_at = created_at, approved_at = created_at;

ALTER TABLE sessions RENAME COLUMN operator_id TO user_id;
ALTER INDEX sessions_operator RENAME TO sessions_user;
ALTER TABLE auth_challenges RENAME COLUMN operator_id TO user_id;
ALTER TABLE auth_challenges DROP CONSTRAINT auth_challenges_kind_check;
ALTER TABLE auth_challenges ADD CONSTRAINT auth_challenges_kind_check
  CHECK (kind IN ('login', 'setup', 'enroll'));

CREATE TABLE quota_plans (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name       text UNIQUE NOT NULL,              -- Personal | Team | Unlimited | custom
  limits     jsonb NOT NULL,                    -- keys per V2 §10.3; absent or null = unlimited
  created_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO quota_plans (name, limits) VALUES
  ('Personal', '{"projects": 10, "branches": 10, "shared_storage_mb": 5120, "project_storage_mb": 512,
                 "project_connections": 20, "backup_storage_mb": 10240, "webhook_deliveries_per_min": 60,
                 "scheduled_jobs": 20, "job_min_interval_s": 300, "http_job_runs_per_hour": 120,
                 "console_queries": 2, "operations_in_flight": 3}'),
  ('Team',     '{"projects": 25, "branches": 25, "shared_storage_mb": 25600, "project_storage_mb": 8192,
                 "project_connections": 30, "backup_storage_mb": 51200, "webhook_deliveries_per_min": 300,
                 "scheduled_jobs": 50, "job_min_interval_s": 60, "http_job_runs_per_hour": 600,
                 "console_queries": 5, "operations_in_flight": 5}'),
  ('Unlimited', '{}');

CREATE TABLE organizations (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name                text NOT NULL,
  slug                citext UNIQUE NOT NULL,
  personal_owner_id   uuid UNIQUE REFERENCES users(id),   -- set for personal orgs
  plan_id             uuid NOT NULL REFERENCES quota_plans(id),
  limit_overrides     jsonb NOT NULL DEFAULT '{}',
  dedicated_allowance jsonb NOT NULL DEFAULT '{}',
  settings            jsonb NOT NULL DEFAULT '{}',          -- members_can_create_projects, …
  status              text NOT NULL DEFAULT 'active'
                      CHECK (status IN ('active', 'suspended', 'deleting', 'deleted')),
  suspended_reason    text,
  outbound_disabled   boolean NOT NULL DEFAULT false,
  delete_after        timestamptz,
  created_at          timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE org_members (
  org_id     uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role       text NOT NULL CHECK (role IN ('owner', 'admin', 'member')),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (org_id, user_id)
);
CREATE INDEX org_members_user ON org_members (user_id);

ALTER TABLE projects ADD COLUMN org_id uuid REFERENCES organizations(id);

CREATE TABLE project_members (
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  org_id     uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,  -- the project's org, for scoping
  role       text NOT NULL CHECK (role IN ('admin', 'developer', 'read_only')),
  added_by   uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (project_id, user_id)
);
CREATE INDEX project_members_user ON project_members (user_id, org_id);

CREATE TABLE invitations (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  email         citext NOT NULL,
  token_hash    text UNIQUE NOT NULL,
  kind          text NOT NULL CHECK (kind IN ('platform', 'org')),
  org_id        uuid REFERENCES organizations(id) ON DELETE CASCADE,
  org_role      text CHECK (org_role IN ('owner', 'admin', 'member')),
  project_roles jsonb NOT NULL DEFAULT '[]',       -- [{project_id, role}]
  invited_by    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at    timestamptz NOT NULL,
  accepted_at   timestamptz,
  accepted_by   uuid REFERENCES users(id) ON DELETE SET NULL,
  revoked_at    timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),
  CHECK ((kind = 'org') = (org_id IS NOT NULL AND org_role IS NOT NULL))
);
CREATE INDEX invitations_org ON invitations (org_id, created_at DESC);
CREATE INDEX invitations_email ON invitations (email) WHERE accepted_at IS NULL AND revoked_at IS NULL;

CREATE TABLE project_db_users (           -- personal database credentials (V2 §3.5)
  project_id     uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  user_id        uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  org_id         uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  role_name      text UNIQUE NOT NULL,
  scram_verifier text NOT NULL,
  access         text NOT NULL CHECK (access IN ('read_write', 'read_only')),
  created_at     timestamptz NOT NULL DEFAULT now(),
  rotated_at     timestamptz,
  PRIMARY KEY (project_id, user_id)
);

CREATE TABLE email_tokens (               -- verification and password reset
  token_hash text PRIMARY KEY,
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  purpose    text NOT NULL CHECK (purpose IN ('verify', 'reset')),
  expires_at timestamptz NOT NULL,
  used_at    timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX email_tokens_user ON email_tokens (user_id, purpose);

CREATE TABLE terms_versions (
  version      int PRIMARY KEY,
  terms_md     text NOT NULL,
  privacy_md   text NOT NULL,
  published_by uuid REFERENCES users(id) ON DELETE SET NULL,
  published_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE terms_acceptances (
  user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  version     int  NOT NULL REFERENCES terms_versions(version),
  accepted_at timestamptz NOT NULL DEFAULT now(),
  ip          inet,
  PRIMARY KEY (user_id, version)
);

-- Audit scope and actor (V2 §2.5, §2.7).
ALTER TABLE audit_log RENAME COLUMN operator_id TO user_id;
ALTER TABLE audit_log
  ADD COLUMN actor_kind  text NOT NULL DEFAULT 'session' CHECK (actor_kind IN ('session', 'token', 'system')),
  ADD COLUMN token_id    uuid,
  ADD COLUMN org_id      uuid,                    -- NULL = platform-level event
  ADD COLUMN project_id  uuid,
  ADD COLUMN break_glass boolean NOT NULL DEFAULT false;
CREATE INDEX audit_log_org ON audit_log (org_id, id DESC);
CREATE INDEX audit_log_project ON audit_log (project_id, id DESC);

-- V1 data: every user gets a personal organisation; the platform admin's
-- (the first, by creation) owns every existing project.
INSERT INTO organizations (name, slug, personal_owner_id, plan_id)
SELECT split_part(u.email::text, '@', 1) || '''s projects',
       trim(both '-' from left(regexp_replace(lower(split_part(u.email::text, '@', 1)), '[^a-z0-9]+', '-', 'g'), 30))
         || '-' || left(md5(u.id::text), 6),
       u.id,
       (SELECT id FROM quota_plans WHERE name = CASE u.platform_role WHEN 'platform_admin' THEN 'Unlimited' ELSE 'Personal' END)
FROM users u;
INSERT INTO org_members (org_id, user_id, role)
SELECT o.id, o.personal_owner_id, 'owner' FROM organizations o;
UPDATE projects SET org_id = (
  SELECT o.id FROM organizations o JOIN users u ON u.id = o.personal_owner_id
  ORDER BY (u.platform_role = 'platform_admin') DESC, u.created_at LIMIT 1);
-- Projects with nobody to own them (no users yet) go to a holding org the
-- first platform admin is made owner of at setup.
INSERT INTO organizations (name, slug, plan_id)
SELECT 'Existing projects', 'existing-projects', (SELECT id FROM quota_plans WHERE name = 'Unlimited')
WHERE EXISTS (SELECT 1 FROM projects WHERE org_id IS NULL);
UPDATE projects SET org_id = (SELECT id FROM organizations WHERE slug = 'existing-projects') WHERE org_id IS NULL;
ALTER TABLE projects ALTER COLUMN org_id SET NOT NULL;
CREATE INDEX projects_org ON projects (org_id, created_at DESC) WHERE deleted_at IS NULL;

-- +goose Down
DROP INDEX projects_org;
ALTER TABLE projects DROP COLUMN org_id;
DROP INDEX audit_log_project;
DROP INDEX audit_log_org;
ALTER TABLE audit_log DROP COLUMN actor_kind, DROP COLUMN token_id, DROP COLUMN org_id,
  DROP COLUMN project_id, DROP COLUMN break_glass;
ALTER TABLE audit_log RENAME COLUMN user_id TO operator_id;
DROP TABLE terms_acceptances;
DROP TABLE terms_versions;
DROP TABLE email_tokens;
DROP TABLE project_db_users;
DROP TABLE invitations;
DROP TABLE project_members;
DROP TABLE org_members;
DROP TABLE organizations;
DROP TABLE quota_plans;
DELETE FROM auth_challenges WHERE kind = 'enroll';
ALTER TABLE auth_challenges DROP CONSTRAINT auth_challenges_kind_check;
ALTER TABLE auth_challenges ADD CONSTRAINT auth_challenges_kind_check CHECK (kind IN ('login', 'setup'));
ALTER TABLE auth_challenges RENAME COLUMN user_id TO operator_id;
ALTER INDEX sessions_user RENAME TO sessions_operator;
ALTER TABLE sessions RENAME COLUMN user_id TO operator_id;
ALTER TABLE users DROP COLUMN name, DROP COLUMN email_verified_at, DROP COLUMN approved_at,
  DROP COLUMN recovery_codes, DROP COLUMN last_active_at;
ALTER TABLE users DROP CONSTRAINT users_platform_role_check;
ALTER TABLE users ALTER COLUMN platform_role DROP DEFAULT;
UPDATE users SET platform_role = CASE platform_role WHEN 'platform_admin' THEN 'owner' ELSE 'member' END;
ALTER TABLE users ADD CONSTRAINT operators_role_check CHECK (platform_role IN ('owner', 'member'));
ALTER TABLE users RENAME COLUMN platform_role TO role;
ALTER TABLE users RENAME TO operators;
