-- +goose Up
-- V4.1-M10: cost attribution for backend services (V4.1 §11). Nodes can be
-- dedicated edge nodes, capacity can propose them, and edges report their
-- CPU so a region's sustained load can.
ALTER TABLE nodes DROP CONSTRAINT nodes_role_check,
  ADD CONSTRAINT nodes_role_check CHECK (role IN ('shared', 'dedicated', 'both', 'pooler', 'edge'));
ALTER TABLE capacity_proposals DROP CONSTRAINT capacity_proposals_tier_check,
  ADD CONSTRAINT capacity_proposals_tier_check CHECK (tier IN ('shared', 'dedicated', 'edge'));

-- Each edge report's CPU: the edge process's share of its host's CPUs
-- since the previous report. Kept two days.
CREATE TABLE edge_cpu_samples (
  edge        text NOT NULL,
  region      text NOT NULL DEFAULT '',
  at          timestamptz NOT NULL DEFAULT now(),
  cpu_percent real NOT NULL
);
CREATE INDEX edge_cpu_samples_region ON edge_cpu_samples (region, at);

-- +goose Down
DROP TABLE edge_cpu_samples;
DELETE FROM capacity_proposals WHERE tier = 'edge';
ALTER TABLE capacity_proposals DROP CONSTRAINT capacity_proposals_tier_check,
  ADD CONSTRAINT capacity_proposals_tier_check CHECK (tier IN ('shared', 'dedicated'));
UPDATE nodes SET role = 'shared' WHERE role = 'edge';
ALTER TABLE nodes DROP CONSTRAINT nodes_role_check,
  ADD CONSTRAINT nodes_role_check CHECK (role IN ('shared', 'dedicated', 'both', 'pooler'));
