import { beforeEach, describe, expect, it, vi } from "vitest";
import { clearCurrentOrg, setCurrentOrg } from "./org";

describe("remembered organisation", () => {
  const store = new Map<string, string>();
  beforeEach(() => {
    store.clear();
    vi.stubGlobal("localStorage", {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => void store.set(k, v),
      removeItem: (k: string) => void store.delete(k),
    });
  });

  it("is forgotten on sign-out", () => {
    setCurrentOrg("org-1");
    expect(store.get("pgdock.org")).toBe("org-1");
    clearCurrentOrg();
    expect(store.has("pgdock.org")).toBe(false);
  });
});
