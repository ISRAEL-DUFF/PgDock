-- +goose Up
-- V2 s9 (M15): database webhooks and scheduled jobs.

-- A webhook POSTs row changes of chosen tables to a URL. The project's
-- database holds the transactional outbox (schema pgdock, owned by the
-- superuser); this is the configuration and the delivery log.
CREATE TABLE webhooks (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id           uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name                 text NOT NULL,
  tables               text[] NOT NULL,
  events               text[] NOT NULL,
  columns              text[],
  url                  text NOT NULL,
  headers_enc          bytea,
  secret_enc           bytea NOT NULL,
  enabled              boolean NOT NULL DEFAULT true,
  status               text NOT NULL DEFAULT 'healthy' CHECK (status IN ('healthy', 'failing', 'paused', 'broken')),
  status_reason        text,
  consecutive_failures int NOT NULL DEFAULT 0,
  created_by           uuid REFERENCES users(id),
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),
  UNIQUE (project_id, name)
);

CREATE TABLE webhook_deliveries (
  id               bigserial PRIMARY KEY,
  webhook_id       uuid NOT NULL REFERENCES webhooks(id) ON DELETE CASCADE,
  event_id         text NOT NULL,
  attempt          int NOT NULL,
  status_code      int,
  latency_ms       int,
  response_snippet text,
  error            text,
  succeeded        boolean NOT NULL DEFAULT false,
  dead_lettered    boolean NOT NULL DEFAULT false,
  replayed_at      timestamptz,
  payload          jsonb, -- kept for dead letters, to replay
  created_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX webhook_deliveries_log ON webhook_deliveries (webhook_id, created_at DESC);
CREATE INDEX webhook_deliveries_dead ON webhook_deliveries (webhook_id) WHERE dead_lettered AND replayed_at IS NULL;

-- A scheduled job runs SQL as the project owner, or calls a URL.
CREATE TABLE scheduled_jobs (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id           uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name                 text NOT NULL,
  cron                 text NOT NULL,
  timezone             text NOT NULL,
  kind                 text NOT NULL CHECK (kind IN ('sql', 'http')),
  spec_enc             bytea NOT NULL, -- the SQL, or the request (headers may hold secrets)
  timeout_s            int NOT NULL CHECK (timeout_s BETWEEN 1 AND 3600),
  overlap              text NOT NULL DEFAULT 'skip' CHECK (overlap IN ('skip', 'queue')),
  enabled              boolean NOT NULL DEFAULT true,
  next_run_at          timestamptz,
  consecutive_failures int NOT NULL DEFAULT 0,
  created_by           uuid REFERENCES users(id),
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),
  UNIQUE (project_id, name)
);
CREATE INDEX scheduled_jobs_due ON scheduled_jobs (next_run_at) WHERE enabled;

CREATE TABLE job_runs (
  id            bigserial PRIMARY KEY,
  job_id        uuid NOT NULL REFERENCES scheduled_jobs(id) ON DELETE CASCADE,
  scheduled_for timestamptz NOT NULL,
  started_at    timestamptz,
  finished_at   timestamptz,
  status        text NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'timed_out', 'skipped')),
  rows_affected bigint,
  status_code   int,
  error         text,
  trigger       text NOT NULL DEFAULT 'schedule' CHECK (trigger IN ('schedule', 'manual'))
);
CREATE INDEX job_runs_history ON job_runs (job_id, scheduled_for DESC);

-- V2 s10.7: hosts the platform admin lets one organisation reach although
-- they are internal (or over plain http), and per-day counts of the
-- organisation's outbound requests by host, kept 30 days.
CREATE TABLE outbound_allowlist (
  org_id     uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  host       text NOT NULL,
  created_by uuid REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (org_id, host)
);

CREATE TABLE outbound_counters (
  org_id   uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  host     text NOT NULL,
  day      date NOT NULL,
  requests bigint NOT NULL DEFAULT 0,
  failures bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (org_id, host, day)
);

-- +goose Down
DROP TABLE outbound_counters;
DROP TABLE outbound_allowlist;
DROP TABLE job_runs;
DROP TABLE scheduled_jobs;
DROP TABLE webhook_deliveries;
DROP TABLE webhooks;
