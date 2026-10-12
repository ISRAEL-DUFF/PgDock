-- +goose Up
-- Cards, saved cards, iSpend mandates and auto top-up are gone: organisations
-- pay from their wallets (migration 00051). Old payments, payment intents and
-- their CHECK constraints stay as history.
DROP TABLE payment_methods;
ALTER TABLE billing_accounts DROP COLUMN auto_topup;

-- +goose Down
ALTER TABLE billing_accounts ADD COLUMN auto_topup jsonb;

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
