-- +goose Up
-- V2 s8 (M13): branches are shared-tier projects with a parent. They
-- expire at expires_at (NULL: kept until deleted), skip nightly backups
-- unless branch_backups, and copy the parent's data from its latest
-- backup or live, optionally schema only.
ALTER TABLE projects
  ADD COLUMN parent_project_id  uuid REFERENCES projects(id),
  ADD COLUMN branch_source      text CHECK (branch_source IN ('backup','live')),
  ADD COLUMN branch_schema_only boolean,
  ADD COLUMN expires_at         timestamptz,
  ADD COLUMN expiry_notified_at timestamptz,
  ADD COLUMN branch_backups     boolean NOT NULL DEFAULT false,
  ADD COLUMN sensitive_data     boolean NOT NULL DEFAULT false;
CREATE INDEX projects_parent ON projects (parent_project_id) WHERE parent_project_id IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX projects_expiry ON projects (expires_at) WHERE expires_at IS NOT NULL AND deleted_at IS NULL;
-- An organisation can make "contains sensitive data" the default for new
-- projects: organizations.settings.sensitive_by_default (no column).

-- +goose Down
DROP INDEX projects_expiry;
DROP INDEX projects_parent;
ALTER TABLE projects DROP COLUMN parent_project_id, DROP COLUMN branch_source, DROP COLUMN branch_schema_only,
  DROP COLUMN expires_at, DROP COLUMN expiry_notified_at, DROP COLUMN branch_backups, DROP COLUMN sensitive_data;
