import { beforeEach, describe, expect, it } from "vitest";
import { ApiRequestError, getVersion, request, setCsrfToken } from "./client";

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
