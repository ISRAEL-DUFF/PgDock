import { describe, expect, it } from "vitest";
import { formatCacheTTLs, parseCacheTTLs } from "./cacheTtl";

describe("cache TTLs", () => {
  it("round-trips", () => {
    const text = formatCacheTTLs({ "rpc.search": 30, "public.products": 60 });
    expect(text).toBe("public.products 60\nrpc.search 30");
    expect(parseCacheTTLs(text)).toEqual({
      ok: true,
      value: { "public.products": 60, "rpc.search": 30 },
    });
  });
  it("accepts = and : and blank lines", () => {
    expect(parseCacheTTLs("a.b=5\n\n c.d: 7 ")).toEqual({
      ok: true,
      value: { "a.b": 5, "c.d": 7 },
    });
  });
  it("names the bad line", () => {
    const r = parseCacheTTLs("public.products sixty");
    expect(r.ok).toBe(false);
  });
});
