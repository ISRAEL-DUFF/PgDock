-- +goose Up
-- V3 M22: the Free tier's pause and archive (V3 §4) and open signup (§7.4).

-- A project's lifecycle, apart from its status: a paused project's
-- database refuses connections and its pooler route points at the waker;
-- an archived project's database is dropped and kept as an archive
-- backup. Only Free projects pause or archive (§4.3).
ALTER TABLE projects
  ADD COLUMN lifecycle         text NOT NULL DEFAULT 'active' CHECK (lifecycle IN ('active', 'paused', 'archived')),
  -- The last time a client was seen through the poolers (or used the
  -- console); NULL until the first sample, when created_at stands in.
  ADD COLUMN last_active_at    timestamptz,
  ADD COLUMN pause_warned_at   timestamptz,
  ADD COLUMN paused_at         timestamptz,
  ADD COLUMN archived_at       timestamptz,
  ADD COLUMN archive_backup_id uuid REFERENCES backups(id),
  -- The deletion notices sent for an archived project: 30, then 7 (days).
  ADD COLUMN archive_notice_days int;
CREATE INDEX projects_lifecycle ON projects (lifecycle) WHERE deleted_at IS NULL AND lifecycle <> 'active';
-- Existing projects start their week now, rather than from their creation:
-- the poolers' activity wasn't recorded before.
UPDATE projects SET last_active_at = now() WHERE deleted_at IS NULL;

-- An archive backup is kept for as long as the project is archived.
ALTER TABLE backups DROP CONSTRAINT backups_kind_check;
ALTER TABLE backups ADD CONSTRAINT backups_kind_check
  CHECK (kind IN ('logical','base','final','safety','metadata','archive'));

-- Where each account signed up from, for the per-IP signup limit (§7.4).
ALTER TABLE users ADD COLUMN signup_ip inet;
CREATE INDEX users_signup_ip ON users (signup_ip, created_at) WHERE signup_ip IS NOT NULL;

-- +goose Down
DROP INDEX users_signup_ip;
ALTER TABLE users DROP COLUMN signup_ip;
DROP INDEX projects_lifecycle;
ALTER TABLE projects DROP COLUMN archive_notice_days, DROP COLUMN archive_backup_id, DROP COLUMN archived_at,
  DROP COLUMN paused_at, DROP COLUMN pause_warned_at, DROP COLUMN last_active_at, DROP COLUMN lifecycle;
DELETE FROM backups WHERE kind = 'archive';
ALTER TABLE backups DROP CONSTRAINT backups_kind_check;
ALTER TABLE backups ADD CONSTRAINT backups_kind_check CHECK (kind IN ('logical','base','final','safety','metadata'));
