-- Backups, restore, import, and agents (M3).

-- +goose Up
-- Backups may belong to no project (metadata self-backups) and record the
-- wrapped per-object file key, so the control plane can restore without
-- reading the object header first.
ALTER TABLE backups ALTER COLUMN project_id DROP NOT NULL;
ALTER TABLE backups DROP CONSTRAINT backups_kind_check;
ALTER TABLE backups ADD CONSTRAINT backups_kind_check
  CHECK (kind IN ('logical','base','final','safety','metadata'));
ALTER TABLE backups
  ADD COLUMN storage_target_id uuid REFERENCES storage_targets(id),
  ADD COLUMN operation_id      uuid REFERENCES operations(id),
  ADD COLUMN key_wrapped       bytea,
  ADD COLUMN error             text,
  ADD COLUMN deleted_at        timestamptz;   -- object removed by retention
ALTER TABLE backups ADD CONSTRAINT backups_status_check
  CHECK (status IN ('running','succeeded','failed','deleted'));
CREATE INDEX backups_project ON backups (project_id, started_at DESC);
CREATE INDEX backups_kind ON backups (kind, started_at DESC);

-- Agents: where the agent listens (it may differ from the Postgres host the
-- poolers use), its version, and one-time registration tokens.
ALTER TABLE nodes
  ADD COLUMN agent_host          text,
  ADD COLUMN agent_version       text,
  ADD COLUMN registration_token  text,          -- SHA-256 of the one-time token
  ADD COLUMN registration_expires_at timestamptz;

-- +goose Down
ALTER TABLE nodes DROP COLUMN agent_host, DROP COLUMN agent_version,
  DROP COLUMN registration_token, DROP COLUMN registration_expires_at;
DROP INDEX backups_kind;
DROP INDEX backups_project;
ALTER TABLE backups DROP CONSTRAINT backups_status_check;
ALTER TABLE backups DROP COLUMN storage_target_id, DROP COLUMN operation_id,
  DROP COLUMN key_wrapped, DROP COLUMN error, DROP COLUMN deleted_at;
ALTER TABLE backups DROP CONSTRAINT backups_kind_check;
ALTER TABLE backups ADD CONSTRAINT backups_kind_check CHECK (kind IN ('logical','base','final','safety'));
ALTER TABLE backups ALTER COLUMN project_id SET NOT NULL;
