-- Promotion (M5): the shared copy a promoted project leaves behind, kept
-- read-only for 48 hours as a rollback option, then dropped.

-- +goose Up
CREATE TABLE retired_databases (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id  uuid NOT NULL REFERENCES projects(id),
  instance_id uuid NOT NULL REFERENCES instances(id),
  db_name     text NOT NULL,
  owner_role  text NOT NULL,
  reason      text NOT NULL,
  drop_after  timestamptz NOT NULL,
  dropped_at  timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX retired_databases_due ON retired_databases (drop_after) WHERE dropped_at IS NULL;

-- +goose Down
DROP TABLE retired_databases;
