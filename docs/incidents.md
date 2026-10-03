# Incident process

What to do when something goes wrong that affects other people's data or
access: a leak between tenants, a compromised account or server, lost
data, or a long outage (V2 §10.10). Keep it short; write as you go.

## 1. Detect

Incidents start from an alert (Alerts, email, your webhook), a user's
report, the nightly isolation check, or something odd in an audit log.
Note the time you noticed it: the notification deadline counts from then.

## 2. Contain

Stop it getting worse before working out why. Each of these is reversible
and audited:

| To stop | Do |
| --- | --- |
| One organisation's apps or background work | **Suspend** it (Organisations → the org): routes off, logins lose `CONNECT`, webhooks, jobs and scheduled backups pause, a final backup is taken. |
| Its outbound traffic only | **Outbound off** (Organisations → the org → Outbound). |
| A person | **Disable** the user: sessions, API tokens and database logins end at once. |
| A leaked API token | Revoke it (the org's owners can, under Organisation → Tokens, or the user). |
| A leaked app password | Project → Settings → **Rotate password**. |
| A compromised node | Stop its agent; move its projects off with promotion, demotion or restores if it can't be trusted again. |
| The master key or backup key exposed | Rotate the master key (`pgdock-server -rotate-master-key`); generate a new backup key (new backups use it; old ones still need the old key, so keep it until they expire). |

Take a backup or a disk snapshot of anything you'll need to investigate
before changing it further.

## 3. Notify

Email the **owners of every affected organisation within 72 hours** of
noticing (the time the default [terms](terms-template.md) promise; use
your own if you changed it), even when you don't know everything yet:

- what happened, as far as you know;
- what data or access was affected, and since when;
- what you have done, and what they should do (rotate passwords, check
  their audit log, restore a backup);
- when they will hear from you next.

If personal data about other people may have been exposed, the
organisations (and you, as the operator) may have legal duties to report
it; in Nigeria, under the Nigeria Data Protection Act. Get advice.

## 4. Recover

Restore what was lost (Backups → Restore; point-in-time for dedicated
projects), fix the cause, reinstate what you suspended, and watch the
alerts for a day.

## 5. Follow up in writing

Within two weeks, send the same owners a short write-up:

- a timeline (noticed, contained, notified, recovered);
- the cause, plainly;
- what changes so it doesn't happen again, with dates.

Keep the write-ups with the project's [decisions](decisions.md): they are
the platform's history.
