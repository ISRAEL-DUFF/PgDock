-- +goose Up
-- V3 M19: HA for dedicated instances (V3 §2.2) and the SLA's availability
-- record (§2.7).

-- The etcd cluster Patroni keeps its state in: one member per node, three
-- nodes.
CREATE TABLE etcd_members (
  node_id    uuid PRIMARY KEY REFERENCES nodes(id),
  name       text UNIQUE NOT NULL,
  client_url text NOT NULL,
  peer_url   text NOT NULL,
  status     text NOT NULL DEFAULT 'starting' CHECK (status IN ('starting', 'healthy', 'unhealthy')),
  error      text,
  checked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- An HA instance is one row in instances, tracking its current leader
-- (node_id, host, port and the admin address follow it), and a member per
-- node in instance_members. A member's id is also the agent's key for its
-- container; the first member's is the instance's own id.
ALTER TABLE instances
  ADD COLUMN ha_enabled       boolean NOT NULL DEFAULT false,
  ADD COLUMN sync_replication boolean NOT NULL DEFAULT false,
  ADD COLUMN patroni          boolean NOT NULL DEFAULT false,
  ADD COLUMN leader_member    uuid,
  ADD COLUMN patroni_secret   bytea;

CREATE TABLE instance_members (
  id          uuid PRIMARY KEY,
  instance_id uuid NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  node_id     uuid NOT NULL REFERENCES nodes(id),
  role        text NOT NULL DEFAULT 'starting'
              CHECK (role IN ('leader', 'replica', 'sync_standby', 'starting', 'stopped', 'unknown')),
  state       text,
  host        text,
  port        int,
  rest_host   text,
  rest_port   int,
  admin_host  text,
  admin_port  int,
  lag_bytes   bigint,
  timeline    int,
  error       text,
  updated_at  timestamptz NOT NULL DEFAULT now(),
  created_at  timestamptz NOT NULL DEFAULT now(),
  deleted_at  timestamptz
);
CREATE UNIQUE INDEX instance_members_node ON instance_members (instance_id, node_id) WHERE deleted_at IS NULL;

CREATE TABLE failover_events (
  id          bigserial PRIMARY KEY,
  instance_id uuid NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  from_member uuid,
  to_member   uuid,
  from_node   uuid,
  to_node     uuid,
  kind        text NOT NULL CHECK (kind IN ('failover', 'switchover')),
  duration_ms int,
  occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX failover_events_instance ON failover_events (instance_id, occurred_at DESC);

-- One row per minute per HA project: whether the pooler endpoint accepted
-- a connection and ran a query, from each vantage point. A minute is
-- unavailable when every vantage point that probed in it failed.
CREATE TABLE availability_minutes (
  project_id  uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  minute      timestamptz NOT NULL,
  internal_ok boolean,
  external_ok boolean,
  available   boolean NOT NULL,
  excluded    boolean NOT NULL DEFAULT false,
  PRIMARY KEY (project_id, minute)
);

-- +goose Down
DROP TABLE availability_minutes;
DROP TABLE failover_events;
DROP TABLE instance_members;
ALTER TABLE instances DROP COLUMN ha_enabled, DROP COLUMN sync_replication, DROP COLUMN patroni,
  DROP COLUMN leader_member, DROP COLUMN patroni_secret;
DROP TABLE etcd_members;
