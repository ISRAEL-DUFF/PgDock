# Xenibase documentation

Xenibase is managed Postgres with backend services: a data API, auth (email,
phone and WhatsApp codes, OAuth, MFA), file storage and realtime, each
project in its own database.

## Build an app

1. Create a project (Projects → New project), or import an existing
   database ([from Supabase](migrate-from-supabase.md), or
   [from a Docker VPS](guides/import-docker-vps-db.md)).
2. Turn on backend services (Project → API). You get a publishable key for
   apps and a secret key for servers.
3. Write row-level security policies for your tables
   ([how](backend-services.md#row-level-security-comes-first)).
4. Use an SDK:

| | Install | Reference |
| --- | --- | --- |
| TypeScript (web, React Native, Node) | `npm install @xenibase/client` | [JavaScript SDK](sdk/javascript.md) |
| Dart / Flutter | `dart pub add xenibase` | [Dart SDK](sdk/dart.md) |
| Go (servers) | `go get github.com/israel-duff/xenibase/sdk/go` | [Go SDK](sdk/go.md) |

```ts
import { createClient } from "@xenibase/client";

const xb = createClient("https://k7f3m2q9.api.xenibase.ng", "xb_pub_…");
await xb.auth.signInWithOtp({ phone: "+2348012345678", channel: "whatsapp" });
await xb.auth.verifyOtp({ type: "whatsapp", phone: "+2348012345678", token: "123456" });

const { data } = await xb.data.from("todos").select("id, title, done").eq("done", false).order("created_at", "desc").limit(20);
xb.realtime.channel("todos").onChange({ table: "todos" }, (change) => refresh(change)).subscribe();
```

The guides walk through whole apps: [Next.js](guides/nextjs.md),
[React Native](guides/react-native.md) and [Flutter](guides/flutter.md).

## Run Xenibase

[Install it on a VPS](install.md), then see [operating](operations.md),
[upgrading](upgrade.md) and [disaster recovery](disaster-recovery.md).
