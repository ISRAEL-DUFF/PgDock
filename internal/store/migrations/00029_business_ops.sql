-- +goose Up
-- V3 M23: support, the revenue dashboard, and legal documents (V3 §7).

-- Support staff (§7.1): see the support console and organisations'
-- metadata; change nothing.
ALTER TABLE users DROP CONSTRAINT users_platform_role_check;
ALTER TABLE users ADD CONSTRAINT users_platform_role_check CHECK (platform_role IN ('platform_admin', 'support', 'user'));

-- Tickets (§7.1), from the dashboard, email or WhatsApp. number is the
-- reference people quote ("T-1042") and that threads email replies.
CREATE SEQUENCE ticket_numbers START 1001;
CREATE TABLE tickets (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  number            bigint UNIQUE NOT NULL DEFAULT nextval('ticket_numbers'),
  org_id            uuid REFERENCES organizations(id) ON DELETE SET NULL,
  requester         citext NOT NULL,          -- an email address, or a WhatsApp number
  requester_name    text,
  requester_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
  channel           text NOT NULL CHECK (channel IN ('dashboard', 'email', 'whatsapp')),
  subject           text NOT NULL,
  status            text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'pending', 'solved', 'closed')),
  priority          text NOT NULL DEFAULT 'normal' CHECK (priority IN ('low', 'normal', 'high', 'urgent')),
  plan              text,                      -- the org's plan when opened: the response target
  assignee          uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  first_response_at timestamptz,
  respond_by        timestamptz                -- the plan's response target, in business hours
);
CREATE INDEX tickets_org ON tickets (org_id, created_at DESC);
CREATE INDEX tickets_open ON tickets (status, respond_by) WHERE status IN ('open', 'pending');

CREATE TABLE ticket_messages (
  id             bigserial PRIMARY KEY,
  ticket_id      uuid NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  direction      text NOT NULL CHECK (direction IN ('in', 'out', 'note')),  -- from the customer, to them, internal
  author         text NOT NULL,              -- an email address, a phone number, or a staff member's email
  author_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
  body           text NOT NULL,
  attachments    jsonb NOT NULL DEFAULT '[]',
  external_id    text,                       -- an email Message-ID or a WhatsApp message id
  created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX ticket_messages_external ON ticket_messages (external_id) WHERE external_id IS NOT NULL;
CREATE INDEX ticket_messages_ticket ON ticket_messages (ticket_id, id);

-- WhatsApp numbers an organisation registered (§7.1), E.164.
CREATE TABLE org_support_phones (
  phone      text PRIMARY KEY CHECK (phone ~ '^\+[1-9][0-9]{6,14}$'),
  org_id     uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  added_by   uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX org_support_phones_org ON org_support_phones (org_id);

-- Legal documents (§7.3). The terms, privacy notice and acceptable use
-- policy are one versioned set every user accepts (terms_versions); the
-- SLA, the DPA and an organisation's order form are accepted for the
-- organisation by an owner.
ALTER TABLE terms_versions ADD COLUMN aup_md text NOT NULL DEFAULT '';
CREATE TABLE legal_documents (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  kind         text NOT NULL CHECK (kind IN ('sla', 'dpa', 'order_form')),
  org_id       uuid REFERENCES organizations(id) ON DELETE CASCADE, -- order forms only
  version      int NOT NULL,
  title        text NOT NULL,
  body_md      text NOT NULL,
  published_by uuid REFERENCES users(id) ON DELETE SET NULL,
  published_at timestamptz NOT NULL DEFAULT now(),
  CHECK ((kind = 'order_form') = (org_id IS NOT NULL))
);
CREATE UNIQUE INDEX legal_documents_version ON legal_documents (kind, coalesce(org_id, '00000000-0000-0000-0000-000000000000'::uuid), version);
CREATE TABLE legal_acceptances (
  document_id uuid NOT NULL REFERENCES legal_documents(id) ON DELETE CASCADE,
  org_id      uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  user_id     uuid REFERENCES users(id) ON DELETE SET NULL,
  accepted_at timestamptz NOT NULL DEFAULT now(),
  ip          inet,
  PRIMARY KEY (document_id, org_id)
);

-- Monthly recurring revenue per organisation (§7.2), kept current through
-- each month; the month's last value stands.
CREATE TABLE mrr_snapshots (
  month      date NOT NULL,
  org_id     uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  plan       text NOT NULL,
  term       text NOT NULL,
  mrr_minor  bigint NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (month, org_id)
);

-- +goose Down
DROP TABLE mrr_snapshots;
DROP TABLE legal_acceptances;
DROP TABLE legal_documents;
ALTER TABLE terms_versions DROP COLUMN aup_md;
DROP TABLE org_support_phones;
DROP TABLE ticket_messages;
DROP TABLE tickets;
DROP SEQUENCE ticket_numbers;
UPDATE users SET platform_role = 'user' WHERE platform_role = 'support';
ALTER TABLE users DROP CONSTRAINT users_platform_role_check;
ALTER TABLE users ADD CONSTRAINT users_platform_role_check CHECK (platform_role IN ('platform_admin', 'user'));
