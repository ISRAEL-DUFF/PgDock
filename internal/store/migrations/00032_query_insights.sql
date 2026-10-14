-- +goose Up
-- V3 M26: query insights (V3 §8).

-- Per query per project, the work done in each bucket: 5-minute buckets
-- for the last day, rolled up to hours for 30 days. Sums, so any range
-- adds up across both.
CREATE TABLE query_stats (
  project_id       uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  queryid          bigint NOT NULL,
  bucket           timestamptz NOT NULL,
  calls            bigint NOT NULL,
  total_ms         double precision NOT NULL,
  rows             bigint NOT NULL,
  shared_blks_hit  bigint NOT NULL,
  shared_blks_read bigint NOT NULL,
  max_ms           double precision NOT NULL DEFAULT 0,
  PRIMARY KEY (project_id, queryid, bucket)
);
CREATE INDEX query_stats_bucket ON query_stats (bucket);

-- The normalised text of each query, and the latest example with its
-- literals, captured from pg_stat_activity, for EXPLAIN.
CREATE TABLE query_texts (
  project_id  uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  queryid     bigint NOT NULL,
  query       text NOT NULL,
  example     text,
  example_at  timestamptz,
  first_seen  timestamptz NOT NULL DEFAULT now(),
  last_seen   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (project_id, queryid)
);

-- The last cumulative pg_stat_statements counters per instance, to take
-- deltas from.
CREATE TABLE query_snapshots (
  instance_id      uuid NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  dbid             bigint NOT NULL,
  userid           bigint NOT NULL,
  queryid          bigint NOT NULL,
  toplevel         boolean NOT NULL,
  calls            bigint NOT NULL,
  total_ms         double precision NOT NULL,
  rows             bigint NOT NULL,
  shared_blks_hit  bigint NOT NULL,
  shared_blks_read bigint NOT NULL,
  max_ms           double precision NOT NULL,
  taken_at         timestamptz NOT NULL,
  PRIMARY KEY (instance_id, dbid, userid, queryid, toplevel)
);

-- Statements over the slow-query threshold: seen running at a snapshot,
-- a new maximum between snapshots, or cancelled by the reaper (V3 §8).
CREATE TABLE slow_queries (
  id          bigserial PRIMARY KEY,
  project_id  uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  queryid     bigint,
  query       text NOT NULL,
  duration_ms double precision NOT NULL,
  source      text NOT NULL CHECK (source IN ('running', 'snapshot', 'reaper')),
  role_name   text NOT NULL DEFAULT '',
  seen_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX slow_queries_project ON slow_queries (project_id, seen_at DESC);

-- +goose Down
DROP TABLE slow_queries;
DROP TABLE query_snapshots;
DROP TABLE query_texts;
DROP TABLE query_stats;
