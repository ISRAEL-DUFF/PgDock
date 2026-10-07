package auth

// DefaultTerms is version 1 of the terms of use, published when a platform
// has none (V2 §10.10). The platform admin replaces it in the platform
// settings; every user then accepts the new version.
const DefaultTerms = `# Terms of use

This PGDock installation is run by its platform admin for friends and
colleagues. By using it you agree to the following.

## What the platform admin can and cannot see

The platform admin manages servers, plans, and accounts. They can see your
organisations' names, member and project counts, sizes, and usage. They
cannot open your databases, SQL console, backups, connection strings, or
audit details through PGDock, except through **break-glass access**: a
time-limited session (at most 4 hours) with a written reason, that every
owner of your organisation is emailed about at once, that is shown to your
organisation while it lasts, and that is recorded in your audit log.

Break-glass is a policy and audit control, not a cryptographic one: anyone
with root access to the servers can technically read any data stored on
them.

## Backups and retention

Projects are backed up nightly (7 daily and 4 weekly copies) unless you
choose otherwise. Deleted projects keep a final backup for 30 days. Backups in
a bucket your organisation owns are yours and are never deleted by PGDock
when your organisation is removed.

## No guarantees

Except where the service level agreement (the SLA) says otherwise, the
service is provided as is, without uptime or durability guarantees.
Keep your own copies of anything you cannot afford to lose: every project
can be exported as a ` + "`pg_dump`" + ` file at any time.

## Prices and repricing

Paid plans are priced in naira, in versioned price books. Prices are
reviewed at least quarterly. A new price book takes effect no sooner than
30 days after your billing contacts are told, with a comparison of old and
new prices for your usage; annual terms keep their prices until they renew.

## Support

Support is by email, from the dashboard, and (on Pro and Team) by
WhatsApp. The first response comes within these targets, in Nigerian
business hours (09:00 to 17:00 WAT, Monday to Friday): Free, best effort;
Pro, one business day; Team, four business hours; urgent issues on a paid
plan (an HA project down), one hour at any time.

## Acceptable use

Do not use the platform to break the law, to attack other tenants or other
systems, or to store data you have no right to hold. Accounts that do may be
suspended.

## Incidents

If something goes wrong that affects your data, the owners of your
organisation are told by email within 72 hours of it being found, with a
written follow-up once it is understood.
`

// DefaultPrivacy is version 1 of the privacy notice.
const DefaultPrivacy = `# Privacy notice

PGDock stores your name, email address, a hash of your password, your
two-factor secret (encrypted), the addresses and browsers of your sessions,
and an audit log of what you do, so it can sign you in, keep your
organisations secure, and show your colleagues who changed what.

Your email address is used for account email (verification, password
resets, invitations) and for notices about your organisations. It is not
shared with anyone else.

What you store in your databases is yours. If it includes personal data
about other people, you are responsible for having the right to hold it.

You can ask the platform admin to delete your account at any time.
`

// DefaultAUP is version 1 of the acceptable use policy (V3 §7.3), accepted
// with the terms. A template: have it reviewed before a paid launch.
const DefaultAUP = `# Acceptable use policy

You may not use PGDock, or let others use your projects, to:

- break the law, or help anyone else break it;
- store or process data you have no right to hold, or personal data
  without a lawful basis;
- send spam, or host phishing or malware;
- attack, probe or overload PGDock, its other customers, or any other
  system (including running scanners or crypto miners from scheduled jobs
  or webhooks);
- get around the limits of your plan, the Free tier's pause and archive,
  or one Free organisation per person;
- resell PGDock without a written agreement.

We may suspend projects or organisations that do, with notice where the
law and the situation allow. Report abuse to the support address.
`
