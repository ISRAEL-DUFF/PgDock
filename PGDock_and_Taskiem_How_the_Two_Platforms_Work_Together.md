# PGDock × Taskiem: How the Two Platforms Work Together

Oct 7, 2026 · @EaziDeFi

## Summary

PGDock is where a customer's data lives; Taskiem is what reacts to that data and moves it between systems. Each fills a gap in the other, and together they make one offer for the same buyer: Nigerian startups and SMEs who want a backend and automation without hiring a platform team.

|  | PGDock | Taskiem |
| --- | --- | --- |
| What it is | Managed Postgres platform: shared and dedicated databases, branching, backups, webhooks and scheduled jobs; backend services (data API, auth, storage, realtime) in V4 | Workflow automation platform (n8n/Zapier class): visual and code workflows, connectors, durable execution |
| Core strength | Reliable data, transactional change capture, row-level security | Connecting systems, multi-step logic, retries, approvals |
| Gap the other fills | Turning database events into business actions without code | A real database for workflow data, files, state, and end-user auth |
| Buyer | Nigerian startups and SMEs, billed in naira | Same |

**Recommendation:** integrate the two through public APIs and webhooks only, starting with a native PGDock connector in Taskiem (a "row changed" trigger plus query, write, and function actions). It is small, shows the pairing at once, and needs nothing new from either core product.

## How PGDock strengthens Taskiem

PGDock gives Taskiem five capabilities it would otherwise build or borrow: reliable database triggers, data tables, file storage, a durable execution store, and end-user sign-in.

**1. Database changes as a first-class trigger.** PGDock's webhooks capture row inserts, updates, and deletes in the same transaction as the change, then deliver them signed, in commit order, with retries and a dead-letter queue. A native "PGDock row changed" trigger is more reliable than the polling most automation tools rely on, and it supports Taskiem's money-safe durable execution goal: a rolled-back transaction never starts a workflow, and a committed one always does.

**2. Data tables backed by real Postgres.** The data tables feature in Taskiem's V2 spec can be PGDock projects. Users get a table view for simple cases and a full database when they outgrow it: SQL, branching for testing, backups, and point-in-time recovery on dedicated instances.

**3. File storage.** Taskiem's file handling can use PGDock storage (V4): buckets, signed URLs, image transforms, and access rules, instead of running a separate store.

**4. A durable home for execution state.** Taskiem's own execution history and workflow state can run on PGDock dedicated instances with HA and point-in-time recovery. That is a concrete answer to customers who ask what happens to their runs when something fails.

**5. Auth for forms and embedding.** Taskiem's forms and white-label embedding need end-user sign-in. PGDock auth (V4) provides it, including phone and WhatsApp OTP, which suits Nigerian users.

| Taskiem need | PGDock capability | Available |
| --- | --- | --- |
| Reliable event triggers | Transactional webhooks | V2 (now) |
| Data tables | Projects, branching, data API | V2 projects now; data API in V4 |
| Files | Storage service | V4 |
| Execution store | Dedicated instances with HA and PITR | V3 |
| Form and embed sign-in | Auth service | V4 |

## How Taskiem strengthens PGDock

Taskiem turns PGDock's data events into business actions for people who don't write code, extends what scheduled jobs can do, and automates PGDock's own operations.

**1. Database events become actions without code.** Today a PGDock webhook delivers a change to a URL, and the customer must write a server to act on it. With Taskiem, "new order row" becomes "send a WhatsApp message, update the CRM, post to Slack, charge through Flutterwave" in a visual editor. This covers much of what customers would otherwise ask PGDock for as edge functions, which eases pressure to build that heavy feature (currently planned for V5).

**2. Scheduled jobs with more power.** PGDock's scheduled jobs run one SQL statement or call one URL. Anything beyond that, such as several steps, branching, retries with backoff, or a human approval, becomes a Taskiem workflow triggered on the same schedule.

**3. Automating PGDock's own operations.** PGDock has recurring operational processes that are workflows by nature:

- Dunning reminders and payment follow-ups (V3 §3.8).
- Onboarding sequences for new organisations.
- Withholding-tax credit-note chasing (V3 §3.7).
- Capacity, backup, and incident alerts routed to the right people and channels.

Running these on Taskiem removes custom code from PGDock and gives Taskiem a demanding, money-handling reference customer from day one.

## Shared advantages

Beyond the product integration, the two platforms share a buyer, a billing model, and several hard engineering problems that only need solving once.

**One customer, one bill.** Both target Nigerian startups and SMEs, both bill in naira through Flutterwave and iSpend, and both use flat, predictable pricing. A combined "database + automation" plan raises the value per customer and makes both products harder to replace. Each product must still be sold and usable on its own.

**Shared building blocks.** These are built once and reused, either as a shared internal library or by one team adopting the other's design:

| Building block | PGDock reference | Benefit to Taskiem |
| --- | --- | --- |
| Organisations, roles, permissions, break-glass access | V2 §2 | Same tenancy and admin model across both products |
| Usage metering, rating, price books, ledger | V2 §10.9, V3 §3 | Flat pricing plus metered extras without a second billing engine |
| Payment provider interface (Flutterwave, iSpend) | V3 §3.4 | Same collection channels, reconciliation, and WHT handling |
| Message provider interface (SMS, WhatsApp, email) | V4 §4.6 | Taskiem's WhatsApp interface and notifications share providers and fraud controls |
| SSRF-safe outbound HTTP delivery with signing and retries | V2 §9.1 | Every Taskiem HTTP action and webhook is safe by default |
| Provider and capacity automation on InterServer and Hetzner | V3 §5 | Taskiem's workers run on the same infrastructure and cost model |

**African connectors on both sides.** Taskiem's African connector library (Flutterwave, Paystack, Termii, WhatsApp and others) is exactly what PGDock's auth hooks, webhooks, and scheduled jobs want to reach. Every connector Taskiem adds becomes a reason to choose PGDock, and every PGDock customer is a ready Taskiem user.

## Integration principles

The two products integrate the way any third party would: through public, versioned APIs and signed webhooks, never through shared databases or private internal calls.

1. **Each product stands alone.** Many customers will use only one, and some will pair PGDock with Zapier or Taskiem with Supabase. Neither product may require the other to work.
2. **Public surfaces only.** Taskiem calls PGDock's documented APIs (management API, data API, webhooks) with ordinary API keys or tokens. PGDock calls Taskiem's public workflow-trigger endpoints. No shared tables, queues, or service accounts.
3. **The third-party test.** Anything Taskiem does with PGDock, an outside tool must be able to do too, just less conveniently. If an integration needs a private endpoint, the endpoint should be made public instead.
4. **Separate tenancy and security.** Each product keeps its own organisations, users, and secrets. Linking accounts is an explicit OAuth-style consent by the customer, scoped to chosen projects and revocable from either side.
5. **Versioned contracts.** Breaking changes to any API the other product depends on follow the normal deprecation policy (12 months' overlap for PGDock V4 APIs). Each team runs the other's integration tests in CI.
6. **Independent releases and outages.** An outage in one product degrades the integration (queued events, retried actions) but never takes down the other product.

## First integration: the PGDock connector in Taskiem

The first deliverable is a native PGDock connector in Taskiem's connector library: one trigger and four actions, built only on PGDock's existing and planned public APIs.

**Connecting an account.** The user links PGDock from Taskiem with an OAuth-style flow (or by pasting a PGDock API token). They choose the organisation and projects Taskiem may use and the scopes it gets (`read`, `write`). The connection is listed and revocable in both products.

**Trigger**

| Trigger | How it works | Depends on |
| --- | --- | --- |
| Row changed | Taskiem creates a PGDock webhook through the management API for the chosen table, events (insert, update, delete), and optional changed columns. PGDock delivers signed events in commit order; Taskiem verifies the signature and de-duplicates on the event ID. Deleting the trigger deletes the webhook. | PGDock V2 webhooks + webhook management API |

**Actions**

| Action | What it does | Depends on |
| --- | --- | --- |
| Query rows | Select rows with filters, ordering, and a limit | V4 data API (until then: a parameterised SQL call through the management API) |
| Insert or upsert rows | Write one or many rows; upsert on a chosen conflict column | V4 data API |
| Update or delete rows | Change rows matching a required filter, with a maximum-affected safety limit | V4 data API |
| Call a function | Run a Postgres function with named arguments and return its result | V4 data API RPC |

**Behaviour requirements**

- Actions use a project secret key or a scoped token held in Taskiem's encrypted credential store, never exposed in workflow definitions.
- Writes carry an idempotency key derived from the workflow run and step, so Taskiem's retries never write twice.
- PGDock errors map to Taskiem's error types (validation, permission, conflict, rate limit, temporarily unavailable) so retry and error workflows behave correctly.
- A paused Free-tier PGDock project returns "resuming"; Taskiem treats it as a retryable error.

**Done when:** a Taskiem workflow triggered by a new `orders` row in PGDock sends a WhatsApp message and writes a status update back to the row; a rolled-back insert triggers nothing; and taking Taskiem offline for an hour loses no events.

## Roadmap and team responsibilities

The integration starts with the connector on PGDock's existing webhooks and deepens as PGDock V4 services ship; no phase blocks either product's own roadmap.

&#91;embedded content: integration roadmap · 4 phases, 3 gates\]

Phase 1 can start now; phase 3 waits for PGDock's V4 data API, and phase 4 for V4 auth and storage. Phase 2 can run in parallel with phase 1 if both teams have capacity.

| Work item | Phase | PGDock team | Taskiem team |
| --- | --- | --- | --- |
| Webhook management API (create, list, delete webhooks by token) | 1 | Builds and documents | Reviews the contract |
| Row-changed trigger with signature checks and de-duplication | 1 | Supports, test fixtures | Builds |
| Stopgap actions through the management API | 1 | Exposes a scoped, parameterised SQL endpoint | Builds the actions |
| Account linking with consent and revocation | 2 | Builds the authorisation flow and scoped tokens | Builds the connect screen |
| Shared payment and message provider modules | 2 | Owns the payment module | Owns the message module (WhatsApp interface) |
| Data actions on the V4 data API | 3 | Ships the data API and SDK | Rebuilds actions on it, retires the stopgap |
| Data tables on PGDock projects | 3 | Project creation by token, quotas | Builds the table view and lifecycle |
| Files on PGDock storage; auth for forms and embedding | 4 | Ships V4 storage and auth | Integrates them |
| Combined plan and invoice | 4 | Billing engine support | Pricing page and plan selection |
| PGDock operations workflows (dunning, onboarding, WHT, alerts) | 4 | Defines the processes and events | Builds and hosts the workflows |

**Ways of working:** one integration owner per team; a shared contract document for every API the other team depends on; integration tests from each team running in the other's CI; a short joint review before any change to those contracts.

## Risks and open questions

| Risk | Impact | Mitigation |
| --- | --- | --- |
| Tight coupling creeps in (shared tables, private endpoints) | Each product becomes harder to sell, run, and change alone | Public-surfaces-only rule; the third-party test in design reviews; integration tests in both CIs |
| One product's outage cascades | Customer workflows or apps fail together | Queued, retried delivery both ways; clear degraded-mode behaviour; separate infrastructure failure domains where practical |
| Cross-product data exposure | A Taskiem credential reaches more PGDock data than intended | Scoped, project-restricted tokens; explicit consent; revocation from either side; audit entries in both products |
| Roadmap dependency | Taskiem features wait on PGDock V4 services | Ship the trigger on V2 webhooks now; use the management API as a stopgap for actions until the V4 data API |
| Bundle pricing confusion | Customers unsure what they pay for | Each product priced on its own; the bundle is a discount, not a requirement |
| Shared code as a hidden coupling | A change in a shared library breaks the other product | Versioned internal modules with owners; changes reviewed by both teams |

**Open questions**

1. **Shared library or shared design?** Should common building blocks (orgs, metering, payment and message providers) live in one versioned internal library used by both, or should each team implement them from a shared design?
2. **Account linking.** Is a single sign-on across PGDock and Taskiem wanted (one login for both), or only linked accounts with explicit consent?
3. **Bundle pricing.** What discount, if any, should the combined plan carry, and which product's billing system issues the bundle invoice?
4. **Data tables default.** Should Taskiem's data tables always run on PGDock, or offer PGDock as one backend among others?
5. **Execution store.** Does Taskiem want to run its own execution history on PGDock dedicated instances, or keep its own database operations separate?
