package legal

// The default SLA and DPA (V3 §2.7, §7.3), published as version 1 on a
// platform that has none. They are templates: the platform admin has them
// reviewed and publishes the company's own versions before a paid launch.

// DefaultSLA is the service level agreement.
const DefaultSLA = `# Service level agreement

## What is covered

| Offering | Commitment |
| --- | --- |
| HA dedicated instances | 99.9% monthly availability of the project's pooler endpoint accepting connections and executing queries |
| Standard dedicated instances | Best effort; no commitment |
| Shared tier (Free and paid plans) | Best effort; no commitment |

## How it is measured

An external probe connects through the pooler and runs a trivial query every
minute. A minute is unavailable when the probe fails from two vantage points.
Excluded: scheduled maintenance announced 72 hours in advance, outages the
customer caused, and suspension for non-payment.

## Service credits

Applied to the next invoice, when requested within 30 days of the month's end:

| Monthly availability | Credit, of that instance's monthly charge |
| --- | --- |
| below 99.9% | 10% |
| below 99.0% | 25% |
| below 95.0% | 50% |

Credits are the only remedy for missing the commitment.

## Support response targets

First responses, in Nigerian business hours (09:00 to 17:00 WAT, Monday to
Friday): Free, best effort; Pro, one business day; Team, four business hours;
urgent issues on a paid plan, such as an HA project down, one hour at any time.
`

// SubProcessors names, for each message provider PGDock itself holds an
// account with (messaging.PlatformProviders), how the DPA lists it.
var SubProcessors = map[string]string{
	"termii":         "Termii",
	"africastalking": "Africa's Talking",
	"whatsapp_cloud": "Meta (WhatsApp Business Platform)",
}

// DefaultDPA is the data processing agreement.
const DefaultDPA = `# Data processing agreement

This agreement is part of the terms of use between the organisation (the
**controller**) and the company that runs PGDock (the **processor**).

## Processing

PGDock processes the personal data the organisation puts in its databases,
backups and support tickets only to provide the service, on the
organisation's documented instructions (the terms, the dashboard and the
API), unless the law requires otherwise; it tells the organisation of such a
requirement first where the law allows.

## Confidentiality and security

People with access to the data are bound to confidentiality. PGDock keeps
encryption in transit, encrypted backups, access controls with two-factor
authentication, audit logs, and break-glass access with notice to the
organisation's owners for any access to its data by PGDock staff.

## Sub-processors

PGDock uses these sub-processors, and tells organisations 30 days before
adding or replacing one:

| Sub-processor | Purpose |
| --- | --- |
| Hetzner | Servers and storage (EU region) |
| Cloudflare | DNS, edge protection, the CDN for public files, and the signup challenge |
| Flutterwave | Card payments and bank transfers |
| iSpend | Bank transfers, wallet payments and mandates |
| The email provider | Account, billing, support and app sign-in email |
| Termii | SMS sign-in codes for apps' users (Nigeria) |
| Africa's Talking | SMS sign-in codes for apps' users, when Termii is unavailable |
| Meta (WhatsApp Business Platform) | WhatsApp sign-in codes for apps' users, and WhatsApp support |
| The Lagos region's provider | Servers and storage (Lagos region) |

A project that configures its own SMS, WhatsApp or email provider
contracts with that provider directly; it isn't PGDock's sub-processor.

## Requests, incidents and audits

PGDock helps the organisation answer data subjects' requests, tells it of a
personal data breach without undue delay (within 72 hours of finding it),
and makes available the information needed to show compliance.

## Return and deletion

When the organisation is deleted, its data is deleted after the grace and
backup retention periods stated in the terms; it can export its data at any
time before that.
`
