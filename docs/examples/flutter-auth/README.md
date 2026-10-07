# Flutter sample: WhatsApp codes, Google, and org-scoped data

A small Flutter app that signs in to a PGDock project with a WhatsApp code or
with Google (PKCE), and reads a table that row-level security limits to the
user's organisation, using an `org_id` claim a custom-claims hook adds
(V4-M32's done-when).

It talks to the project's APIs over HTTP (`lib/pgdock_auth.dart`); the auth
paths follow Supabase's (`/auth/v1/...`), so `supabase_flutter`'s auth client
also works against them.

## 1. The project

In the dashboard, turn on backend services (Project Settings → API), then in
Authentication:

- **Phone**: turn on *Codes by WhatsApp* (Nigeria is allowed by default; the
  daily cap stops SMS pumping).
- **Providers**: turn on Google with your OAuth client's ID and secret, and
  register the callback URL shown there with Google.
- **Sign-in and sessions**: add `com.example.pgdocksample://login-callback` to
  the redirect URLs.
- **Hooks**: set *Custom claims* to `public.custom_claims` (below).

## 2. The database

Run in the SQL editor (replace `p_xxxxxxxxxx` with your project's database
name; the hook role and user role are shown in Authentication → Hooks and
Project Settings → API):

```sql
CREATE TABLE members (
  user_id uuid PRIMARY KEY REFERENCES pgd_auth.users (id) ON DELETE CASCADE,
  org_id  text NOT NULL
);
GRANT SELECT ON members TO "p_xxxxxxxxxx_auth_hook";

-- Called when a token is issued: adds the user's org to the claims.
CREATE FUNCTION public.custom_claims(event jsonb) RETURNS jsonb LANGUAGE sql STABLE AS $$
  SELECT jsonb_build_object('claims', (event -> 'claims') || coalesce(
    (SELECT jsonb_build_object('org_id', org_id) FROM members
     WHERE user_id = (event ->> 'user_id')::uuid), '{}'::jsonb))
$$;

CREATE TABLE docs (id bigserial PRIMARY KEY, org_id text NOT NULL, title text NOT NULL);
ALTER TABLE docs ENABLE ROW LEVEL SECURITY;
CREATE POLICY same_org ON docs FOR SELECT TO "p_xxxxxxxxxx_user"
  USING (org_id = pgd_auth.claim('org_id'));
```

After a user first signs in, give them an org (`INSERT INTO members ...`) and
tap *Refresh*: the new token carries `org_id`, and only that org's documents
come back.

## 3. Run it

```sh
flutter create --platforms=android,ios --org com.example . # adds the platform folders
flutter pub get
flutter run --dart-define=PGDOCK_URL=https://<ref>.<domain> --dart-define=PGDOCK_KEY=<publishable key>
```

For the Google redirect, register the `com.example.pgdocksample` scheme as
`flutter_web_auth_2`'s README describes (an intent filter on Android,
nothing extra on iOS).
