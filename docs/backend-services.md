# Backend services

Backend services (V4) let an app talk to its project over HTTPS, straight
from a browser or a phone, without a server of its own: the data API, auth,
storage and realtime. This page covers what is in place now: the edge gateway, project API
hostnames, API keys and the per-request roles and claims (V4-M28), and the
data API's reads (V4-M29). Writes, auth, storage and realtime arrive in the
milestones after them.

## How a request is served

```
app ──HTTPS──▶ <ref>.<domain> ──▶ pgdock-edge (per region)
                                    │ key, token, CORS, rate limit
                                    ▼
                        transaction pooler ──▶ the project's database
                        BEGIN; SET LOCAL ROLE <db>_anon|_user|_service;
                        set_config('pgd.claims', …, true); …; COMMIT
```

- Each project with backend services has a **reference**, eight letters and
  digits starting with a letter (`k7f3m2q9`), and is served at
  `https://<ref>.<domain>`, where the domain is `PGDOCK_API_DOMAIN`.
- **pgdock-edge** resolves exactly one project per request from the
  hostname, checks the API key against that project's keys only, and runs
  the request in one transaction through the transaction pooler. It logs
  in as the project's `<db>_edge` role, which owns nothing and can only
  `SET ROLE` to the three request roles.
- The claims of the request are set for that transaction only, so nothing
  carries over to the next request on the same pooled connection.

## Keys

| Key | Prefix | Used from | Requests run as |
| --- | --- | --- | --- |
| Publishable | `pgd_pub_` | Browsers and mobile apps; meant to be embedded | `<db>_anon`, or `<db>_user` with a signed-in user's token |
| Secret | `pgd_sec_` | Servers only | `<db>_service` (bypasses row-level security) |

- Send the key in the `apikey` header. A signed-in user's access token goes
  in `Authorization: Bearer …`; it is an ES256 JWT for this project
  (`aud` is the reference), verified with the project's public keys.
- Keys are stored hashed. The publishable key stays visible in the
  dashboard; a **secret key is shown once**, when it is made.
- A secret key sent from a page (a request with an `Origin` header) is
  refused unless the project allows it.
- Make a second key and move your apps to it before revoking the first:
  revoking takes effect at the edge within seconds.

## Turning it on

Project → Settings → **API** → **Enable backend services**, or
`pgdock services enable <project>`. Enabling:

1. creates `<db>_edge`, `<db>_anon`, `<db>_user` and `<db>_service` and lets
   the pooler accept the edge login;
2. adds the `pgd_auth`, `pgd_storage` and `pgd_realtime` schemas, owned by
   the platform, with `pgd_auth.uid()`, `role()`, `claims()` and
   `claim(text)` for your row-level security policies;
3. grants the three request roles use of what the owner creates in
   `public` (row-level security decides which rows);
4. makes the first publishable and secret keys and the project's signing
   key.

```sql
ALTER TABLE todos ENABLE ROW LEVEL SECURITY;
CREATE POLICY own_todos ON todos FOR ALL TO "<db>_user"
  USING (owner_id = pgd_auth.uid()) WITH CHECK (owner_id = pgd_auth.uid());
```

Turning it off revokes the keys and switches the edge login off. The
`pgd_*` schemas and their data stay, so turning it on again restores
everything except the keys, which are new.

The `pgd_*` schemas are part of the database: backups, restores and
branches carry them. After a restore or a move, PGDock takes them back from
the project's owner (a restore recreates them as the owner's) and re-applies
the request roles' grants, within seconds. A promotion or demotion creates
the request roles on the new instance before copying.

Check that it works:

```sh
curl -H "apikey: pgd_pub_…" https://k7f3m2q9.api.pgdock.ng/data/v1/health
# {"status":"ok","project":"k7f3m2q9","role":"anon","region":"ng-lagos"}
```

## Reading data

`https://<ref>.<domain>/data/v1/<table>` reads a table or view in the
exposed schemas (`public` unless you choose others). Name a table in another
exposed schema as `schema.table`.

```sh
curl -G https://k7f3m2q9.api.pgdock.ng/data/v1/todos \
  -H "apikey: pgd_pub_…" -H "Authorization: Bearer $ACCESS_TOKEN" \
  --data-urlencode "select=id,title,done,owner:profiles(name)" \
  --data-urlencode "where=done:eq:false" \
  --data-urlencode "order=created_at:desc" --data-urlencode "limit=20"
# {"data":[{"id":3,"title":"c","done":false,"owner":{"name":"Ada"}}, …],"next_cursor":"eyJr…"}
```

| Parameter | |
| --- | --- |
| `select` | Columns (`*` by default), `alias:column`, JSON paths (`meta->plan`), and related rows through foreign keys: `author(name)` gives an object (or `null`) when this table points at it, and `comments(id,body)` an array when it points at this table. Name a relation by its table (or by the foreign key column without `_id`); when two foreign keys reach the same table, say which: `editor:profiles!editor_id(name)`. Up to 3 levels; arrays hold up to 1,000 rows. |
| `where` | `column:operator:value`, repeatable (all must hold). Operators: `eq`, `neq`, `lt`, `lte`, `gt`, `gte`, `in` (`a,b,"c,d"`), `like`, `ilike` (`%` wildcards), `is` (`null`, `true`, `false`), `contains`, `contained_by` (arrays, jsonb), `search` (full text, `websearch_to_tsquery`). JSON paths compare as text: `where=meta->tag:eq:work`. |
| `or` | One group of conditions any of which may hold: `or=status:eq:draft,owner_id:eq:…`. Repeatable; each group must hold. |
| `order` | `column:asc` or `column:desc`, comma-separated. The primary key is added so pages are stable. |
| `limit` | Default 100, at most 1,000. |
| `cursor` | The `next_cursor` of the previous page (keyset pagination, for any depth). `offset` also works, up to 10,000. |
| `count` | `exact` (capped at 2 seconds, `null` if it ran out) or `estimated` (the planner's). |

- **One row:** `GET /data/v1/todos/42` (single-column primary keys) returns
  `{"data":{…}}`, or `404` when it doesn't exist or the caller can't see it.
- **Complex queries:** `POST /data/v1/todos/query` with
  `{"select":"…","where":{"or":[{"column":"done","op":"eq","value":true},{"not":{…}}]},"order":"title:desc","limit":50}`;
  `and`, `or` and `not` nest.
- **The project's OpenAPI document:** `GET /data/v1/openapi.json` with the
  secret key, generated from the current schema.

### Row-level security comes first

With the publishable key, requests run as `<db>_anon` (or `<db>_user` with a
signed-in user's token), and **a table without row-level security is not
readable**: the API answers `403 rls_required`. Enable it and write policies,
or, for data anyone may read, list the table under **Public tables**.
Views must run as the caller (`CREATE VIEW … WITH (security_invoker = true)`)
so the underlying tables' policies apply; embedded tables are checked the
same way. The secret key bypasses row-level security entirely.

```sql
ALTER TABLE posts ENABLE ROW LEVEL SECURITY;
CREATE POLICY readable ON posts FOR SELECT
  USING (published OR author_id = pgd_auth.uid());
```

Column privileges hold too: `REVOKE SELECT (email) ON profiles FROM "<db>_anon"`
hides a column from anonymous callers.

### Guardrails

- Each request has its statement timeout (8 s by default), at most 30
  filters and 3 levels of relations, and a 10 MB result.
- Every new shape of query is planned once (`EXPLAIN`) and refused with
  `400 query_too_expensive` when its estimated cost is over the project's
  limit (1,000,000 by default: a scan of a few million rows). Add an index or
  a filter, or raise the limit in the settings.
- Schema changes are picked up within about two seconds, with no restart.

### Errors

```json
{"error":{"code":"unknown_column","message":"todos has no column \"nope\"","request_id":"req_…"}}
```

| Status | Codes |
| --- | --- |
| 400 | `invalid_filter`, `invalid_select`, `invalid_value`, `unknown_column`, `unknown_relation`, `ambiguous_relation`, `invalid_cursor`, `invalid_limit`, `query_too_expensive`, `filter_required`, `too_many_rows`, `invalid_body`, `no_matching_function`, `ambiguous_function`, … |
| 401 | `key_required`, `invalid_key`, `invalid_token` |
| 403 | `rls_required`, `permission_denied`, `secret_key_in_browser`, `origin_not_allowed`, `project_suspended` |
| 404 | `unknown_table`, `unknown_function`, `not_found` |
| 409, 422 | `unique_violation`, `foreign_key_violation`; `not_null_violation`, `check_violation` |
| 405 | `method_not_allowed`, `volatile_function` |
| 413 | `result_too_large` |
| 429 | `rate_limited` |
| 503, 504 | `project_resuming`, `database_unavailable`, `statement_timeout` |

## Writing data

Writes run as the caller's role in one transaction, so row-level security's
`USING` and `WITH CHECK` decide what each caller may change. With the
publishable key, a table is writable only with row-level security on (a
view only with `security_invoker`); **public tables are readable, never
writable, without it**.

```sh
# Insert one row or a list (up to 1,000); the new rows come back.
curl -X POST https://k7f3m2q9.api.pgdock.ng/data/v1/todos \
  -H "apikey: pgd_pub_…" -H "Authorization: Bearer $ACCESS_TOKEN" \
  -d '{"owner_id":"…","title":"Buy milk"}'
# {"affected":1,"data":[{"id":7,"owner_id":"…","title":"Buy milk","done":false}]}

# Update the rows a filter matches, or one row by key.
curl -X PATCH ".../data/v1/todos?where=done:eq:false" -d '{"done":true}'
curl -X PATCH ".../data/v1/todos/7" -d '{"title":"Buy oat milk"}'

# Delete.
curl -X DELETE ".../data/v1/todos?where=title:eq:Buy%20milk"
curl -X DELETE ".../data/v1/todos/7"
```

| Parameter | |
| --- | --- |
| `on_conflict` | Upsert: the columns of a unique constraint (`on_conflict=email`). Conflicting rows are updated with the sent columns; with `resolution=ignore` they are left alone. |
| `select` | Which columns of the written rows come back (the read API's `select`, without relations). |
| `return` | `representation` (default) or `minimal` (only `affected`). |
| `where`, `or` | Which rows an update or delete changes, as for reads. **An update or delete needs a filter or a key**: `400 filter_required` otherwise. |
| `max_affected` | Refuse (`400 too_many_rows`, nothing changed) when more rows would change; 1,000 by default, at most 100,000. |

`PUT` isn't used: insert or upsert with `POST`, change with `PATCH`.

### Batches

`POST /data/v1/batch` runs up to 50 writes in one transaction: all of them
happen or none do.

```json
{"operations":[
  {"op":"insert","table":"orders","rows":[{"item":"tea"}]},
  {"op":"upsert","table":"stock","rows":{"item":"tea","n":9},"on_conflict":["item"]},
  {"op":"update","table":"carts","where":{"column":"id","op":"eq","value":4},"set":{"closed":true}},
  {"op":"delete","table":"cart_items","key":4,"return":"minimal"}]}
```

The answer is `{"results":[…]}`, one per operation. When one fails, the error
names it (`details.operation`, counted from 0) and says nothing was changed.

## Functions

`/data/v1/rpc/<function>` calls a function in the exposed schemas as the
caller, with named arguments:

```sh
curl -X POST .../data/v1/rpc/close_cart -d '{"cart_id": 4}'
curl ".../data/v1/rpc/search_todos?q=milk&limit=5"   # STABLE or IMMUTABLE only
```

- `POST` takes the arguments as a JSON object; `GET` takes them as query
  parameters and works only for `STABLE` or `IMMUTABLE` functions
  (`405 volatile_function` otherwise).
- A scalar result comes back as `{"data": 42}`; a set or a table as a list,
  with the read API's `select`, `where`, `order` and `limit` applied to its
  rows.
- Overloads are chosen by the argument names given; `400
  ambiguous_function` when more than one fits.
- `SECURITY DEFINER` functions run as their owner and skip row-level
  security: the security advisor lists them.

## Generated types

Typed rows, inserts and updates for every table, view and function the data
API exposes:

```sh
pgdock gen types --lang ts --project my-app > src/database.types.ts
pgdock gen types --lang dart --project my-app -o lib/database_types.dart
pgdock gen types --lang go --project my-app --package db -o db/types.go
```

- **TypeScript:** a `Database` interface (`Database["public"]["Tables"]["todos"]["Row" | "Insert" | "Update"]`, `Views`, `Functions` with `Args` and `Returns`) and `Tables<"todos">`-style helpers. Columns with a default or that accept null are optional in `Insert`; generated columns are left out.
- **Dart:** a class per table and view with `fromJson` and `toJson`.
- **Go:** a struct per table and view, `…Insert` and `…Update` structs (pointers with `omitempty` for what may be left out) and a `…Table` constant per name.

The same files download from Project → Settings → API → Generate types, or
`GET /api/v1/projects/{id}/services/types?lang=ts|dart|go`.

## Security advisor

Project → Settings → API → Security advisor (or
`GET /api/v1/projects/{id}/services/advisor`) checks the exposed schemas:

| Level | Finding |
| --- | --- |
| danger | A table without row-level security (`rls_disabled`); a write policy open to anon (or `PUBLIC`) with no condition (`policy_allows_everything`). |
| warn | `SECURITY DEFINER` functions (`security_definer_function`); views that don't run as the caller (`view_not_invoker`); columns named like secrets (password, token, secret…) readable by anon or user (`secret_column`). |
| info | Public tables; row-level security with no policies (only the secret key gets in); read policies open to everyone. |

Each finding says what to do and, where one statement fixes it, gives it.

**The policy helper:** in the Table Editor, **Policies** on a table turns on
row-level security and writes the policies for one of three templates
(owner only, members of an organisation through a membership table, public
read with owner writes), showing the SQL before it runs.

## Request explorer

Project → Settings → API → Request explorer sends a data API request as
anon, as a user (by their id, which becomes the token's `sub`) or as the
service role and shows the answer, without handing out a key or token
(`POST /api/v1/projects/{id}/services/explore`; developers and up). Writes
made there are real.

## Auth

Users live in the project's own database, in `pgd_auth` (V4 §4.2), so they
move with it: backups, restores, branches and promotions keep them, and
data residency covers them. Only pgdock-edge reads the auth tables; your
SQL sees the safe view `pgd_auth.user_profiles` (no password hashes,
tokens or codes) and may reference `pgd_auth.users(id)` from its own
tables:

```sql
CREATE TABLE todos (
  id bigserial PRIMARY KEY,
  owner_id uuid NOT NULL DEFAULT pgd_auth.uid() REFERENCES pgd_auth.users (id) ON DELETE CASCADE,
  title text NOT NULL
);
ALTER TABLE todos ENABLE ROW LEVEL SECURITY;
CREATE POLICY own ON todos FOR ALL TO "<db>_user"
  USING (owner_id = pgd_auth.uid()) WITH CHECK (owner_id = pgd_auth.uid());
```

### Signing up and in

All at `https://<ref>.<domain>/auth/v1/`, with the publishable key:

```sh
curl -X POST .../auth/v1/signup -H "apikey: pgd_pub_…" \
  -d '{"email":"ada@example.com","password":"…","data":{"name":"Ada"},"redirect_to":"https://app.example.com/welcome"}'
# {"confirmation_sent":true}       (or a session, when confirmation is off)

curl -X POST .../auth/v1/signin/password -H "apikey: pgd_pub_…" -d '{"email":"ada@example.com","password":"…"}'
# {"access_token":"eyJ…","token_type":"bearer","expires_in":3600,"expires_at":…,"refresh_token":"…","user":{…}}
```

| Endpoint | |
| --- | --- |
| `POST signup` | Email and password (argon2id). With **Confirm email addresses** on (the default) the user gets a link and a 6-digit code and can't sign in with the password until they use one; the answer is the same whether or not the address was new. |
| `POST signin/password` (or `POST token?grant_type=password`) | A session. Five wrong passwords in a row lock the user for a minute, doubling with each further failure (`429 user_locked`). |
| `POST signin/otp` | A magic link and a code by email (`create_user: false` to only sign in existing users). Proving an unconfirmed address this way (or by a reset) clears any password set on it before, so nobody can claim an address ahead of its owner. |
| `POST verify` | `{"type":"signup"\|"magiclink"\|"email"\|"recovery"\|"invite"\|"email_change","email":"…","token":"123456"}`, or `{"type":…,"token_hash":"…"}` with the link's token: a session. A code works once, for 10 minutes (invitations a day), and 5 wrong tries use it up. |
| `GET verify?token=…&type=…&redirect_to=…` | The link in the email (no key needed): redirects to `redirect_to` with the session in the fragment (`#access_token=…&refresh_token=…`), or `#error=access_denied&error_code=otp_expired`. |
| `POST resend` | Another confirmation email (`{"type":"signup","email":…}`). |
| `POST recover` | A password-reset link and code; verifying it signs the user in to set a new password. |
| `POST token?grant_type=refresh_token` | `{"refresh_token":"…"}`: new tokens; the refresh token is single use. |
| `POST signout?scope=local\|others\|global` | With the access token: this session, the others, or every session. |
| `GET user`, `PATCH user` | The signed-in user with their `identities` and `factors`; change the password, `data` (merged into the user's metadata), the email (confirmed by a link sent to the new address) or the `phone` (confirmed by a code to the new number). |
| `GET settings` | Which methods are on and the password rules, for your sign-in form. |
| `GET .well-known/jwks.json` | The public keys (no key needed). |

Links go only to the **site URL** (and pages under it) or the exact
**redirect URLs** you list (`*` and `**` wildcards when allowed);
anything else is `400 redirect_not_allowed`.

### Phone: SMS and WhatsApp codes

Turn the channels on in Authentication → Phone. Numbers may be written the
local way (`0803 123 4567`) or internationally (`+2348031234567`); they are
stored in E.164.

```sh
curl -X POST .../auth/v1/signin/otp -H "apikey: pgd_pub_…" -d '{"phone":"08031234567","channel":"whatsapp"}'
curl -X POST .../auth/v1/verify -H "apikey: pgd_pub_…" -d '{"type":"sms","phone":"+2348031234567","token":"123456"}'
```

| Endpoint | |
| --- | --- |
| `POST signin/otp` with `phone` and `channel` (`sms` default, or `whatsapp`) | A code to the number, creating the user when sign-ups are on (`create_user: false` to only sign in). |
| `POST signup` with `phone` and `password` | A phone and password account; with **Confirm numbers at sign-up** on (the default) a code goes to the number first. |
| `POST signin/password` with `phone` | Signs in with the number and password. |
| `POST verify` | `{"type":"sms","phone":…,"token":…}` (also `"whatsapp"`), or `"phone_change"` for a new number. |
| `PATCH user` with `phone` (and `channel`) | Sends a code to the new number. |
| `POST resend` with `{"type":"sms","phone":…}` | Another code (only to an existing user). |

**Who sends.** By default, PGDock's own accounts: Termii for SMS, through
its DND route so numbers registered as do-not-disturb still get codes,
and PGDock's WhatsApp Business number with an approved authentication
template. Each message is metered (`messages_sms`, `messages_whatsapp`)
and its cost shown under Authentication → Phone. Free projects bring their
own provider unless the install allows otherwise. A project can use its
own **Termii, Africa's Talking or Twilio** account for SMS and its own
**WhatsApp Cloud API** number (or Twilio) for WhatsApp; PGDock doesn't bill
those messages. A **send-message hook** (below) replaces sending entirely.

**SMS pumping.** Paid codes invite fraud: someone requests codes to numbers
they profit from. Four limits stop it:

- **Countries**: numbers must be in the project's countries (Nigeria only
  by default; `403 phone_country_not_allowed` otherwise).
- **Per number**: one code a minute from the edge, and at most 5 an hour at
  pgdock-server (`429 over_sms_send_rate_limit`).
- **Daily cap**: 200 codes a day per project by default. At the cap, codes
  stop (`429`, "daily limit") and the project's admins are emailed; they
  are also emailed when a quarter of the cap goes out in one hour.
- **Captcha** (below) on code requests.

### OAuth: Google, Apple, GitHub, Facebook, Microsoft

Each project uses its own OAuth app. In Authentication → Providers, turn a
provider on with its client ID and secret (Apple: the Services ID, team
ID, key ID and the `.p8` key) and register the **callback URL** shown
there, `https://<ref>.<domain>/auth/v1/callback`, with the provider.

With PKCE (what Supabase's clients do by default):

1. The app sends the browser to
   `GET /auth/v1/authorize?provider=google&redirect_to=<allowed URL>&code_challenge=…&code_challenge_method=s256`
   (no key needed).
2. After the provider, the browser returns to `redirect_to?code=…`.
3. The app exchanges it: `POST /auth/v1/token?grant_type=pkce` with
   `{"auth_code":…,"code_verifier":…}`. The code works once, for 5
   minutes.

Without a `code_challenge` the session comes back in the redirect's
fragment instead. A provider's verified email joins the user who has that
email (and clears a password someone set on it unconfirmed); an
unverified one never does. Microsoft's email isn't treated as verified.

### Anonymous users and identities

With **Anonymous sign-in** on, `POST /auth/v1/signup` with an empty body
returns a session for a new user with `is_anonymous: true` (the claim is
in the token, and `pgd_auth.is_anonymous()` reads it in policies). The
user becomes permanent when they add an email or phone (`PATCH user`, then
verify) or link a provider.

A signed-in user can link a provider with
`GET /auth/v1/user/identities/authorize?provider=…&redirect_to=…` (with
their token; the answer is `{"url":…}` to open) and unlink one with
`DELETE /auth/v1/user/identities/{id}`; the last way to sign in can't be
unlinked. Both can be turned off.

### Multi-factor authentication

Users enrol an authenticator app (TOTP) or, when allowed, a phone:

| Endpoint (with the user's token) | |
| --- | --- |
| `POST factors` `{"factor_type":"totp","friendly_name":…}` | The secret and an `otpauth://` URI for a QR code. Phone: `{"factor_type":"phone","phone":…}`. |
| `POST factors/{id}/challenge` | A challenge (a phone factor gets a code). |
| `POST factors/{id}/verify` `{"challenge_id":…,"code":…}` | New tokens at `aal2`; the factor is verified. |
| `DELETE factors/{id}` | Removes a factor (a verified one needs `aal2`). |

`/mfa/enroll`, `/mfa/challenge` and `/mfa/verify` (with `factor_id` in the
body) do the same. The **policy** (Authentication → MFA and captcha) is
optional (users choose), required (the data API answers only `aal2`
tokens: `403 mfa_required`), or required by claim for users whose
`app_metadata.mfa_required` is true. Policies can check
`pgd_auth.aal() = 'aal2'` themselves.

### Hooks

| Hook | | |
| --- | --- | --- |
| **Custom claims** | A Postgres function `schema.name(event jsonb) returns jsonb` | Called when a token is issued with `{"user_id","claims","authentication_method"}`; return `{"claims":{…}}`. Claims the edge sets (`sub`, `role`, `aud`, `exp`, `iat`, `session_id`, `aal`, `amr`, `is_anonymous`) can't be changed. |
| **Before sign-up** | A Postgres function, or a webhook | Called with `{"user","method","ip"}`; `{"decision":"reject","message":…}` (or a webhook's 4xx) refuses with `403 signup_rejected`. A webhook that doesn't answer in 3 seconds lets the sign-up through. |
| **After sign-up**, **after sign-in** | Webhooks | `{"type":"after_signup","data":{"user":…,"method":…}}`, queued and retried like database webhooks. |
| **Send message** | A webhook | Receives every email and code (`{"type":"send_message","channel","kind","to","body","code","link"}`) instead of PGDock sending it. |

Postgres hooks run in their own transaction, with a 2-second timeout, as
the project's **hook role** `<db>_auth_hook` (a login of its own, like the
request roles: code that runs `RESET ROLE` stays that role), which has no privileges
beyond reading `pgd_auth.user_profiles` and the `pgd_auth` functions:
grant it what the hook reads.

```sql
GRANT SELECT ON members TO "<db>_auth_hook";
CREATE FUNCTION public.custom_claims(event jsonb) RETURNS jsonb LANGUAGE sql STABLE AS $$
  SELECT jsonb_build_object('claims', (event -> 'claims') || coalesce(
    (SELECT jsonb_build_object('org_id', org_id) FROM members WHERE user_id = (event ->> 'user_id')::uuid), '{}'::jsonb))
$$;
-- then, in a policy: USING (org_id = pgd_auth.claim('org_id'))
```

A failing custom-claims hook fails the sign-in (`500`), so test it before
turning it on. Webhooks are signed with the project's **hook secret**
(`PGDock-Signature`, the same scheme as database webhooks), go through the
organisation's outbound rules, and are listed with their results under
Authentication → Hooks.

### Captcha

With captcha on (a Cloudflare Turnstile site key and secret in
Authentication → MFA and captcha), sign-up, password sign-in and code
requests need a Turnstile token, sent as
`"gotrue_meta_security":{"captcha_token":…}` (what Supabase's clients
send) or `"captcha_token"` (`400 captcha_failed` otherwise).

### Tokens and sessions

- The access token is an ES256 JWT signed with the project's key, an hour
  by default (5 minutes to 24 hours): `sub` (the user id), `role: "user"`,
  `aud` (the project ref), `session_id`, `aal`, `amr`, `email`, `phone`,
  `is_anonymous`, `app_metadata`, `user_metadata`, and whatever a
  custom-claims hook adds. Send it as `Authorization: Bearer …`
  with the publishable key: requests run as `<db>_user` and
  `pgd_auth.uid()` is its `sub`.
- **Your servers verify tokens with the JWKS endpoint**; there is no shared
  secret. Rotating the key (Authentication → Signing keys, or
  `pgdock auth rotate-key`) signs new tokens with a new key and keeps the
  old one in the JWKS for a day, so nobody is signed out.
- Refresh tokens rotate on every use. **Presenting a refresh token that was
  already used ends its whole session** (`400 refresh_token_reused`): a
  stolen token is good for one refresh at most, and only until the
  rightful app refreshes. Apps should refresh once at a time.
- Sessions can be limited in length and by inactivity, or to one per user.
  A ban or a deletion takes effect at the next refresh; signing out
  revokes refresh tokens at once, and the access token's lifetime bounds
  the rest.

### Emails

Confirmation, magic-link (with the code), password-reset, invitation and
email-change emails are sent by pgdock-server from the project's
templates (Authentication → Emails: plain text with `{{.Code}}`,
`{{.Link}}`, `{{.Email}}`, `{{.SiteURL}}`, with a preview).

- **Platform email** works out of the box for development, at 30 emails an
  hour per project (`429 over_email_send_rate_limit` past that).
- **Your own SMTP server** (Authentication → Emails) sends from your
  domain without that limit. Its password is encrypted; **Send test**
  checks it before saving.
- One address gets at most one code a minute; sends are counted in the
  usage (`message_sends`) and shown with failures.

A Flutter sample that signs in with a WhatsApp code and Google and reads
org-scoped rows is in [examples/flutter-auth](examples/flutter-auth/).

### Managing users

- **Dashboard:** Project → Authentication lists users (search by email,
  phone or id), shows a user's sessions and recent activity, and invites,
  bans, unbans, confirms, signs out and deletes them. Developers and up.
- **Admin API**, with the **secret key**: `GET|POST /auth/v1/admin/users`,
  `GET|PATCH|DELETE /auth/v1/admin/users/{id}` (`ban_duration: "24h"` or
  `"none"`), `POST /auth/v1/admin/users/{id}/signout`,
  `POST /auth/v1/admin/invite`, `POST /auth/v1/admin/generate-link`
  (a link and code without sending them, for your own emails).
- **CLI:** `pgdock auth users list|show|invite|ban|unban|signout|delete`,
  `pgdock auth config|set|hooks`.

Deleting a user deletes their sessions and sign-in methods; rows of your
tables that reference them follow your foreign keys.

Monthly active users (anyone who signed in or refreshed a token in the
month) are recorded as usage (`auth_mau`) and shown on the Users tab.

## Settings

- **Allowed origins**: the pages that may call the API. Empty allows any
  origin, which is fine for the publishable key; list your app's origins
  once you go live.
- **Statement timeout**: each request's limit, 8 seconds by default and 15
  at most.
- **Rate limits**: requests per minute per IP address (600) and per key
  (12,000). Over the limit, the edge answers `429` with `Retry-After`.
- **Exposed schemas**: the schemas the data API serves (`public`). Exposing
  another grants the request roles use of what the owner makes there.
  Platform schemas (`pgd_*`, `pg_*`, `pgdock`) can't be exposed.
- **Public tables**: tables and views the publishable key may read without
  row-level security. Anyone with your app can read them.
- **Maximum query cost**: the cost guard's limit (above).

A paused Free project answers `503 project_resuming` with `Retry-After` and
is woken, as a database connection would wake it. A suspended
organisation's projects answer `403 project_suspended`.

## Usage and logs

The edge counts each request that passed the key check (`api_requests`)
and the bytes it returned (`api_egress_gb`), per project and hour, and
sends them to pgdock-server every few seconds with the request log. A
report is recorded once even if it is retried. The request log (method,
path, status, latency, role, user, key, IP) is on the API page and at
`GET /api/v1/projects/{id}/services/logs`, kept 7 days.

## Running pgdock-edge

Run one pgdock-edge per region, on the region's nodes. It keeps no state.

| Setting | |
| --- | --- |
| `PGDOCK_API_DOMAIN` (server) | The API domain: projects are at `<ref>.<domain>`. |
| `PGDOCK_EDGE_SECRET` (server and edges) | At least 32 characters, the same everywhere. Without it pgdock-server serves no edge. |
| `PGDOCK_EDGE_CONTROL_URL` | pgdock-server's URL as the edge reaches it. |
| `PGDOCK_EDGE_DOMAIN` | The same as `PGDOCK_API_DOMAIN`. |
| `PGDOCK_EDGE_REGION` | The region it serves (empty for all). |
| `PGDOCK_EDGE_TLS_CERT`, `PGDOCK_EDGE_TLS_KEY` | A wildcard certificate for `*.<domain>`. |
| `PGDOCK_EDGE_LISTEN` | Default `:8443`. |
| `PGDOCK_EDGE_POOLER_ADDR` | Optional: the transaction pooler as the edge reaches it. |
| `PGDOCK_EDGE_TRUSTED_PROXIES` | Optional: CIDRs allowed to set `X-Forwarded-For`. |

`deploy/edge/Dockerfile` builds the image, and `deploy/edge/edge.env.example`
lists the settings.

- **DNS:** a wildcard record `*.<domain>` pointing at the region's edge.
- **TLS:** get the wildcard certificate with a DNS-01 client for your DNS
  provider (certbot, lego, acme.sh) and point the two settings at its
  files. The edge re-reads them when they change. Behind a TLS terminator,
  leave them empty and the edge serves plain HTTP.

The edge follows pgdock-server's configuration feed. A change, such as a
key revoked, an origin added, a project paused or an organisation
suspended, reaches it within a second or two, and it re-reads everything
every minute in case a change was missed. While pgdock-server is down, the
edge keeps serving from what it has and holds its reports, retrying them
later. `GET /healthz` on any host that isn't a project answers once the
first configuration has loaded.

## Platform SMS and WhatsApp (operators)

pgdock-server sends projects' codes when they don't bring their own
provider. Set:

| Variable | |
| --- | --- |
| `PGDOCK_TERMII_API_KEY` (or `_FILE`), `PGDOCK_TERMII_SENDER_ID` | Termii's API key and an approved sender ID. Codes go by Termii's `dnd` channel. `PGDOCK_TERMII_URL` is the account's API base if it isn't `https://api.ng.termii.com`. |
| `PGDOCK_WHATSAPP_OTP_TEMPLATE`, `PGDOCK_WHATSAPP_OTP_LANGUAGE` | An approved **authentication** template (with a copy-code button) on the WhatsApp number support uses (`PGDOCK_WHATSAPP_PHONE_NUMBER_ID`, `PGDOCK_WHATSAPP_ACCESS_TOKEN`); the language defaults to `en`. |
| `PGDOCK_SMS_PRICE_MINOR`, `PGDOCK_WHATSAPP_PRICE_MINOR`, `PGDOCK_MESSAGE_CURRENCY` | What a message costs the project, in minor units (default NGN 4.50 and NGN 15.00), shown as spend; usage is metered as `messages_sms` and `messages_whatsapp`. |
| `PGDOCK_PHONE_AUTH_FREE` | `true` lets Free projects use the platform's providers (by default they bring their own). |

Without them, phone sign-in works only for projects with their own
provider.

## Not yet

These come in the next milestones (V4 §14): storage (M33), realtime (M34)
and read replicas (M35). Auth's leaked-password check and bounce handling
for auth emails are not built yet. Rating the new usage on invoices and per-plan limits come
with billing (M37).
