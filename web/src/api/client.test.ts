import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiRequestError, api, getVersion, request, setCsrfToken } from "./client";

function fakeFetch(status: number, body: unknown, seen?: RequestInit[]): typeof fetch {
  return (async (_url: string, init?: RequestInit) => {
    seen?.push(init ?? {});
    return new Response(status === 204 ? null : JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    });
  }) as typeof fetch;
}

describe("api client", () => {
  beforeEach(() => setCsrfToken("tok"));

  it("returns the parsed version", async () => {
    const v = { version: "1.0.0", commit: "abc", build_date: "now", go_version: "go1.25" };
    await expect(getVersion(fakeFetch(200, v))).resolves.toEqual(v);
  });

  it("accepts a successful response with an empty body", async () => {
    const empty = (status: number): typeof fetch => (async () => new Response(null, { status })) as typeof fetch;
    await expect(request("POST", "/api/v1/auth/signup", {}, empty(202))).resolves.toBeUndefined();
    await expect(request("DELETE", "/x", undefined, empty(204))).resolves.toBeUndefined();
    await expect(request("POST", "/x", {}, empty(200))).resolves.toBeUndefined();
  });

  it("throws ApiRequestError with the server code and message", async () => {
    const err = await request<never>("DELETE", "/x", undefined, fakeFetch(403, { code: "reauth_required", message: "confirm" })).catch(
      (e: unknown) => e as ApiRequestError,
    );
    expect(err).toBeInstanceOf(ApiRequestError);
    expect(err.status).toBe(403);
    expect(err.code).toBe("reauth_required");
    expect(err.message).toBe("confirm");
  });

  it("sends the CSRF token on mutations only", async () => {
    setCsrfToken("tok");
    const seen: RequestInit[] = [];
    await request("POST", "/x", { a: 1 }, fakeFetch(200, {}, seen));
    await request("GET", "/x", undefined, fakeFetch(200, {}, seen));
    expect((seen[0].headers as Record<string, string>)["X-CSRF-Token"]).toBe("tok");
    expect((seen[1].headers as Record<string, string>)["X-CSRF-Token"]).toBeUndefined();
    expect(seen[0].body).toBe('{"a":1}');
  });

  it("fetches the CSRF token before a first mutation", async () => {
    setCsrfToken("");
    const urls: string[] = [];
    const f = (async (url: string, init?: RequestInit) => {
      urls.push(`${init?.method} ${url} ${(init?.headers as Record<string, string>)["X-CSRF-Token"] ?? ""}`);
      return new Response(JSON.stringify({ csrf_token: "fresh" }), { status: 200 });
    }) as typeof fetch;
    await request("POST", "/x", {}, f);
    await request("POST", "/y", {}, f);
    expect(urls).toEqual(["GET /api/v1/session ", "POST /x fresh", "POST /y fresh"]);
  });

  it("handles 204 responses", async () => {
    await expect(request("POST", "/x", undefined, fakeFetch(204, null))).resolves.toBeUndefined();
  });
});

describe("wallet endpoints", () => {
  beforeEach(() => setCsrfToken("tok"));
  afterEach(() => vi.unstubAllGlobals());

  function stub(status: number, body: unknown) {
    const calls: { url: string; method?: string; body?: unknown }[] = [];
    vi.stubGlobal("fetch", async (url: string, init?: RequestInit) => {
      calls.push({ url, method: init?.method, body: init?.body ? JSON.parse(String(init.body)) : undefined });
      return new Response(status === 204 ? null : JSON.stringify(body), { status });
    });
    return calls;
  }

  it("reads the wallet", async () => {
    const calls = stub(200, { available: true });
    await expect(api.wallet("o1")).resolves.toEqual({ available: true });
    expect(calls[0].url).toBe("/api/v1/orgs/o1/billing/wallet");
  });

  it("opens a wallet with the setup details", async () => {
    const calls = stub(200, { available: true, wallet_id: "w1" });
    const b = { business_name: "Acme", email: "a@acme.test", bvn: "12345678901" };
    await api.openWallet("o1", b);
    expect(calls[0]).toMatchObject({ url: "/api/v1/orgs/o1/billing/wallet", method: "POST", body: b });
  });

  it("accepts and withdraws debit consent", async () => {
    const calls = stub(200, { at: "now", version: "v1", current: true });
    await api.allowDebits("o1", "v1");
    expect(calls[0]).toMatchObject({ url: "/api/v1/orgs/o1/billing/debit-consent", method: "POST", body: { terms_version: "v1" } });
    const del = stub(204, null);
    await expect(api.withdrawDebitConsent("o1")).resolves.toBeUndefined();
    expect(del[0]).toMatchObject({ url: "/api/v1/orgs/o1/billing/debit-consent", method: "DELETE" });
  });

  it("pays an invoice from the wallet", async () => {
    const calls = stub(200, { succeeded: true, skipped: false, unavailable: false, paid_minor: 100 });
    await expect(api.payFromWallet("o1", "i1")).resolves.toMatchObject({ succeeded: true, paid_minor: 100 });
    expect(calls[0]).toMatchObject({ url: "/api/v1/orgs/o1/billing/invoices/i1/pay-from-wallet", method: "POST" });
  });
});
