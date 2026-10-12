-- +goose Up
-- V4-M28: backend services' edge foundation (V4 §2). A project with backend
-- services enabled gets a short reference (its API hostname), publishable
-- and secret API keys, an ES256 signing key for its users' tokens, and four
-- roles in its database. pgdock-edge serves it from a per-region cache of
-- this configuration, which it follows by changed_seq.
CREATE SEQUENCE edge_config_seq;

CREATE TABLE project_services (
  project_id      uuid PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  ref             text UNIQUE NOT NULL CHECK (ref ~ '^[a-z][a-z0-9]{7}$'),
  enabled         boolean NOT NULL DEFAULT false,
  exposed_schemas text[] NOT NULL DEFAULT '{public}',
  public_tables   text[] NOT NULL DEFAULT '{}',
  cors_origins    text[] NOT NULL DEFAULT '{}',
  settings        jsonb NOT NULL DEFAULT '{}',
  -- The edge login's SCRAM verifier, for the poolers' auth file.
  edge_verifier   text,
  -- The version of the pgd_* schemas applied to the project database, and
  -- the instance they were applied on (a move re-applies the roles).
  schema_version  int NOT NULL DEFAULT 0,
  roles_instance  uuid,
  config_version  bigint NOT NULL DEFAULT 1,
  changed_seq     bigint NOT NULL DEFAULT nextval('edge_config_seq'),
  enabled_at      timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX project_services_changed ON project_services (changed_seq);

CREATE TABLE project_api_keys (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id   uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  kind         text NOT NULL CHECK (kind IN ('publishable', 'secret')),
  name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 64),
  key_hash     text UNIQUE NOT NULL,
  prefix       text NOT NULL,
  -- The whole publishable key (it is meant to be embedded in apps); never
  -- a secret key, which is shown once.
  display      text,
  last_used_at timestamptz,
  revoked_at   timestamptz,
  created_by   uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  CHECK (kind = 'publishable' OR display IS NULL)
);
CREATE INDEX project_api_keys_project ON project_api_keys (project_id);

CREATE TABLE project_jwt_keys (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id  uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  kid         text UNIQUE NOT NULL,
  algorithm   text NOT NULL DEFAULT 'ES256' CHECK (algorithm = 'ES256'),
  public_jwk  jsonb NOT NULL,
  private_enc bytea NOT NULL,
  status      text NOT NULL CHECK (status IN ('active', 'verifying', 'retired')),
  created_at  timestamptz NOT NULL DEFAULT now(),
  retired_at  timestamptz
);
CREATE INDEX project_jwt_keys_project ON project_jwt_keys (project_id);
CREATE UNIQUE INDEX project_jwt_keys_active ON project_jwt_keys (project_id) WHERE status = 'active';

-- API request logs (V4 §8.3), kept 7 days.
CREATE TABLE api_request_logs (
  id          bigserial PRIMARY KEY,
  project_id  uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  at          timestamptz NOT NULL,
  request_id  text NOT NULL,
  method      text NOT NULL,
  path        text NOT NULL,
  status      int NOT NULL,
  latency_ms  int NOT NULL,
  role        text,
  user_id     uuid,
  key_id      uuid,
  ip          text,
  bytes_out   bigint NOT NULL DEFAULT 0
);
CREATE INDEX api_request_logs_project ON api_request_logs (project_id, at DESC);
CREATE INDEX api_request_logs_at ON api_request_logs (at);

-- Reports from the edges, by batch, so a retried report counts once.
CREATE TABLE edge_reports (
  batch_id    text PRIMARY KEY,
  edge        text NOT NULL,
  received_at timestamptz NOT NULL DEFAULT now()
);

-- Every change the edge cares about moves a project's row to the end of
-- edge_config_seq: its own settings, keys and signing keys, and the
-- project's and organisation's state.
-- +goose StatementBegin
CREATE FUNCTION project_services_bump() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  NEW.changed_seq := nextval('edge_config_seq');
  NEW.config_version := OLD.config_version + 1;
  RETURN NEW;
END $$;

CREATE FUNCTION project_services_touch() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  pid uuid;
BEGIN
  IF TG_OP = 'DELETE' THEN pid := OLD.project_id; ELSE pid := NEW.project_id; END IF;
  UPDATE project_services SET config_version = config_version WHERE project_id = pid;
  RETURN NULL;
END $$;

CREATE FUNCTION project_services_touch_project() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  UPDATE project_services SET config_version = config_version WHERE project_id = NEW.id;
  RETURN NULL;
END $$;

CREATE FUNCTION project_services_touch_org() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  UPDATE project_services s SET config_version = s.config_version
  FROM projects p WHERE p.id = s.project_id AND p.org_id = NEW.id;
  RETURN NULL;
END $$;
-- +goose StatementEnd

CREATE TRIGGER project_services_bump BEFORE UPDATE ON project_services
  FOR EACH ROW EXECUTE FUNCTION project_services_bump();
-- Not on last_used_at: the edge's reports set it.
CREATE TRIGGER project_api_keys_touch AFTER INSERT OR DELETE OR UPDATE OF revoked_at, key_hash, kind ON project_api_keys
  FOR EACH ROW EXECUTE FUNCTION project_services_touch();
CREATE TRIGGER project_jwt_keys_touch AFTER INSERT OR UPDATE OR DELETE ON project_jwt_keys
  FOR EACH ROW EXECUTE FUNCTION project_services_touch();
CREATE TRIGGER projects_services_touch AFTER UPDATE OF lifecycle, status, deleted_at, region, org_id, db_name ON projects
  FOR EACH ROW EXECUTE FUNCTION project_services_touch_project();
CREATE TRIGGER organizations_services_touch AFTER UPDATE OF status ON organizations
  FOR EACH ROW EXECUTE FUNCTION project_services_touch_org();

-- +goose Down
DROP TRIGGER organizations_services_touch ON organizations;
DROP TRIGGER projects_services_touch ON projects;
DROP TABLE edge_reports;
DROP TABLE api_request_logs;
DROP TABLE project_jwt_keys;
DROP TABLE project_api_keys;
DROP TABLE project_services;
DROP FUNCTION project_services_touch_org();
DROP FUNCTION project_services_touch_project();
DROP FUNCTION project_services_touch();
DROP FUNCTION project_services_bump();
DROP SEQUENCE edge_config_seq;
