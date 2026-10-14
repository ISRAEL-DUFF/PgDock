-- Legal documents (V3 §7.3).

-- name: CurrentLegalDocument :one
-- tenant: system - the platform-wide SLA or DPA in effect, public.
SELECT * FROM legal_documents WHERE kind = @kind AND org_id IS NULL ORDER BY version DESC LIMIT 1;

-- name: CurrentOrderForm :one
SELECT * FROM legal_documents WHERE kind = 'order_form' AND org_id = @org_id ORDER BY version DESC LIMIT 1;

-- name: GetLegalDocument :one
-- tenant: system - a document the caller checks belongs to its org or is platform-wide.
SELECT * FROM legal_documents WHERE id = @id;

-- name: InsertLegalDocument :one
-- tenant: system - the platform admin publishes a version (an order form names its org).
INSERT INTO legal_documents (kind, org_id, version, title, body_md, published_by)
VALUES (@kind, sqlc.narg(org_id), (SELECT coalesce(max(version), 0) + 1 FROM legal_documents
  WHERE kind = @kind AND org_id IS NOT DISTINCT FROM sqlc.narg(org_id)), @title, @body_md, sqlc.narg(published_by))
RETURNING *;

-- name: AcceptLegalDocument :exec
INSERT INTO legal_acceptances (document_id, org_id, user_id, ip) VALUES (@document_id, @org_id, sqlc.narg(user_id), sqlc.narg(ip))
ON CONFLICT (document_id, org_id) DO NOTHING;

-- name: OrgLegalAcceptance :one
SELECT a.*, u.email AS accepted_by FROM legal_acceptances a LEFT JOIN users u ON u.id = a.user_id
WHERE a.document_id = @document_id AND a.org_id = @org_id;

-- name: LegalAcceptances :many
-- tenant: system - the platform admin's record of who accepted a document.
SELECT a.*, o.name AS org_name, u.email AS accepted_by FROM legal_acceptances a
JOIN organizations o ON o.id = a.org_id LEFT JOIN users u ON u.id = a.user_id
WHERE a.document_id = @document_id ORDER BY a.accepted_at DESC;

-- name: ListLegalVersions :many
-- tenant: system - the platform admin's history of platform-wide documents.
SELECT id, kind, version, title, published_at, (SELECT count(*) FROM legal_acceptances a WHERE a.document_id = d.id)::bigint AS acceptances
FROM legal_documents d WHERE org_id IS NULL ORDER BY kind, version DESC;
