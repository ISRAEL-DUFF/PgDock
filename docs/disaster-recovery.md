# Disaster recovery

What to do when the control node is lost (spec §11.6). Practise it once
before you need it.

## What you need, kept off the server

- **`.env`** from `deploy/compose` (above all `PGDOCK_MASTER_KEY`: it
  decrypts the secrets in the metadata DB).
- **The backup key file** you downloaded in the setup wizard
  (`pgdock-backup-key-<fingerprint>.txt`). Without it no backup can be
  decrypted, by you or anyone else.
- **Credentials for the backup bucket.**

## 1. Rebuild the control node

On the new host, clone the repository and put the saved `.env` in
`deploy/compose` **before** running `./install.sh`, so it keeps the same
master key. Start only the metadata database for now:

```sh
cd deploy/compose
docker compose up -d metadata-db
```

## 2. Restore the metadata

The metadata DB backs itself up every night to
`<prefix>/metadata/<time>.dump.enc` in the bucket. Fetch the newest one
with any S3 client, decrypt it with the backup key, and restore it:

```sh
aws s3 cp s3://<bucket>/<prefix>/metadata/<newest>.dump.enc .   # or rclone, mc, …
# --user 0: the key file is usually readable by root only.
docker compose run --rm -T --no-deps --user 0 -v "$PWD:/work:ro" pgdock-agent \
  decrypt --key-file /work/pgdock-backup-key-<fingerprint>.txt < <newest>.dump.enc > metadata.dump
docker compose cp metadata.dump metadata-db:/tmp/metadata.dump
docker compose exec metadata-db pg_restore -U pgdock -d pgdock --clean --if-exists --no-owner /tmp/metadata.dump
```

`pgdock-agent decrypt` checks every chunk; a wrong key or a damaged object
fails without writing a partial file.

## 3. Start everything

```sh
docker compose up -d --wait
```

pgdock-server runs its migrations, re-renders the pooler configuration
(routes and SCRAM verifiers come from the metadata), and reloads the
poolers, so existing connection strings and passwords keep working once
the databases are back.

## 4. Re-register agents

If an agent's state volume (`agent-state`) survived, it reconnects by
itself. Otherwise the metadata still pins the old certificate: open
**Nodes → Re-register…**, and run the printed command on the node (the
token works once, for 24 hours).

## 5. Bring back project data

If the shared cluster's volume survived, there is nothing to do. If it was
lost too, each project's database is gone while its record remains: open
the project's **Backups** tab and **Restore** its latest backup into a new
project (in-place restore needs the old database to exist). Point the app at
the new connection string. Final backups of deleted projects stay listed
for 30 days.
