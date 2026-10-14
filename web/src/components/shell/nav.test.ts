import { describe, expect, it } from "vitest";
import { contextFor, orgRail, platformRail } from "./nav";

describe("platform rail", () => {
  it("gives support staff the support console only", () => {
    expect(platformRail("support").map((r) => r.to)).toEqual([
      "/admin/support",
    ]);
    expect(contextFor("/admin/support").kind).toBe("platform");
  });
  it("gives platform admins revenue, legal and support", () => {
    const to = platformRail("platform_admin").map((r) => r.to);
    expect(to).toEqual(
      expect.arrayContaining([
        "/admin/revenue",
        "/admin/legal",
        "/admin/support",
        "/nodes",
      ]),
    );
  });
});

describe("org rail", () => {
  it("shows support and legal to every member", () => {
    const to = orgRail({ role: "member" } as Parameters<typeof orgRail>[0]).map(
      (r) => r.to,
    );
    expect(to).toEqual(expect.arrayContaining(["/org/support", "/org/legal"]));
  });
});
