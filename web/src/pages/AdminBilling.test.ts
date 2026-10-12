import { describe, expect, it } from "vitest";
import { withAddOnFields } from "./AdminBilling";

describe("withAddOnFields", () => {
  it("fills the add-on prices an older book lacks, keeping its own", () => {
    const prices = {
      currency: "NGN" as const,
      plans: {},
      dedicated: {
        vcpu_hour: "2740",
        ram_gb_hour: "685",
        disk_gb_hour: "34.25",
      },
      addons: {
        ha_premium_percent: "30",
        sync_replication_hour: "1370",
        pitr_14_hour: "700",
      },
    };
    const out = withAddOnFields(prices);
    expect(out.addons.ha_premium_percent).toBe("30");
    expect(out.addons.pitr_14_hour).toBe("700");
    expect(out.addons.pitr_30_hour).toBe("1644");
    expect(out.addons.region_premium_percent).toEqual({});
  });
});
