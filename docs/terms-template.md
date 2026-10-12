# Terms of use: a template

PGDock publishes a short default (`internal/auth/terms.go`) on a fresh
install. Before inviting people, replace it (Platform → Terms) with your
own, starting from the text below. Fill in everything in `[brackets]`;
publishing a new version makes every user accept it again. This is not
legal advice: if the platform holds other people's personal data, get some
(V2 §10.10).

---

```markdown
# Terms of use

[Name] ("we") runs this PGDock installation at [address] for [friends and
colleagues / the members of …]. By using it you agree to these terms.
Questions: [email].

## The service

You can create organisations, databases, backups, branches, webhooks and
scheduled jobs within the limits of your organisation's plan. We may change
the plans, with [two weeks'] notice by email for anything that lowers
yours.

## What we can and cannot see

We manage the servers, plans and accounts. Through PGDock we can see your
organisations' names, member and project counts, sizes and usage, and the
host names (not the contents) your webhooks and jobs call. We cannot open
your databases, SQL console, backups, connection strings, webhook URLs or
secrets, job SQL, or audit details, except through **break-glass access**:
a session of at most 4 hours, with a written reason, that every owner of
your organisation is emailed about at once, that your organisation sees a
banner for while it lasts, and that is recorded in your audit log. Any owner
can end it.

Break-glass is a policy and audit control, not a cryptographic one: anyone
with root access to the servers can technically read any data on them. We
use that access only to run and repair the servers.

## Your data

What you store is yours. You are responsible for having the right to store
it, especially personal data about other people.

- **Backups:** nightly, 7 daily and 4 weekly copies, unless you choose
  otherwise; dedicated projects can also be recovered to any point in the
  last 7 days.
- **Export:** organisation owners can download any backup as a `pg_dump`
  file at any time (Backups → Download).
- **Your own bucket:** backups in a bucket your organisation adds are
  yours; we never delete them.
- **Deletion:** a deleted organisation is kept 7 days, in case you change
  your mind, then removed. Final backups on our storage are kept 30 days,
  then deleted. Usage records (without project names) and audit logs are
  kept [1 year].

## No guarantees

The service is provided as is, without uptime or durability guarantees
[or: with the following targets: …]. Keep your own copies of anything you
cannot afford to lose.

## Acceptable use

Do not use the platform to break the law, to attack or probe other tenants
or other systems, to send unsolicited traffic through webhooks or jobs, or
to store data you have no right to hold. We may turn off an organisation's
outbound traffic, or suspend it, to stop abuse; we will tell its owners why.

## Incidents

If something goes wrong that affects your data or access, we email the
owners of your organisation within [72 hours] of finding it, with what we
know and what you should do, and send a written follow-up once it is
understood.

## Postgres versions

We announce the retirement of a Postgres major version at least [180
days] ahead, by email to your organisation's owners and admins. After that
date your projects on it keep running, but they are unsupported: we do not
fix problems specific to that version, may not be able to restore its
backups onto newer infrastructure, and do not upgrade it for you. Upgrade
before the date.

## Ending

You can delete your account or organisation at any time. We may close the
service with [30 days'] notice, during which you can export everything.

## Changes

We will email you before these terms change; you accept the new version
the next time you sign in.
```

```markdown
# Privacy notice

We store your name, email address, a hash of your password, your
two-factor secret (encrypted), the addresses and browsers of your sessions,
and a log of what you do, to sign you in, keep organisations secure, and
show your colleagues who changed what. Your email address is used for
account email and notices about your organisations, and is not shared.

[Where the servers are: provider, country.]

You can ask us ([email]) for a copy of what we hold about you, or to
delete your account.
```
