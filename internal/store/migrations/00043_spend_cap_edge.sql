-- +goose Up
-- V4-M37: a spend cap throttles backend services (V4 §12). The edge reads
-- the organisation's capped flag from its configuration feed, so a change
-- to it moves each of the organisation's projects to the end of the feed.
-- +goose StatementBegin
CREATE FUNCTION project_services_touch_billing() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'UPDATE' AND OLD.capped IS NOT DISTINCT FROM NEW.capped THEN RETURN NULL; END IF;
  UPDATE project_services s SET config_version = s.config_version
  FROM projects p WHERE p.id = s.project_id AND p.org_id = NEW.org_id;
  RETURN NULL;
END $$;
-- +goose StatementEnd

CREATE TRIGGER billing_accounts_services_touch AFTER INSERT OR UPDATE OF capped ON billing_accounts
  FOR EACH ROW EXECUTE FUNCTION project_services_touch_billing();

-- +goose Down
DROP TRIGGER billing_accounts_services_touch ON billing_accounts;
DROP FUNCTION project_services_touch_billing();
