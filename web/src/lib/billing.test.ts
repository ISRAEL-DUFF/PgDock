import { describe, expect, it } from "vitest";
import { naira, parseNaira, periodLabel, periodOf } from "./billing";

describe("naira", () => {
  it("formats kobo", () => {
    expect(naira(1500000)).toBe("₦15,000.00");
    expect(naira(5)).toBe("₦0.05");
    expect(naira(-919355)).toBe("-₦9,193.55");
    expect(naira(0)).toBe("₦0.00");
  });
  it("parses what people type", () => {
    expect(parseNaira("15,000")).toBe(1500000);
    expect(parseNaira("₦ 40000.5")).toBe(4000050);
    expect(parseNaira("")).toBeNull();
    expect(parseNaira("1.234")).toBeNull();
    expect(parseNaira("abc")).toBeNull();
  });
  it("names periods", () => {
    expect(periodOf(new Date(Date.UTC(2026, 9, 31, 23)))).toBe("2026-10");
    expect(periodLabel("2026-10")).toBe("October 2026");
  });
});
