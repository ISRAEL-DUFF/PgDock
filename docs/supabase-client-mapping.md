# From supabase-js to @pgdock/client

The [migration helper](migrate-from-supabase.md) moves your data, users
and files. Your client code changes as below.

The shapes are deliberately close: results are `{ data, error }`, and
tokens refresh on their own. The main differences:

- filters and ordering take the operator as a word: `.where("done", "eq", false)`
  or `.eq(…)`, and `.order("col", "desc")`;
- tables are under `pgd.data`, not on the client;
- realtime is `onChange` instead of `on("postgres_changes", …)`.

## Setup

| supabase-js | @pgdock/client |
| --- | --- |
| `createClient(SUPABASE_URL, SUPABASE_ANON_KEY)` | `createClient(PGDOCK_URL, PGDOCK_PUBLISHABLE_KEY)` |
| `createClient<Database>(…)` with `supabase gen types` | `createClient<Database>(…)` with `pgdock gen types --lang ts` |
| service role key on servers | secret key (`pgd_sec_…`) on servers |

## Data

| supabase-js | @pgdock/client |
| --- | --- |
| `supabase.from("todos").select("id, title")` | `pgd.data.from("todos").select("id, title")` |
| `.select("*, list:lists(name)")` | `.select("*, list:lists(name)")` |
| `.eq("done", false)`, `.neq`, `.gt`, `.gte`, `.lt`, `.lte` | the same |
| `.in("id", [1, 2])` | `.in("id", [1, 2])` |
| `.is("deleted_at", null)` | `.is("deleted_at", null)` |
| `.like`, `.ilike` | the same |
| `.contains("tags", ["a"])`, `.containedBy` | `.contains("tags", ["a"])`, `.where("tags", "contained_by", […])` |
| `.textSearch("body", "milk")` | `.search("body", "milk")` |
| `.or("done.eq.true,owner_id.eq.123")` | `.or(cond("done", "eq", true), cond("owner_id", "eq", "123"))` |
| `.not("status", "eq", "draft")` | `.not(cond("status", "eq", "draft"))` |
| `.order("created_at", { ascending: false })` | `.order("created_at", "desc")` |
| `.limit(20)`, `.range(20, 39)` | `.limit(20)`, `.limit(20).offset(20)`, or `.cursor(nextCursor)` |
| `.select("*", { count: "exact" })` | `.select("*").count()` (`count` on the result) |
| `.single()` / `.maybeSingle()` | `.get(id)` by primary key, or `.limit(1)` and `data[0]` |
| `.insert(row).select()` | `.insert(row)` (rows come back by default) |
| `.insert(row, { returning: "minimal" })` | `.insert(row, { returning: "minimal" })` |
| `.upsert(row, { onConflict: "email" })` | `.upsert(row, { onConflict: ["email"] })` |
| `.upsert(row, { ignoreDuplicates: true })` | `.upsert(row, { onConflict: […], ignore: true })` |
| `.update({ done: true }).eq("id", 7)` | the same |
| `.delete().eq("id", 7)` | the same, or `.delete().byKey(7)` |
| `supabase.rpc("fn", args)` | `pgd.data.rpc("fn", args)` (`{ get: true }` for a cacheable GET) |
| `supabase.schema("api").from("items")` | `pgd.data.schema("api").from("items")` or `pgd.data.from("api.items")` |

Updates and deletes need a filter or a key. Without one the API refuses
with `filter_required`.

## Auth

| supabase-js | @pgdock/client |
| --- | --- |
| `auth.signUp({ email, password, options: { data } })` | `auth.signUp({ email, password, data })` |
| `auth.signInWithPassword({ email, password })` | the same (also `{ phone, password }`) |
| `auth.signInWithOtp({ email })` | the same |
| `auth.signInWithOtp({ phone, options: { channel: "whatsapp" } })` | `auth.signInWithOtp({ phone, channel: "whatsapp" })` |
| `auth.verifyOtp({ email, token, type: "email" })` | the same |
| `auth.verifyOtp({ phone, token, type: "sms" })` | the same (`"whatsapp"` too) |
| `auth.signInAnonymously()` | the same |
| `auth.signInWithOAuth({ provider, options: { redirectTo } })` | `auth.signInWithOAuth({ provider, redirectTo })` |
| `auth.exchangeCodeForSession(code)` | the same |
| `auth.getSession()` | `auth.getSession()` (the session itself, not `{ data: { session } }`) |
| `auth.getUser()` | `auth.getUser()` (`data` is the user) |
| `auth.updateUser({ password, data })` | the same |
| `auth.resetPasswordForEmail(email, { redirectTo })` | the same |
| `auth.onAuthStateChange((event, session) => …)` | the same; it returns the unsubscribe function |
| `auth.signOut({ scope })` | the same |
| `auth.mfa.enroll/challenge/verify/unenroll` | the same, with `{ factorType: "totp" }` |
| `auth.admin.*` (service role) | the admin API with the secret key ([docs](backend-services.md#managing-users)) |

In SQL, policies use `pgd_auth.uid()`, and the roles are `<db>_anon`,
`<db>_user` and `<db>_service`. The
[policies step](migrate-from-supabase.md#3-policies) rewrites imported
policies for you.

## Storage

| supabase-js | @pgdock/client |
| --- | --- |
| `storage.from("avatars")` | `storage.bucket("avatars")` |
| `.upload(path, file, { contentType, upsert })` | the same |
| `.download(path)` | the same (`data` is a Blob) |
| `.download(path, { transform })` | the same |
| `.getPublicUrl(path).data.publicUrl` | `.publicUrl(path)` (a string) |
| `.createSignedUrl(path, 60)` | `.signedUrl(path, { expiresIn: 60 })` |
| `.createSignedUrls(paths, 60)` | `.signedUrls(paths, { expiresIn: 60 })` |
| `.createSignedUploadUrl(path)` | `.signedUploadUrl(path)` |
| `.list(prefix, { limit })` | `.list({ prefix, limit, cursor })` |
| `.remove([path])` | the same |
| `.move(from, to)`, `.copy(from, to)` | the same |
| resumable (TUS) uploads | `.uploadLarge(path, file, { onProgress })` (presigned parts) |
| `storage.createBucket(id, { public })` | `storage.createBucket(id, { public, allowedMimeTypes })` |

In policies, `storage.objects` becomes `pgd_storage.objects`. Its columns
`bucket_id` and `name` are `bucket` and `path`, and `storage.foldername()`
is `pgd_storage.foldername()`. The storage step rewrites these when it
copies the policies.

## Realtime

| supabase-js | @pgdock/client |
| --- | --- |
| `supabase.channel("x")` | `pgd.realtime.channel("x")` |
| `.on("postgres_changes", { event, schema, table, filter }, cb)` | `.onChange({ event, schema, table, filter }, cb)` |
| `.on("broadcast", { event }, cb)` | `.onBroadcast(event, cb)` |
| `.on("presence", { event: "sync" }, cb)` | `.onPresenceSync(cb)`, `.onPresenceJoin`, `.onPresenceLeave` |
| `.subscribe(status => …)` | the same |
| `channel.send({ type: "broadcast", event, payload })` | `channel.send(event, payload)` |
| `channel.track(state)`, `untrack()` | the same |
| `supabase.removeChannel(ch)` | `ch.unsubscribe()` |
| `channel("x", { config: { private: true } })` | `channel("x", { private: true })` |

There are two additions with no supabase-js equivalent:

- `onResync(cb)` is called when changes may have been missed, either
  because the server dropped them or because the client reconnected.
- `data.from(…).select(…).live(cb)` keeps a query's result current using
  both.

Realtime still speaks Supabase's protocol, so an unchanged supabase-js
realtime client also connects. Moving to `@pgdock/client` gets you token
refresh and the live queries.

## Errors

| supabase-js | @pgdock/client |
| --- | --- |
| `error.message`, `error.code` (PostgREST codes like `PGRST116`) | `error.message`, `error.code` (`not_found`, `rls_required`, `unique_violation`, …), `error.status`, `error.requestId` |
| `AuthApiError` | `PgdockError` with an auth code (`invalid_credentials`, `otp_expired`, `user_locked`, …) |
