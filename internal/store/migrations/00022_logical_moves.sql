-- +goose Up
-- V3 M18: logical-replication moves (V3 §2.3) and major upgrades (§2.4).
ALTER TABLE projects DROP CONSTRAINT projects_status_check;
ALTER TABLE projects ADD CONSTRAINT projects_status_check CHECK (status IN
  ('provisioning','active','promoting','demoting','moving','upgrading','restoring','deleting','deleted','error'));

-- One row per move of a project's database between instances, by
-- whichever operation ran it (promotion, demotion, node move, upgrade).
CREATE TABLE moves (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  operation_id    uuid NOT NULL REFERENCES operations(id) ON DELETE CASCADE,
  project_id      uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  source_instance uuid NOT NULL REFERENCES instances(id),
  target_instance uuid NOT NULL REFERENCES instances(id),
  mode            text NOT NULL CHECK (mode IN ('logical', 'dump')),
  fallback_reason text,
  phase           text NOT NULL DEFAULT 'preparing'
                  CHECK (phase IN ('preparing', 'copying', 'streaming', 'cutover', 'done', 'failed')),
  tables_total    int,
  tables_ready    int,
  lag_bytes       bigint,
  freeze_ms       int,
  started_at      timestamptz NOT NULL DEFAULT now(),
  finished_at     timestamptz,
  UNIQUE (operation_id)
);
CREATE INDEX moves_project ON moves (project_id, started_at DESC);

-- +goose Down
DROP TABLE moves;
UPDATE projects SET status = 'active' WHERE status IN ('moving', 'upgrading');
ALTER TABLE projects DROP CONSTRAINT projects_status_check;
ALTER TABLE projects ADD CONSTRAINT projects_status_check CHECK (status IN
  ('provisioning','active','promoting','demoting','restoring','deleting','deleted','error'));
