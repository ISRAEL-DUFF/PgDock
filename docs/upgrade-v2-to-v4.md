# Moving from V2 (PGDock) to Xenibase V4.1

The product was called PGDock until V4.1. The rename was a clean break:
settings are `XENIBASE_*` (not `PGDOCK_*`), the containers, database user,
schemas (`xb_auth`, `xb_storage`, `xb_realtime`) and API keys (`xb_pub_`,
`xb_sec_`) are renamed, and there is no fallback to the old names. **An
install running `v2.0.0` or `v2.1.0` can't be upgraded in place.** Install
V4.1 fresh, on a new server, and move the projects across one at a time.
The old server keeps running until the last project has moved.

[Upgrading](upgrade.md) explains what each version changed. This page is
the order to do things in.

## 1. Before the day

Decide these, and write them down:

| Question | Why it matters | Where |
| --- | --- | --- |
| Hostnames | The web UI and the database host are new names; apps get new connection strings anyway. | [Install](install.md) |
| Which organisations pay, and on which plan? | New organisations start on the **Free** plan: idle Free projects pause after 7 days and are archived after 90. Paid plans and the **Unlimited** quota plan (your own platform admin's organisation) never pause. | [The Free tier](free-tier.md) |
| Prices and the dollar rate | Prices are in US dollars and invoiced in naira at a rate frozen on the invoice. Nothing is invoiced until automatic issue is on. | [Billing](billing.md), [Payments](payments.md#the-dollar-rate) |
| The wallet provider | Payments need the iswallet settings in `.env`, with a webhook subscription whose secret goes in `XENIBASE_ISWALLET_WEBHOOK_SECRET`. | [iswallet](iswallet.md) |
| Region | Every node is put in `XENIBASE_REGION` (default `eu-central`). | [Regions](regions.md) |
| Legal documents | On first start the server publishes template SLA and DPA documents. Review them before customers sign in. | [Legal documents](legal.md) |

Payments, support email and WhatsApp, the status page, cloud capacity and
backend services stay off until their settings are set; they can wait
until after the install.

## 2. Install the new server

Follow [Install](install.md) on the new server, with new DNS names and a
new backup bucket (the old server's backups stay where they are). Then:

1. Back up `deploy/compose/.env` off the server. `XENIBASE_MASTER_KEY` in it
   decrypts every secret Xenibase stores.
2. Create the first administrator with the one-time setup code, and turn on
   two-factor sign-in.
3. Add the settings you decided above to `.env` and restart the server.
4. Check `xenibase-server -version`, that the log shows no failed migration,
   and that a backup of the metadata database reaches the bucket
   (Settings → Backup checks).

**The iswallet webhook comes last.** The server refuses to start with an
API key and no webhook secret, and the secret exists only once the
subscription does, which needs the server to be reachable. Start with the
API key and a temporary secret of at least 16 characters. Create the
subscription at `https://<host>/api/v1/payments/webhooks/iswallet` for
`wallet.credit.posted` (and `.reversed` and `.rejected`, which are logged
for now), put the real secret in `.env`, and restart.

## 3. Move one project

For each project on the old server, in turn:

1. In the new Xenibase, create the organisation (if it's new) and a project
   with the same name and Postgres major version.
2. Announce a short write freeze to the project's owner.
3. Dump the old project's database from the old server (`pg_dump -Fc`,
   over its session-mode connection) and restore it into the new project
   with `pg_restore`, over the new project's session-mode connection.
   Rehearse on a throwaway project first: roles and grants the dump
   refers to must exist in the new project (it creates its own request
   roles), and the Xenibase-managed schemas are the new server's own.
   [Migrate from Supabase](migrate-from-supabase.md) describes the same
   steps for a Supabase source, including the role and schema mapping.
4. Re-create what lives outside the database: storage objects, auth
   settings, backend services and secrets.
5. Give the application the new connection string and the new
   `xb_pub_` / `xb_sec_` keys. Old `pgd_` keys don't work.
6. Check the application, then pause the old project (don't delete it yet).

Keep the old server and the paused projects for at least a billing period
after the last move, then take a final backup of each and retire them.

## 4. Going live

- [ ] An invoice issues, is debited from a funded wallet, and reconciles
      (use a real organisation of your own, with a small amount).
- [ ] A bank transfer into an organisation's virtual account shows up as a
      deposit on its wallet.
- [ ] A restore from the new bucket works ([Disaster recovery](disaster-recovery.md)).
- [ ] Announce the new hostnames to customers and give them the date the
      old ones stop.

## 5. If something goes wrong

The old server wasn't touched, so the way back is to keep using it. Point
the application at the old connection string again; changes made on the new
project since the freeze have to be carried back by hand, which is why each
project is frozen only for as long as its move takes.
