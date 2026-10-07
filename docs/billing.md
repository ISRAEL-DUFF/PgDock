# Billing

PGDock bills organisations in naira (V3 §3). This page covers the billing
core that V3's M20 adds: price books, plans and proration, monthly
invoices, VAT and withholding tax, credit notes, the ledger, forecasts and
spend controls. Taking payments (cards, transfers, wallets), prepaid
balances, dunning and WHT certificates are in [Payments](payments.md).

Amounts are integers in **kobo** everywhere: in the API, the database and
the ledger. Unit prices may have fractions of a kobo ("34.25" kobo per
GB-hour); each invoice line is rounded to the kobo, halves away from zero.
Invoices and the CLI show "NGN" (PDF core fonts have no ₦ sign); the UI
shows ₦.

## Who sees billing

Organisation **owners** and members with the **billing** role (V3 §3.2).
The billing role sees the organisation and its billing, and nothing in
its projects: billing members can't open or create projects and can't be
added to one. Admins don't see billing, so only owners can give or take
the billing role.

Org → **Billing** (`/org/billing`), `pgdock billing …`, and
`/api/v1/orgs/{org}/billing/…`.

## Plans and price books

Prices live in **price books**: versioned sets of plan fees, inclusions,
overage unit prices, dedicated rates and add-on prices. Every organisation
is on one version. The first book (version 1) is published when the server
first starts, with placeholder prices to replace with ones derived from
measured costs (V3 §3.1):

| Plan | Monthly | Annual | Limits (quota plan) | Overage |
| --- | --- | --- | --- | --- |
| Free | — | — | Personal | none: hard limits |
| Pro | ₦15,000 | ₦150,000 | Pro (new in V3) | per unit above the allowance |
| Team | ₦60,000 | ₦600,000 | Team | lower unit prices, larger allowance |

Allowances and overage are per month in each metric's unit
(`shared_storage_gb_hours`, `backup_storage_gb_hours`, `branch_hours`,
`pooler_transfer_gb`, `webhook_deliveries`, `job_runs_sql`,
`job_runs_http`). Branch storage is part of shared storage and webhook
retries aren't charged.

**Dedicated instances** are billed by the hour for their vCPUs, RAM and
disk at the book's dedicated rates, whatever the plan. An **HA** standby
costs the same as its primary plus the HA premium (20%), and
**synchronous replication** has an hourly price. V3 records the standby's
hours as `ha_vcpu_hours`, `ha_ram_gb_hours` and `ha_disk_gb_hours`, and
`sync_replication_hours`.

### Repricing

Admin → Billing → **Price books**: start a draft from the current book,
edit its prices (JSON), and **Preview** it: each organisation's last
month rated at its book now and at the draft, so the revenue effect shows
before publishing. **Publish** needs an effective date at least 30 days
ahead. The billing contacts of each affected organisation are emailed
what changes for their plan. On the effective date, organisations move to
the new book except:

- **grandfathered** organisations (`PATCH /api/v1/admin/orgs/{org}/billing`,
  which also sets an org's mode, payment terms and price book), which keep
  theirs;
- **annual** terms, which move at renewal.

A published book can't be edited or deleted (the database refuses it).

## Plan changes and proration

Org → Billing → **Change plan…** shows what a change costs before making
it (`dry_run`):

- **Upgrades** apply at once. The new plan is charged for the rest of the
  month by the day (the day of the change included) and the old one is
  credited for the same days. Example: Pro → Team on 13 October (19 of 31
  days left) credits ₦9,193.55 and charges ₦36,774.19.
- **Downgrades** apply on the 1st of next month (or at the end of an
  annual term), unless the org chooses **now**, with a credit for the
  unused part.
- **Annual terms** start the day they are chosen and are paid in full,
  with a credit for the unused part of the month. They renew
  automatically at the price book then current.
- Choosing the current plan again cancels a scheduled change.

The organisation's limits follow its plan (the plan's quota plan), unless
the platform admin gave it another quota plan, which is kept.

## Monthly invoices

The invoice for a month carries that month's usage **in arrears**, the
next month's plan fee **in advance**, and the month's plan changes:

1. **Overage** above each plan's allowance. When the plan changed during
   the month, each part is rated with its share of its plan's allowance
   (by the day) at its plan's prices.
2. **Dedicated** instances, per project: vCPU-, RAM- and disk-hours.
3. **Add-ons**: HA standbys, the HA premium, synchronous replication.
4. **Proration** and annual-term lines from plan changes.
5. The **plan fee** for the next month (monthly terms).

VAT is added at the configured rate (7.5% by default). Organisations that
deduct withholding tax (Billing → Business details) see the expected WHT
on the invoice.

**When:** drafts are generated on the last day of the month and refreshed
as it runs. Once the month's usage is recorded (the hourly recorder has
passed midnight on the 1st), drafts are re-rated with the full month and
**issued**, unless the platform admin holds one (Admin → Billing →
Invoices → Hold). Issuing:

- numbers it `PGD-YYYY-NNNNNN`, sequential per year and never reused;
- freezes the customer's business details and PGDock's (Admin → Billing →
  Settings) on it;
- posts it to the ledger in the same database transaction;
- emails the billing contacts (or, with none, the owners and billing
  members) with a link to it.

Issued invoices are never edited. **Credit notes** (`PGD-CN-YYYY-NNNNNN`)
correct them: Admin → Billing → Invoices → an invoice → Credit note, an
amount before VAT and a reason. VAT is credited at the invoice's rate, and
an invoice's credits can't exceed its total.

Invoices download as PDF from the Billing page, the admin console, and
`pgdock billing invoice <number> --pdf file.pdf`.

**Automatic issue is off by default.** Until payments are live (M21),
drafts are generated for review and the admin issues them. Turn on
Admin → Billing → Settings → *Issue each month's drafts automatically*
when you are ready. Billing starts with the month the server first ran
V3: usage from before is never invoiced.

## The ledger

Every money movement is a balanced double-entry transaction in
`ledger_entries` (V3 §3.3). Issuing an invoice debits the org's
`receivable` and credits `revenue:<plan>`, `revenue:dedicated`,
`revenue:ha` and `vat_payable`; a credit note reverses part of that.
Each transaction has an idempotency key, so posting it twice is a no-op.

The database enforces the invariants: entries can't be updated, deleted
or truncated (post a reversing transaction instead), and a deferred
trigger refuses any transaction whose debits and credits differ when it
commits. Admin → Billing → **Ledger** (`GET /api/v1/admin/ledger/check`)
checks every transaction and shows each account's balance.

## Forecast and spend controls

Billing → **This month so far** shows the month's forecast before VAT:
its plan fee, plan changes, and the usage recorded so far extrapolated to
the whole month. It is refreshed hourly.

**Budget:** an organisation sets a monthly budget; its billing contacts
are emailed when the forecast reaches 50%, 80% and 100% of it, once each
per month.

**Spend cap:** when the forecast's usage charges (overage, dedicated,
add-ons) reach the cap, PGDock pauses new billable resources until the
cap is raised or the month ends:

- new branches, dedicated instances (created, promoted or recovered
  point-in-time) and HA are refused;
- webhook deliveries queue (they are delivered once the cap lifts);
- scheduled job runs are skipped.

Nothing running stops, no database is disconnected and no data is
deleted. Storage above the plan follows V2's storage locks as before.

**Cost estimates:** the Promote and Enable HA panels show what the
instance or the standby will cost a month (730 hours) at the
organisation's prices. Anyone in the organisation can ask:
`POST /api/v1/orgs/{org}/billing/estimate`.

## Settings

Admin → Billing → **Settings** (`/api/v1/admin/billing/settings`):

- VAT rate (default 0.075) and WHT rate (default 0.05). They are
  configuration: confirm them with your accountant.
- PGDock's legal name, address, TIN, VAT registration and billing email,
  as invoices show them.
- Automatic issue on the 1st.
- Offering USDT top-ups through iSpend (off by default).
- Deleting dedicated projects for non-payment (off by default: the
  notice is sent and nothing is deleted).

## Revenue

Admin → **Revenue** (`/api/v1/admin/revenue`, platform admins), the
first version of V3 §7.2. It shows the following, month by month:

- **MRR**: each paying organisation's plan fee at its price book, with
  annual plans spread over 12 months. ARR is MRR × 12.
- **MRR movements** against the month before:
  - new: an organisation that paid nothing before;
  - expansion: one that pays more;
  - contraction: one that pays less;
  - churned: one that pays nothing now.
- Paying organisations and ARPA.
- **Conversions**: organisations that were on Free in the month before
  and pay now, out of the Free organisations at the start of the month.
- **Metered revenue**: the usage lines of the month's invoices (overage,
  dedicated instances, add-ons). It is kept apart from MRR.
- **Invoiced and collected**: invoices issued for the period, and how
  much of them has been paid.
- **Receivables by age**: open invoices bucketed by days past due (not yet
  due, 1–30, 31–60, 61–90, over 90), and the WHT still waiting for
  credit-note certificates.

The figures come from a snapshot of each organisation's plan, taken every
day and whenever the page is opened. A past month shows its last
snapshot. Suspended organisations count as paying nothing. **CSV for the
accountant** (`?format=csv`) has the same table, the ageing and the WHT,
in naira. Costs, margins and the FX view are on Platform → Costs & margins: see
[Capacity and costs](capacity.md).

## Payments

Paying invoices, prepaid balances, WHT credit notes, dunning and
reconciliation: see [Payments](payments.md).
