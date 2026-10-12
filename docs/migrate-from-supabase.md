# Migrating from Supabase

PGDock has its own APIs, so your app's client code changes (see the
[client code guide](supabase-client-mapping.md)). Your data, users and
files move across with tooling (V4 §9), in four steps:

1. Import the database.
2. Turn on backend services.
3. Rewrite the policies.
4. Copy the users, then the files.

You can run each step again: what is already there is kept. The Supabase
project is only read, never changed. Keep it running until your app is on
PGDock.

## What you need from Supabase

- **The database connection string.** In Supabase, go to Project Settings →
  Database → Connection string. Use the session pooler or the direct
  connection, not the transaction pooler.
- **For files, an S3 access key.** Go to Project Settings → Storage → S3
  connection, then create an access key. Note the endpoint
  (`https://<ref>.supabase.co/storage/v1/s3`) and the region.

PGDock uses these credentials while a step runs and keeps them only in the
server's memory. They are never written to the metadata database or the
operation log.

## 1. Import the database

Go to New project → Import, or call `POST /api/v1/imports`. PGDock detects
the Supabase database and, by default, skips Supabase's own schemas
(`auth`, `storage`, `realtime` and so on). It then:

- creates placeholder roles `anon`, `authenticated` and `service_role`, so
  that policies naming them restore;
- adds `auth.uid()`, `auth.role()` and `auth.jwt()`, so that defaults and
  policies calling them restore.

Foreign keys to `auth.users`, and policies that read `auth.users`, can't
restore because that table isn't copied. The import log lists them. Point
those foreign keys at `pgd_auth.users (id)` after step 4.

## 2. Turn on backend services

Go to Project → API → Enable backend services. This creates the project's
request roles, its `pgd_auth` and `pgd_storage` schemas, and its keys.

## 3. Policies

Go to Project → API → Migrate from Supabase → Policies, or run:

    pgdock migrate supabase my-app --step policies

| Supabase | PGDock |
| --- | --- |
| `anon`, `authenticated`, `service_role` (policy roles) | `<db>_anon`, `<db>_user`, `<db>_service` |
| `auth.uid()` | `pgd_auth.uid()` |
| `auth.role()`, compared with `'authenticated'` / `'service_role'` | `pgd_auth.role()`, compared with `'user'` / `'service'` |
| `auth.jwt()` | `pgd_auth.claims()` (it carries `email`, `phone`, `app_metadata`, `user_metadata`, `role`, `aal`) |
| `auth.email()` | `pgd_auth.claims() ->> 'email'` |

The step rewrites each policy in place (`ALTER POLICY`). It also rewrites
column defaults that call `auth.uid()`, such as an owner column filled in
on insert.

Some policies can't be rewritten, such as one that reads `auth.users`.
These are kept as they were, and the log names each one and says why.

Functions and views that still call `auth.uid()` and the others keep
working: the step points those helpers at PGDock's tokens. `auth.role()`
still answers `authenticated` and `service_role`. The log lists the
functions that use `auth.users` or Supabase's storage tables, so you can
change them.

Grants don't need a step. Backend services already give the request roles
table, sequence and function access in the exposed schemas, as Supabase
does. Row-level security does the rest.

## 4. Users

    PGDOCK_SUPABASE_DB_URL='postgresql://…' pgdock migrate supabase my-app --step users

This step copies `auth.users` and `auth.identities`:

- **Ids are kept**, so your tables' `user_id` columns still match.
- **Passwords:** Supabase's bcrypt hashes are taken as they are. Users sign
  in with their existing passwords. At each user's first sign-in, PGDock
  re-hashes the password with argon2id. The auth log records this as
  `password_rehashed`.
- **Emails, phones and confirmation times** are kept. Phones are stored in
  E.164 (`2348031234567` becomes `+2348031234567`).
- **Metadata:** `app_metadata` and `user_metadata` are copied, along with
  bans, invitations and the last sign-in time. Anonymous users stay
  anonymous.
- **OAuth identities** keep their provider ids, so Google, Apple, GitHub,
  Facebook and Microsoft sign-ins (Supabase's `azure`) find the same user
  without re-linking. Set up the same providers in Project → Auth.

These are not copied:

- **Sessions.** Every user signs in once more.
- **MFA factors.** Users enrol again. The log counts how many are
  affected.
- **SAML SSO users**, and users deleted in Supabase. The log lists them.

A user whose email or phone already belongs to a different PGDock user is
skipped, and the log lists them too.

## 5. Files

    PGDOCK_SUPABASE_DB_URL='postgresql://…' PGDOCK_SUPABASE_S3_SECRET='…' \
      pgdock migrate supabase my-app --step storage \
        --s3-endpoint https://<ref>.supabase.co/storage/v1/s3 --s3-region eu-west-2 --s3-access-key <id>

**Buckets** keep their id, public flag, size limit and allowed types:

- Ids are lowercased.
- A bucket whose id PGDock doesn't accept (for example one with spaces) is
  skipped, along with its files. The log names it.

**Files** are copied byte for byte over the S3 connection:

- Path, type, owner, user metadata and creation time are kept.
- Supabase's `.emptyFolderPlaceholder` objects are left out: in PGDock,
  folders exist through their files.
- The organisation's storage quota applies. If it runs out, the step stops
  and says so. Raise the quota and run the step again to copy the rest.

**Policies** on `storage.objects` become policies on `pgd_storage.objects`:

- `bucket_id` becomes `bucket`.
- `name` becomes `path`.
- `storage.foldername(name)` becomes `pgd_storage.foldername(path)`, and
  `storage.filename()` and `storage.extension()` map the same way.
- `owner_id` becomes `owner::text`.

A policy that uses a column PGDock doesn't have, such as `metadata`, isn't
copied, and the log says so. Policies on `storage.buckets` aren't copied
either: in PGDock, buckets are managed with the secret key and the
dashboard.

## Checking

- Users sign in through `POST /auth/v1/signin/password` with their old
  password.
- The data API returns each user's own rows.
- Each file downloads from `/storage/v1/object/<bucket>/<path>`.

`TestSupabaseMigration` checks all of this against real Supabase services:
GoTrue, storage-api and the supabase/postgres image.
