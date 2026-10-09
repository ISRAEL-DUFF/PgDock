# JavaScript SDK

`@pgdock/client` works in browsers, React Native and Node 18+ (realtime
needs a `WebSocket`, built into browsers, React Native and Node 22). It has
no dependencies.

    npm install @pgdock/client

```ts
import { createClient } from "@pgdock/client";
import type { Database } from "./database.types"; // pgdock gen types --lang ts

export const pgd = createClient<Database>("https://k7f3m2q9.api.pgdock.ng", "pgd_pub_…");
```

The URL and keys are on Project → API. Use the **publishable key** in apps.
Use the **secret key** only on servers: it skips row-level security.

## Results and errors

Calls never throw for API errors. Each one resolves to `{ data, error }`:

- `data` is the result, or `null` when the call failed.
- `error` is a `PgdockError` with `status`, `code` (such as
  `rls_required` or `invalid_credentials`), `message` and `requestId`.

A network failure is `status: 0` with `code: "network_error"`.

```ts
const { data, error } = await pgd.data.from("todos").select();
if (error?.code === "rls_required") …
```

## Data

```ts
// Read
const { data, count, nextCursor } = await pgd.data
  .from("todos")
  .select("id, title, done, list:lists(name)") // related rows through foreign keys
  .eq("done", false)
  .in("priority", [1, 2])
  .or(cond("owner_id", "eq", me), cond("shared", "is", true))
  .order("created_at", "desc")
  .limit(20)
  .count();
const next = await pgd.data.from("todos").select().cursor(nextCursor);
const one = await pgd.data.from("todos").get(42);

// Write
await pgd.data.from("todos").insert({ title: "Buy milk" });
await pgd.data.from("todos").update({ done: true }).eq("id", 42);
await pgd.data.from("todos").delete().byKey(42);
await pgd.data.from("stock").upsert({ item: "tea", n: 9 }, { onConflict: ["item"] });

// Functions and transactions
await pgd.data.rpc("close_cart", { cart_id: 4 });
await pgd.data.batch([
  { op: "insert", table: "orders", rows: [{ item: "tea" }] },
  { op: "update", table: "carts", key: 4, set: { closed: true } },
]);
```

**Filters.** `where(col, op, value)` takes the operators `eq`, `neq`, `lt`,
`lte`, `gt`, `gte`, `in`, `like`, `ilike`, `is`, `contains`,
`contained_by` and `search`. The common ones have shortcuts (`eq`, `in`,
`is`, …). Conditions all hold together; `or(…)` adds a group of which any
may hold, and `not(…)` negates one. The client uses the API's URL form
when it can, so the request is a cacheable `GET` that a read replica may
serve. Use `.replica()` to allow that. Otherwise it uses the JSON query
form.

**Types.** With the generated `Database` type, `from("todos")` returns
rows typed as `Tables<"todos">`, and `insert` and `update` check their
values. Write `from("api.items")` for a table in another exposed schema.

### Live queries

`live()` keeps a query's result current. It runs the query, then runs it
again whenever realtime reports a change to the table, after the server
asks for a resync, and after a reconnect. Turn realtime on for the table
first (Project → Realtime).

```ts
const stop = pgd.data.from("todos").select("id, title").eq("done", false).live(({ data }) => render(data));
// later: stop()
```

## Auth

```ts
await pgd.auth.signUp({ email, password, data: { name: "Ada" } }); // session is null until confirmed
await pgd.auth.signInWithPassword({ email, password });             // or { phone, password }
await pgd.auth.signInWithOtp({ phone: "08031234567", channel: "whatsapp" }); // or { email }
await pgd.auth.verifyOtp({ type: "whatsapp", phone: "+2348031234567", token: "123456" });
await pgd.auth.signInAnonymously();
await pgd.auth.signInWithOAuth({ provider: "google", redirectTo: "https://app.example.com/auth/callback" });
await pgd.auth.signOut();

const session = await pgd.auth.getSession();
const { data: user } = await pgd.auth.getUser();
await pgd.auth.updateUser({ data: { name: "Ada L." } });
pgd.auth.onAuthStateChange((event, session) => …); // SIGNED_IN, TOKEN_REFRESHED, SIGNED_OUT, …
```

- **Sessions** are kept in `localStorage` in browsers, and in memory
  elsewhere. Pass `auth: { storage }` to choose: AsyncStorage in React
  Native, for example.
- **Tokens refresh by themselves** a minute before they expire, and before
  any request that would carry an expired one. Only one refresh runs at a
  time. That matters because a refresh token works once: presenting it
  twice ends the session.
- **OAuth uses PKCE.** In a browser, `signInWithOAuth` navigates to the
  provider. When the user returns to your page with `?code=`, the client
  finishes the sign-in itself. Elsewhere, pass `skipRedirect: true`, open
  the returned URL, and call `exchangeCodeForSession(code)` yourself.
- **MFA:** `auth.mfa.enroll`, `challenge`, `verify` and `unenroll`.

### On a server

On a server, act as the user who made the request by passing their access
token. The client then keeps no session:

```ts
const pgd = createClient(url, publishableKey, { accessToken: () => cookies().get("pgd-access")?.value });
```

## Storage

```ts
const avatars = pgd.storage.bucket("avatars");
await avatars.upload(`${user.id}/me.png`, file, { contentType: "image/png", upsert: true });
const { data: blob } = await avatars.download(`${user.id}/me.png`);
const { data: url } = await avatars.signedUrl(`${user.id}/me.png`, { expiresIn: 3600, transform: { width: 200 } });
avatars.publicUrl("logo.png"); // a public bucket's file
const { data: page } = await avatars.list({ prefix: `${user.id}/` });
await avatars.remove([`${user.id}/old.png`]);
await avatars.uploadLarge(`${user.id}/video.mp4`, file, { onProgress: (sent, total) => … }); // over 50 MB, in parts
```

Buckets are created with the secret key: `storage.createBucket("avatars",
{ allowedMimeTypes: ["image/*"] })`, or from the dashboard.

## Realtime

```ts
const channel = pgd.realtime
  .channel("room-1")
  .onChange({ event: "INSERT", table: "messages", filter: "room_id=eq.1" }, (c) => add(c.record))
  .onBroadcast("typing", (payload) => showTyping(payload))
  .onPresenceSync((state) => showOnline(state))
  .onResync(() => refetch())
  .subscribe((status, err) => …);

await channel.send("typing", { user: me });
await channel.track({ user: me, at: Date.now() });
await channel.unsubscribe();
```

- **One socket.** The client opens one WebSocket for all its channels and
  reconnects with backoff.
- **Rejoins.** After a reconnect it rejoins every channel and calls each
  one's `onResync`, because changes made while it was away were missed.
- **New tokens.** When the access token is refreshed, joined channels are
  sent the new one.

## Type generation

    pgdock gen types --lang ts --project my-app > src/database.types.ts

Pass the generated type to `createClient<Database>(…)`.
