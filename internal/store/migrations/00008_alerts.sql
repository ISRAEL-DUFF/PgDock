-- Alerts (spec §8.8): one row per firing condition, resolved in place.
-- +goose Up
CREATE TABLE alerts (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  kind                 text NOT NULL,
  key                  text NOT NULL,               -- kind + target; one firing row per key
  severity             text NOT NULL CHECK (severity IN ('warning', 'critical')),
  target_type          text NOT NULL,
  target_id            text NOT NULL,
  target_name          text NOT NULL,
  summary              text NOT NULL,
  detail               jsonb NOT NULL DEFAULT '{}',
  status               text NOT NULL DEFAULT 'firing' CHECK (status IN ('firing', 'resolved')),
  started_at           timestamptz NOT NULL DEFAULT now(),
  last_seen_at         timestamptz NOT NULL DEFAULT now(),
  resolved_at          timestamptz,
  -- Delivery: notified_* set once every configured channel accepted it.
  notified_at          timestamptz,
  resolved_notified_at timestamptz,
  delivery_attempts    int NOT NULL DEFAULT 0,
  delivery_error       text,
  delivery_lock        timestamptz
);
CREATE UNIQUE INDEX alerts_firing_key ON alerts (key) WHERE status = 'firing';
CREATE INDEX alerts_started_at ON alerts (started_at DESC);

-- When a node's agent last answered, for "unreachable for 2+ minutes".
ALTER TABLE nodes ADD COLUMN last_reachable_at timestamptz;

-- +goose Down
ALTER TABLE nodes DROP COLUMN last_reachable_at;
DROP TABLE alerts;
