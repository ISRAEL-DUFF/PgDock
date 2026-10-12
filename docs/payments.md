# Payments

How PGDock takes money (V3 §3.4–§3.8, milestone M21): cards through
Flutterwave, bank transfers into per-organisation virtual accounts
(iSpend, with Flutterwave as the fallback), Pay with iSpend and wallet
mandates, optional USDT top-ups, and manual payments. It also covers
prepaid balances, withholding tax, dunning and reconciliation. The prices,
invoices and ledger they pay into are in [Billing](billing.md).

## Setting up the providers

Each provider is off until its credentials are set. With none set,
organisations can still pay by transfer to PGDock's own bank account,
which the platform admin then records by hand.

| Variable | |
| --- | --- |
| `PGDOCK_FLW_SECRET_KEY` (or `_FILE`) | Flutterwave secret key. Use a test key against the Flutterwave sandbox. |
| `PGDOCK_FLW_WEBHOOK_HASH` (or `_FILE`) | The "secret hash" set in Flutterwave → Settings → Webhooks, at least 16 characters. |
| `PGDOCK_FLW_BVN` | The BVN Flutterwave needs to issue permanent virtual accounts. |
| `PGDOCK_FLW_BASE_URL` | Defaults to `https://api.flutterwave.com`. |
| `PGDOCK_ISPEND_BASE_URL` | iSpend's merchant API (sandbox or live). |
| `PGDOCK_ISPEND_API_KEY` (or `_FILE`) | iSpend merchant API key. |
| `PGDOCK_ISPEND_WEBHOOK_SECRET` (or `_FILE`) | Shared secret iSpend uses to sign webhooks, at least 16 characters. |
| `PGDOCK_PAY_CARDS` | Card provider: `flutterwave` (default). |
| `PGDOCK_PAY_VA_PRIMARY` | Virtual-account provider: `ispend` (default) or `flutterwave`. |
| `PGDOCK_PAY_VA_FALLBACK` | Used when the primary can't issue an account: `flutterwave` (default) or `none`. |
| `PGDOCK_PAY_WALLET` | Wallet provider: `ispend` (default). |

A route only counts when its provider is configured. The org's Billing
page offers exactly the channels that work.

Point each provider's webhooks at:

- `https://<your PGDock>/api/v1/payments/webhooks/flutterwave`
- `https://<your PGDock>/api/v1/payments/webhooks/ispend`

The API iSpend implements for PGDock is in
[the iSpend contract](ispend-contract.md).

**Testing without the sandboxes.** The test suite runs both providers
against fakes of their sandboxes (`internal/billing/flutterwave/fake.go`
and `internal/billing/ispend/fake.go`), which send real signed webhooks to
the API. The done-when test drives the fakes directly (it "pays" their
hosted pages), so against the real sandboxes it is replaced by the
rehearsal below.

## Sandbox rehearsal

Before the paid launch, run PGDock's provider clients against the real
sandboxes, then do the steps that need a person.

**1. The clients on their own** (a few seconds, no server needed):

```sh
PGDOCK_FLW_SECRET_KEY=FLWSECK_TEST-… PGDOCK_FLW_BVN=… \
PGDOCK_ISPEND_BASE_URL=https://sandbox.… PGDOCK_ISPEND_API_KEY=… \
make test-payments-sandbox
```

It creates a customer, starts a checkout (and prints its hosted page),
checks an unpaid checkout doesn't verify as paid, issues a virtual account
(printed), and lists the last day's transactions, for each provider whose
keys are set. It refuses a Flutterwave key that isn't a test key. The same
checks run against the fakes in CI (`TestChecksAgainstFakes`).

**2. With a person**, on a staging PGDock reachable from the internet
(the providers must reach its webhook URLs; a tunnel is enough), with
both providers' sandbox webhooks pointed at it:

| Step | Expect |
| --- | --- |
| Pay an invoice by card on Flutterwave's hosted page with one of [Flutterwave's test cards](https://developer.flutterwave.com/docs/integration-guides/testing-helpers) | The invoice is paid and the card is saved (Org → Billing) |
| Issue the next invoice | The saved card is charged on issue |
| Pay with a test card that declines | The payment fails; the card retry schedule starts; nothing is posted |
| Transfer into the org's iSpend virtual account (the sandbox's simulated transfer): the full amount, then on another invoice the amount less exactly its WHT, then less, then more | Paid; paid (WHT pending evidence); partially paid; paid with the rest as credit |
| The same into a Flutterwave virtual account (turn iSpend's off, or use an org issued one by the fallback) | The same four results |
| Pay with iSpend's wallet, once with a mandate; then revoke the mandate in iSpend | Paid; the mandate is saved, charged on the next invoice, then retired with an email asking for another method |
| Redeliver a webhook from each dashboard | Nothing changes (one payment) |
| Stop the staging server for ten minutes during a payment, then start it | Within the hour, the hourly re-query records the payment once |
| Admin → Billing → Reconciliation → Run now | No differences for either provider |
| Admin → Billing → Ledger | No findings |

Then rerun step 1 with `PGDOCK_SANDBOX_FLW_PAID_REF` and
`PGDOCK_SANDBOX_ISPEND_PAID_REF` set to a paid reference from step 2: each
must verify as a succeeded NGN payment with its fee.

Record the date, who ran it and any differences in the table below; the
paid launch waits on a run with none.

| Date | By | Flutterwave | iSpend | Notes |
| --- | --- | --- | --- | --- |
| | | | | |

## How a payment is recorded

1. The provider sends a webhook. PGDock checks its signature: Flutterwave's
   `verif-hash`, or iSpend's HMAC with a 5-minute replay window.
2. PGDock stores the event once. A redelivered event with the same id is
   acknowledged and ignored.
3. PGDock never trusts the webhook body. It fetches the transaction from
   the provider and posts only what the provider confirms: amount,
   currency, status and fee.
4. The payment is attributed to an organisation by PGDock's reference
   (checkouts and charges), or by the virtual account it arrived in
   (transfers). If neither matches, the event waits under Admin → Billing
   → Events to be attributed by hand.
5. Settlement follows (below), all in one ledger transaction. The payer
   gets a receipt by email, and can download it from Billing → Payments.

Missed webhooks are not lost. Every hour PGDock lists the last two hours
of transactions at each provider, and every night the previous day, and
records anything it hasn't seen.

## Settlement

A payment goes, in order:

1. To the invoice it was made for, if any.
2. To the oldest open invoices.
3. Whatever is left becomes **credit**. It is applied to the next invoice
   when that invoice is issued, and is what a prepaid balance is.

A transfer short by exactly the invoice's expected withholding tax
settles the invoice as **Paid (WHT pending)**. The WHT moves to the WHT
receivable until the credit note arrives (below). Any other shortfall
leaves the invoice **partially paid**.

Each payment posts to the ledger:

- Dr `cash:<provider>` (the net amount) and Dr `fees:<provider>` (the fee);
- Cr `receivable` (each invoice it settles) and Cr `credit_balance` (the
  rest).

A refund comes out of credit: Dr `credit_balance`, Cr `cash`, posted when
the provider confirms it. To refund money that paid an invoice, either:

- issue a credit note on the invoice first (the invoice was wrong): the
  credit note becomes credit, and the refund comes from it; or
- tick **reopen the invoices** on the refund (the money went back to the
  payer, a chargeback, a payment to the wrong organisation): the part the
  credit doesn't cover is taken back off the invoices this payment
  settled, newest first (Dr `receivable`, Cr `credit_balance`). Those
  invoices are owed again, and dunning applies once they are overdue. If
  the provider refuses the refund, the credit pays them again.

## Channels

- **Card (Flutterwave).** Pay on Flutterwave's hosted page. The card is
  then saved as a token, sealed with PGDock's master key, and becomes the
  default method. Adding a card on its own charges ₦100, kept as credit.
  The default card is charged when an invoice is issued and for automatic
  top-ups. Cards are reminded about 30 and 7 days before they expire, and
  retired when they do.
- **Bank transfer.** Each organisation gets its own permanent account
  number (Billing → "Show my bank transfer account", or
  `pgdock billing transfer`). If iSpend can't issue one, Flutterwave
  issues it instead. Both kinds work the same way.
- **Pay with iSpend.** The customer approves the debit in iSpend.
  Optionally they also set up a **mandate** up to a monthly limit, which
  PGDock then charges like a card. Revoking it in iSpend removes it from
  PGDock and asks the org for another method.
- **USDT top-ups (iSpend).** Off by default (Admin → Billing → Settings).
  These are for prepaid balances only. iSpend's FX quote is stored with
  the payment.
- **Manual.** The platform admin records a transfer to PGDock's own
  account or a cheque (Admin → Billing → Payments → Record a payment).
  The record needs a bank reference, which can't be used twice, and can
  include proof. These payments post to `cash:bank`.

`pgdock billing pay <invoice> [--wallet]` prints a link to pay.
`pgdock billing payments` lists what was received.

## Prepaid

The platform admin makes an organisation prepaid (Admin → Organisations →
the org → Billing; `PATCH /api/v1/admin/orgs/{org}/billing`). That panel
also sets payment terms, the price book and grandfathering, and shows the
org's standing. Switching to prepaid is refused while issued invoices are
open. Switching back to postpaid returns the deductions for months not
yet invoiced to the balance, and those months are invoiced instead, so
nothing is counted twice. It pays in advance by topping up, by any channel. Every
day PGDock rates the month so far (usage and plan changes, plus VAT) and
deducts the difference since the previous day from the balance. When the
monthly invoice is issued it is already paid: the final true-up,
including the plan fee in advance, comes off the balance.

- **Alerts.** Billing contacts are emailed when the balance falls below
  50%, 25% and 10% of the month's projected spend, and when it covers
  fewer than 3 days of usage.
- **Running out.** At zero, the org has 3 days to top up before the
  dunning ladder starts.
- **Auto top-up.** Charge the default method by an amount when the
  balance falls below a threshold, at most once a day.

## Withholding tax

Organisations that deduct WHT (Billing → Business details) pay invoices
net of it. The invoice shows the expected deduction. Afterwards the org
uploads the WHT credit note on the invoice (PDF, PNG or JPEG, up to
10 MB), or the admin attaches it from Admin → Billing → WHT. The upload
marks the invoice paid. The amount stays in `wht_receivable`, a tax
credit, now backed by its evidence.

Admin → Billing → WHT lists what is outstanding, with each item's age,
and downloads it as CSV for the accountant.

## Dunning

When an invoice passes its due date, or 3 days after a prepaid balance
reaches zero:

| Day | |
| --- | --- |
| 0 | **Overdue.** Emails to the billing contacts (or, if there are none, the owners and billing members); one at each step. |
| 3 | **Restricted.** No new projects, branches or dedicated instances. Running databases are untouched. |
| 7 | **Final notice.** |
| 10 | **Suspended.** The org's databases go offline. The data is kept. |
| 40 | **Deletion notice.** Dedicated projects are deleted 7 days later, but only if "Delete for non-payment" is on (Admin → Billing → Settings, off by default). Otherwise nothing is deleted. |

Free-plan organisations can be restricted but are never suspended. A failing card is
retried on days 3, 5 and 7. Paying returns everything at once: the
restriction lifts, the org is reinstated and the deletion is cancelled.

The admin can hold the ladder until a date (Admin → Organisations → the
org → Billing), for an org that has agreed to pay later.

## Reconciliation

Every night PGDock lists the previous day's transactions at each provider
and compares them with its own payments. It reports:

- payments missing in PGDock;
- payments missing at the provider;
- different amounts;
- different fees.

Admin → Billing → Reconciliation shows the latest result per provider and
can run it now. A clean night shows "No differences".

## What was tested

`TestPaymentsAcrossProviders` (test/integration) is M21's done-when test,
run against the provider fakes. It covers:

- card payments and tokenised charges;
- transfers into both kinds of virtual account: full, short by WHT,
  partial and over;
- wallet payments and mandate charges, and a revoked mandate;
- a declined card;
- duplicate and missed webhooks;
- an iSpend outage falling back to a Flutterwave virtual account.

After all of it, the ledger balances and reconciliation reports no
differences. The unit tests in `internal/billing` cover prepaid
deduction, the dunning ladder, WHT certificates and refunds.
