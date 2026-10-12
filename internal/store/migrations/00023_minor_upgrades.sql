-- +goose Up
-- V3 M18: automated Postgres minor upgrades in a weekly maintenance window
-- (V3 §2.4). Agents report the release an instance runs and the one its
-- image tag now holds; the sweep recreates instances that are behind.
ALTER TABLE instances
  ADD COLUMN pg_release           text,
  ADD COLUMN pg_release_available text,
  ADD COLUMN release_checked_at   timestamptz;

CREATE TABLE minor_upgrades (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  instance_id  uuid NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  from_release text NOT NULL,
  to_release   text NOT NULL,
  started_at   timestamptz NOT NULL DEFAULT now(),
  finished_at  timestamptz,
  pause_ms     int,
  error        text
);
CREATE INDEX minor_upgrades_started ON minor_upgrades (started_at DESC);

-- +goose Down
DROP TABLE minor_upgrades;
ALTER TABLE instances DROP COLUMN pg_release, DROP COLUMN pg_release_available, DROP COLUMN release_checked_at;
