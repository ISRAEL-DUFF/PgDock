-- +goose Up
-- V4.1-M7: maintenance PGDock proposes (V3.1 §4.1, open question 3). When
-- the window gate holds work back, PGDock drafts an announcement for the
-- first window at least 96 hours away; the admin confirms it (announced
-- from then on) or discards it. A draft is never pushed, never emailed,
-- and never excludes anything: announced_at stays unset until confirmed.
ALTER TABLE incidents DROP CONSTRAINT incidents_status_check;
ALTER TABLE incidents ADD CONSTRAINT incidents_status_check
  CHECK (status IN ('draft', 'investigating', 'identified', 'monitoring', 'resolved'));
ALTER TABLE incidents ADD CONSTRAINT incidents_draft_maintenance
  CHECK (status <> 'draft' OR (severity = 'maintenance' AND announced_at IS NULL));
-- What the draft was made for (e.g. "minor_upgrade"); null for the admin's.
ALTER TABLE incidents ADD COLUMN proposed_for text;

-- +goose Down
UPDATE incidents SET status = 'resolved', resolved_at = coalesce(resolved_at, now()) WHERE status = 'draft';
ALTER TABLE incidents DROP COLUMN proposed_for;
ALTER TABLE incidents DROP CONSTRAINT incidents_draft_maintenance;
ALTER TABLE incidents DROP CONSTRAINT incidents_status_check;
ALTER TABLE incidents ADD CONSTRAINT incidents_status_check
  CHECK (status IN ('investigating', 'identified', 'monitoring', 'resolved'));
