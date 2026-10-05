import { useQuery } from "@tanstack/react-query";
import { api, type EstimateRequest } from "../api/client";
import { naira } from "../lib/billing";

/** What a billable action will cost, at the organisation's prices (V3
 * §3.10: a cost estimate before every billable action). */
export function CostEstimate({
  org,
  req,
  what,
}: {
  org: string;
  req: EstimateRequest | null;
  what: string;
}) {
  const q = useQuery({
    queryKey: ["estimate", org, req],
    queryFn: () => api.estimate(org, req!),
    enabled: !!req,
    retry: false,
  });
  // No billing on this server, or nothing to price: say nothing.
  if (!q.data || q.data.monthly_minor <= 0) return null;
  return (
    <p className="text-[13px] text-muted" data-testid="cost-estimate">
      {what} costs about{" "}
      <span className="font-medium text-fg">
        {naira(q.data.monthly_minor)} a month
      </span>{" "}
      ({naira(q.data.hourly_minor)} an hour) before VAT, billed by the hour.
    </p>
  );
}
