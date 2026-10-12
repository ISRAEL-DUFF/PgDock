# PGDock — V3 Specification & Build Plan

*Builds on the V1 and V2 specs. References like "V2 §10.4" point to those documents.*

|  |  |
| --- | --- |
| **Status** | Draft v2 — payments on Flutterwave and iSpend |
| **Theme** | A reliable, billable managed Postgres business for Nigerian customers |
| **Builds on** | V2: organisations, multi-tenant hardening, quotas, usage records, branching, webhooks/jobs, CLI and tokens |
| **Market** | Nigerian startups and SMEs, paying in naira. International customers later. |

---

## 1. Overview

### 1.1 Positioning

> **Managed Postgres you can pay for in naira — no dollar card limits, no FX surprises on your bill, and the option to keep your data in Nigeria.**

Nigerian teams on Supabase, Neon, or AWS pay in dollars on cards with international spending caps, see their bills move with the exchange rate, and have no in-country hosting option. PGDock V3 solves those three problems for that market.

### 1.2 What V3 adds

| # | Area | Section |
| --- | --- | --- |
| 1 | Reliability: standby pooler, HA dedicated instances, zero-downtime moves, multiple Postgres versions, cross-region backup copies, status page, SLA | §2 |
| 2 | Billing: plans, naira pricing, card payments via Flutterwave, transfers and wallet payments via iSpend, prepaid credits, VAT invoices, withholding tax, dunning, spend controls | §3 |
| 3 | Free tier with automatic pause and archive | §4 |
| 4 | Capacity and cost: automatic VM provisioning, node draining and rebalancing, cost attribution | §5 |
| 5 | Regions, including a Lagos region for data residency | §6 |
| 6 | Business operations: support console, revenue dashboard, legal documents, open signup | §7 |
| 7 | Query insights | §8 |

### 1.3 Not in V3

Backend services (auth, auto-generated REST, storage, realtime, edge functions) are planned for V4. Also deferred: dollar billing and international payment processors, read replicas, SSO/SAML, a Terraform provider, schema diff, branch data masking.

### 1.4 Principles

- **Only charge for what you can honour.** The SLA covers HA dedicated instances only. Everything else is sold as best-effort, and the terms say so plainly.
- **Money is a ledger, not a column.** Every charge, payment, credit, and tax deduction is an immutable ledger entry in kobo (integer). Balances are derived, never edited. Every external payment event is processed idempotently.
- **Naira first, FX-aware.** Prices are in naira, but costs are in euros and dollars. Repricing is a configuration change with customer notice, never a deploy.
- **Usage drives everything.** V2's usage records are the single source for rating, invoices, cost attribution, and margin reporting.
- **Status lives elsewhere.** The status page and its alerting run on infrastructure separate from PGDock, so an outage can't take down the page announcing it.

---

## 2. Reliability

### 2.1 Standby edge pooler

The edge pooler was the last single point of failure in V1 and V2.

- **Two pooler hosts per region** (on different physical hosts, enforced with a Hetzner placement group), each running both PgBouncer processes (`:5432` session, `:6543` transaction).
- A **floating IP** (Hetzner Floating IP) points at the active host. `keepalived` on both hosts health-checks local PgBouncer and moves the IP on failure; the control plane also checks health externally and can move the IP through the Hetzner API if `keepalived` is split.
- **Config sync:** `pgdock-server` writes pooler config to both hosts on every change (route, auth file) and verifies both hashes match. A host with a stale config refuses to take the floating IP.
- **TLS certificates** are distributed to both hosts.
- On failover, clients reconnect (existing connections drop). Failover target: **under 10 seconds**.

### 2.2 High availability for dedicated instances (paid add-on)

**Topology:** a primary and one streaming standby on **different nodes** (placement group), managed by **Patroni** with a 3-member **etcd** cluster spread across the control node and two other nodes.

| Aspect | Design |
| --- | --- |
| Replication | Asynchronous by default (data-loss window of seconds at most). **Synchronous** optional per project (no committed data lost on failover, at the cost of write latency). |
| Failover | Patroni promotes the standby when the primary fails its leader lease. Target: **under 60 seconds** from failure to accepting writes. |
| Routing | `pgdock-server` watches each Patroni cluster's leader through its REST API and rewrites that project's pooler route to the new primary, with `PAUSE`/`RESUME` around the switch. Clients keep the same URL. |
| Fencing | Patroni's watchdog plus the leader lease prevent two primaries. The old primary rejoins as a standby with `pg_rewind`. |
| Backups | WAL-G runs from the current primary; Patroni callbacks move the archive role on failover. |
| Manual switchover | Planned switchover from the UI/CLI for maintenance, with a few seconds of paused writes and no data loss. |
| Visibility | Project page shows role of each member, replication lag, last failover, and failover history. |

Enabling HA on an existing dedicated project creates the standby from a base backup and joins it without downtime. Disabling HA destroys the standby.

### 2.3 Zero-downtime moves with logical replication

V1 and V2 move projects with dump/restore and a write freeze. V3 adds a **logical replication** path used for promotion, demotion, node moves, draining, region moves, and major version upgrades.

**Flow**

1. Create the target (database, roles with the same SCRAM verifiers, extensions).
2. Copy the **schema** with `pg_dump --schema-only`.
3. Create a **publication** on the source for all tables and a **subscription** on the target. Postgres performs the initial data copy, then streams changes.
4. Wait until replication lag stays under a threshold (default 1 MB) for 30 seconds.
5. **Cutover:** `PAUSE` the pooler route; wait for lag to reach zero; copy **sequence values** (logical replication doesn't carry sequences) with `setval` plus a safety margin; verify row counts on a sample of tables.
6. Switch the route and `RESUME`. Writes are paused for **a few seconds**, regardless of database size.
7. Drop the subscription and publication. Keep the source read-only for 48 hours as a rollback option.

**Preconditions and fallbacks**

- Tables without a primary key get `REPLICA IDENTITY FULL` for the move (slower for updates); the preflight lists them.
- **DDL isn't replicated.** During a move, PGDock installs an event trigger on the source that blocks DDL with a clear error, and the UI shows the move in progress.
- Large objects (`lo_*`) aren't replicated; projects using them fall back to dump/restore.
- Any precondition failure, or a failed cutover check, falls back to the V1 dump/restore path or aborts with the source untouched.

### 2.4 Multiple Postgres versions and major upgrades

- **Supported versions:** the two most recent Postgres majors at any time (e.g. 17 and 18), plus the next major once it has had its first minor release. A version is retired with **6 months' notice**.
- **Shared tier:** one shared cluster per supported version in each region. New projects choose a version (default: newest).
- **Dedicated:** any supported version.
- **Major upgrade** = a logical-replication move (§2.3) into a new instance on the target version. The preflight checks extension compatibility on the target version and flags deprecated features found in the schema.
- **Minor upgrades** remain rolling restarts (V1 §11.3), now automated in a weekly maintenance window per region, with HA projects switched over instead of restarted.

### 2.5 Cross-region backup copies

- Every backup written to a platform storage target is **copied to a second target in a different region or provider** (e.g. R2 plus Backblaze B2), asynchronously, with checksum verification.
- The weekly automated restore test (V1 §6.5) alternates between primary and copy targets.
- Projects with the **data residency** setting (§6.3) are excluded from copies outside their region.
- Org (bring-your-own) targets aren't copied; that's the org's responsibility.

### 2.6 Status page and incidents

- A **public status page** listing components per region: control plane & dashboard, edge pooler, shared tier, dedicated instances, backups, webhooks & jobs, billing.
- **Hosted separately** from PGDock: either a hosted status service or a tiny static site on a different provider, updated by an external health checker that probes from outside PGDock's network.
- **Incidents:** created automatically when probes fail for a sustained period, or manually by the platform admin. Updates are posted from the admin console and pushed to the status page.
- **Subscriptions:** anyone can subscribe by email; paying orgs are subscribed automatically for components they use.
- **Uptime history:** 90 days per component, using the same measurements as the SLA.

### 2.7 SLA

| Offering | Commitment |
| --- | --- |
| **HA dedicated instances** | **99.9% monthly availability** of the project's pooler endpoint accepting connections and executing queries. |
| Standard dedicated instances | Best-effort, no SLA. |
| Shared tier (Free and paid plans) | Best-effort, no SLA. |

**Measurement:** an external probe connects through the pooler and runs a trivial query every minute. A minute is unavailable if the probe fails from two vantage points. Scheduled maintenance announced 72 hours in advance, customer-caused outages, and suspension for non-payment are excluded.

**Service credits** (applied to the next invoice, on request within 30 days):

| Monthly availability | Credit (of that instance's monthly charge) |
| --- | --- |
| \< 99.9% | 10% |
| \< 99.0% | 25% |
| \< 95.0% | 50% |

---

## 3. Billing

### 3.1 Plans and pricing structure

**Plans (per organisation)**

| Plan | For | Includes | Overages |
| --- | --- | --- | --- |
| **Free** | Hobby projects, evaluation | 2 shared projects (paused when inactive, §4), small storage, 2 branches, daily backups with 7-day retention, community support | None — hard limits |
| **Pro** | Small teams and startups | Flat monthly fee with generous included shared storage, branches, backup storage, webhook deliveries, job runs; email support | Metered per unit above included amounts |
| **Team** | Larger teams | Higher inclusions, more members and projects, priority support with faster response times | Metered, at a lower unit rate |

**Dedicated instances** are priced per size, per month, billed by the hour:

| Size | vCPU | RAM | Disk (default) |
| --- | --- | --- | --- |
| D-Small | 2 | 4 GB | 40 GB |
| D-Medium | 4 | 8 GB | 80 GB |
| D-Large | 8 | 16 GB | 160 GB |
| D-XLarge | 16 | 32 GB | 320 GB |

**Add-ons:** HA (adds the standby's cost plus a premium), extra disk per GB, extended point-in-time recovery (14 or 30 days), extra backup retention, Lagos region premium (§6), synchronous replication.

**Setting prices.** Prices are not fixed in this spec. Each unit price is derived from measured cost:

```
unit price = unit cost × (1 + target gross margin) × FX buffer
```

- **Unit cost** comes from cost attribution (§5.4): the real monthly cost of a shared GB, a dedicated vCPU, backup storage, and so on, from at least one month of V2 usage and provider bills.
- **Target gross margin**: e.g. 60–70% for shared tiers, 40–50% for dedicated (more competitive).
- **FX buffer**: covers expected naira movement until the next repricing (e.g. 10–15%).

### 3.2 Billing accounts

Each organisation is a billing account with:

- Plan, billing mode (§3.5), price book version (§3.9).
- **Billing contacts** (receive invoices and payment emails) separate from org admins.
- **Business details** for B2B invoices: legal name, address, TIN, VAT status.
- **Withholding tax applicability**: whether this customer deducts WHT (§3.7).
- Provider customer records (Flutterwave customer, iSpend merchant customer) and any saved payment authorisations.
- A **dedicated virtual account** number for transfers (§3.4.3), issued by the primary virtual-account provider.

Only org owners and members with a new **billing** org role can see or change billing. (V3 adds `billing` alongside `owner`, `admin`, `member`: billing-only access without project access, for finance staff.)

### 3.3 The ledger

All money movement is recorded in an append-only, double-entry ledger. Amounts are integers in **kobo**.

| Account (per org) | Meaning |
| --- | --- |
| `receivable` | What the org owes (postpaid) |
| `credit_balance` | Prepaid funds and credits available |
| `wht_receivable` | WHT deducted by the customer, awaiting a credit note |

| Platform account | Meaning |
| --- | --- |
| `revenue:<plan/product>` | Earned revenue by product line |
| `vat_payable` | VAT collected, owed to the tax authority |
| `cash:flutterwave`, `cash:ispend`, `cash:bank` | Money received, per collection channel (settlement accounts) |
| `fees:<provider>` | Processing fees charged by each provider |
| `credits_issued` | SLA credits, promotional credits |

Each business event posts a balanced set of entries (debits equal credits):

- **Invoice issued:** debit `receivable`, credit `revenue` and `vat_payable`.
- **Card or transfer payment:** debit `cash:*`, credit `receivable` (postpaid) or `credit_balance` (prepaid top-up).
- **Prepaid usage:** debit `credit_balance`, credit `revenue` and `vat_payable`.
- **WHT deduction:** debit `wht_receivable`, credit `receivable`. When the credit note arrives, the WHT entry is marked evidenced.
- **SLA credit:** debit `credits_issued`, credit `credit_balance`.
- **Refund:** reverses the payment entries; nothing is ever edited or deleted.

**Rules:** every entry references its source (invoice, provider event, admin action) and an **idempotency key**; a nightly job reconciles the ledger against each provider's transaction and settlement reports and flags differences; provider fees are posted as separate entries so gross revenue and net settlement both reconcile; balances are computed from entries (and cached with a version for speed).

### 3.4 Payments

#### 3.4.1 Payment provider interface

PGDock never talks to a payment provider directly from billing logic. Each provider sits behind one interface, so channels can be added, swapped, or failed over without touching rating, invoicing, or the ledger. (This mirrors the composable provider pattern used in iSpend.)

```go
type PaymentProvider interface {
    Name() string                                                  // "flutterwave" | "ispend"
    Capabilities() Capabilities                                    // cards, saved-auth charges, virtual accounts, wallet debit, mandates, refunds
    CreateCustomer(ctx, Org) (ProviderCustomer, error)
    Checkout(ctx, CheckoutRequest) (CheckoutSession, error)        // hosted page: first card payment, top-up, wallet approval
    ChargeSaved(ctx, ChargeRequest) (Charge, error)                // tokenised card or approved wallet mandate
    IssueVirtualAccount(ctx, Org) (VirtualAccount, error)          // permanent NUBAN for the org
    Verify(ctx, providerRef string) (Transaction, error)           // authoritative status lookup
    ParseWebhook(ctx, *http.Request) (Event, error)                // authenticates and normalises provider events
    Refund(ctx, RefundRequest) (Refund, error)
    ListTransactions(ctx, from, to time.Time) ([]Transaction, error) // for reconciliation and missed-event recovery
}
```

Every provider event is normalised into one internal event shape (`payment.succeeded`, `payment.failed`, `transfer.received`, `refund.completed`, `mandate.revoked`), stored in `payment_events`, and posted to the ledger by the same code regardless of provider.

**Channel routing (configuration, not code):**

| Channel | Primary | Fallback |
| --- | --- | --- |
| Cards (first payment and saved-card charges) | Flutterwave | — |
| Dedicated virtual accounts (bank transfer) | iSpend | Flutterwave virtual accounts |
| Wallet payments ("Pay with iSpend") | iSpend | — |
| Prepaid top-ups | Customer's choice: card (Flutterwave), transfer (iSpend VA), or iSpend wallet | — |

If the primary virtual-account provider is unavailable when an org needs an account, PGDock issues one from the fallback; an org can hold accounts from both, and transfers into either settle the same way.

#### 3.4.2 Cards via Flutterwave

- The first card payment happens on Flutterwave's hosted checkout (card details never touch PGDock), with 3-D Secure/OTP handled there.
- A successful charge returns a **card token**. PGDock stores the token encrypted (plus brand, last four digits, expiry for display) and uses Flutterwave's **tokenised charge** for each later invoice and auto top-up.
- An org can save several cards and choose a default. Expiring cards trigger reminder emails 30 and 7 days before expiry.
- Card payments are also available as a one-off option on any invoice, for customers who don't want a saved card.

#### 3.4.3 Bank transfer via iSpend virtual accounts

- Each paying org gets a **permanent virtual account (NUBAN)** issued through iSpend. Any transfer into it is attributed to that org through iSpend's webhook.
- Incoming transfers settle the oldest open invoice (postpaid) or top up the credit balance (prepaid). Overpayments become credit. Transfers short by exactly the expected WHT follow §3.7.
- If iSpend can't issue an account (outage, or before its licensing and banking arrangements are live), PGDock issues a Flutterwave virtual account instead. Both kinds behave identically in the dashboard.

#### 3.4.4 Wallet payments via iSpend

For customers who hold an iSpend wallet:

- **Pay with iSpend** on any invoice or top-up: the customer approves the debit in iSpend, and PGDock receives the confirmation by webhook.
- **Recurring mandate (optional):** the customer authorises PGDock to debit their iSpend wallet for invoices up to a monthly limit they set. Revoking the mandate in iSpend sends a `mandate.revoked` event, and PGDock asks for another payment method.
- Wallet payments settle instantly into PGDock's iSpend merchant wallet, avoiding card failure rates and bank transfer delays.

#### 3.4.5 Stablecoin top-ups via iSpend (optional, off by default)

Because iSpend handles crypto as first-class money, PGDock can accept **USDT top-ups of the prepaid credit balance** through iSpend:

- The naira credit amount is **fixed at the rate iSpend quotes at receipt** and recorded with the quote reference. PGDock's ledger only ever holds naira; any conversion happens inside iSpend.
- Invoices are never priced in crypto, and this path is prepaid-only.
- Kept behind a platform setting, off by default, until the regulatory position for accepting crypto-funded payments is confirmed with legal advice.

#### 3.4.6 The iSpend contract PGDock needs

iSpend is built in-house, so V3 defines the merchant capabilities PGDock depends on. These become requirements for iSpend's merchant API:

| Capability | Endpoint / event |
| --- | --- |
| Create merchant customer | `POST /merchant/customers` |
| Issue permanent virtual account for a customer | `POST /merchant/customers/:id/virtual-accounts` |
| Hosted payment / approval page (wallet, top-up) | `POST /merchant/checkout-sessions` |
| Create and revoke recurring wallet mandates with a limit | `POST /merchant/mandates`, `DELETE /merchant/mandates/:id` |
| Charge against a mandate (idempotent by reference) | `POST /merchant/mandates/:id/charges` |
| Verify a transaction by reference | `GET /merchant/transactions/:ref` |
| List transactions and settlements for a period | `GET /merchant/transactions?from=&to=`, `GET /merchant/settlements` |
| Refund | `POST /merchant/refunds` |
| Webhooks: `transfer.received`, `payment.succeeded`, `payment.failed`, `mandate.revoked`, `refund.completed` | Signed with HMAC-SHA256 over the raw body, with a timestamp header for replay protection |
| Sandbox environment | Separate keys and base URL, with simulated transfers |

PGDock should be iSpend's **first merchant integration**, which makes it a useful design partner for iSpend's merchant API as well as a payment channel for PGDock.

#### 3.4.7 Manual payments (platform admin)

For transfers to the company's ordinary bank account, cheques, or negotiated arrangements: the admin records the payment with a reference and proof of payment. Recorded in the ledger as `cash:bank`.

#### 3.4.8 Webhook handling and reliability

- **Authentication per provider:** Flutterwave webhooks are checked against the configured secret hash in the `verif-hash` header; iSpend webhooks by HMAC signature and timestamp (§3.4.6).
- **Re-verification:** before anything is posted to the ledger, PGDock calls the provider's verify endpoint for the transaction and checks amount, currency (NGN), and reference. Webhook bodies are never trusted on their own.
- **Idempotency:** events are keyed by provider and provider event/transaction ID; duplicates are no-ops.
- **Missed events:** a job re-queries each provider's recent transactions every hour and the full previous day nightly, posting anything not yet seen.
- **Provider outage:** charges against an unavailable provider are retried with backoff and don't count as customer payment failures for dunning purposes; the billing banner offers the other available channels.

### 3.5 Billing modes

**Postpaid (default for Pro and Team)**

- Plan fee billed **in advance** on the 1st of each month; metered usage and hourly dedicated charges billed **in arrears** for the previous month on the same invoice.
- Card customers: charged automatically on invoice issue.
- Transfer customers: invoice due in **7 days** (Pro) or **14 days** (Team, or by agreement).

**Prepaid (opt-in, or required for some customers)**

- The org keeps a credit balance. Usage is rated **daily** and deducted from it.
- Alerts at 50%, 25%, and 10% of the projected month's spend remaining, and when the balance covers fewer than 3 days of usage.
- Auto top-up with a saved card is optional (top up ₦X when below ₦Y).
- At zero balance: **3-day grace**, then the dunning path (§3.8).

**Annual plans:** Pro and Team can be paid yearly in advance at a discount. The price is locked for the term (§3.9), and metered overages are still billed monthly.

### 3.6 Rating and invoicing

**Rating** turns usage into money:

1. Read `usage_records` (V2 §10.9) for the period.
2. Apply the org's **price book version** (§3.9) and plan inclusions.
3. Produce **line items**: plan fee, each overage metric, each dedicated instance (hours × hourly rate per size), each add-on, credits.
4. **Proration:** plan changes mid-month prorate the plan fee by the day. Upgrades take effect immediately; downgrades take effect at the next cycle unless the org chooses immediately (with a credit for the unused part).

**Invoices**

- Sequential numbering per year (`PGD-2027-000123`), never reused.
- Show the org's business details and TIN, PGDock's company details, TIN, and VAT registration, line items, subtotal, **VAT** (currently 7.5%; the rate is configuration, confirm with your accountant), and total in naira.
- Generated as **PDF** and HTML, emailed to billing contacts, and available in the dashboard.
- **Credit notes** for corrections and refunds; issued invoices are never edited.
- **Receipts** for each payment.

**Draft review:** each month's invoices are generated as drafts on the last day of the month and issued automatically on the 1st unless the platform admin holds one for review (e.g. disputed usage).

### 3.7 Withholding tax

Nigerian corporate customers often deduct withholding tax from service invoices and pay the rest, later providing a WHT credit note.

- Orgs flagged as WHT-deducting show the **expected WHT amount** on each invoice (the rate is configuration; confirm the applicable rate with your accountant).
- When a transfer arrives for **invoice total minus WHT**, PGDock matches it to the invoice, posts the WHT amount to `wht_receivable`, and marks the invoice **paid (WHT pending evidence)**.
- The customer uploads the **credit note** in the dashboard (or the admin attaches it). The WHT entry becomes evidenced.
- The admin console lists **outstanding WHT credit notes** by age, for follow-up, and exports WHT receivable for tax filing.
- Payments short by an amount that doesn't match the expected WHT are treated as partial payments, not WHT.

### 3.8 Dunning and non-payment

**Card payment failure:** retry on days 0, 3, 5, and 7 with emails each time and an in-app banner asking for a new card.

**Overdue invoice or exhausted prepaid balance:**

| Day after due / zero balance | Action |
| --- | --- |
| 0 | Email + banner |
| 3 | Email; creating new projects, branches, and dedicated instances is blocked |
| 7 | Final notice email |
| 10 | **Suspension** (V2 §10.8): connections disabled, data retained, one final backup |
| 40 | Paid resources beyond Free limits are **scheduled for deletion** with 7 days' notice; final backups retained 30 days |

Paying the outstanding amount at any point reverses the restrictions automatically. The platform admin can extend grace for an org. Free-plan orgs are never suspended for billing.

### 3.9 Price books and repricing

- Prices live in **price books**: versioned tables of unit prices, plan fees, inclusions, and add-on prices, each with an effective date.
- Every org is attached to a price book version. New orgs get the current version.
- **Repricing:** the platform admin creates a new version with an effective date at least **30 days** ahead. Affected billing contacts are emailed with a comparison of old and new prices for their typical usage. On the effective date, orgs move to the new version, except:
  - **Annual plans** keep their version until renewal.
  - **Grandfathered** orgs (admin choice) keep their version.
- The terms of service state that prices are reviewed quarterly and may change with 30 days' notice.
- A **price preview** in the admin console estimates each org's next invoice under a draft price book, so the revenue effect of a change is visible before publishing.

### 3.10 Spend controls

- **Budget alerts:** an org sets a monthly budget; alerts at 50%, 80%, and 100% of forecast spend.
- **Hard spend cap (optional):** when projected overage charges would exceed the cap, PGDock stops creating new billable resources and throttles metered features (webhook deliveries queue, job runs pause, new branches blocked). It **never deletes data or disconnects databases** because of a cap; storage over plan follows V2's storage locks.
- **Cost estimate** before every billable action: promoting, enabling HA, resizing, adding disk.
- **Current-month forecast** on the Billing page, updated hourly.

---

## 4. Free Tier: Pause and Archive

### 4.1 Limits

Free orgs use a **Free** quota plan (V2 §10.3) with hard limits and no overages. One Free org per user (the personal org), and verified email plus TOTP as in V2. Open signup (§7.4) applies Turnstile and per-IP limits.

### 4.2 Pause after inactivity

- A Free project with **no client connections for 7 days** is paused. Owners get an email 24 hours before.
- **Paused** means: the pooler route points to the **waker** instead of the shared cluster, and the database has `ALLOW_CONNECTIONS false`. Disk is kept.
- The **waker** is a small Go service that speaks enough of the Postgres wire protocol to accept a connection, return a clear error (*"This project was paused for inactivity and is resuming. Retry in about 30 seconds."*), and trigger a resume operation. Resume restores the route and connections; retries then succeed.
- Owners can also resume from the dashboard or CLI. Scheduled jobs and webhooks on paused projects are suspended and resume with the project.

### 4.3 Archive after long inactivity

- A Free project **paused for 90 days** is **archived**: a final logical backup is verified, then the database is dropped, freeing disk.
- An archived project keeps its metadata, connection string, and credentials. Resuming restores it from the archive backup (minutes, not seconds), and the waker message says so.
- Archived projects are deleted after **12 months** archived, with 30 and 7 days' notice by email.

Paid plans are never paused or archived.

---

## 5. Capacity and Cost

### 5.1 Provider interface

```go
type Provider interface {
    CreateServer(ctx, ServerSpec) (Server, error)   // type, region, image, cloud-init, placement group, labels
    DeleteServer(ctx, id string) error
    ListServers(ctx, filter) ([]Server, error)
    CreateVolume(ctx, VolumeSpec) (Volume, error)
    AttachVolume(ctx, volumeID, serverID string) error
    AssignFloatingIP(ctx, ipID, serverID string) error
    PriceCatalog(ctx) ([]ServerPrice, error)         // for cost attribution and estimates
}
```

- **Hetzner Cloud** is the first implementation.
- A **manual** provider covers machines PGDock can't create through an API (colocated servers in Lagos, §6.2): the admin registers them, and PGDock treats them as a fixed pool.
- New servers boot with **cloud-init** that installs the agent, hardens SSH, joins the private network, and registers with a one-time token (V1 agent registration, now automatic).
- Other providers (including a future in-house VPS platform) can be added behind the same interface.

### 5.2 Automatic capacity

- **Thresholds per region and tier:** e.g. add a shared node when projected disk use exceeds 70% within 14 days, or a dedicated host when free capacity can't fit the largest size.
- When a threshold trips, PGDock creates a **capacity proposal** (server type, region, monthly cost). Proposals within the platform's **monthly infrastructure budget** are applied automatically; above it, they wait for admin approval.
- New dedicated instances are placed on hosts with free capacity; if none fit, a host is provisioned first (adding a few minutes to creation, shown to the user).

### 5.3 Draining and rebalancing

- **Drain a node:** every project on it is moved elsewhere with zero-downtime moves (§2.3), one at a time, respecting HA placement rules. Used for maintenance, hardware issues, and decommissioning.
- **Rebalancer:** weekly, it proposes moves that even out disk and CPU across shared nodes in a region. The admin approves a batch, or enables automatic rebalancing during the maintenance window.
- **Empty nodes** are flagged for deletion after 24 hours, then deleted (if from an API provider) to stop paying for them.

### 5.4 Cost attribution

- Each node's monthly cost comes from the provider's price catalog (or a manually entered cost for the manual provider), plus object storage, egress, floating IPs, and fixed overheads (control plane, status page, email).
- Costs are allocated to orgs daily from usage records: shared-node cost by each project's share of storage and connection-hours; dedicated-host cost by instance size; backup storage by GB.
- Result: **cost per org, per plan, and per unit**, which feeds pricing (§3.1) and the margin dashboard (§7.2).

---

## 6. Regions

### 6.1 Region model

- A **region** groups nodes, pooler hosts, a storage target, and a status-page component: e.g. `eu-central` (Hetzner) and `ng-lagos`.
- Each region has its **own pooler pair** and hostname (`db.eu.pgdock.ng`, `db.ng.pgdock.ng`), because connections should terminate in the same region as the database.
- **Projects choose a region at creation.** Moving a project between regions is a zero-downtime move (§2.3) that changes the connection hostname; the old hostname keeps routing for 30 days through a cross-region pooler forward.
- The control plane stays in one place; only data-path components live in each region.

### 6.2 The Lagos region

**Infrastructure options** (choose one before M24):

- **Colocation** of PGDock-owned servers in a Lagos data centre, registered through the manual provider.
- **A local cloud or VPS provider** with an API, added as a provider implementation.

**Minimum requirements:** redundant power and cooling, at least two upstream carriers, enough public IPv4 addresses, private networking between hosts, out-of-band management for colocated servers, and in-country S3-compatible object storage (a provider's, or MinIO on separate PGDock hosts with erasure coding).

**What's offered at launch:** dedicated instances (with HA), in-country backups, and branches of Lagos projects (placed in Lagos). A Lagos shared tier can follow if demand justifies it.

**Pricing:** the Lagos region premium (§3.1 add-on) reflects its higher cost and is set from cost attribution like everything else.

### 6.3 Data residency setting

A per-project **"data must stay in Nigeria"** setting, available for Lagos projects:

- Backups stay on in-country targets only; cross-region copies (§2.5) are skipped.
- Branches can only be created in Lagos.
- Moving the project out of Lagos requires turning the setting off (owner only, step-up reauth, audited).
- Exports (V2 §10.10) are still allowed, since the customer controls where their own export goes.

Whether a specific customer's regulator requires in-country hosting is the customer's determination; PGDock provides the controls and documents what they guarantee.

---

## 7. Business Operations

### 7.1 Support

- **Support inbox:** an inbound email address creates tickets, matched to an org by the sender's email. Tickets can also be opened from the dashboard (with the org attached automatically).
- **WhatsApp channel** for Pro and Team: messages via the WhatsApp Business Platform are threaded into the same ticket system and linked to the org by the registered phone number.
- **Support console** (platform admin and support staff): ticket list and thread, plus the org's plan, billing status, usage, recent operations, active incidents, and quota state — **without** tenant data. Accessing data still requires break-glass (V2 §2.4).
- **Support staff role:** a new platform role `support` that can see the support console and org metadata, but can't change quotas, billing, or nodes.
- **Response targets** by plan (e.g. Free: community/best effort; Pro: 1 business day; Team: 4 business hours; HA incidents: 1 hour, any time), published in the terms. Business hours are Nigerian business hours (WAT).

### 7.2 Revenue and cost dashboard

For the platform admin:

- **MRR and ARR**, with new, expansion, contraction, and churned MRR each month.
- **Active paying orgs**, ARPA, conversion from Free to paid.
- **Collections:** invoices issued vs paid, accounts-receivable ageing, outstanding WHT credit notes.
- **Costs and margin:** infrastructure cost by region and tier, gross margin per plan and per org (from §5.4), cost of the free tier.
- **FX view:** infrastructure cost in its billing currency and in naira at the current rate, so margin erosion is visible before the next repricing.
- CSV export of everything for the accountant.

### 7.3 Legal documents

Versioned, with recorded acceptance (V2 §10.10):

- Terms of service, including pricing review and repricing notice.
- **SLA** (§2.7).
- **Data processing agreement**, with PGDock as processor and the customer as controller; sub-processors listed (Hetzner, Cloudflare, Flutterwave, iSpend, email provider, Lagos provider).
- Privacy notice.
- Acceptable use policy.
- An optional **order form** for Team customers with negotiated terms.

The company issuing invoices must be registered, with a TIN and VAT registration in place before the paid launch. Get advice on data protection registration and audit obligations for a platform holding many organisations' data.

### 7.4 Open signup

With Free tier economics controlled by pause and archive, signup moves from invite-only to **open**, with Turnstile, per-IP limits, verified email, mandatory TOTP, and one Free org per user (V2 §3.1). A marketing site and pricing page live outside the PGDock binary and link into signup.

---

## 8. Query Insights

Available on Pro, Team, and dedicated projects.

- **Collection:** `pg_stat_statements` is enabled cluster-wide on shared clusters and on every dedicated instance. Every 5 minutes, the control plane snapshots it (as `pgdock_admin`, which can read all rows) and stores **deltas per query per project**, filtered by database ID. Tenants never query the view directly on shared clusters.
- **Top queries:** by total time, mean time, calls, and rows, over 1h/24h/7d/30d, with normalised query text.
- **Query detail:** time-series of calls and latency; **EXPLAIN** (without `ANALYZE`) run as the project role from a captured example, with the plan shown as a tree.
- **Slow-query log:** statements over a threshold (`log_min_duration_statement` on dedicated; derived from the reaper and snapshots on shared).
- **Index suggestions:** heuristics from `pg_stat_user_tables` (large tables with heavy sequential scans), foreign keys without indexes, and filter columns in top queries. Where the `hypopg` extension is available, PGDock tests a hypothetical index and shows the estimated plan improvement. Suggestions come with the `CREATE INDEX CONCURRENTLY` statement and a "Save as migration" option (V2 §4.3).
- **Unused and duplicate indexes**, with their size.
- **Table bloat estimates** and the reclaim-space action (V2 §10.4).
- **Lock and wait view:** current blocking chains for the project.

---

## 9. Data Model Changes

```sql
-- Regions (§6)
CREATE TABLE regions (
  id            text PRIMARY KEY,               -- 'eu-central', 'ng-lagos'
  name          text NOT NULL,
  pooler_host   text NOT NULL,                  -- db.eu.pgdock.ng
  provider      text NOT NULL,                  -- hetzner | manual | …
  storage_target_id uuid REFERENCES storage_targets(id),
  copy_target_id    uuid REFERENCES storage_targets(id),  -- cross-region copies (§2.5)
  status        text NOT NULL DEFAULT 'active'
);
ALTER TABLE nodes     ADD COLUMN region_id text REFERENCES regions(id), ADD COLUMN provider_server_id text,
                      ADD COLUMN monthly_cost_minor bigint, ADD COLUMN cost_currency text;
ALTER TABLE instances ADD COLUMN region_id text REFERENCES regions(id),
                      ADD COLUMN ha_enabled boolean NOT NULL DEFAULT false,
                      ADD COLUMN sync_replication boolean NOT NULL DEFAULT false;
ALTER TABLE projects  ADD COLUMN region_id text REFERENCES regions(id),
                      ADD COLUMN data_residency boolean NOT NULL DEFAULT false,
                      ADD COLUMN lifecycle text NOT NULL DEFAULT 'active';  -- active|paused|archived

-- HA members (§2.2)
CREATE TABLE instance_members (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  instance_id  uuid NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  node_id      uuid NOT NULL REFERENCES nodes(id),
  role         text NOT NULL,                   -- leader|replica
  lag_bytes    bigint,
  updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE failover_events (
  id bigserial PRIMARY KEY, instance_id uuid NOT NULL, from_node uuid, to_node uuid,
  kind text NOT NULL,                           -- failover|switchover
  duration_ms int, occurred_at timestamptz NOT NULL DEFAULT now()
);

-- Billing (§3)
CREATE TABLE price_books (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  version      int UNIQUE NOT NULL,
  effective_at timestamptz NOT NULL,
  prices       jsonb NOT NULL,                  -- plans, inclusions, unit prices, sizes, add-ons (kobo)
  notes        text,
  published_at timestamptz
);

CREATE TABLE billing_accounts (
  org_id            uuid PRIMARY KEY REFERENCES organizations(id),
  plan              text NOT NULL,              -- free|pro|team
  term              text NOT NULL DEFAULT 'monthly', -- monthly|annual
  term_ends_at      timestamptz,
  mode              text NOT NULL DEFAULT 'postpaid', -- postpaid|prepaid
  price_book_version int NOT NULL REFERENCES price_books(version),
  grandfathered     boolean NOT NULL DEFAULT false,
  legal_name text, address text, tin text, vat_registered boolean,
  deducts_wht       boolean NOT NULL DEFAULT false,
  provider_customers jsonb NOT NULL DEFAULT '{}',  -- {flutterwave: id, ispend: id}
  payment_terms_days int NOT NULL DEFAULT 7,
  budget_minor      bigint, spend_cap_minor bigint,
  auto_topup        jsonb,                      -- {threshold_minor, amount_minor}
  dunning_state     text NOT NULL DEFAULT 'ok', -- ok|retrying|overdue|restricted|suspended
  grace_until       timestamptz
);

CREATE TABLE billing_contacts (
  org_id uuid NOT NULL REFERENCES organizations(id), email citext NOT NULL, name text,
  PRIMARY KEY (org_id, email)
);

CREATE TABLE payment_methods (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id             uuid NOT NULL REFERENCES organizations(id),
  provider           text NOT NULL,             -- flutterwave | ispend
  kind               text NOT NULL,             -- card | wallet_mandate
  token_enc          bytea NOT NULL,            -- card token or mandate ID, encrypted
  card_brand text, last4 text, exp_month int, exp_year int, bank text,
  mandate_limit_minor bigint,                   -- wallet mandates
  is_default         boolean NOT NULL DEFAULT false,
  created_at         timestamptz NOT NULL DEFAULT now(),
  removed_at         timestamptz
);

CREATE TABLE virtual_accounts (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id        uuid NOT NULL REFERENCES organizations(id),
  provider      text NOT NULL,                  -- ispend | flutterwave
  provider_ref  text NOT NULL,
  bank_name     text NOT NULL, account_number text NOT NULL, account_name text NOT NULL,
  is_primary    boolean NOT NULL DEFAULT true,
  created_at    timestamptz NOT NULL DEFAULT now(),
  closed_at     timestamptz,
  UNIQUE (provider, account_number)
);

CREATE TABLE invoices (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id        uuid NOT NULL REFERENCES organizations(id),
  number        text UNIQUE,                    -- assigned on issue
  period_start  date NOT NULL, period_end date NOT NULL,
  status        text NOT NULL,                  -- draft|issued|paid|paid_wht_pending|partially_paid|void
  subtotal_minor bigint NOT NULL, vat_minor bigint NOT NULL, total_minor bigint NOT NULL,
  wht_expected_minor bigint NOT NULL DEFAULT 0,
  vat_rate      numeric NOT NULL,
  due_at        timestamptz, issued_at timestamptz, paid_at timestamptz,
  pdf_object_key text,
  price_book_version int NOT NULL
);

CREATE TABLE invoice_lines (
  id          bigserial PRIMARY KEY,
  invoice_id  uuid NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,
  kind        text NOT NULL,                    -- plan|overage|dedicated|addon|credit|proration
  description text NOT NULL,
  project_id  uuid,
  metric      text,
  quantity    numeric NOT NULL,
  unit_price_minor bigint NOT NULL,
  amount_minor bigint NOT NULL
);

CREATE TABLE credit_notes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), invoice_id uuid NOT NULL REFERENCES invoices(id),
  number text UNIQUE NOT NULL, amount_minor bigint NOT NULL, reason text NOT NULL,
  issued_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ledger_entries (
  id            bigserial PRIMARY KEY,
  txn_id        uuid NOT NULL,                  -- groups the balanced entries of one event
  org_id        uuid,                           -- NULL for platform-only accounts
  account       text NOT NULL,                  -- receivable|credit_balance|wht_receivable|revenue:*|vat_payable|cash:*|credits_issued
  direction     text NOT NULL CHECK (direction IN ('debit','credit')),
  amount_minor  bigint NOT NULL CHECK (amount_minor > 0),
  source_type   text NOT NULL,                  -- invoice|payment|usage|wht|refund|credit|adjustment
  source_id     text NOT NULL,
  idempotency_key text UNIQUE NOT NULL,
  created_by    uuid,                           -- NULL for system
  created_at    timestamptz NOT NULL DEFAULT now()
);
-- Invariant (checked in the posting function and nightly): per txn_id, sum(debits) = sum(credits).
-- No UPDATE or DELETE grants on ledger_entries for the application role.

CREATE TABLE payments (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id        uuid NOT NULL REFERENCES organizations(id),
  provider      text,                           -- flutterwave | ispend | NULL for manual
  method        text NOT NULL,                  -- card|virtual_account|wallet|stablecoin_topup|manual
  provider_ref  text,                           -- provider transaction reference
  amount_minor  bigint NOT NULL,                -- naira credited, in kobo
  fee_minor     bigint NOT NULL DEFAULT 0,      -- provider fee, posted to fees:<provider>
  fx_quote      jsonb,                          -- stablecoin top-ups: asset, amount, rate, quote ref
  status        text NOT NULL,                  -- pending|succeeded|failed|refunded
  applied_to    jsonb NOT NULL DEFAULT '[]',    -- [{invoice_id, amount_minor, wht_minor}]
  proof_object_key text,                        -- manual payments
  received_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (provider, provider_ref)
);

CREATE TABLE wht_certificates (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), org_id uuid NOT NULL, invoice_id uuid NOT NULL REFERENCES invoices(id),
  amount_minor bigint NOT NULL, object_key text, status text NOT NULL, -- pending|received
  received_at timestamptz
);

CREATE TABLE payment_events (
  provider     text NOT NULL,
  event_id     text NOT NULL,                   -- provider event or transaction ID (idempotency)
  type         text NOT NULL,                   -- normalised type, e.g. payment.succeeded
  raw_type     text NOT NULL,                   -- provider's own event name
  payload      jsonb NOT NULL,
  verified     boolean NOT NULL DEFAULT false,
  processed_at timestamptz,
  received_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (provider, event_id)
);

-- Capacity & cost (§5)
CREATE TABLE capacity_proposals (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), region_id text NOT NULL, kind text NOT NULL,
  spec jsonb NOT NULL, monthly_cost_minor bigint NOT NULL, status text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(), decided_by uuid, decided_at timestamptz
);
CREATE TABLE cost_allocations (
  org_id uuid NOT NULL, day date NOT NULL, component text NOT NULL,
  cost_minor bigint NOT NULL, currency text NOT NULL, ngn_minor bigint NOT NULL,
  PRIMARY KEY (org_id, day, component)
);

-- Status & SLA (§2.6–2.7)
CREATE TABLE incidents (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), title text NOT NULL, components text[] NOT NULL,
  region_id text, severity text NOT NULL, status text NOT NULL,   -- investigating|identified|monitoring|resolved
  started_at timestamptz NOT NULL, resolved_at timestamptz
);
CREATE TABLE incident_updates (id bigserial PRIMARY KEY, incident_id uuid NOT NULL, body text NOT NULL, posted_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE availability_minutes (               -- SLA measurement for HA projects
  project_id uuid NOT NULL, minute timestamptz NOT NULL, available boolean NOT NULL,
  PRIMARY KEY (project_id, minute)
);  -- rolled up monthly after 90 days

-- Support (§7.1)
CREATE TABLE tickets (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), org_id uuid, requester citext NOT NULL,
  channel text NOT NULL, subject text, status text NOT NULL, priority text NOT NULL,
  assignee uuid, created_at timestamptz NOT NULL DEFAULT now(), first_response_at timestamptz
);
CREATE TABLE ticket_messages (
  id bigserial PRIMARY KEY, ticket_id uuid NOT NULL REFERENCES tickets(id), author text NOT NULL,
  body text NOT NULL, attachments jsonb NOT NULL DEFAULT '[]', created_at timestamptz NOT NULL DEFAULT now()
);

-- Query insights (§8)
CREATE TABLE query_stats (
  project_id uuid NOT NULL, queryid bigint NOT NULL, bucket timestamptz NOT NULL,
  calls bigint, total_ms double precision, rows bigint, shared_blks_hit bigint, shared_blks_read bigint,
  PRIMARY KEY (project_id, queryid, bucket)
);
CREATE TABLE query_texts (project_id uuid NOT NULL, queryid bigint NOT NULL, query text NOT NULL, PRIMARY KEY (project_id, queryid));

-- Roles
ALTER TABLE org_members DROP CONSTRAINT org_members_role_check,
  ADD CONSTRAINT org_members_role_check CHECK (role IN ('owner','admin','member','billing'));
ALTER TABLE users DROP CONSTRAINT users_platform_role_check,
  ADD CONSTRAINT users_platform_role_check CHECK (platform_role IN ('platform_admin','support','user'));
```

**Operation kinds added:** `logical_move`, `major_upgrade`, `enable_ha`, `disable_ha`, `switchover`, `pause_project`, `resume_project`, `archive_project`, `unarchive_project`, `drain_node`, `provision_node`, `deprovision_node`, `region_move`.

---

## 10. API Additions

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/projects/:id/ha` · DELETE | Enable / disable HA (cost estimate in preflight) |
| POST | `/projects/:id/switchover` | Planned switchover |
| GET | `/projects/:id/ha` | Members, lag, failover history |
| POST | `/projects/:id/upgrade` | Major version upgrade (preflight + operation) |
| POST | `/projects/:id/move` | Move to another node or region (logical) |
| POST | `/projects/:id/resume` | Resume a paused or archived Free project |
| GET | `/projects/:id/insights/queries` · `/queries/:qid` · `/indexes` · `/bloat` · `/locks` | Query insights |
| POST | `/projects/:id/insights/explain` | EXPLAIN a captured query |
| GET/PATCH | `/orgs/:org/billing` | Plan, mode, business details, budget, cap, auto top-up |
| POST | `/orgs/:org/billing/plan` | Change plan (proration preview with `dry_run`) |
| GET/POST/DELETE | `/orgs/:org/billing/payment-methods[/:id]` | Saved cards (Flutterwave checkout URL) and iSpend wallet mandates (iSpend approval URL) |
| GET | `/orgs/:org/billing/virtual-accounts` | Transfer details (primary and any fallback account) |
| POST | `/orgs/:org/billing/top-up` | Prepaid top-up by card, iSpend wallet, or (if enabled) stablecoin; returns the provider's checkout or approval URL |
| POST | `/orgs/:org/billing/invoices/:id/pay` | Pay one invoice now with a chosen method |
| GET | `/orgs/:org/billing/invoices[/:id]` · `/:id/pdf` | Invoices |
| POST | `/orgs/:org/billing/invoices/:id/wht-certificate` | Upload WHT credit note |
| GET | `/orgs/:org/billing/forecast` · `/balance` | Forecast and balances |
| GET/POST | `/orgs/:org/billing/contacts` | Billing contacts |
| POST | `/orgs/:org/support/tickets` · GET | Open and view tickets |
| POST | `/webhooks/flutterwave` · `/webhooks/ispend` | Provider events (authenticated per provider, public) |
| GET | `/regions` | Available regions and their features |
| **Admin** |  |  |
| GET/POST | `/admin/price-books[/:v/publish]` · `/admin/price-books/:v/preview` | Price books, publish, revenue preview |
| GET/PATCH | `/admin/billing/orgs/:org` | Grace, grandfathering, payment terms, manual adjustments |
| POST | `/admin/billing/payments` | Record a manual payment |
| GET | `/admin/billing/reconciliation` · `/ar-ageing` · `/wht` | Finance views |
| GET | `/admin/revenue` · `/admin/costs` | Dashboards |
| GET/POST | `/admin/capacity/proposals[/:id/approve]` | Capacity proposals |
| POST | `/admin/nodes/:id/drain` · `/admin/rebalance` | Drain and rebalance |
| GET/POST/PATCH | `/admin/regions[/:id]` | Regions |
| GET/POST/PATCH | `/admin/incidents[/:id]` · `/:id/updates` | Incidents |
| GET/PATCH | `/admin/support/tickets[/:id]` | Support console |

---

## 11. Web UI Additions

| Area | Additions |
| --- | --- |
| Project overview | Region, Postgres version, HA status and lag, lifecycle (paused/archived banner with Resume). |
| Project → Settings | Enable HA, switchover, synchronous replication, upgrade version, move node/region, data residency, resize — each with a cost estimate. |
| Project → Insights | Top queries, query detail with plan tree, slow queries, index suggestions, unused indexes, bloat, locks. |
| Create project | Region and version selection; price shown for dedicated sizes and add-ons. |
| Org → Billing | Plan and change plan (with proration preview), current-month forecast, balance (prepaid), budget and cap, saved cards and iSpend mandates, virtual account details, top-up (card, iSpend wallet, stablecoin if enabled), pay-now per invoice, invoices and receipts, WHT certificate upload, billing contacts, business details. |
| Org → Support | Tickets and new ticket. |
| Banners | Payment failed, overdue, restricted, suspended; budget thresholds; incident affecting your projects. |
| Pricing page (dashboard) | Current price book, plan comparison, calculator. |
| Admin → Finance | Revenue dashboard, AR ageing, reconciliation, WHT receivable, manual payments, price books with preview and publish. |
| Admin → Capacity | Regions, nodes with cost, proposals, drain, rebalance, infrastructure budget. |
| Admin → Incidents | Create and update incidents, status page preview. |
| Admin → Support | Ticket console with org context. |

---

## 12. Build Plan

Milestones continue from V2 (M8–M16). One engineer, roughly full time: about **20 weeks**, with a **paid launch possible around week 13** (EU region only) and the Lagos region following.

**Start in week 1, outside the code:** company registration and TIN/VAT, Flutterwave business account (cards, tokenised charges, and virtual accounts approved), the iSpend merchant API (§3.4.6) ready in sandbox,, legal documents (terms, SLA, DPA), accountant engaged for VAT/WHT handling, and sourcing for the Lagos region. These have lead times longer than the code.

### M17 — Standby pooler and status page (Week 1)

Pooler pair with floating IP and `keepalived`, config sync and hash checks, external failover via Hetzner API; external health checker; separately hosted status page with components, incidents, and subscriptions. **Done when:** killing the active pooler host moves traffic in under 10 seconds, and the status page reflects it automatically from outside.

### M18 — Logical-replication moves and Postgres versions (Weeks 2–4)

Logical move engine (schema copy, publication/subscription, lag tracking, DDL block, sequence sync, cutover, rollback, fallbacks); used for promotion, demotion, and node moves; shared cluster per version; major upgrade preflight and flow; automated minor upgrades in maintenance windows. **Done when:** a 20 GB project under continuous writes moves nodes with under 5 seconds of paused writes and zero lost commits, and a 17 → 18 upgrade completes the same way.

### M19 — HA for dedicated instances (Weeks 5–6)

etcd cluster; Patroni in the dedicated image; enable/disable HA without downtime; leader watcher and pooler re-pointing; switchover; WAL-G role handling; failover history; SLA probes and availability minutes. **Done when:** killing an HA primary's node under load restores writes in under 60 seconds with the URL unchanged, and the availability record shows the outage correctly.

### M20 — Billing core (Weeks 7–9)

Price books, billing accounts and the `billing` org role, ledger with posting functions and invariants, rating engine, proration, invoice drafts, numbering, PDF generation, credit notes, VAT, forecast, spend controls. **Done when:** a month of real V2 usage for test orgs produces correct, balanced invoices; a mid-month upgrade prorates correctly; and the ledger invariant holds across every scenario in the test suite.

### M21 — Payments, prepaid, WHT, and dunning (Weeks 10–11)

Payment provider interface and normalised events; Flutterwave checkout and tokenised charges, Flutterwave fallback virtual accounts; iSpend virtual accounts, wallet payments, and mandates against the iSpend sandbox; webhook authentication, re-verification, idempotency, hourly and nightly re-query; transfer matching; manual payments; stablecoin top-up path behind its setting; prepaid daily deduction and alerts, auto top-up, WHT matching and certificates, dunning ladder with restriction and suspension, reconciliation job. **Done when:** in Flutterwave and iSpend sandboxes, card payments and tokenised charges, transfers into both kinds of virtual account (full, short-by-WHT, partial, over), wallet payments and mandate charges, a revoked mandate, failed cards, duplicate and missed webhooks all produce the correct ledger state, a simulated iSpend outage falls back to a Flutterwave virtual account, and reconciliation against both providers reports zero differences.

### M22 — Free tier pause/archive and open signup (Week 12)

Inactivity detection, waker service, pause/resume, archive/unarchive, notices; open signup with Turnstile and limits; pricing page. **Done when:** a paused Free project resumes automatically on the first connection attempt with the waker's message, and an archived one restores from its archive.

### M23 — Business operations and paid launch (Week 13)

Support inbox and console, `support` role, WhatsApp channel, revenue dashboard (first version), legal documents with acceptance. **Launch gate:** company, Flutterwave account, iSpend merchant API in production, legal, and accounting set up; M17–M22 done; one week of internal billing on real usage reconciled. **Paid launch in the EU region.**

### M24 — Capacity automation and cost attribution (Weeks 14–15)

Provider interface and Hetzner implementation, cloud-init bootstrap, capacity proposals with budget, drain and rebalance, empty-node cleanup, cost attribution, margin dashboard, FX view. **Done when:** filling a test region triggers a proposal that provisions a node which joins and takes projects automatically, and per-org margins match a manual calculation.

### M25 — Regions and Lagos (Weeks 16–17)

Region model, per-region pooler pairs and hostnames, region selection, cross-region moves with forwarding, data residency setting, manual provider or local provider implementation, in-country backups, cross-region backup copies for other projects. **Done when:** a Lagos HA dedicated project with data residency on runs, backs up in-country only, and survives a node failure.

### M26 — Query insights (Weeks 18–19)

Snapshot collection and deltas, top queries, detail and EXPLAIN, slow queries, index suggestions (with `hypopg` where available), unused indexes, bloat, locks. **Done when:** a seeded missing-index workload produces a correct suggestion whose migration measurably reduces the query's mean time.

### M27 — Hardening (Week 20)

Billing audit (ledger invariants, rounding, VAT/WHT edge cases reviewed with the accountant), security review of payment webhooks and billing permissions, chaos tests (etcd member loss, pooler split-brain, Flutterwave or iSpend outage, waker failure), load test of rating at 1,000 orgs, documentation, **Lagos region launch**.

### Timeline summary

| Weeks | Milestone | Outcome |
| --- | --- | --- |
| 1 | M17 Standby pooler, status page | No single point of failure in the data path |
| 2–4 | M18 Logical moves, versions | Seconds-long moves and upgrades |
| 5–6 | M19 HA dedicated | Sellable SLA |
| 7–9 | M20 Billing core | Correct invoices from real usage |
| 10–11 | M21 Payments, WHT, dunning | Money in, reconciled |
| 12 | M22 Free tier, open signup | Cheap acquisition channel |
| 13 | M23 Business ops | **Paid launch (EU)** |
| 14–15 | M24 Capacity, cost | Margins visible, capacity on demand |
| 16–17 | M25 Regions, Lagos | Data residency offering |
| 18–19 | M26 Query insights | Retention feature for paying teams |
| 20 | M27 Hardening | **Lagos launch** |

---

## 13. Risks & Mitigations

| Risk | Impact | Mitigation |
| --- | --- | --- |
| **Billing errors** | Lost trust, revenue leakage, disputes | Double-entry ledger with invariants, idempotency everywhere, draft review before issue, nightly reconciliation against Flutterwave and iSpend with fees posted separately, accountant review before launch. |
| **Naira devaluation** | Margin erosion | FX buffer in prices, quarterly repricing with notice, annual prepaid plans, FX view in dashboard, foreign-currency cash reserve for hosting. |
| **Split brain in HA** | Two primaries, data divergence | Patroni leases and watchdog, 3-member etcd across failure domains, pooler follows the Patroni leader only, chaos tests. |
| **Logical move edge cases** | Data mismatch after move | Preflight checks, DDL blocking, sequence sync with margin, row-count verification, 48-hour rollback window, dump/restore fallback. |
| **Free tier abuse or cost** | Infrastructure cost without revenue | One Free org per user, Turnstile and IP limits, hard limits, pause at 7 days, archive at 90, free-tier cost tracked in the dashboard. |
| **Payment provider outage** | Can't collect money | Provider interface with two providers; virtual accounts fall back from iSpend to Flutterwave; provider outages never count as customer payment failures; manual payments; hourly re-query. |
| **iSpend readiness** | PGDock's launch waits on iSpend's merchant API, licensing, or banking partnership | Flutterwave covers cards and virtual accounts on its own, so PGDock can launch with Flutterwave only and switch iSpend on as primary when ready; the contract in §3.4.6 is agreed early so both teams build to it. |
| **Related-party arrangement** | Both PGDock and iSpend sit in your group; the intercompany terms matter for tax and audit | A written merchant agreement between the two entities on standard, arm's-length terms; fees recorded like any other provider. |
| **Crypto-funded payments** | Regulatory uncertainty | Stablecoin top-ups off by default, conversion inside iSpend, PGDock ledger holds naira only; enable only after legal advice. |
| **Lagos infrastructure reliability** | Outages in the premium region | Minimum requirements in §6.2, HA required for SLA, in-country backups, honest SLA scope, start with dedicated only. |
| **SLA exposure** | Credits and reputation | SLA limited to HA dedicated, measured externally, credits capped at 50%, maintenance excluded with notice. |
| **Support load** | Slow responses, churn | Published response targets by plan, support console with org context, status page reduces duplicate tickets, docs. |
| **Regulatory and tax missteps** | Penalties, invoice rejection | Company, TIN, and VAT set up first; accountant review of VAT/WHT; legal review of terms and DPA; data protection advice. |

---

## 14. Open Questions

1. **Lagos infrastructure.** Colocation with your own servers, or a local cloud/VPS provider? This decides whether the Lagos region uses the manual provider or a new provider implementation, and its lead time.
2. **Prepaid as default?** Should new paid orgs start on prepaid credits (lower collection risk, familiar to Nigerian customers) with postpaid available on request, or the reverse?
3. **Team plan.** Is a third plan needed at launch, or should it start with Free, Pro, and dedicated instances, adding Team once there are customers asking for it?
4. **Support channels.** Is WhatsApp support something you're prepared to staff from launch, or should it start as email only?
5. **Billing engine.** Build the rating and invoicing in-house as specced (it fits the existing usage records and ledger approach), or evaluate an open-source billing engine such as Lago for rating and invoices while keeping the ledger in PGDock?
6. **Company entity.** Which entity issues invoices and holds the Flutterwave account and the iSpend merchant account: a PGDock company under your holdco, or the holdco itself?
7. **iSpend timeline.** Will iSpend's merchant API, virtual accounts, and mandates be in production by PGDock's paid launch (around week 13)? If not, launch on Flutterwave only and switch iSpend on afterwards?
8. **Stablecoin top-ups.** Do you want this path at launch (subject to legal advice), or later?