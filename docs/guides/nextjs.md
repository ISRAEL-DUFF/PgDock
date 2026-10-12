# Next.js

This guide builds a small notes app on Next.js's App Router:

- users sign in with an email code;
- server components read each user's notes under row-level security;
- a client component keeps the list live.

## 1. The project

In the dashboard:

1. Create a project.
2. Turn on backend services (Project → API).
3. Copy the URL and the publishable key.

Then, in the SQL editor:

```sql
CREATE TABLE notes (
  id bigserial PRIMARY KEY,
  owner_id uuid NOT NULL DEFAULT pgd_auth.uid() REFERENCES pgd_auth.users (id) ON DELETE CASCADE,
  body text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE notes ENABLE ROW LEVEL SECURITY;
CREATE POLICY own ON notes FOR ALL TO "<db>_user"
  USING (owner_id = pgd_auth.uid()) WITH CHECK (owner_id = pgd_auth.uid());
SELECT pgd_realtime.enable('notes');
```

`<db>_user` is your project's user role. Project → API shows its name.

Under Authentication → URL configuration, add `http://localhost:3000` to
the redirect URLs.

## 2. Install

    npm install @pgdock/client
    pgdock gen types --lang ts --project my-app > lib/database.types.ts

`.env.local`:

    NEXT_PUBLIC_PGDOCK_URL=https://k7f3m2q9.api.pgdock.ng
    NEXT_PUBLIC_PGDOCK_KEY=pgd_pub_…

## 3. Clients

The browser client keeps the session in `localStorage` and refreshes it
by itself. The server needs the user's access token too, so the browser
copies it into a cookie whenever it changes.

```ts
// lib/pgdock-browser.ts
"use client";
import { createClient } from "@pgdock/client";
import type { Database } from "./database.types";

export const pgd = createClient<Database>(process.env.NEXT_PUBLIC_PGDOCK_URL!, process.env.NEXT_PUBLIC_PGDOCK_KEY!);

pgd.auth.onAuthStateChange((_event, session) => {
  document.cookie = session
    ? `pgd-access=${session.access_token}; path=/; max-age=${session.expires_in}; samesite=lax; secure`
    : "pgd-access=; path=/; max-age=0";
});
```

```ts
// lib/pgdock-server.ts
import { createClient } from "@pgdock/client";
import { cookies } from "next/headers";
import type { Database } from "./database.types";

/** A client acting as the signed-in user of this request (or anon). */
export async function serverClient() {
  const jar = await cookies();
  return createClient<Database>(process.env.NEXT_PUBLIC_PGDOCK_URL!, process.env.NEXT_PUBLIC_PGDOCK_KEY!, {
    accessToken: () => jar.get("pgd-access")?.value,
  });
}
```

You don't need to verify the token in Next.js: the API checks it on every
request, and row-level security decides what it may read. A server that
makes decisions from the token itself, such as checking roles, should
verify it against the project's JWKS. The Go SDK's `Auth.VerifyToken`
does that, and so does any JOSE library.

## 4. Sign in with a code

```tsx
// app/login/page.tsx
"use client";
import { useState } from "react";
import { useRouter } from "next/navigation";
import { pgd } from "@/lib/pgdock-browser";

export default function Login() {
  const [email, setEmail] = useState("");
  const [code, setCode] = useState("");
  const [sent, setSent] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const router = useRouter();

  async function send() {
    const { error } = await pgd.auth.signInWithOtp({ email });
    error ? setError(error.message) : setSent(true);
  }
  async function verify() {
    const { error } = await pgd.auth.verifyOtp({ type: "email", email, token: code });
    if (error) return setError(error.message);
    router.push("/");
    router.refresh();
  }

  return sent ? (
    <form action={verify}>
      <input value={code} onChange={(e) => setCode(e.target.value)} placeholder="6-digit code" />
      <button>Sign in</button> {error}
    </form>
  ) : (
    <form action={send}>
      <input value={email} onChange={(e) => setEmail(e.target.value)} type="email" />
      <button>Email me a code</button> {error}
    </form>
  );
}
```

## 5. Read on the server, update live on the client

```tsx
// app/page.tsx
import { redirect } from "next/navigation";
import { serverClient } from "@/lib/pgdock-server";
import { Notes } from "./notes";

export default async function Home() {
  const pgd = await serverClient();
  const { data, error } = await pgd.data.from("notes").select("id, body").order("created_at", "desc");
  if (error?.status === 401 || (await pgd.data.health()).data?.role !== "user") redirect("/login");
  return <Notes initial={data ?? []} />;
}
```

```tsx
// app/notes.tsx
"use client";
import { useEffect, useState } from "react";
import { pgd } from "@/lib/pgdock-browser";
import type { Tables } from "@/lib/database.types";

type Note = Pick<Tables<"notes">, "id" | "body">;

export function Notes({ initial }: { initial: Note[] }) {
  const [notes, setNotes] = useState(initial);
  const [body, setBody] = useState("");
  useEffect(() => pgd.data.from("notes").select("id, body").order("created_at", "desc").live(({ data }) => data && setNotes(data)), []);

  return (
    <>
      <form action={async () => { await pgd.data.from("notes").insert({ body }); setBody(""); }}>
        <input value={body} onChange={(e) => setBody(e.target.value)} />
        <button>Add</button>
      </form>
      <ul>{notes.map((n) => <li key={n.id}>{n.body}</li>)}</ul>
    </>
  );
}
```

How the pieces fit:

- The insert leaves out `owner_id`. Its default, `pgd_auth.uid()`, fills
  it in, and the policy's `WITH CHECK` makes sure nobody writes rows for
  someone else.
- `live()` refetches whenever the table changes, the server asks for a
  resync, or the socket reconnects. Every tab stays current with no cache
  logic of your own.
- The server render shows the notes before any JavaScript runs.

## 6. Sign out

```tsx
<button onClick={async () => { await pgd.auth.signOut(); location.assign("/login"); }}>Sign out</button>
```

## OAuth

Instead of the code form, you can sign in with a provider:

1. Turn the provider on in Authentication → Providers.
2. Call `pgd.auth.signInWithOAuth({ provider: "google", redirectTo: location.origin + "/" })`.
3. The client finishes the PKCE exchange itself when the user comes back
   with `?code=`.

## Deploying

- Set the two `NEXT_PUBLIC_` variables in your host.
- Add your production URL to the redirect URLs.
- If the browser calls the API from your domain, add the domain to Project
  → API → CORS origins.
