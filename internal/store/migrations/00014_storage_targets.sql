-- +goose Up
-- V2 s6 (M12): platform and organisation storage targets, per-project
-- backup keys.

-- Org targets belong to one organisation (NULL: a platform target). Names
-- are unique among the platform's targets and within each organisation.
-- Deleting an organisation never touches its targets (no cascade). A
-- deleted target keeps its row (backups reference it) without credentials.
ALTER TABLE storage_targets
  ADD COLUMN org_id     uuid REFERENCES organizations(id),
  ADD COLUMN region     text NOT NULL DEFAULT '',
  ADD COLUMN path_style boolean NOT NULL DEFAULT false,
  ADD COLUMN created_by uuid REFERENCES users(id),
  ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now(),
  ADD COLUMN deleted_at timestamptz;
ALTER TABLE storage_targets DROP CONSTRAINT storage_targets_name_key;
CREATE UNIQUE INDEX storage_targets_platform_name ON storage_targets (name) WHERE org_id IS NULL AND deleted_at IS NULL;
CREATE UNIQUE INDEX storage_targets_org_name ON storage_targets (org_id, name) WHERE org_id IS NOT NULL AND deleted_at IS NULL;
ALTER TABLE storage_targets ADD CONSTRAINT storage_targets_default_is_platform
  CHECK (NOT is_default OR (org_id IS NULL AND deleted_at IS NULL));
-- The V1 default target was named "default"; region and path style were
-- only inside the sealed credentials and are filled in on the next save.

-- Per-project backup keys (NULL project: the instance key, which stays in
-- settings). key_enc is the 32-byte secret sealed with the master key; the
-- project's OpenPGP key is derived from it.
CREATE TABLE backup_keys (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id    uuid REFERENCES projects(id),
  key_enc       bytea NOT NULL,
  fingerprint   text NOT NULL,
  created_by    uuid REFERENCES users(id),
  created_at    timestamptz NOT NULL DEFAULT now(),
  retired_at    timestamptz
);
CREATE INDEX backup_keys_project ON backup_keys (project_id, created_at DESC);

-- A project's target (NULL: the platform default) and key (NULL: the
-- instance key).
ALTER TABLE projects ADD COLUMN backup_key_id uuid REFERENCES backup_keys(id);
-- Which key encrypted each backup (NULL: the instance key) and, for base
-- backups, the WAL-G prefix they live under.
ALTER TABLE backups
  ADD COLUMN encryption_key_id uuid REFERENCES backup_keys(id),
  ADD COLUMN walg_prefix       text;
-- A backup copied to another target: the copy is a new row; the original
-- stays (status 'copied') until the user deletes it.
ALTER TABLE backups ADD COLUMN copy_of uuid REFERENCES backups(id);
ALTER TABLE backups DROP CONSTRAINT backups_status_check;
ALTER TABLE backups ADD CONSTRAINT backups_status_check
  CHECK (status IN ('running','succeeded','failed','deleted','copied'));
CREATE INDEX backups_target ON backups (storage_target_id) WHERE status IN ('succeeded','copied');
-- Where a dedicated instance archives now, and with which key (NULL target:
-- the default at the time; NULL key: the instance key).
ALTER TABLE instances
  ADD COLUMN walg_target_id uuid REFERENCES storage_targets(id),
  ADD COLUMN walg_key_id    uuid REFERENCES backup_keys(id);
UPDATE instances i SET walg_target_id = (SELECT id FROM storage_targets WHERE is_default)
WHERE i.walg_prefix IS NOT NULL;
UPDATE backups b SET walg_prefix = i.walg_prefix
FROM projects p JOIN instances i ON i.id = p.instance_id
WHERE b.kind = 'base' AND b.project_id = p.id;

-- +goose Down
UPDATE backups SET status = 'deleted' WHERE status = 'copied';
ALTER TABLE instances DROP COLUMN walg_target_id, DROP COLUMN walg_key_id;
DROP INDEX backups_target;
ALTER TABLE backups DROP CONSTRAINT backups_status_check;
ALTER TABLE backups ADD CONSTRAINT backups_status_check
  CHECK (status IN ('running','succeeded','failed','deleted'));
ALTER TABLE backups DROP COLUMN copy_of, DROP COLUMN encryption_key_id, DROP COLUMN walg_prefix;
ALTER TABLE projects DROP COLUMN backup_key_id;
DROP TABLE backup_keys;
ALTER TABLE storage_targets DROP CONSTRAINT storage_targets_default_is_platform;
DROP INDEX storage_targets_org_name;
DROP INDEX storage_targets_platform_name;
ALTER TABLE storage_targets ADD CONSTRAINT storage_targets_name_key UNIQUE (name);
ALTER TABLE storage_targets DROP COLUMN org_id, DROP COLUMN region, DROP COLUMN path_style,
  DROP COLUMN created_by, DROP COLUMN updated_at, DROP COLUMN deleted_at;
