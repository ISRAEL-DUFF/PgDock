import { describe, expect, it } from "vitest";
import type { UsageRecord } from "../api/client";
import { formatQuantity, hourly, quotaRatio } from "./usage";

// A week of hourly storage records for two projects, as the API returns
// them: project A holds (i+1) x 0.1 GB in hour i, project B a steady 1 GB.
function week(): UsageRecord[] {
  const start = Date.UTC(2026, 8, 1);
  const out: UsageRecord[] = [];
  for (let i = 0; i < 168; i++) {
    const ts = new Date(start + i * 3600_000).toISOString();
    out.push({ metric: "shared_storage_gb_hours", granularity: "hour", period_start: ts, project_id: "a", project_name: "App", quantity: (i + 1) * 0.1 });
    out.push({ metric: "shared_storage_gb_hours", granularity: "hour", period_start: ts, project_id: "b", project_name: "Blog", quantity: 1 });
  }
  out.push({ metric: "pooler_transfer_gb", granularity: "hour", period_start: new Date(start).toISOString(), quantity: 5 });
  return out;
}

describe("usage", () => {
  it("turns a week of records into 168 hourly points", () => {
    const pts = hourly(week());
    expect(pts).toHaveLength(168);
    expect(pts[0].ts).toBe("2026-09-01T00:00:00.000Z");
    expect(pts[167].ts).toBe("2026-09-07T23:00:00.000Z");
    pts.forEach((p, i) => {
      expect(p.byProject.App).toBeCloseTo((i + 1) * 0.1, 6);
      expect(p.byProject.Blog).toBe(1);
      expect(p.total).toBeCloseTo((i + 1) * 0.1 + 1, 6);
    });
  });

  it("formats quantities", () => {
    expect(formatQuantity(16.8)).toBe("16.8");
    expect(formatQuantity(0.1)).toBe("0.1");
    expect(formatQuantity(1419.6)).toBe("1420");
    expect(formatQuantity(0)).toBe("0");
  });

  it("measures quotas", () => {
    expect(quotaRatio({ limit: "projects", used: 5, max: 10 })).toBe(0.5);
    expect(quotaRatio({ limit: "projects", used: 12, max: 10 })).toBe(1);
    expect(quotaRatio({ limit: "projects", used: 3 })).toBeNull();
  });
});
