import { describe, expect, it } from "vitest";
import { noticeGiven, noticeHours } from "./IncidentsMaintenance";

describe("maintenance notice", () => {
  const now = new Date("2026-10-01T00:00:00Z");
  it("counts hours to the start", () => {
    expect(noticeGiven(new Date("2026-10-04T01:00:00Z"), now)).toBe(73);
    expect(noticeGiven(new Date("2026-10-03T23:00:00Z"), now)).toBeLessThan(noticeHours);
  });
});
