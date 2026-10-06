import { useQuery } from "@tanstack/react-query";
import { Download } from "lucide-react";
import { useState } from "react";
import { api, errorMessage, type RevenueMonth } from "../api/client";
import { LineChart } from "../components/LineChart";
import {
  Alert,
  Page,
  PageSkeleton,
  Panel,
  Select,
  Stat,
  Table,
} from "../components/ui";
import { naira, periodLabel } from "../lib/billing";
import { formatDate } from "../lib/format";

function change(cur: number, prev: number | undefined): string | undefined {
  if (prev === undefined || prev === 0) return undefined;
  const pct = ((cur - prev) / prev) * 100;
  return `${pct >= 0 ? "+" : ""}${pct.toFixed(1)}% on last month`;
}

/** Platform → Revenue (V3 §7.2, first version): recurring revenue and its
 * movements, paying organisations and conversion from Free, metered
 * revenue, collections, and receivables by age. Costs and margin follow
 * with cost attribution. */
export function AdminRevenuePage() {
  const [months, setMonths] = useState(12);
  const q = useQuery({
    queryKey: ["admin", "revenue", months],
    queryFn: () => api.revenue(months),
  });
  if (q.isPending) return <PageSkeleton />;
  if (q.error) return <Alert>{errorMessage(q.error)}</Alert>;
  const r = q.data;
  const cur: RevenueMonth | undefined = r.months[r.months.length - 1];
  const prev: RevenueMonth | undefined = r.months[r.months.length - 2];
  const outstanding = r.ageing.reduce((s, b) => s + b.amount_minor, 0);
  const pts = (f: (m: RevenueMonth) => number) =>
    r.months.map((m) => ({ ts: `${m.month}-01T00:00:00Z`, value: f(m) / 100 }));
  return (
    <Page
      title="Revenue"
      description={`Monthly recurring revenue is plan subscriptions (annual plans spread over twelve months); metered charges are counted apart. Figures as of ${formatDate(r.as_of)}.`}
      testId="admin-revenue"
      actions={
        <>
          <Select
            aria-label="Months"
            value={months}
            onChange={(e) => setMonths(Number(e.target.value))}
          >
            <option value={6}>6 months</option>
            <option value={12}>12 months</option>
            <option value={24}>24 months</option>
          </Select>
          <a
            className="inline-flex h-[30px] items-center gap-1.5 rounded-md border border-line-strong bg-surface-2 px-2.5 text-[13px] hover:bg-surface-3"
            href={api.revenueCsvUrl(months)}
            download
            data-testid="revenue-csv"
          >
            <Download className="h-3.5 w-3.5" /> CSV for the accountant
          </a>
        </>
      }
    >
      {cur && (
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <Stat
            label="MRR"
            value={naira(cur.mrr_minor)}
            hint={change(cur.mrr_minor, prev?.mrr_minor)}
            testId="revenue-mrr"
          />
          <Stat label="ARR" value={naira(cur.arr_minor)} />
          <Stat
            label="Paying organisations"
            value={cur.paying_orgs}
            hint={`ARPA ${naira(cur.arpa_minor)}`}
            testId="revenue-paying"
          />
          <Stat
            label="Receivables"
            value={naira(outstanding)}
            hint={`WHT awaiting certificates ${naira(r.outstanding_wht_minor)}`}
          />
        </div>
      )}
      <Panel title="Recurring revenue">
        <LineChart
          from={Date.parse(`${r.months[0]?.month ?? ""}-01T00:00:00Z`)}
          to={Date.parse(`${cur?.month ?? ""}-01T00:00:00Z`)}
          format={(v) => naira(v * 100)}
          label="Monthly recurring and metered revenue"
          series={[
            {
              name: "MRR (₦)",
              color: "var(--accent)",
              points: pts((m) => m.mrr_minor),
            },
            {
              name: "Metered (₦)",
              color: "#d97706",
              points: pts((m) => m.usage_revenue_minor),
            },
          ]}
        />
      </Panel>
      <Table
        head={[
          "Month",
          "MRR",
          "New",
          "Expansion",
          "Contraction",
          "Churned",
          "Paying",
          "From Free",
          "Metered",
          "Invoiced",
          "Collected",
        ]}
      >
        {[...r.months].reverse().map((m) => (
          <tr key={m.month} data-testid={`revenue-${m.month}`}>
            <td className="px-3 py-2 whitespace-nowrap">
              {periodLabel(m.month)}
            </td>
            <td className="px-3 py-2 font-medium">{naira(m.mrr_minor)}</td>
            <td className="px-3 py-2 text-ok-text">
              {m.new_minor ? `+${naira(m.new_minor)}` : "—"}
            </td>
            <td className="px-3 py-2 text-ok-text">
              {m.expansion_minor ? `+${naira(m.expansion_minor)}` : "—"}
            </td>
            <td className="px-3 py-2 text-danger-text">
              {m.contraction_minor ? `-${naira(m.contraction_minor)}` : "—"}
            </td>
            <td className="px-3 py-2 text-danger-text">
              {m.churned_minor ? `-${naira(m.churned_minor)}` : "—"}
            </td>
            <td className="px-3 py-2">{m.paying_orgs}</td>
            <td className="px-3 py-2">
              {m.conversions}
              {m.free_orgs > 0 && (
                <span className="text-xs text-muted"> of {m.free_orgs}</span>
              )}
            </td>
            <td className="px-3 py-2">{naira(m.usage_revenue_minor)}</td>
            <td className="px-3 py-2">
              {naira(m.invoiced_minor)}
              <span className="text-xs text-muted"> ({m.invoices})</span>
            </td>
            <td className="px-3 py-2">{naira(m.collected_minor)}</td>
          </tr>
        ))}
      </Table>
      <Panel
        title="Receivables by age"
        description="Issued invoices not yet paid, by days past their due date."
      >
        <Table head={["Age", "Amount", "Invoices"]}>
          {r.ageing.map((b) => (
            <tr key={b.label}>
              <td className="px-3 py-2">{b.label}</td>
              <td className="px-3 py-2">{naira(b.amount_minor)}</td>
              <td className="px-3 py-2">{b.invoices}</td>
            </tr>
          ))}
        </Table>
      </Panel>
    </Page>
  );
}
