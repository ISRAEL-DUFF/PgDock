-- +goose Up
-- The SQL Editor's saved queries (docs/ui-redesign.md, phase 3): each
-- belongs to a project and its owner, private to them or shared with
-- everyone who can use the project's console. They go with the project
-- when it is deleted, and move with it to another organisation.
CREATE TABLE saved_queries (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id     uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  owner_id   uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
  sql        text NOT NULL CHECK (length(sql) <= 200000),
  visibility text NOT NULL DEFAULT 'private' CHECK (visibility IN ('private', 'shared')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX saved_queries_project ON saved_queries (project_id, updated_at DESC);

CREATE TABLE saved_query_favorites (
  query_id   uuid NOT NULL REFERENCES saved_queries(id) ON DELETE CASCADE,
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  org_id     uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (query_id, user_id)
);

-- +goose Down
DROP TABLE saved_query_favorites;
DROP TABLE saved_queries;
