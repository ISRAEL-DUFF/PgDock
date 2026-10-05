-- +goose Up
-- V3 M21: payments, prepaid balances, WHT and dunning (V3 §3.4–§3.8).

-- Every provider event, normalised (§3.4.1), kept as received. Duplicates
-- (same provider and event id) are no-ops.
CREATE TABLE payment_events (
  id                bigserial PRIMARY KEY,
  provider          text NOT NULL,
  provider_event_id text NOT NULL,
  kind              text NOT NULL CHECK (kind IN ('payment.succeeded', 'payment.failed', 'transfer.received', 'refund.completed', 'mandate.revoked')),
  org_id            uuid REFERENCES organizations(id),
  reference         text,          -- ours (a checkout or charge reference), when the payment started here
  provider_ref      text,          -- the provider's transaction id
  amount_minor      bigint,
  currency          text,
  payload           jsonb NOT NULL DEFAULT '{}',
  received_at       timestamptz NOT NULL DEFAULT now(),
  processed_at      timestamptz,
  outcome           text,          -- posted, duplicate, unmatched, rejected, failed
  error             text,
  UNIQUE (provider, provider_event_id)
);
CREATE INDEX payment_events_unprocessed ON payment_events (received_at) WHERE processed_at IS NULL;

-- What PGDock asked a provider for: a checkout, a saved-card or mandate
-- charge. The reference is ours and unique; a provider event names it.
CREATE TABLE payment_intents (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id       uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  reference    text UNIQUE NOT NULL,
  provider     text NOT NULL,
  channel      text NOT NULL CHECK (channel IN ('card', 'saved_card', 'wallet', 'mandate', 'transfer', 'stablecoin')),
  purpose      text NOT NULL CHECK (purpose IN ('invoice', 'topup', 'card_setup')),
  invoice_id   uuid REFERENCES invoices(id),
  method_id    uuid,
  amount_minor bigint NOT NULL CHECK (amount_minor > 0),
  status       text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'succeeded', 'failed', 'expired')),
  checkout_url text,
  automatic    boolean NOT NULL DEFAULT false, -- charged by PGDock (on issue, auto top-up, a retry)
  error        text,
  created_at   timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz
);
CREATE INDEX payment_intents_org ON payment_intents (org_id, created_at DESC);

-- Money received, from any channel; settled against invoices or credit.
CREATE TABLE payments (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id           uuid NOT NULL REFERENCES organizations(id),
  provider         text NOT NULL,   -- flutterwave, ispend, bank (manual)
  channel          text NOT NULL CHECK (channel IN ('card', 'saved_card', 'wallet', 'mandate', 'transfer', 'stablecoin', 'manual')),
  provider_ref     text NOT NULL,
  reference        text,
  amount_minor     bigint NOT NULL CHECK (amount_minor > 0),
  fee_minor        bigint NOT NULL DEFAULT 0 CHECK (fee_minor >= 0),
  refunded_minor   bigint NOT NULL DEFAULT 0 CHECK (refunded_minor >= 0),
  -- A stablecoin top-up: the naira amount was fixed at iSpend's quote (§3.4.5).
  fx_quote         jsonb,
  note             text,
  proof_object_key text,
  recorded_by      uuid REFERENCES users(id),
  received_at      timestamptz NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (provider, provider_ref)
);
CREATE INDEX payments_org ON payments (org_id, received_at DESC);

-- How a payment settled invoices (the rest became credit).
CREATE TABLE payment_allocations (
  payment_id   uuid NOT NULL REFERENCES payments(id),
  invoice_id   uuid NOT NULL REFERENCES invoices(id),
  amount_minor bigint NOT NULL CHECK (amount_minor >= 0),
  wht_minor    bigint NOT NULL DEFAULT 0 CHECK (wht_minor >= 0),
  PRIMARY KEY (payment_id, invoice_id)
);

CREATE TABLE refunds (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  payment_id   uuid NOT NULL REFERENCES payments(id),
  amount_minor bigint NOT NULL CHECK (amount_minor > 0),
  reason       text NOT NULL,
  provider_ref text,
  status       text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'completed', 'failed')),
  error        text,
  created_by   uuid REFERENCES users(id),
  created_at   timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz
);

-- Saved cards (Flutterwave tokens, sealed with the master key) and iSpend
-- wallet mandates.
CREATE TABLE payment_methods (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id        uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  provider      text NOT NULL,
  kind          text NOT NULL CHECK (kind IN ('card', 'mandate')),
  token_sealed  bytea,
  provider_ref  text,
  brand         text,
  last4         text,
  exp_month     int,
  exp_year      int,
  limit_minor   bigint,          -- a mandate's monthly limit
  is_default    boolean NOT NULL DEFAULT false,
  status        text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked', 'expired', 'removed')),
  reminded_days int NOT NULL DEFAULT 0, -- smallest expiry reminder sent (30, then 7)
  created_at    timestamptz NOT NULL DEFAULT now(),
  UNIQUE (provider, provider_ref)
);
CREATE UNIQUE INDEX payment_methods_one_default ON payment_methods (org_id) WHERE is_default AND status = 'active';

-- Permanent bank accounts for transfers (§3.4.3); an org may hold one per provider.
CREATE TABLE virtual_accounts (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id         uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  provider       text NOT NULL,
  account_number text NOT NULL,
  bank_name      text NOT NULL,
  account_name   text NOT NULL,
  provider_ref   text,
  created_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (provider, account_number),
  UNIQUE (org_id, provider)
);

-- WHT (§3.7): what was deducted on an invoice, and the credit note that
-- evidences it.
ALTER TABLE invoices
  ADD COLUMN paid_minor         bigint NOT NULL DEFAULT 0,
  ADD COLUMN wht_deducted_minor bigint NOT NULL DEFAULT 0,
  ADD COLUMN wht_evidenced_at   timestamptz;
CREATE TABLE wht_certificates (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  invoice_id  uuid NOT NULL REFERENCES invoices(id),
  org_id      uuid NOT NULL REFERENCES organizations(id),
  object_key  text NOT NULL,
  filename    text NOT NULL,
  size_bytes  bigint NOT NULL,
  uploaded_by uuid REFERENCES users(id),
  uploaded_at timestamptz NOT NULL DEFAULT now()
);

-- Prepaid (§3.5): what has been deducted for a month, per revenue account,
-- so each day posts only the difference.
CREATE TABLE prepaid_deductions (
  org_id       uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  month        date NOT NULL,
  account      text NOT NULL,
  amount_minor bigint NOT NULL,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (org_id, month, account)
);

-- Dunning (§3.8): each step taken, once per cycle.
CREATE TABLE dunning_steps (
  id         bigserial PRIMARY KEY,
  org_id     uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  cycle      timestamptz NOT NULL, -- when the cycle started (due date or zero balance)
  step       text NOT NULL,
  detail     text,
  taken_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, cycle, step)
);

ALTER TABLE billing_accounts
  ADD COLUMN dunning_since         timestamptz,
  ADD COLUMN deletion_scheduled_at timestamptz,
  ADD COLUMN balance_alerted       int NOT NULL DEFAULT 0, -- lowest prepaid alert sent (50, 25, 10, or 3 for "3 days")
  ADD COLUMN balance_month         date,
  ADD COLUMN zero_balance_at       timestamptz,
  ADD COLUMN card_failing_since    timestamptz;

-- +goose Down
ALTER TABLE billing_accounts DROP COLUMN dunning_since, DROP COLUMN deletion_scheduled_at, DROP COLUMN balance_alerted,
  DROP COLUMN balance_month, DROP COLUMN zero_balance_at, DROP COLUMN card_failing_since;
DROP TABLE dunning_steps;
DROP TABLE prepaid_deductions;
DROP TABLE wht_certificates;
ALTER TABLE invoices DROP COLUMN paid_minor, DROP COLUMN wht_deducted_minor, DROP COLUMN wht_evidenced_at;
DROP TABLE virtual_accounts;
DROP TABLE payment_methods;
DROP TABLE refunds;
DROP TABLE payment_allocations;
DROP TABLE payments;
DROP TABLE payment_intents;
DROP TABLE payment_events;
