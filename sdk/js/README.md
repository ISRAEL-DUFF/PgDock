# @pgdock/client

PGDock's client for web, React Native and Node apps. It covers the data
API, auth (email, phone and WhatsApp codes, OAuth, MFA), storage and
realtime.

    npm install @pgdock/client

```ts
import { createClient } from "@pgdock/client";

const pgd = createClient("https://<ref>.api.pgdock.ng", "pgd_pub_…");
await pgd.auth.signInWithOtp({ phone: "+2348012345678", channel: "whatsapp" });
const { data, error } = await pgd.data.from("todos").select("id, title").eq("done", false).limit(20);
```

- Typed rows come from `pgdock gen types --lang ts`.
- Tokens refresh by themselves.
- `live()` keeps a query current.

The full reference is [docs/sdk/javascript.md](../../docs/sdk/javascript.md).
Moving from supabase-js? See
[the mapping](../../docs/supabase-client-mapping.md).

Development: `npm test` runs the unit tests. The live tests run against a
real edge in `TestSDKs` (`make test-integration`).
