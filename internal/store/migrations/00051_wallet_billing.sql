-- +goose Up
-- Wallet billing: prices are in US dollars and an invoice is converted to
-- naira when it is issued, at a rate frozen on it; an organisation's wallet
-- (at the wallet provider) is debited for it under a consent PGDock records.

-- What an invoice was priced at, in cents, and the naira conversion. The
-- *_minor columns stay in kobo: it is the money that moves, so the ledger,
-- payments, credit notes and dunning are unchanged.
ALTER TABLE invoices
  ADD COLUMN usd_subtotal_minor bigint,
  ADD COLUMN usd_vat_minor      bigint,
  ADD COLUMN usd_total_minor    bigint,
  ADD COLUMN fx                 jsonb;

ALTER TABLE invoice_lines
  ADD COLUMN usd_unit_price   numeric,
  ADD COLUMN usd_amount_minor bigint;

-- The org's agreement to be debited from its wallet for invoices. NULL means
-- no consent: PGDock sends an invoice to pay instead of debiting.
ALTER TABLE billing_accounts
  ADD COLUMN debit_consent_at    timestamptz,
  ADD COLUMN debit_consent_by    uuid REFERENCES users(id),
  ADD COLUMN debit_terms_version text;

-- One wallet debit in flight per invoice: a second attempt must wait for the
-- first to be resolved (it may have reached the provider without an answer).
CREATE UNIQUE INDEX payment_intents_one_wallet_debit ON payment_intents (invoice_id)
  WHERE provider = 'iswallet' AND status = 'pending' AND invoice_id IS NOT NULL;

-- +goose Down
DROP INDEX payment_intents_one_wallet_debit;
ALTER TABLE billing_accounts
  DROP COLUMN debit_terms_version,
  DROP COLUMN debit_consent_by,
  DROP COLUMN debit_consent_at;
ALTER TABLE invoice_lines
  DROP COLUMN usd_amount_minor,
  DROP COLUMN usd_unit_price;
ALTER TABLE invoices
  DROP COLUMN fx,
  DROP COLUMN usd_total_minor,
  DROP COLUMN usd_vat_minor,
  DROP COLUMN usd_subtotal_minor;
