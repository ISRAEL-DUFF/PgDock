-- +goose Up
-- V4.1-M5: the Postgres version lifecycle (V3 §2.4). Each major is
-- preview (new projects only when asked for), supported, deprecated (a
-- retirement date at least 180 days out, owners notified) or retired (no
-- new projects; existing ones keep running, unsupported). Majors the
-- server is configured with (PGDOCK_PG_VERSIONS) are added as supported
-- when it starts.
CREATE TABLE pg_versions (
  major          int PRIMARY KEY,
  status         text NOT NULL DEFAULT 'supported' CHECK (status IN ('preview', 'supported', 'deprecated', 'retired')),
  deprecated_at  timestamptz,
  retires_at     timestamptz,
  notes          text NOT NULL DEFAULT '',
  updated_at     timestamptz NOT NULL DEFAULT now(),
  CHECK (status <> 'deprecated' OR retires_at IS NOT NULL)
);

-- The notices sent to an organisation about a deprecated major: when it
-- was deprecated (days_before 0) and at 90, 30 and 7 days before it retires.
CREATE TABLE pg_version_notices (
  major        int NOT NULL REFERENCES pg_versions(major) ON DELETE CASCADE,
  org_id       uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  days_before  int NOT NULL,
  sent_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (major, org_id, days_before)
);

-- +goose Down
DROP TABLE pg_version_notices;
DROP TABLE pg_versions;
