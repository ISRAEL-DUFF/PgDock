-- +goose Up
-- V2 s4.3: each user's table-editor preferences per project (the migration
-- format they export).
CREATE TABLE editor_preferences (
  project_id       uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  user_id          uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  org_id           uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  migration_format text NOT NULL DEFAULT 'sql' CHECK (migration_format IN ('sql', 'goose', 'dbmate')),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (project_id, user_id)
);

-- +goose Down
DROP TABLE editor_preferences;
