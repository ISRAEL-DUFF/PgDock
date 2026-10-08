import type { QuotaItem, UsageRecord } from "../api/client";

/** Shared-tier storage, recorded hourly as GB-hours (V2 §10.9). */
export const STORAGE_METRIC = "shared_storage_gb_hours";

export type HourlyPoint = { ts: string; total: number; byProject: Record<string, number> };

/**
 * One point per hour of a metric's hourly records, summed across projects
 * and sorted by time. An hour's GB-hours of storage is also its average
 * size in GB, which is what the chart shows.
 */
export function hourly(records: UsageRecord[], metric = STORAGE_METRIC): HourlyPoint[] {
  const by = new Map<string, HourlyPoint>();
  for (const r of records) {
    if (r.metric !== metric || r.granularity !== "hour") continue;
    const ts = new Date(r.period_start).toISOString();
    let p = by.get(ts);
    if (!p) {
      p = { ts, total: 0, byProject: {} };
      by.set(ts, p);
    }
    p.total += r.quantity;
    const name = r.project_name ?? (r.project_id ? "Deleted project" : "Organisation");
    p.byProject[name] = (p.byProject[name] ?? 0) + r.quantity;
  }
  return [...by.values()].sort((a, b) => a.ts.localeCompare(b.ts));
}

/** Rounds a quantity for display: 0.1 GB-hours, 2 decimals, or 3 when small. */
export function formatQuantity(v: number): string {
  if (v === 0) return "0";
  if (Math.abs(v) >= 100) return v.toFixed(0);
  if (Math.abs(v) >= 1) return v.toFixed(2).replace(/\.?0+$/, "");
  return v.toFixed(3).replace(/\.?0+$/, "");
}

/** Names of the limit keys (V2 §10.3), for the quotas card. */
export const LIMIT_LABELS: Record<string, { label: string; unit?: "MB" }> = {
  projects: { label: "Projects" },
  branches: { label: "Branches" },
  shared_storage_mb: { label: "Shared-tier storage", unit: "MB" },
  project_storage_mb: { label: "Largest project", unit: "MB" },
  project_connections: { label: "Connections per project" },
  backup_storage_mb: { label: "Backup storage", unit: "MB" },
  webhook_deliveries_per_min: { label: "Webhook deliveries per minute" },
  scheduled_jobs: { label: "Scheduled jobs" },
  job_min_interval_s: { label: "Shortest job interval (s)" },
  http_job_runs_per_hour: { label: "HTTP job runs per hour" },
  console_queries: { label: "Concurrent console queries" },
  operations_in_flight: { label: "Operations in flight" },
  file_storage_mb: { label: "File storage", unit: "MB" },
  storage_egress_mb_per_month: { label: "File downloads per month", unit: "MB" },
  image_transforms_per_month: { label: "Image transforms per month" },
  upload_max_mb: { label: "Largest upload", unit: "MB" },
  realtime_connections: { label: "Realtime connections" },
  realtime_messages_per_month: { label: "Realtime messages per month" },
};

/** The share of a limit used, 0–1 (null when unlimited or not a usage). */
export function quotaRatio(q: QuotaItem): number | null {
  if (q.max == null || q.max <= 0) return null;
  if (q.limit === "project_connections" || q.limit === "job_min_interval_s" || q.limit === "upload_max_mb" || q.limit === "realtime_connections") return null;
  return Math.min(1, q.used / q.max);
}

/** The first day of this month, UTC. */
export function monthStart(now = new Date()): Date {
  return new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1));
}
