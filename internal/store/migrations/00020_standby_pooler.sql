-- +goose Up
-- V3 M17: the standby edge pooler and incidents (V3 §2.1, §2.6, §9).
--
-- Pooler hosts are nodes with role 'pooler': each runs the agent, both
-- PgBouncers and keepalived. Placement only ever picks 'shared',
-- 'dedicated' or 'both' nodes, so a pooler host never gets a database.
ALTER TABLE nodes DROP CONSTRAINT nodes_role_check,
  ADD CONSTRAINT nodes_role_check CHECK (role IN ('shared', 'dedicated', 'both', 'pooler'));

-- The provider's ID for the machine (a Hetzner server ID), which the
-- floating IP is assigned to.
ALTER TABLE nodes ADD COLUMN provider_server_id text;

-- What each pooler host last reported: the config generation it serves,
-- its keepalived state, and whether both PgBouncers answer.
ALTER TABLE nodes
  ADD COLUMN pooler_generation bigint,
  ADD COLUMN pooler_hash       text,
  ADD COLUMN pooler_vrrp_state text,
  ADD COLUMN pooler_ready      boolean,
  ADD COLUMN pooler_checked_at timestamptz;

-- The pooler configuration last rendered (routes, users, TLS pair). The
-- generation goes up whenever the content changes; a host serving an
-- older generation is stale and must not hold the floating IP.
CREATE TABLE pooler_config (
  id         int PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  generation bigint NOT NULL,
  hash       text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- Failovers, reassignments, split brain, stale hosts, failed pushes.
CREATE TABLE pooler_events (
  id          bigserial PRIMARY KEY,
  node_id     uuid REFERENCES nodes(id) ON DELETE SET NULL,
  kind        text NOT NULL CHECK (kind IN ('took_ip', 'reassigned', 'split_brain', 'stale', 'push_failed', 'recovered')),
  detail      jsonb NOT NULL DEFAULT '{}',
  occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX pooler_events_time ON pooler_events (occurred_at DESC);

-- Incidents on the status page (V3 §2.6), written by the platform admin
-- or opened automatically, and pushed to pgdock-status.
CREATE TABLE incidents (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  title       text NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
  components  text[] NOT NULL,
  region_id   text,
  severity    text NOT NULL CHECK (severity IN ('minor', 'major', 'critical', 'maintenance')),
  status      text NOT NULL CHECK (status IN ('investigating', 'identified', 'monitoring', 'resolved')),
  started_at  timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz,
  created_by  uuid REFERENCES users(id) ON DELETE SET NULL,
  pushed_at   timestamptz
);
CREATE INDEX incidents_started ON incidents (started_at DESC);

CREATE TABLE incident_updates (
  id          bigserial PRIMARY KEY,
  incident_id uuid NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
  status      text NOT NULL CHECK (status IN ('investigating', 'identified', 'monitoring', 'resolved')),
  body        text NOT NULL CHECK (length(body) BETWEEN 1 AND 5000),
  posted_by   uuid REFERENCES users(id) ON DELETE SET NULL,
  posted_at   timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE incident_updates;
DROP TABLE incidents;
DROP TABLE pooler_events;
DROP TABLE pooler_config;
ALTER TABLE nodes
  DROP COLUMN pooler_checked_at, DROP COLUMN pooler_ready, DROP COLUMN pooler_vrrp_state,
  DROP COLUMN pooler_hash, DROP COLUMN pooler_generation, DROP COLUMN provider_server_id;
DELETE FROM nodes WHERE role = 'pooler';
ALTER TABLE nodes DROP CONSTRAINT nodes_role_check,
  ADD CONSTRAINT nodes_role_check CHECK (role IN ('shared', 'dedicated', 'both'));
