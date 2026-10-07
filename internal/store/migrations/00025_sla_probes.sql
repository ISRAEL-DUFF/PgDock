-- +goose Up
-- V3 M19: SLA probes (V3 §2.7). Each HA project has a login that can only
-- connect and run SELECT 1, accepted by the poolers like any other.
ALTER TABLE projects ADD COLUMN probe_verifier text;

-- A minute is unavailable when every vantage point that probed in it
-- failed; with no result at all it isn't counted against the project.
ALTER TABLE availability_minutes DROP COLUMN available;
ALTER TABLE availability_minutes ADD COLUMN available boolean
  GENERATED ALWAYS AS (internal_ok IS TRUE OR external_ok IS TRUE OR (internal_ok IS NULL AND external_ok IS NULL)) STORED;

-- +goose Down
ALTER TABLE availability_minutes DROP COLUMN available;
ALTER TABLE availability_minutes ADD COLUMN available boolean NOT NULL DEFAULT true;
ALTER TABLE projects DROP COLUMN probe_verifier;
