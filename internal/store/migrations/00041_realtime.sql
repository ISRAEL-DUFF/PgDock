-- +goose Up
-- V4-M34: backend services' realtime (V4 §6). Captured changes, private
-- channel policies and broadcast history live in each project's database
-- (pgd_realtime, schema version 5); pgdock-edge serves the WebSockets.

-- What the edge applies, recomputed by the realtime sweep: concurrent
-- connections per edge process (null is unlimited) and whether the month's
-- messages are used up. A change moves the project in the edge feed.
ALTER TABLE project_services
  ADD COLUMN realtime_max_connections  int,
  ADD COLUMN realtime_messages_blocked boolean NOT NULL DEFAULT false;

-- The plans' realtime limits (V4 §10.1): connections on every plan; the
-- free plan's messages stop at their monthly allowance, paid plans' are
-- metered past it.
UPDATE quota_plans SET limits = limits || '{"realtime_connections": 100, "realtime_messages_per_month": 1000000}' WHERE name = 'Personal';
UPDATE quota_plans SET limits = limits || '{"realtime_connections": 1000}' WHERE name = 'Pro';
UPDATE quota_plans SET limits = limits || '{"realtime_connections": 5000}' WHERE name = 'Team';

-- +goose Down
UPDATE quota_plans SET limits = limits - 'realtime_connections' - 'realtime_messages_per_month';
ALTER TABLE project_services DROP COLUMN realtime_max_connections, DROP COLUMN realtime_messages_blocked;
