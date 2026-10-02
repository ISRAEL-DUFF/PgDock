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
choose otherwise. Deleted projects keep a final backup for 7 days. Backups in
a bucket your organisation owns are yours and are never deleted by PGDock
when your organisation is removed.

## No guarantees

The service is provided as is, without uptime or durability guarantees.
Keep your own copies of anything you cannot afford to lose: every project
can be exported as a ` + "`pg_dump`" + ` file at any time.

## Acceptable use

Do not use the platform to break the law, to attack other tenants or other
systems, or to store data you have no right to hold. Accounts that do may be
suspended.

## Incidents

If something goes wrong that affects your data, the owners of your
organisation are told by email, with a written follow-up.
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
