# Importing a database that runs in Docker on a VPS

## projects running inside docker in a vps
> This guide assumes the docker db isn't reachable outside the VPS

The easy way: **push** the data from the source VPS to PGDock with one command. Nothing is opened on the source, there's no firewall rule and no forwarder, and the transfer is encrypted. PGDock's database port is already open.

**1. In PGDock, create an empty project** (Projects → New project, any name). Copy its **session URL** from the credentials screen. The password is shown once, so save it. It looks like `postgres://p_xxxx_owner:PASSWORD@db.synledger.name.ng:5432/p_xxxx?sslmode=require`.

If the database uses extensions, enable them on the project first (Settings → Extensions), because the restore can't create them otherwise. To see which ones you use, run this on the source VPS:
```bash
docker exec <db-container> psql -U postgres DBNAME -c '\dx'
```

**2. On the source VPS, run one command:**
```bash
docker exec <db-container> pg_dump -U postgres -Fc DBNAME \
 | docker run --rm -i postgres:18 pg_restore --no-owner --no-acl -d "<SESSION-URL>"
```
- Replace `<db-container>`, `DBNAME` and the user (`postgres`, or whichever user owns the data) with yours. Put the whole session URL in quotes.
- Use the **session URL** (port 5432), not the pooled one on 6543. `pg_restore` needs session mode.
- Don't add `-t` or `-it` to `docker exec`, or the output gets corrupted.
- It streams straight from the old database into PGDock. No file is written to disk.

**3. Check the result.** `pg_restore` prints any problems at the end. A few warnings about ownership or privileges are normal. Errors saying a table, extension or function failed to create are not. Then compare a few row counts between the old and new database:
```bash
docker exec <db-container> psql -U postgres DBNAME -c 'select count(*) from your_table'
```
Run the same query on the new project in PGDock's SQL Editor.

**4. Switch your app** to the pooled URL (port 6543), and keep the old database until you're sure.

**Things to know**
- This is a point-in-time copy. Stop writes to the old database before step 2, or you'll lose anything written after it starts.
- Custom roles and permissions from the old database don't come across. The app uses the new project's owner role.
- This is not the Import feature. It skips PGDock's preflight report and its automatic row-count verification, so step 3 is yours to do.
