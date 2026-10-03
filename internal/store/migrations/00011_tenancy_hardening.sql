-- Tenancy hardening, quotas, and usage (V2 §2.4, §10, §11; milestone M9).

-- +goose Up

-- §10.2 Opaque names. db_name is the backend database (opaque for every
-- project once renamed); alias_db_name is the V1 name existing connection
-- strings still use, routed by the poolers to db_name.
ALTER TABLE projects
  ADD COLUMN alias_db_name text UNIQUE,
  -- Switch to opaque credentials: the V1 owner role keeps working until
  -- legacy_until, then is dropped (with the alias).
  ADD COLUMN legacy_owner_role     text,
  ADD COLUMN legacy_scram_verifier text,
  ADD COLUMN legacy_until          timestamptz,
  -- §10.4 storage enforcement: none | warn | soft | hard.
  ADD COLUMN storage_state    text NOT NULL DEFAULT 'none'
                              CHECK (storage_state IN ('none', 'warn', 'soft', 'hard')),
  ADD COLUMN storage_state_at timestamptz;

-- §10.5 per-org shared clusters: a shared instance tagged with an org
-- takes only that org's projects.
ALTER TABLE instances ADD COLUMN org_id uuid REFERENCES organizations(id);
CREATE INDEX instances_org ON instances (org_id) WHERE org_id IS NOT NULL;

ALTER TABLE organizations
  ADD COLUMN suspended_at   timestamptz,
  ADD COLUMN delete_requested_by uuid REFERENCES users(id) ON DELETE SET NULL;

-- §2.4 break-glass
CREATE TABLE break_glass_sessions (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id     uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  admin_id   uuid NOT NULL REFERENCES users(id),
  reason     text NOT NULL,
  starts_at  timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  ended_at   timestamptz,
  ended_by   uuid REFERENCES users(id) ON DELETE SET NULL
);
CREATE INDEX break_glass_open ON break_glass_sessions (org_id, admin_id) WHERE ended_at IS NULL;

-- §10.6 dedicated requests
CREATE TABLE dedicated_requests (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id        uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  project_id    uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  requested_by  uuid NOT NULL REFERENCES users(id),
  profile       jsonb NOT NULL,                  -- {profile, volume_gb, node_id}
  reason        text,
  status        text NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled')),
  decided_by    uuid REFERENCES users(id) ON DELETE SET NULL,
  decided_at    timestamptz,
  decision_note text,
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX dedicated_requests_one_pending ON dedicated_requests (project_id) WHERE status = 'pending';

-- §10.9 usage. project_id is nullable (org-level metrics) and survives the
-- project's deletion, so it is not a foreign key.
CREATE TABLE usage_records (
  org_id       uuid NOT NULL REFERENCES organizations(id),
  project_id   uuid NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000',
  metric       text NOT NULL,
  granularity  text NOT NULL CHECK (granularity IN ('hour', 'day')),
  period_start timestamptz NOT NULL,
  quantity     numeric NOT NULL,
  plan_id      uuid NOT NULL,
  PRIMARY KEY (org_id, metric, granularity, period_start, project_id)
);
CREATE INDEX usage_records_period ON usage_records (period_start);

-- §10.4 the reaper's log, per project.
CREATE TABLE reaped_sessions (
  id         bigserial PRIMARY KEY,
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  org_id     uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  kind       text NOT NULL CHECK (kind IN ('statement', 'idle_in_transaction')),
  role_name  text NOT NULL,
  duration_s int NOT NULL,
  query      text,                                -- the tenant's own query, truncated
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX reaped_sessions_project ON reaped_sessions (project_id, created_at DESC);

-- +goose Down
DROP TABLE reaped_sessions;
DROP TABLE usage_records;
DROP TABLE dedicated_requests;
DROP TABLE break_glass_sessions;
ALTER TABLE organizations DROP COLUMN suspended_at, DROP COLUMN delete_requested_by;
DROP INDEX instances_org;
ALTER TABLE instances DROP COLUMN org_id;
ALTER TABLE projects
  DROP COLUMN alias_db_name, DROP COLUMN legacy_owner_role, DROP COLUMN legacy_scram_verifier,
  DROP COLUMN legacy_until, DROP COLUMN storage_state, DROP COLUMN storage_state_at;
