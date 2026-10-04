import { useQuery } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { api, type OrgQuotas, type QuotaItem } from "../api/client";
import { LineChart } from "../components/LineChart";
import { Download } from "lucide-react";
import { Alert, Badge, Panel, cx, EmptyState, Page, Section, Select, Spinner, Stat, Table } from "../components/ui";
import { formatDate } from "../lib/format";
import { canManageOrg, useCurrentOrg } from "../lib/org";
import { formatQuantity, hourly, LIMIT_LABELS, monthStart, quotaRatio } from "../lib/usage";

const COLORS = ["var(--accent)", "#16a34a", "#d97706", "#7c3aed", "#db2777"];

type Range = "month" | "7d" | "30d";

function rangeBounds(r: Range, now = new Date()): { from: Date; to: Date } {
  const to = new Date(Math.floor(now.getTime() / 3600_000) * 3600_000);
  if (r === "month") return { from: monthStart(now), to };
  return { from: new Date(to.getTime() - (r === "7d" ? 7 : 30) * 86400_000), to };
}

/** One limit with a usage bar (V2 §13 "quota usage bars"). */
export function QuotaBar({ q }: { q: QuotaItem }) {
  const meta = LIMIT_LABELS[q.limit] ?? { label: q.limit };
  const ratio = quotaRatio(q);
  const unit = meta.unit ? ` ${meta.unit}` : "";
  return (
    <div className="flex flex-col gap-1" data-testid={`quota-${q.limit}`}>
      <div className="flex justify-between text-sm">
        <span>{meta.label}</span>
        <span className="text-muted">
          {ratio != null || q.limit === "projects" ? `${formatQuantity(q.used)}${unit} of ` : ""}
          {q.max == null ? "unlimited" : `${q.max}${unit}`}
        </span>
      </div>
      {ratio != null && (
        <div className="h-1.5 overflow-hidden rounded-full bg-surface-2">
          <div
            className={cx("h-full rounded-full", ratio >= 1 ? "bg-danger" : ratio >= 0.9 ? "bg-warn" : "bg-accent")}
            style={{ width: `${Math.max(2, ratio * 100)}%` }}
          />
        </div>
      )}
    </div>
  );
}

/** The plan's limits that apply to the shared tier, with use. */
export function QuotasCard({ quotas, compact }: { quotas: OrgQuotas; compact?: boolean }) {
  const shown = compact
    ? quotas.items.filter((q) => ["projects", "shared_storage_mb", "project_storage_mb"].includes(q.limit))
    : quotas.items.filter(
        (q) =>
          LIMIT_LABELS[q.limit] &&
          !["branches", "webhook_deliveries_per_min", "scheduled_jobs", "job_min_interval_s", "http_job_runs_per_hour"].includes(q.limit),
      );
  const a = quotas.dedicated_allowance;
  const u = quotas.dedicated_use;
  return (
    <Panel title={compact ? undefined : `Plan: ${quotas.plan}`}>
      <div className={cx("grid gap-3", compact ? "sm:grid-cols-3" : "sm:grid-cols-2")} data-testid="quotas">
        {shown.map((q) => (
          <QuotaBar key={q.limit} q={q} />
        ))}
        {!compact && (
          <div className="flex flex-col gap-1 text-sm" data-testid="dedicated-allowance">
            <span>Dedicated instances</span>
            <span className="text-muted">
              {a.unlimited
                ? `${u.instances} running; no allowance needed`
                : a.instances === 0
                  ? `${u.instances} running; none included, ask for one from a project's settings`
                  : `${u.instances} of ${a.instances} (${u.cpus}/${a.cpus} vCPU, ${u.memory_mb}/${a.memory_mb} MB RAM, ${u.disk_gb}/${a.disk_gb} GB disk)`}
            </span>
          </div>
        )}
      </div>
    </Panel>
  );
}

/** Org → Usage & quotas (V2 §10.9, §13). */
export function UsagePage() {
  const { org } = useCurrentOrg();
  const [range, setRange] = useState<Range>("month");
  const { from, to } = useMemo(() => rangeBounds(range), [range]);
  const params = { from: from.toISOString(), to: to.toISOString() };
  const quotas = useQuery({ queryKey: ["org", org?.id, "quotas"], queryFn: () => api.orgQuotas(org!.id), enabled: !!org });
  const usage = useQuery({
    queryKey: ["org", org?.id, "usage", range],
    queryFn: () => api.orgUsage(org!.id, params),
    enabled: !!org && canManageOrg(org),
  });
  const requests = useQuery({
    queryKey: ["org", org?.id, "dedicated-requests"],
    queryFn: () => api.orgDedicatedRequests(org!.id),
    enabled: !!org && canManageOrg(org),
  });
  const points = useMemo(() => hourly(usage.data?.records ?? []), [usage.data]);
  const projects = useMemo(() => {
    const total: Record<string, number> = {};
    for (const p of points) for (const [k, v] of Object.entries(p.byProject)) total[k] = (total[k] ?? 0) + v;
    return Object.entries(total)
      .sort((a, b) => b[1] - a[1])
      .slice(0, COLORS.length - 1)
      .map(([k]) => k);
  }, [points]);

  if (!org) return <Spinner />;
  if (!canManageOrg(org)) return <EmptyState title="Only owners and admins see the organisation's usage" />;
  const units = Object.fromEntries((usage.data?.metrics ?? []).map((m) => [m.name, m.unit]));
  const series = [
    { name: "All projects", color: COLORS[0], points: points.map((p) => ({ ts: p.ts, value: p.total })) },
    ...(projects.length > 1
      ? projects.map((name, i) => ({ name, color: COLORS[i + 1], points: points.map((p) => ({ ts: p.ts, value: p.byProject[name] ?? 0 })) }))
      : []),
  ];

  return (
    <Page
      title="Usage & quotas"
      description="What your organisation uses, recorded hourly. Nothing is billed yet; this is so you can see where it goes."
      actions={
        <div className="flex items-center gap-2">
          <Select aria-label="Range" value={range} onChange={(e) => setRange(e.target.value as Range)} data-testid="usage-range">
            <option value="month">This month</option>
            <option value="7d">Last 7 days</option>
            <option value="30d">Last 30 days</option>
          </Select>
          <a
            className="inline-flex h-[30px] items-center gap-1.5 rounded-md border border-line-strong bg-surface-2 px-2.5 text-[13px] hover:bg-surface-3"
            href={api.orgUsageCsvUrl(org.id, params)}
            download
            data-testid="usage-csv"
          >
            <Download className="h-3.5 w-3.5" />
            Export CSV
          </a>
        </div>
      }
    >
      <div className="flex flex-col gap-8">
        {quotas.data && (
          <Section title="Plan limits" description="What the shared tier allows, and how much of it you use.">
            <QuotasCard quotas={quotas.data} />
          </Section>
        )}
        {usage.isError && <Alert>Could not load usage.</Alert>}
        {usage.isPending && <Spinner />}
        {usage.data && (
          <>
            <Section title="Totals" description="Over the range picked above.">
              {usage.data.totals.length === 0 ? (
                <p className="text-[13px] text-muted">Nothing recorded in this range yet. Usage is recorded at the end of each hour.</p>
              ) : (
                <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
                  {usage.data.totals.map((t) => (
                    <Stat
                      key={t.metric}
                      label={t.metric.replaceAll("_", " ")}
                      value={
                        <span className="font-mono">
                          {formatQuantity(t.quantity)} <span className="text-[13px] text-muted">{units[t.metric] ?? ""}</span>
                        </span>
                      }
                      testId={`usage-total-${t.metric}`}
                    />
                  ))}
                </div>
              )}
            </Section>
            <Panel title="Shared-tier storage by hour (GB)">
              <LineChart label="Storage by hour" series={series} from={from.getTime()} to={to.getTime()} format={(v) => `${formatQuantity(v)} GB`} />
              <details className="mt-3">
                <summary className="cursor-pointer text-sm text-muted" data-testid="usage-hours-toggle">
                  {points.length} hour{points.length === 1 ? "" : "s"} recorded
                </summary>
                <div className="mt-2 max-h-96 overflow-y-auto">
                  <Table head={["Hour (UTC)", "GB-hours", ...projects]}>
                    {points.map((p) => (
                      <tr key={p.ts} data-testid="usage-hour">
                        <td className="px-3 py-1 font-mono text-xs">{p.ts.slice(0, 13).replace("T", " ")}:00</td>
                        <td className="px-3 py-1 font-mono text-xs" data-testid="usage-hour-total">
                          {formatQuantity(p.total)}
                        </td>
                        {projects.map((name) => (
                          <td key={name} className="px-3 py-1 font-mono text-xs">
                            {formatQuantity(p.byProject[name] ?? 0)}
                          </td>
                        ))}
                      </tr>
                    ))}
                  </Table>
                </div>
              </details>
            </Panel>
          </>
        )}
        {requests.data && requests.data.items.length > 0 && (
          <Panel title="Dedicated instance requests">
            <Table head={["Project", "Size", "Status", "Asked"]}>
              {requests.data.items.map((r) => (
                <tr key={r.id}>
                  <td className="px-3 py-2">{r.project_name}</td>
                  <td className="px-3 py-2 text-xs">
                    {r.profile}, {r.volume_gb} GB
                  </td>
                  <td className="px-3 py-2">
                    <Badge tone={r.status === "approved" ? "ok" : r.status === "rejected" ? "danger" : "warn"}>{r.status}</Badge>
                    {r.decision_note && <div className="text-xs text-muted">{r.decision_note}</div>}
                  </td>
                  <td className="px-3 py-2 text-xs text-muted">{formatDate(r.created_at)}</td>
                </tr>
              ))}
            </Table>
          </Panel>
        )}
      </div>
    </Page>
  );
}
