import { describe, expect, it } from "vitest";
import { ApiRequestError, getVersion } from "./client";

function fakeFetch(status: number, body: unknown): typeof fetch {
  return (async () =>
    new Response(JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    })) as typeof fetch;
}

describe("getVersion", () => {
  it("returns the parsed version", async () => {
    const v = { version: "1.0.0", commit: "abc", build_date: "now", go_version: "go1.25" };
    await expect(getVersion(fakeFetch(200, v))).resolves.toEqual(v);
  });

  it("throws ApiRequestError with the server message", async () => {
    const err = await getVersion(fakeFetch(500, { code: "boom", message: "it broke" })).catch((e) => e);
    expect(err).toBeInstanceOf(ApiRequestError);
    expect(err.status).toBe(500);
    expect(err.message).toBe("it broke");
  });
});
