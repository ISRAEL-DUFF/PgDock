// The live test TestSDKs runs against a real edge (see
// test/integration/sdk_test.go for the project it sets up).
import assert from "node:assert/strict";
import { createClient, memoryStorage } from "../dist/index.js";

const url = process.env.PGDOCK_SDK_URL;
const pub = process.env.PGDOCK_SDK_PUB;
const password = process.env.PGDOCK_SDK_PASSWORD;
if (!url) {
  console.log("PGDOCK_SDK_URL not set; skipping");
  process.exit(0);
}
const sameOrigin = (u) => {
  const x = new URL(u);
  const base = new URL(url);
  x.protocol = base.protocol;
  x.host = base.host;
  return x.toString();
};
const until = async (what, ok, ms = 10000) => {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    if (await ok()) return;
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error("timed out waiting for " + what);
};

const pgd = createClient(url, pub, { auth: { storage: memoryStorage() } });

// Anonymous first.
let h = await pgd.data.health();
assert.equal(h.error, null);
assert.equal(h.data.role, "anon");

// Sign in.
const s = await pgd.auth.signInWithPassword({ email: "js@sdk.test", password });
assert.equal(s.error, null, s.error?.message);
const uid = s.data.user.id;
h = await pgd.data.health();
assert.equal(h.data.role, "user");

// Realtime: an insert reaches the channel.
const changes = [];
let status = "";
const ch = pgd.realtime
  .channel("notes-js")
  .onChange({ event: "INSERT", table: "notes" }, (c) => changes.push(c))
  .subscribe((st, err) => {
    status = st;
    if (err) console.error(err);
  });
await until("SUBSCRIBED", () => status === "SUBSCRIBED");

// A live query follows the table.
const live = [];
const stop = pgd.data.from("notes").select("body").order("id").live((r) => live.push(r));
await until("the live query's first result", () => live.length > 0);
assert.deepEqual(live.at(-1).data, []);

// Writes, with the owner from the rewritten default.
const ins = await pgd.data.from("notes").insert({ body: "js 1" });
assert.equal(ins.error, null, ins.error?.message);
assert.equal(ins.data[0].owner_id, uid);
await until("the INSERT change", () => changes.some((c) => c.record.body === "js 1"));
await until("the live query to update", () => live.at(-1).data?.length === 1);

// Reads under RLS: only our rows.
const all = await pgd.data.from("notes").select("id,body").count();
assert.equal(all.error, null);
assert.deepEqual(all.data.map((r) => r.body), ["js 1"]);
assert.equal(all.count, 1);
const none = await pgd.data.from("notes").select().eq("body", "not yours");
assert.deepEqual(none.data, []);
const upd = await pgd.data.from("notes").update({ body: "js 1b" }).eq("body", "js 1");
assert.equal(upd.affected, 1);

// A refresh gives new tokens that work.
const before = (await pgd.auth.getSession()).access_token;
const r = await pgd.auth.refreshSession();
assert.equal(r.error, null, r.error?.message);
assert.notEqual(r.data.access_token, before);
assert.equal((await pgd.data.from("notes").select()).data.length, 1);

// Files.
const bucket = pgd.storage.bucket("files");
const up = await bucket.upload(`${uid}/hello.txt`, "hello from js", { contentType: "text/plain" });
assert.equal(up.error, null, up.error?.message);
const down = await bucket.download(`${uid}/hello.txt`);
assert.equal(await down.data.text(), "hello from js");
const listed = await bucket.list({ prefix: `${uid}/` });
assert.deepEqual(listed.data.items.map((i) => i.name), ["hello.txt"]);
const signed = await bucket.signedUrl(`${uid}/hello.txt`, { expiresIn: 60 });
assert.equal(signed.error, null);
assert.equal(await (await fetch(sameOrigin(signed.data))).text(), "hello from js");
const denied = await bucket.upload(`someone-else/x.txt`, "no", { contentType: "text/plain" });
assert.equal(denied.error?.status, 403);

// Errors.
const bad = await pgd.data.from("nope").select();
assert.equal(bad.error?.status, 404);
assert.equal(bad.error?.code, "unknown_table");

stop();
await ch.unsubscribe();
await pgd.auth.signOut();
assert.equal(await pgd.auth.getSession(), null);
assert.equal((await pgd.data.health()).data.role, "anon");
pgd.realtime.close();
console.log("js: ok");
process.exit(0);
