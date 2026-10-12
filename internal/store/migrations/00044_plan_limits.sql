-- +goose Up
-- V4.1-M2: per-plan limits for backend services (V4 §10). The quota plans
-- get monthly hard limits (Free's data API requests and monthly active
-- users) and ceilings on what a project may set for itself (timeout, rate
-- limits, SMS codes a day). The sweep writes what the edge must apply onto
-- project_services, which moves the feed.
UPDATE quota_plans SET limits = limits || '{"api_requests_per_month": 500000, "auth_mau_per_month": 10000,
  "api_timeout_ms": 5000, "api_rate_per_ip_per_min": 300, "api_rate_per_key_per_min": 3000}' WHERE name = 'Personal';
UPDATE quota_plans SET limits = limits || '{"api_timeout_ms": 8000, "api_rate_per_ip_per_min": 600,
  "api_rate_per_key_per_min": 12000, "sms_codes_per_day": 1000}' WHERE name = 'Pro';
UPDATE quota_plans SET limits = limits || '{"api_timeout_ms": 15000, "api_rate_per_ip_per_min": 1200,
  "api_rate_per_key_per_min": 30000, "sms_codes_per_day": 5000}' WHERE name = 'Team';

ALTER TABLE project_services
  ADD COLUMN plan_timeout_ms        int,
  ADD COLUMN plan_rate_per_ip       int,
  ADD COLUMN plan_rate_per_key      int,
  ADD COLUMN api_requests_blocked   boolean NOT NULL DEFAULT false,
  ADD COLUMN mau_blocked            boolean NOT NULL DEFAULT false,
  -- This month's counted users (a Bloom filter), sent while MAU is blocked
  -- so users already counted can still sign in.
  ADD COLUMN mau_counted            bytea;

-- Installs upgrading now get the limits from the first full month after
-- the release (V4.1 §18 question 1); new installs from now.
INSERT INTO settings (key, value)
SELECT 'plan_limits_from', to_jsonb(CASE WHEN EXISTS (SELECT 1 FROM projects)
  THEN date_trunc('month', now()) + interval '1 month' ELSE now() END)
ON CONFLICT (key) DO NOTHING;

-- One notice per organisation, limit, month and level (80% and 100%).
CREATE TABLE plan_limit_notices (
  org_id   uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  limit_key text NOT NULL,
  month    date NOT NULL,
  level    int  NOT NULL,
  sent_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (org_id, limit_key, month, level)
);

-- +goose Down
DROP TABLE plan_limit_notices;
DELETE FROM settings WHERE key = 'plan_limits_from';
ALTER TABLE project_services DROP COLUMN plan_timeout_ms, DROP COLUMN plan_rate_per_ip, DROP COLUMN plan_rate_per_key,
  DROP COLUMN api_requests_blocked, DROP COLUMN mau_blocked, DROP COLUMN mau_counted;
UPDATE quota_plans SET limits = limits - 'api_requests_per_month' - 'auth_mau_per_month' - 'api_timeout_ms'
  - 'api_rate_per_ip_per_min' - 'api_rate_per_key_per_min' - 'sms_codes_per_day';
