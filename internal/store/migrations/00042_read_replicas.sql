-- +goose Up
-- V4-M35: read replicas of dedicated projects (V4 §7). A replica is a
-- member of the instance's Patroni cluster tagged nofailover and nosync:
-- it streams from whichever member leads, is never promoted and never
-- holds up commits. Its row in instance_members says so, so HA's own
-- logic (standbys, switchovers, billing) leaves it alone.
ALTER TABLE instance_members ADD COLUMN replica boolean NOT NULL DEFAULT false;

-- One row per replica. Its id is its member's (and the agent's key for its
-- container). in_rotation says whether the pooler's <db>_ro route sends
-- reads to it: streaming and no more than the lag threshold behind.
CREATE TABLE read_replicas (
  id                  uuid PRIMARY KEY,
  project_id          uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  instance_member_id  uuid REFERENCES instance_members(id),
  node_id             uuid NOT NULL REFERENCES nodes(id),
  region_id           text REFERENCES regions(id),
  size                text NOT NULL,
  status              text NOT NULL DEFAULT 'creating'
                      CHECK (status IN ('creating', 'streaming', 'lagging', 'down', 'deleting', 'detaching', 'detached', 'failed', 'deleted')),
  in_rotation         boolean NOT NULL DEFAULT false,
  lag_bytes           bigint,
  lag_ms              bigint,
  error               text,
  -- The standalone project a detached replica became.
  detached_project_id uuid REFERENCES projects(id) ON DELETE SET NULL,
  rotation_changed_at timestamptz,
  updated_at          timestamptz NOT NULL DEFAULT now(),
  created_at          timestamptz NOT NULL DEFAULT now(),
  deleted_at          timestamptz
);
CREATE INDEX read_replicas_project ON read_replicas (project_id) WHERE deleted_at IS NULL;

-- A replica appearing or going moves the project in the edge feed: the
-- edge reads GETs from <db>_ro while it has some.
CREATE TRIGGER read_replicas_services_touch AFTER INSERT OR UPDATE OF deleted_at ON read_replicas
  FOR EACH ROW EXECUTE FUNCTION project_services_touch();

-- +goose Down
DROP TABLE read_replicas;
ALTER TABLE instance_members DROP COLUMN replica;
