-- +goose Up
-- V4.1-M3: billing add-ons (V3 §3.1). A dedicated instance's point-in-time
-- recovery window (7, 14 or 30 days) and a project's backup retention
-- (standard, extended or long, in projects.settings) are chosen per
-- project, within the plan's limits.
ALTER TABLE instances ADD COLUMN pitr_days int NOT NULL DEFAULT 7 CHECK (pitr_days IN (7, 14, 30));

UPDATE quota_plans SET limits = limits || '{"pitr_days_max": 7, "backup_retention_max": 0}' WHERE name = 'Personal';
UPDATE quota_plans SET limits = limits || '{"pitr_days_max": 30, "backup_retention_max": 2}' WHERE name IN ('Pro', 'Team');

-- +goose Down
UPDATE quota_plans SET limits = limits - 'pitr_days_max' - 'backup_retention_max';
ALTER TABLE instances DROP COLUMN pitr_days;
