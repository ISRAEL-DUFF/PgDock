# The iSpend merchant contract

PGDock talks to iSpend through the merchant API below. iSpend is built in
house, so this page is the contract both sides build to: PGDock's client
is `internal/billing/ispend`, and `internal/billing/ispend/fake.go` is a
working reference server used by the tests. A change here needs a change
on both sides.

Amounts are integers in **kobo** (`amount_minor`). Currency is `NGN`.
Times are RFC 3339 in UTC.

## Requests

Base URL: `PGDOCK_ISPEND_BASE_URL`. Every request carries
`Authorization: Bearer <PGDOCK_ISPEND_API_KEY>` and a JSON body.

| Status | PGDock treats it as |
| --- | --- |
| 2xx | success |
| 402 | a decline: `{"message": "..."}` is shown to the customer |
| 429, 5xx, no answer | iSpend unavailable: retried later, or the fallback provider used |
| other 4xx | an error, logged with the body |

### Customers and virtual accounts

`POST /merchant/customers`
`{"reference": "<org id>", "name": "...", "email": "..."}` →
`{"id": "cus_..."}`. Idempotent by `reference`: a second call returns the
same customer.

`POST /merchant/customers/{id}/virtual-accounts` `{}` →
`{"id": "va_...", "account_number": "0123456789", "bank_name": "...", "account_name": "..."}`.
One permanent account per customer; a second call returns the same one.

### Hosted approval (wallet payments, mandates, USDT top-ups)

`POST /merchant/checkout-sessions`

```json
{
  "reference": "pgd_...",          // PGDock's, unique per payment attempt
  "amount_minor": 1612500,
  "currency": "NGN",
  "kind": "payment",               // or "stablecoin"
  "purpose": "invoice",            // or "topup"
  "description": "Invoice PGD-2026-000042",
  "redirect_url": "https://pgdock.example/org/billing?paid=...",
  "customer_id": "cus_...",
  "mandate": {"monthly_limit_minor": 5000000}   // optional
}
```

→ `{"id": "cs_...", "url": "https://..."}`. PGDock sends the customer to
`url`; iSpend sends them back to `redirect_url` and reports the outcome by
webhook. With `mandate`, approving also sets up a recurring mandate up to
that monthly limit. A `stablecoin` session quotes USDT and settles in
naira; the transaction carries the quote in `fx_quote` (stored as given).

### Mandates

`POST /merchant/mandates/{mandate id}/charges`
`{"reference": "pgd_...", "amount_minor": 1612500, "description": "..."}`
→ a transaction (below). Idempotent by `reference`. Over the monthly
limit or with too little in the wallet: 402.

`DELETE /merchant/mandates/{mandate id}` revokes it (when the customer
removes it in PGDock).

### Transactions

`GET /merchant/transactions/{id or reference}` → a transaction.

`GET /merchant/transactions?from=<time>&to=<time>` →
`{"items": [transaction, ...]}`, every transaction created in
`[from, to)`, for reconciliation. Only succeeded ones are compared.

A transaction:

```json
{
  "id": "txn_...",
  "reference": "pgd_...",           // PGDock's; empty for a transfer
  "status": "succeeded",            // or "failed", "pending"
  "amount_minor": 1612500,
  "fee_minor": 16125,
  "currency": "NGN",
  "channel": "wallet",              // or "transfer", "mandate", "stablecoin"
  "virtual_account_number": "0123456789",   // transfers
  "mandate": {"id": "mnd_...", "monthly_limit_minor": 5000000},  // when one was set up
  "fx_quote": {},                   // stablecoin
  "message": "",                    // why it failed
  "created_at": "2026-10-05T09:00:00Z"
}
```

### Refunds

`POST /merchant/refunds`
`{"transaction_id": "txn_...", "amount_minor": 50000, "reference": "pgr_..."}`
→ `{"id": "rf_...", "status": "pending"}` (or `completed`, `failed`). A
pending refund completes by webhook.

## Webhooks

iSpend POSTs events to `https://<PGDock>/api/v1/payments/webhooks/ispend`:

```
X-ISpend-Timestamp: 1791190800
X-ISpend-Signature: <hex HMAC-SHA256 of "<timestamp>.<raw body>" with PGDOCK_ISPEND_WEBHOOK_SECRET>
```

```json
{"id": "evt_...", "type": "transfer.received", "data": { ... }}
```

| `type` | `data` |
| --- | --- |
| `payment.succeeded`, `payment.failed` | a transaction |
| `transfer.received` | a transaction with `virtual_account_number` |
| `refund.completed` | `{"id": "rf_..."}` |
| `mandate.revoked` | `{"id": "mnd_..."}` |

PGDock answers 401 to a bad signature or a timestamp more than 5 minutes
off, 200 once the event is stored (including a duplicate `id`, which is
ignored), and 500 if processing failed, so iSpend should retry until it
gets a 2xx. PGDock never trusts the body: it fetches the transaction with
`GET /merchant/transactions/{id}` before posting anything, and it
re-queries the last two hours every hour and the previous day every
night, so a lost webhook delays a payment but doesn't lose it. Other
event types get 200 and are ignored.
