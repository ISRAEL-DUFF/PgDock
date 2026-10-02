-- +goose Up
-- V2 s7: API tokens for the CLI, CI and scripts, and the device login that
-- issues them.

CREATE TABLE api_tokens (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id            uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  org_id             uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  name               text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
  token_hash         text UNIQUE NOT NULL,   -- sha256 hex
  prefix             text NOT NULL,          -- first characters, for display
  scopes             text[] NOT NULL CHECK (cardinality(scopes) > 0 AND scopes <@ ARRAY['read', 'write', 'admin']),
  project_ids        uuid[],                 -- NULL = every project the user can access in org_id
  expires_at         timestamptz NOT NULL,
  last_used_at       timestamptz,
  last_used_ip       inet,
  revoked_at         timestamptz,
  revoked_by         uuid REFERENCES users(id) ON DELETE SET NULL,
  created_via        text NOT NULL DEFAULT 'ui' CHECK (created_via IN ('ui', 'device', 'api')),
  expiry_notified_at timestamptz,
  created_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX api_tokens_user ON api_tokens (user_id, created_at DESC);
CREATE INDEX api_tokens_org ON api_tokens (org_id, created_at DESC);

CREATE TABLE device_auth_requests (
  device_code_hash text PRIMARY KEY,
  user_code        text UNIQUE NOT NULL,
  client_name      text NOT NULL DEFAULT '',
  requested_scopes text[] NOT NULL,
  org_id           uuid REFERENCES organizations(id) ON DELETE CASCADE,
  approved_by      uuid REFERENCES users(id) ON DELETE CASCADE,
  token_id         uuid REFERENCES api_tokens(id) ON DELETE CASCADE,
  -- The new token, sealed with the master key, until the CLI collects it.
  sealed_token     bytea,
  denied_at        timestamptz,
  last_polled_at   timestamptz,
  expires_at       timestamptz NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE device_auth_requests;
DROP TABLE api_tokens;
