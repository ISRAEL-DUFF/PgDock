-- Initial metadata schema (spec §9).

-- +goose Up
CREATE EXTENSION IF NOT EXISTS citext;

-- Operators & sessions
CREATE TABLE operators (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  email         citext UNIQUE NOT NULL,
  password_hash text NOT NULL,              -- argon2id
  totp_secret   bytea,                      -- encrypted with master key
  role          text NOT NULL CHECK (role IN ('owner','member')),
  created_at    timestamptz NOT NULL DEFAULT now(),
  disabled_at   timestamptz
);

CREATE TABLE sessions (
  id            text PRIMARY KEY,           -- random 256-bit, hashed at rest
  operator_id   uuid NOT NULL REFERENCES operators(id) ON DELETE CASCADE,
  created_at    timestamptz NOT NULL DEFAULT now(),
  last_seen_at  timestamptz NOT NULL DEFAULT now(),
  reauth_at     timestamptz,                -- last step-up auth
  ip            inet,
  user_agent    text
);

-- Infrastructure
CREATE TABLE nodes (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name            text UNIQUE NOT NULL,
  private_addr    inet NOT NULL,
  agent_port      int  NOT NULL DEFAULT 7070,
  role            text NOT NULL CHECK (role IN ('shared','dedicated','both')),
  agent_cert_fp   text NOT NULL,            -- pinned client cert fingerprint
  pg_admin_secret bytea,                    -- encrypted; for shared cluster
  capacity        jsonb NOT NULL,           -- cpu, mem, disk reported by agent
  status          text NOT NULL DEFAULT 'healthy',
  last_heartbeat  timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE instances (                    -- a Postgres server: shared cluster or dedicated
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  node_id       uuid NOT NULL REFERENCES nodes(id),
  kind          text NOT NULL CHECK (kind IN ('shared','dedicated')),
  pg_version    int  NOT NULL,
  port          int  NOT NULL,
  container_id  text,                       -- dedicated only
  cpu_limit     numeric,
  mem_limit_mb  int,
  volume_gb     int,
  status        text NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now()
);

-- Backup storage (V1: one default target; per-project targets in V1.1)
CREATE TABLE storage_targets (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name        text UNIQUE NOT NULL,
  endpoint    text NOT NULL,
  bucket      text NOT NULL,
  prefix      text NOT NULL DEFAULT '',
  credentials bytea NOT NULL,              -- encrypted with master key
  is_default  boolean NOT NULL DEFAULT false,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX storage_targets_one_default ON storage_targets (is_default) WHERE is_default;

-- Projects
CREATE TABLE projects (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name              text NOT NULL,
  slug              text NOT NULL,
  db_name           text UNIQUE NOT NULL,   -- e.g. blog_k2f9
  owner_role        text NOT NULL,          -- e.g. blog_k2f9_owner
  scram_verifier    text NOT NULL,          -- for pooler auth_file
  tier              text NOT NULL CHECK (tier IN ('shared','dedicated')),
  instance_id       uuid NOT NULL REFERENCES instances(id),
  status            text NOT NULL,          -- provisioning|active|promoting|restoring|deleting|deleted|error
  settings          jsonb NOT NULL DEFAULT '{}', -- conn limit, timeouts, pool size, disk warn, console_read_only
  storage_target_id uuid NOT NULL REFERENCES storage_targets(id),
  extensions        text[] NOT NULL DEFAULT '{}',
  description       text,
  created_by        uuid REFERENCES operators(id),
  created_at        timestamptz NOT NULL DEFAULT now(),
  deleted_at        timestamptz
);

-- Jobs
CREATE TABLE operations (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  kind         text NOT NULL,               -- create|import|delete|backup|restore|promote|rotate|enable_ext|restore_test
  project_id   uuid REFERENCES projects(id),
  params       jsonb NOT NULL DEFAULT '{}',
  status       text NOT NULL DEFAULT 'queued'
               CHECK (status IN ('queued','running','succeeded','failed')),
  attempts     int  NOT NULL DEFAULT 0,
  run_after    timestamptz NOT NULL DEFAULT now(),
  locked_by    text,
  locked_at    timestamptz,
  log          jsonb NOT NULL DEFAULT '[]', -- [{ts, step, level, msg}]
  error        text,
  created_by   uuid REFERENCES operators(id),
  created_at   timestamptz NOT NULL DEFAULT now(),
  finished_at  timestamptz
);
CREATE INDEX operations_status_run_after ON operations (status, run_after);
CREATE INDEX operations_created_at ON operations (created_at DESC, id DESC);

-- Wake workers and SSE streams on new operations, log appends, and status
-- changes. Heartbeats (locked_at only) stay quiet.
-- +goose StatementBegin
CREATE FUNCTION operations_notify() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  PERFORM pg_notify('pgdock_operations',
    json_build_object('id', NEW.id, 'status', NEW.status)::text);
  RETURN NULL;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER operations_notify_insert
  AFTER INSERT ON operations
  FOR EACH ROW EXECUTE FUNCTION operations_notify();

CREATE TRIGGER operations_notify_update
  AFTER UPDATE ON operations
  FOR EACH ROW
  WHEN (OLD.status IS DISTINCT FROM NEW.status
        OR OLD.log IS DISTINCT FROM NEW.log
        OR OLD.run_after IS DISTINCT FROM NEW.run_after)
  EXECUTE FUNCTION operations_notify();

-- Backups
CREATE TABLE backups (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id   uuid NOT NULL REFERENCES projects(id),
  kind         text NOT NULL CHECK (kind IN ('logical','base','final','safety')),
  object_key   text NOT NULL,
  size_bytes   bigint,
  checksum     text,
  started_at   timestamptz NOT NULL,
  finished_at  timestamptz,
  status       text NOT NULL,
  expires_at   timestamptz
);

-- Metrics
CREATE TABLE metric_points (
  scope      text NOT NULL,                 -- 'project' | 'node'
  scope_id   uuid NOT NULL,
  metric     text NOT NULL,
  ts         timestamptz NOT NULL,
  resolution text NOT NULL,                 -- '1m' | '1h'
  value      double precision NOT NULL,
  PRIMARY KEY (scope, scope_id, metric, resolution, ts)
);

-- Audit & settings
CREATE TABLE audit_log (
  id          bigserial PRIMARY KEY,
  operator_id uuid REFERENCES operators(id),
  action      text NOT NULL,
  target_type text,
  target_id   text,
  detail      jsonb NOT NULL DEFAULT '{}',
  ip          inet,
  user_agent  text,
  outcome     text NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE settings (
  key        text PRIMARY KEY,
  value      jsonb NOT NULL,                -- secrets inside are encrypted blobs
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE settings;
DROP TABLE audit_log;
DROP TABLE metric_points;
DROP TABLE backups;
DROP TABLE operations;
DROP FUNCTION operations_notify();
DROP TABLE projects;
DROP TABLE storage_targets;
DROP TABLE instances;
DROP TABLE nodes;
DROP TABLE sessions;
DROP TABLE operators;
