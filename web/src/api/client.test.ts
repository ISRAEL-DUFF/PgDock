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
  beforeEach(() => setCsrfToken(""));

  it("returns the parsed version", async () => {
    const v = { version: "1.0.0", commit: "abc", build_date: "now", go_version: "go1.25" };
    await expect(getVersion(fakeFetch(200, v))).resolves.toEqual(v);
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

  it("handles 204 responses", async () => {
    await expect(request("POST", "/x", undefined, fakeFetch(204, null))).resolves.toBeUndefined();
  });
});
