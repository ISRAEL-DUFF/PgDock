-- +goose Up
-- V3.1-M3: announced maintenance (V3.1 §4). A maintenance announcement is
-- a status page incident of severity maintenance with a scheduled window,
-- a region (or all), and optionally the projects or nodes it covers. An HA
-- project's availability minute inside an announcement made at least 72
-- hours before it is excluded from the SLA, and says which one.
ALTER TABLE incidents
  ADD COLUMN scheduled_start timestamptz,
  ADD COLUMN scheduled_end   timestamptz,
  ADD COLUMN announced_at    timestamptz,
  ADD COLUMN cancelled_at    timestamptz,
  ADD COLUMN replaces        uuid REFERENCES incidents(id),
  ADD CONSTRAINT incidents_schedule CHECK (scheduled_start IS NULL OR scheduled_end > scheduled_start);
CREATE INDEX incidents_scheduled ON incidents (scheduled_start) WHERE scheduled_start IS NOT NULL;

CREATE TABLE incident_scope (
  incident_id uuid NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
  project_id  uuid REFERENCES projects(id) ON DELETE CASCADE,
  node_id     uuid REFERENCES nodes(id) ON DELETE CASCADE,
  CHECK (project_id IS NOT NULL OR node_id IS NOT NULL)
);
CREATE INDEX incident_scope_incident ON incident_scope (incident_id);

ALTER TABLE availability_minutes ADD COLUMN excluded_by uuid REFERENCES incidents(id) ON DELETE SET NULL;

-- Whether announcement i covers project p: its named projects, or the
-- nodes p's members are on; with neither named, its region (all regions
-- when none).
-- +goose StatementBegin
CREATE FUNCTION maintenance_covers(i incidents, p projects) RETURNS boolean LANGUAGE sql STABLE AS $$
  SELECT CASE WHEN EXISTS (SELECT 1 FROM incident_scope s WHERE s.incident_id = i.id)
    THEN EXISTS (SELECT 1 FROM incident_scope s WHERE s.incident_id = i.id AND (
           s.project_id = p.id
           OR s.node_id IN (SELECT m.node_id FROM instance_members m WHERE m.instance_id = p.instance_id AND m.deleted_at IS NULL)
           OR s.node_id = (SELECT n.node_id FROM instances n WHERE n.id = p.instance_id)))
    ELSE i.region_id IS NULL OR i.region_id = p.region END
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION maintenance_covers(incidents, projects);
ALTER TABLE availability_minutes DROP COLUMN excluded_by;
DROP TABLE incident_scope;
ALTER TABLE incidents DROP CONSTRAINT incidents_schedule, DROP COLUMN replaces, DROP COLUMN cancelled_at,
  DROP COLUMN announced_at, DROP COLUMN scheduled_end, DROP COLUMN scheduled_start;
