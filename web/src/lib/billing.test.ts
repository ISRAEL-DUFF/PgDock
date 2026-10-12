import { describe, expect, it } from "vitest";
import {
  approxNaira,
  bps,
  money,
  naira,
  parseNaira,
  parseUsd,
  periodLabel,
  periodOf,
  rate,
  usd,
  usdUnit,
} from "./billing";

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

describe("dollars", () => {
  it("formats cents", () => {
    expect(usd(1000)).toBe("$10.00");
    expect(usd(4900)).toBe("$49.00");
    expect(usd(5)).toBe("$0.05");
    expect(usd(123456789)).toBe("$1,234,567.89");
    expect(usd(-250)).toBe("-$2.50");
  });
  it("parses what people type", () => {
    expect(parseUsd("10")).toBe(1000);
    expect(parseUsd("$1,000.5")).toBe(100050);
    expect(parseUsd("")).toBeNull();
    expect(parseUsd("1.234")).toBeNull();
    expect(parseUsd("x")).toBeNull();
  });
  it("picks the currency", () => {
    expect(money("USD", 1000)).toBe("$10.00");
    expect(money("NGN", 1000)).toBe("₦10.00");
    expect(money(undefined, 1000)).toBe("₦10.00");
  });
  it("shows fractions of a cent", () => {
    expect(usdUnit(0.0002)).toBe("0.0002¢");
    expect(usdUnit(0.5)).toBe("0.5¢");
    expect(usdUnit(250)).toBe("$2.50");
  });
});

describe("rates", () => {
  const fx = { effective_rate: "1515" };
  it("words a rate", () => {
    expect(rate(fx)).toBe("₦1,515 per $1");
    expect(rate({ effective_rate: "1515.5" })).toBe("₦1,515.50 per $1");
    expect(rate(null)).toBe("");
  });
  it("converts dollars to approximate naira", () => {
    expect(approxNaira(1000, fx)).toBe("≈ ₦15,150");
    expect(approxNaira(4900, { effective_rate: "1515.25" })).toBe("≈ ₦74,247");
    expect(approxNaira(1000, undefined)).toBe("");
  });
  it("words a spread", () => {
    expect(bps(100)).toBe("1%");
    expect(bps(150)).toBe("1.5%");
  });
});
