import { describe, expect, it } from "vitest";
import { formatBytes, parseBytes, relativeTime } from "./format";

describe("format", () => {
  it("formats bytes", () => {
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(1536)).toBe("1.5 KiB");
    expect(formatBytes(1 << 30)).toBe("1.0 GiB");
  });
  it("parses bytes", () => {
    expect(parseBytes("1 GiB")).toBe(1 << 30);
    expect(parseBytes("500mb")).toBe(500 * 1024 * 1024);
    expect(parseBytes("42")).toBe(42);
    expect(parseBytes("lots")).toBeNull();
  });
  it("formats relative times", () => {
    const now = Date.parse("2026-01-01T12:00:00Z");
    expect(relativeTime("2026-01-01T11:59:58Z", now)).toBe("just now");
    expect(relativeTime("2026-01-01T11:30:00Z", now)).toBe("30m ago");
    expect(relativeTime(null, now)).toBe("—");
  });
});
