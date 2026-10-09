import { describe, expect, it } from "vitest";
import { cond, createClient, encodeCondition, memoryStorage } from "../src/index.js";

type Call = { url: URL; method: string; headers: Record<string, string>; body?: string };

function fakeFetch(handler: (c: Call) => { status?: number; body?: unknown; headers?: Record<string, string> }) {
  const calls: Call[] = [];
  const f = async (input: RequestInfo | URL, init?: RequestInit) => {
    const c: Call = {
      url: new URL(String(input)),
      method: init?.method ?? "GET",
      headers: Object.fromEntries(Object.entries((init?.headers as Record<string, string>) ?? {}).map(([k, v]) => [k.toLowerCase(), v])),
      body: typeof init?.body === "string" ? init.body : undefined,
    };
    calls.push(c);
    const r = handler(c);
    return new Response(r.body === undefined ? null : JSON.stringify(r.body), {
      status: r.status ?? 200,
      headers: { "Content-Type": "application/json", ...r.headers },
    });
  };
  return { f: f as typeof fetch, calls };
}

const URL_ = "https://k7f3m2q9.api.pgdock.test";

function jwt(exp: number) {
  const b = (o: object) => btoa(JSON.stringify(o)).replace(/=+$/, "");
  return `${b({ alg: "ES256" })}.${b({ sub: "u1", exp })}.sig`;
}

describe("filters", () => {
  it("encodes conditions the way the edge parses them", () => {
    expect(encodeCondition("done", "eq", false)).toBe("done:eq:false");
    expect(encodeCondition("id", "in", [1, 2, "a,b", 'q"x'])).toBe('id:in:1,2,"a,b","q\\"x"');
    expect(encodeCondition("deleted_at", "is", null)).toBe("deleted_at:is:null");
    expect(encodeCondition("at", "gte", new Date("2026-01-02T03:04:05Z"))).toBe("at:gte:2026-01-02T03:04:05.000Z");
    expect(encodeCondition("tags", "contains", ["a"])).toBe('tags:contains:["a"]');
  });

  it("reads with GET, and with the JSON query form for not()", async () => {
    const { f, calls } = fakeFetch(() => ({ body: { data: [{ id: 1 }], next_cursor: "c2", count: 7 } }));
    const pgd = createClient(URL_, "pgd_pub_x", { fetch: f, auth: { storage: memoryStorage() } });
    const r = await pgd.data.from("todos").select("id,title").eq("done", false).or(cond("a", "eq", 1), cond("b", "eq", 2)).order("created_at", "desc").limit(20).count();
    expect(r.error).toBeNull();
    expect(r.data).toEqual([{ id: 1 }]);
    expect(r.nextCursor).toBe("c2");
    expect(r.count).toBe(7);
    const c = calls[0]!;
    expect(c.method).toBe("GET");
    expect(c.url.pathname).toBe("/data/v1/todos");
    expect(c.url.searchParams.getAll("where")).toEqual(["done:eq:false"]);
    expect(c.url.searchParams.get("or")).toBe("a:eq:1,b:eq:2");
    expect(c.url.searchParams.get("order")).toBe("created_at:desc");
    expect(c.headers["apikey"]).toBe("pgd_pub_x");

    await pgd.data.from("api.items").select().not(cond("x", "is", null)).replica();
    const q = calls[1]!;
    expect(q.method).toBe("POST");
    expect(q.url.pathname).toBe("/data/v1/api.items/query");
    expect(JSON.parse(q.body!).where).toEqual({ and: [{ not: { column: "x", op: "is", value: null } }] });
    expect(q.headers["read-replica"]).toBe("allowed");
  });

  it("writes with filters, keys and upserts", async () => {
    const { f, calls } = fakeFetch(() => ({ body: { affected: 1, data: [{ id: 7 }] } }));
    const pgd = createClient(URL_, "pgd_pub_x", { fetch: f, auth: { storage: memoryStorage() } });
    const w = await pgd.data.from("todos").update({ done: true }).eq("id", 7).atMost(1);
    expect(w.affected).toBe(1);
    expect(calls[0]!.method).toBe("PATCH");
    expect(calls[0]!.url.searchParams.get("max_affected")).toBe("1");
    await pgd.data.from("todos").delete().byKey(7);
    expect(calls[1]!.url.pathname).toBe("/data/v1/todos/7");
    await pgd.data.from("stock").upsert({ item: "tea" }, { onConflict: ["item"], ignore: true });
    expect(calls[2]!.url.searchParams.get("on_conflict")).toBe("item");
    expect(calls[2]!.url.searchParams.get("resolution")).toBe("ignore");
  });

  it("returns the API's errors as PgdockError", async () => {
    const { f } = fakeFetch(() => ({ status: 403, body: { error: { code: "rls_required", message: "enable it", request_id: "req_1" } } }));
    const pgd = createClient(URL_, "pgd_pub_x", { fetch: f, auth: { storage: memoryStorage() } });
    const r = await pgd.data.from("todos").select();
    expect(r.data).toBeNull();
    expect(r.error?.status).toBe(403);
    expect(r.error?.code).toBe("rls_required");
    expect(r.error?.requestId).toBe("req_1");
  });
});

describe("auth", () => {
  it("refreshes once at a time, and sends the new token", async () => {
    let refreshes = 0;
    const now = Math.floor(Date.now() / 1000);
    const { f, calls } = fakeFetch((c) => {
      if (c.url.pathname === "/auth/v1/signin/password")
        return { body: { access_token: jwt(now + 30), refresh_token: "r1", token_type: "bearer", expires_in: 30, expires_at: now + 30, user: { id: "u1" } } };
      if (c.url.pathname === "/auth/v1/token") {
        refreshes++;
        return { body: { access_token: "fresh", refresh_token: "r2", token_type: "bearer", expires_in: 3600, expires_at: now + 3600, user: { id: "u1" } } };
      }
      return { body: { data: [] } };
    });
    const pgd = createClient(URL_, "pgd_pub_x", { fetch: f, auth: { storage: memoryStorage(), autoRefresh: false } });
    const s = await pgd.auth.signInWithPassword({ email: "a@b.c", password: "pw" });
    expect(s.error).toBeNull();
    // The token expires within the margin: both reads refresh it first, once.
    await Promise.all([pgd.data.from("a").select(), pgd.data.from("b").select()]);
    expect(refreshes).toBe(1);
    const reads = calls.filter((c) => c.url.pathname.startsWith("/data/"));
    expect(reads.map((c) => c.headers["authorization"])).toEqual(["Bearer fresh", "Bearer fresh"]);
    expect(JSON.parse(calls.find((c) => c.url.pathname === "/auth/v1/token")!.body!)).toEqual({ refresh_token: "r1" });
  });

  it("keeps the session in its storage and signs out on a refused refresh", async () => {
    const store = memoryStorage();
    const now = Math.floor(Date.now() / 1000);
    await store.setItem(
      "pgdock.auth.k7f3m2q9.api.pgdock.test",
      JSON.stringify({ access_token: "old", refresh_token: "used", token_type: "bearer", expires_in: 1, expires_at: now - 10, user: { id: "u1" } }),
    );
    const { f } = fakeFetch((c) =>
      c.url.pathname === "/auth/v1/token" ? { status: 400, body: { error: { code: "refresh_token_reused", message: "reused" } } } : { body: {} },
    );
    const pgd = createClient(URL_, "pgd_pub_x", { fetch: f, auth: { storage: store } });
    const events: string[] = [];
    pgd.auth.onAuthStateChange((e) => events.push(e));
    expect(await pgd.auth.getSession()).toBeNull();
    expect(await store.getItem("pgdock.auth.k7f3m2q9.api.pgdock.test")).toBeNull();
    expect(events).toContain("SIGNED_OUT");
  });

  it("builds a PKCE authorize URL", async () => {
    const { f } = fakeFetch(() => ({ body: {} }));
    const pgd = createClient(URL_, "pgd_pub_x", { fetch: f, auth: { storage: memoryStorage() } });
    const r = await pgd.auth.signInWithOAuth({ provider: "google", redirectTo: "https://app.test/cb", skipRedirect: true });
    const u = new URL(r.data!.url);
    expect(u.pathname).toBe("/auth/v1/authorize");
    expect(u.searchParams.get("provider")).toBe("google");
    expect(u.searchParams.get("code_challenge_method")).toBe("s256");
    expect(u.searchParams.get("code_challenge")).toMatch(/^[A-Za-z0-9_-]{43}$/);
  });
});

describe("storage", () => {
  it("escapes paths and builds public URLs", async () => {
    const { f, calls } = fakeFetch(() => ({ body: { id: "1", bucket: "avatars", path: "u 1/me.png" } }));
    const pgd = createClient(URL_, "pgd_pub_x", { fetch: f, auth: { storage: memoryStorage() } });
    await pgd.storage.bucket("avatars").upload("u 1/me.png", new Uint8Array([1, 2]), { contentType: "image/png", upsert: true });
    expect(calls[0]!.url.pathname).toBe("/storage/v1/object/avatars/u%201/me.png");
    expect(calls[0]!.headers["x-upsert"]).toBe("true");
    expect(calls[0]!.headers["content-type"]).toBe("image/png");
    expect(pgd.storage.bucket("docs").publicUrl("a/b.txt")).toBe(URL_ + "/storage/v1/public/docs/a/b.txt");
    expect(pgd.storage.bucket("img").publicUrl("x.png", { transform: { width: 100 } })).toBe(URL_ + "/storage/v1/render/img/x.png?width=100");
  });
});
