-- +goose Up
-- V3 M20: billing core (V3 §3). Amounts are integers in kobo.

-- The billing org role: billing without project access (V3 §3.2).
ALTER TABLE org_members DROP CONSTRAINT org_members_role_check;
ALTER TABLE org_members ADD CONSTRAINT org_members_role_check CHECK (role IN ('owner', 'admin', 'member', 'billing'));
ALTER TABLE invitations DROP CONSTRAINT invitations_org_role_check;
ALTER TABLE invitations ADD CONSTRAINT invitations_org_role_check CHECK (org_role IN ('owner', 'admin', 'member', 'billing'));

-- Versioned prices (V3 §3.9). A draft has no published_at; published ones
-- are immutable (trigger below).
CREATE TABLE price_books (
  version      int PRIMARY KEY,
  effective_at timestamptz NOT NULL,
  prices       jsonb NOT NULL,
  notes        text,
  created_at   timestamptz NOT NULL DEFAULT now(),
  published_at timestamptz,
  published_by uuid REFERENCES users(id)
);

CREATE TABLE billing_accounts (
  org_id             uuid PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
  plan               text NOT NULL DEFAULT 'free',
  term               text NOT NULL DEFAULT 'monthly' CHECK (term IN ('monthly', 'annual')),
  term_ends_at       timestamptz,
  mode               text NOT NULL DEFAULT 'postpaid' CHECK (mode IN ('postpaid', 'prepaid')),
  price_book_version int NOT NULL REFERENCES price_books(version),
  grandfathered      boolean NOT NULL DEFAULT false,
  legal_name         text,
  address            text,
  tin                text,
  vat_registered     boolean NOT NULL DEFAULT false,
  deducts_wht        boolean NOT NULL DEFAULT false,
  provider_customers jsonb NOT NULL DEFAULT '{}',
  payment_terms_days int NOT NULL DEFAULT 7,
  budget_minor       bigint CHECK (budget_minor > 0),
  spend_cap_minor    bigint CHECK (spend_cap_minor > 0),
  auto_topup         jsonb,
  dunning_state      text NOT NULL DEFAULT 'ok' CHECK (dunning_state IN ('ok', 'retrying', 'overdue', 'restricted', 'suspended')),
  grace_until        timestamptz,
  -- Spend controls (V3 §3.10), refreshed with the forecast.
  forecast_minor     bigint,
  forecast_at        timestamptz,
  capped             boolean NOT NULL DEFAULT false,
  budget_alerted     int NOT NULL DEFAULT 0,      -- highest budget threshold (50, 80, 100) emailed this month
  budget_month       date,
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE billing_contacts (
  org_id     uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  email      citext NOT NULL,
  name       text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (org_id, email)
);

-- Plan changes, for proration (V3 §3.6): upgrades take effect at once;
-- downgrades at the next cycle unless asked for now.
CREATE TABLE billing_plan_changes (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id       uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  from_plan    text NOT NULL,
  to_plan      text NOT NULL,
  from_term    text NOT NULL,
  to_term      text NOT NULL,
  effective_at timestamptz NOT NULL,
  requested_at timestamptz NOT NULL DEFAULT now(),
  requested_by uuid REFERENCES users(id),
  applied      boolean NOT NULL DEFAULT false
);
CREATE INDEX billing_plan_changes_org ON billing_plan_changes (org_id, effective_at);

CREATE TABLE invoices (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id             uuid NOT NULL REFERENCES organizations(id),
  number             text UNIQUE,
  period_start       date NOT NULL,
  period_end         date NOT NULL,
  status             text NOT NULL CHECK (status IN ('draft', 'issued', 'paid', 'paid_wht_pending', 'partially_paid', 'void')),
  held               boolean NOT NULL DEFAULT false,
  hold_reason        text,
  subtotal_minor     bigint NOT NULL,
  vat_minor          bigint NOT NULL,
  total_minor        bigint NOT NULL,
  wht_expected_minor bigint NOT NULL DEFAULT 0,
  vat_rate           numeric NOT NULL,
  -- What the invoice shows, frozen at issue: both parties' details.
  bill_to            jsonb NOT NULL DEFAULT '{}',
  seller             jsonb NOT NULL DEFAULT '{}',
  due_at             timestamptz,
  issued_at          timestamptz,
  paid_at            timestamptz,
  pdf_object_key     text,
  price_book_version int NOT NULL REFERENCES price_books(version),
  created_at         timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, period_start)
);

CREATE TABLE invoice_lines (
  id               bigserial PRIMARY KEY,
  invoice_id       uuid NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,
  kind             text NOT NULL CHECK (kind IN ('plan', 'overage', 'dedicated', 'addon', 'credit', 'proration')),
  description      text NOT NULL,
  project_id       uuid,
  metric           text,
  quantity         numeric NOT NULL,
  unit_price_minor numeric NOT NULL,
  amount_minor     bigint NOT NULL,
  revenue_account  text NOT NULL
);

CREATE TABLE credit_notes (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  invoice_id   uuid NOT NULL REFERENCES invoices(id),
  org_id       uuid NOT NULL REFERENCES organizations(id),
  number       text UNIQUE NOT NULL,
  amount_minor bigint NOT NULL CHECK (amount_minor > 0),
  vat_minor    bigint NOT NULL DEFAULT 0,
  reason       text NOT NULL,
  issued_by    uuid REFERENCES users(id),
  issued_at    timestamptz NOT NULL DEFAULT now()
);

-- Numbers are sequential per kind and year and never reused.
CREATE TABLE billing_sequences (
  kind text NOT NULL,
  year int  NOT NULL,
  last int  NOT NULL DEFAULT 0,
  PRIMARY KEY (kind, year)
);

-- The double-entry ledger (V3 §3.3): append-only; each txn_id balances.
CREATE TABLE ledger_entries (
  id              bigserial PRIMARY KEY,
  txn_id          uuid NOT NULL,
  org_id          uuid REFERENCES organizations(id),
  account         text NOT NULL,
  direction       text NOT NULL CHECK (direction IN ('debit', 'credit')),
  amount_minor    bigint NOT NULL CHECK (amount_minor > 0),
  source_type     text NOT NULL CHECK (source_type IN ('invoice', 'payment', 'usage', 'wht', 'refund', 'credit', 'adjustment')),
  source_id       text NOT NULL,
  idempotency_key text UNIQUE NOT NULL,
  memo            text,
  created_by      uuid,
  created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ledger_entries_txn ON ledger_entries (txn_id);
CREATE INDEX ledger_entries_org_account ON ledger_entries (org_id, account);

-- +goose StatementBegin
CREATE FUNCTION ledger_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'ledger_entries is append-only: post a reversing transaction instead';
END $$;
-- +goose StatementEnd
CREATE TRIGGER ledger_no_update BEFORE UPDATE OR DELETE ON ledger_entries
  FOR EACH ROW EXECUTE FUNCTION ledger_append_only();
CREATE TRIGGER ledger_no_truncate BEFORE TRUNCATE ON ledger_entries
  FOR EACH STATEMENT EXECUTE FUNCTION ledger_append_only();

-- Checked when the posting transaction commits: debits equal credits.
-- +goose StatementBegin
CREATE FUNCTION ledger_balanced() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE d bigint; c bigint;
BEGIN
  SELECT coalesce(sum(amount_minor) FILTER (WHERE direction = 'debit'), 0),
         coalesce(sum(amount_minor) FILTER (WHERE direction = 'credit'), 0)
    INTO d, c FROM ledger_entries WHERE txn_id = NEW.txn_id;
  IF d <> c THEN
    RAISE EXCEPTION 'ledger transaction % is unbalanced: debits % <> credits %', NEW.txn_id, d, c;
  END IF;
  RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER ledger_balanced AFTER INSERT ON ledger_entries
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION ledger_balanced();

-- Published price books don't change.
-- +goose StatementBegin
CREATE FUNCTION price_book_frozen() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.published_at IS NOT NULL THEN
    RAISE EXCEPTION 'price book % is published and cannot change', OLD.version;
  END IF;
  RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END $$;
-- +goose StatementEnd
CREATE TRIGGER price_book_frozen BEFORE UPDATE OR DELETE ON price_books
  FOR EACH ROW EXECUTE FUNCTION price_book_frozen();

-- +goose Down
DROP TABLE ledger_entries;
DROP FUNCTION ledger_balanced();
DROP FUNCTION ledger_append_only();
DROP TABLE billing_sequences;
DROP TABLE credit_notes;
DROP TABLE invoice_lines;
DROP TABLE invoices;
DROP TABLE billing_plan_changes;
DROP TABLE billing_contacts;
DROP TABLE billing_accounts;
DROP TABLE price_books;
DROP FUNCTION price_book_frozen();
DELETE FROM org_members WHERE role = 'billing';
UPDATE invitations SET org_role = 'member' WHERE org_role = 'billing';
ALTER TABLE invitations DROP CONSTRAINT invitations_org_role_check;
ALTER TABLE invitations ADD CONSTRAINT invitations_org_role_check CHECK (org_role IN ('owner', 'admin', 'member'));
ALTER TABLE org_members DROP CONSTRAINT org_members_role_check;
ALTER TABLE org_members ADD CONSTRAINT org_members_role_check CHECK (role IN ('owner', 'admin', 'member'));
