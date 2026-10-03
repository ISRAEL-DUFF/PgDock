-- +goose Up
-- Shared-tier roles created before V2 never got temp_file_limit (V2 §10.4),
-- and re-saving settings doesn't help when nothing changed. Re-apply each
-- active shared project's guardrails once; apply_settings sets the limit.
INSERT INTO operations (kind, project_id)
SELECT 'apply_settings', id FROM projects
WHERE tier = 'shared' AND status = 'active';

-- +goose Down
SELECT 1;
