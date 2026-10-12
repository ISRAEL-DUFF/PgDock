# The wallet provider (iswallet)

PGDock keeps each organisation's money in a wallet at iswallet, a ledger
and wallet API, and debits it for invoices. This page is the contract:
what PGDock calls, what it relies on, what it assumes, and what is still
open with the provider. How it looks to an organisation is in
[Payments](payments.md). The client is `internal/billing/iswallet` and
`internal/billing/iswallet/fake.go` is a working fake used by the tests
and the local demo. The provider's own reference is its OpenAPI document;
where this page and the running provider differ, the provider is right:
fix whichever is wrong.

Amounts are integers in **kobo**. Wallets are in NGN. Times are RFC 3339.
Every request carries `Authorization: Bearer <key>`.

## What it is, and is not

iswallet is a ledger and a wallet API. It is **not** the merchant product
PGDock first designed for, and a few things don't exist at all:

| PGDock needed | In iswallet |
| --- | --- |
| Standing permission to debit (a mandate) | None. The API key is the authorisation: it can move money between any two wallets PGDock created, for any amount, at any time. **PGDock records and honours the customer's consent** ([Payments](payments.md#debit-consent)). |
| A hosted page or push approval to top up from an iSwallet balance | None, and no transfer between different tenants' wallets. Wallets are funded by bank transfer into the virtual account only. |
| Reserve funds, capture later | None. Every debit is immediate and final, so PGDock debits the exact amount. |
| A rate locked into a debit | None. PGDock quotes, computes the naira amount, records the quote on the invoice and debits that amount. |
| A refund tied to its debit | A reverse transfer, with nothing linking them but the narration. PGDock refunds with a credit note and keeps the link itself. |
| History of past rates | Not readable with a client key. PGDock keeps the whole quote on each invoice. |

## Calls PGDock makes

| Call | For |
| --- | --- |
| `POST /v1/wallets` (`Idempotency-Key`) | Open an organisation's wallet: `owner_type` `client_end_business`, a namespaced `owner_ref` (`pgdock:prod:org:<id>`), `currency` NGN, the business's name, email and phone, a director's BVN, and `initial_tier` TIER_2. A `409 WALLET_ALREADY_EXISTS` carries the existing wallet and counts as success. |
| `POST /v1/wallets/{id}/virtual-account` | The permanent bank account (NUBAN) that funds the wallet. Re-asking returns the same one. Needs the BVN to have been given. |
| `GET /v1/wallets/{id}/balance` | The balance. PGDock reads `balances[]` (the top-level `balance` is the canonical currency for older integrators) and uses `available`. |
| `GET /v1/platform/account` | PGDock's operating wallet, where debits go. Provisioned per tenant: PGDock never creates one. |
| `POST /v1/transfers` (`Idempotency-Key`) | The debit: customer wallet → operating wallet, with the invoice number as `narration` (255 characters at most; transfers have no structured metadata). Synchronous and final: a `200` means the money moved. |
| `GET /v1/requests/{key}` | What happened to a call made under an idempotency key. A `404 IDEMPOTENCY_KEY_NOT_FOUND` guarantees nothing was posted. |
| `GET /v1/rates?from=USD&to=NGN&amount=100` | The dollar's price in naira: `market_rate`, `spread_bps`, `effective_rate`, `fetched_at`, `sources`, `derived`. Rates are exact decimal strings. |
| `GET /v1/wallets/{id}/transactions/{txn}` | Verify a deposit before booking it. |
| `GET /v1/ledger/export?from&to&format=json` | PGDock's transactions in a window, a day at a time, for the hourly re-query and the nightly reconciliation. |

Webhooks come from a subscription PGDock makes
(`POST /v1/webhooks/subscriptions`, whose signing secret is shown once):
`wallet.credit.posted` is the only event acted on.

## What PGDock relies on

- **Idempotency.** The key is required on creating a wallet and on a
  transfer. Within 24 hours the same key and body replay the original
  answer; the same key with another body is `422 IDEMPOTENCY_KEY_REUSED`.
  PGDock makes a new key for every debit attempt, records it before
  calling, and looks it up after an unclear answer. After 24 hours a
  repeat of a key is refused by a database constraint, as a `500`
  indistinguishable from a transient fault: PGDock never retries a charge
  that old automatically.
- **Error codes.** Final (not retried): `INSUFFICIENT_FUNDS`,
  `WALLET_INACTIVE` (frozen, suspended or closed), `EXCEEDS_SINGLE_TXN_LIMIT`,
  `EXCEEDS_DAILY_LIMIT`, `CURRENCY_MISMATCH`, `WALLET_NOT_FOUND`. Retried
  with the same key: `429`, connection failures, `503`. Any other `500` is
  ambiguous and resolved by the key.
- **Signatures.** `X-iSpend-Signature: sha256=<hex>`, an HMAC-SHA256 over
  `"<X-iSpend-Timestamp>.<raw body>"`, compared in constant time, refused
  if the timestamp is more than 300 seconds from now.
- **Delivery.** Ten attempts over eight to eleven hours, then the event is
  parked. Duplicates are possible and order is not guaranteed, so PGDock
  dedupes on `data.txn_id` and treats the balance as authoritative. A missed
  event is recovered by the hourly re-query. The envelope's fields are under
  `data`.
- **Deposits are net.** A deposit arrives net of the provider's collection
  fee (1.4%, at most ₦2,000), and `data.amount_credited` is what the wallet
  was credited. PGDock books that, not the gross.
- **Rate limit.** 100 requests a minute per key, with `X-RateLimit-Reset`
  on every response; a replayed idempotent request doesn't count. PGDock's
  volume is tiny; spread a large month-end run over the hour.
- **Keys.** There is one kind of client key: it reads and moves money.
  Rotation is done by the provider on request.

## Limits

A wallet's limits come from its KYC tier and apply to money going **out**
(transfers and payouts); deposits aren't capped. PGDock's debits are
transfers, so a plan that costs more than the tier's limit can't be debited:
a new wallet (tier 1) moves at most ₦50,000 a day, which is below a
$49 plan. PGDock opens wallets at **tier 2**, which needs a BVN, and
Pro and Team fit within its limits. The provider's own pages disagree on
the tier-2 numbers (a daily limit of ₦200,000 and a balance cap of ₦500,000
in the OpenAPI; ₦500,000 a transaction and ₦1,000,000 a day in its answer
to PGDock). Until that is settled, treat ₦200,000 a day as the ceiling for
planning, and ask for tier 3 for an organisation with a larger invoice.

## What is assumed or still open

These are in the code and the fake, and need a check against the sandbox
before launch (the sandbox is provisioned by the provider on request):

1. **The tier numbers** above, and the ceiling PGDock needs: the largest
   expected monthly invoice, which for an organisation with dedicated
   instances can be hundreds of dollars.
2. **Transaction statuses.** The OpenAPI doesn't list them. PGDock treats
   `posted`, `completed`, `confirmed`, `success`, `succeeded` and `settled`
   as done, `pending`, `processing` and `initiated` as waiting, and the rest
   as failed.
3. **Reversals.** `wallet.credit.reversed` (a credit the bank clawed back)
   and `wallet.credit.rejected` are not handled: PGDock ignores the event.
   A reversal leaves the books holding more than the wallet does, and the
   nightly balance reconciliation will flag it. Handling it (a ledger
   reversal, and dunning if an invoice was paid with it) is the next piece
   of work here.
4. **The webhook's field names**, in particular `data.amount_credited`
   (named in the OpenAPI for transaction rows; PGDock falls back to
   `data.amount` if an event lacks it).
5. **Wallet creation for tier 2** at once: the request's flat `email` and
   `phone` are sent with `initial_tier`, as the OpenAPI says a tier above 1
   needs.
6. **Rate history** isn't readable by a client key. The provider has it on
   its list; PGDock needs it for audits and for customers' tax records, and
   until then relies on the quote kept on each invoice.
7. **The dollar rate and spread**: a commercial decision with the
   provider ([Payments](payments.md#the-dollar-rate)).
8. **Regulatory position** (agent, merchant of record, neither) and the
   production go-live process are owned by the provider's compliance and
   commercial teams, not by this document.
