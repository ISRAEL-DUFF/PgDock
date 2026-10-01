import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { errorMessage, type MetricRange, type MetricsResponse } from "../api/client";
import { formatBytes } from "../lib/format";
import { LineChart, type ChartSeries } from "./LineChart";
import { Alert, Card, Spinner, cx } from "./ui";

const ranges: MetricRange[] = ["1h", "24h", "7d"];
const rangeMs: Record<MetricRange, number> = { "1h": 3600_000, "24h": 86400_000, "7d": 7 * 86400_000 };

const colors = ["var(--accent)", "var(--ok)", "var(--warn)", "var(--danger)"];

export type ChartDef = {
  title: string;
  metrics: { metric: string; name: string }[];
  format: (v: number) => string;
};

const num = (v: number) => (v >= 100 || Number.isInteger(v) ? Math.round(v).toLocaleString() : v.toFixed(v < 10 ? 2 : 1));
export const fmt = {
  bytes: (v: number) => formatBytes(v),
  count: num,
  rate: (v: number) => `${num(v)}/s`,
  bytesRate: (v: number) => `${formatBytes(v)}/s`,
  percent: (v: number) => `${v.toFixed(v >= 10 ? 0 : 1)}%`,
  ratio: (v: number) => `${(v * 100).toFixed(1)}%`,
};

export function RangePicker({ value, onChange }: { value: MetricRange; onChange: (r: MetricRange) => void }) {
  return (
    <div role="radiogroup" aria-label="Range" className="inline-flex rounded-md border border-line">
      {ranges.map((r) => (
        <button
          key={r}
          type="button"
          role="radio"
          aria-checked={value === r}
          onClick={() => onChange(r)}
          className={cx("px-2.5 py-1 text-xs", value === r ? "bg-accent text-accent-fg" : "text-muted hover:text-fg")}
        >
          {r}
        </button>
      ))}
    </div>
  );
}

/** Charts for one scope's metrics, refreshed every 30 s. */
export function MetricCharts({
  queryKey,
  fetch,
  charts,
  children,
}: {
  queryKey: unknown[];
  fetch: (r: MetricRange) => Promise<MetricsResponse>;
  charts: ChartDef[];
  children?: (data: MetricsResponse) => React.ReactNode;
}) {
  const [range, setRange] = useState<MetricRange>("1h");
  const q = useQuery({ queryKey: [...queryKey, range], queryFn: () => fetch(range), refetchInterval: 30_000 });
  const now = q.dataUpdatedAt || Date.now();
  const byMetric = new Map((q.data?.series ?? []).map((s) => [s.metric, s.points]));
  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between gap-3">
        <p className="text-xs text-muted">
          {q.data ? `${q.data.resolution === "1h" ? "Hourly averages" : "One point per minute"}; refreshes every 30 seconds.` : " "}
        </p>
        <RangePicker value={range} onChange={setRange} />
      </div>
      {q.isPending ? (
        <Spinner />
      ) : q.isError ? (
        <Alert>{errorMessage(q.error)}</Alert>
      ) : (
        <>
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
            {charts.map((c) => {
              const series: ChartSeries[] = c.metrics.map((m, i) => ({ name: m.name, color: colors[i % colors.length], points: byMetric.get(m.metric) ?? [] }));
              const latest = series.map((s) => s.points[s.points.length - 1]?.value);
              return (
                <Card
                  key={c.title}
                  title={c.title}
                  actions={
                    <span className="text-xs text-muted" data-testid={`latest-${c.metrics[0].metric}`}>
                      {latest.every((v) => v == null)
                        ? "—"
                        : series.map((s, i) => (latest[i] == null ? null : `${series.length > 1 ? `${s.name} ` : ""}${c.format(latest[i]!)}`)).filter(Boolean).join(" · ")}
                    </span>
                  }
                >
                  <LineChart series={series} from={now - rangeMs[range]} to={now} format={c.format} label={c.title} />
                  {series.length > 1 && (
                    <div className="mt-1 flex gap-3 text-xs text-muted">
                      {series.map((s) => (
                        <span key={s.name}>
                          <span style={{ color: s.color }}>●</span> {s.name}
                        </span>
                      ))}
                    </div>
                  )}
                </Card>
              );
            })}
          </div>
          {children?.(q.data)}
        </>
      )}
    </div>
  );
}
