# Support

PGDock keeps every support conversation in one place. Tickets come from the
dashboard, from email and from WhatsApp, and the support console shows each
one with the organisation it came from (V3 §7.1).

## For customers

**Organisation → Support** lists the organisation's tickets. Owners and
admins see all of them; other members see the ones they opened.
**New ticket** asks for a subject and a description. Tick **Urgent** when
production is down or data is at risk. PGDock emails an acknowledgement
with the ticket's reference, such as `[T-1001]`. Replying to any email
about the ticket adds the reply to it, and so does replying on the
dashboard.

First responses are counted in Nigerian business hours, 09:00 to 17:00
WAT, Monday to Friday. Public holidays aren't counted yet.

| Plan | First response |
| --- | --- |
| Free | Best effort |
| Pro | Within one business day (8 business hours) |
| Team | Within 4 business hours |
| Urgent, on Pro or Team | Within 1 hour, at any time |

**WhatsApp** is for Pro and Team organisations. An owner or admin
registers the numbers that may write to PGDock (Organisation → Support →
WhatsApp). Local Nigerian numbers such as `0803…` are stored as `+234…`. A
message from a registered number opens a ticket for that organisation,
or adds to its open ticket from the same number, and replies arrive on
WhatsApp. Any other number gets a reply explaining how to reach support.

## For support staff

**Platform → Support** is the console. It is open to platform admins and
to users with the **support** platform role. Platform admins give that role
under Users → **Platform role** (their own password and a code are
needed). Support staff see the console and nothing else of the platform:
not billing settings, nodes, other organisations' pages or their data.

- The queue shows open tickets first, ordered by when they are due an
  answer, with counts of open, pending and overdue tickets. Filters: Open,
  Pending, Mine, All.
- A ticket shows its thread beside the organisation's context. The
  context covers the plan and term, billing mode, standing and balance,
  members, projects (metadata only), recent operations, open incidents and
  quotas. It never includes the databases' contents. For access to data,
  use break-glass ([operations](operations.md)).
- **Reply to customer** sends the reply by the channel the ticket came in
  on: email (threaded, with the reference in the subject) or WhatsApp.
  Email replies always go to the requester's address. **Add note** is
  internal: customers never see notes, by email or on the dashboard.
- Status: *open*, *pending* (waiting on the customer), *solved*,
  *closed*. A reply from the customer reopens the ticket. You can
  also set the priority and the assignee. The assignee must be support
  staff or a platform admin.
- An email from someone PGDock can't place opens a ticket with no
  organisation. The sender is matched first by an organisation's billing
  contact, then by membership of exactly one organisation.

## Setting it up

| Variable | What |
| --- | --- |
| `PGDOCK_SUPPORT_EMAIL` | The support address, e.g. `support@example.com`. It is the Reply-To of support emails, and mail from it is never turned into tickets. |
| `PGDOCK_SUPPORT_INBOUND_SECRET` (or `_FILE`) | At least 16 characters. The email provider's inbound webhook presents it. |
| `PGDOCK_WHATSAPP_PHONE_NUMBER_ID` | The WhatsApp Business number's ID (Meta Cloud API). |
| `PGDOCK_WHATSAPP_ACCESS_TOKEN` | A system-user token with `whatsapp_business_messaging`. |
| `PGDOCK_WHATSAPP_APP_SECRET` | The Meta app's secret, at least 16 characters. It checks `X-Hub-Signature-256` on every webhook. |
| `PGDOCK_WHATSAPP_VERIFY_TOKEN` | Any string. Meta sends it back when you register the webhook. |
| `PGDOCK_WHATSAPP_GRAPH_URL` | Optional; defaults to `https://graph.facebook.com/v21.0`. |

The bundle's `compose.yaml` passes all of these to pgdock-server. Without
an inbound secret, email can't open tickets, though dashboard tickets and
replies by email still work. Without the WhatsApp variables, the WhatsApp
channel is off.

**Inbound email.** Point your email provider's inbound webhook at:

    https://<your PGDock>/api/v1/support/inbound/email

Authenticate it in one of two ways:

- basic auth, with any username and the inbound secret as the password
  (e.g. `https://inbound:<secret>@<your PGDock>/…`, as Postmark sends it);
- an `X-PGDock-Inbound-Secret` header.

The endpoint accepts either of these JSON bodies:

- Postmark's inbound JSON;
- a plain body: `from`, `from_name`, `subject`, `text`, `message_id`,
  `in_reply_to`, `references`.

A reply joins its ticket by the `[T-n]` reference in its subject, or by
`In-Reply-To` or `References` naming one of PGDock's emails. Quoted text
(`On … wrote:` and the lines starting with `>`) is removed. A redelivered
email is recorded once.

**WhatsApp.** In the Meta app, set the webhook's callback URL and verify
token, and subscribe to `messages`:

- callback URL: `https://<your PGDock>/api/v1/support/whatsapp`
- verify token: the value of `PGDOCK_WHATSAPP_VERIFY_TOKEN`

Replies outside WhatsApp's 24-hour customer-service window need an
approved template, which PGDock doesn't send yet. Answer within the day,
or ask the customer to write again.
