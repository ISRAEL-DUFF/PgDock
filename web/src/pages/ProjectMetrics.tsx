import { Link } from "@tanstack/react-router";
import { api } from "../api/client";
import { MetricCharts, fmt, type ChartDef } from "../components/Metrics";
import { Card, Table } from "../components/ui";
import { ReapedCard } from "../components/TenancyCards";
import { useProject } from "./ProjectOverview";

const charts: ChartDef[] = [
  {
    title: "Database size",
    metrics: [{ metric: "size_bytes", name: "size" }],
    format: fmt.bytes,
  },
  {
    title: "Connections",
    metrics: [
      { metric: "connections_active", name: "active" },
      { metric: "connections_idle", name: "idle" },
      { metric: "pooler_clients", name: "pooler clients" },
    ],
    format: fmt.count,
  },
  {
    title: "Transactions per second",
    metrics: [{ metric: "tps", name: "tps" }],
    format: fmt.rate,
  },
  {
    title: "Cache hit ratio",
    metrics: [{ metric: "cache_hit_ratio", name: "hit ratio" }],
    format: fmt.ratio,
  },
];

/** Project charts and top queries (spec §8.7). */
export function ProjectMetricsPage() {
  const { data: p } = useProject();
  if (!p) return null;
  return (
    <>
      <MetricCharts
        queryKey={["metrics", "project", p.id]}
        fetch={(r) => api.projectMetrics(p.id, r)}
        charts={charts}
      >
        {(data) => (
          <Card title="Top queries by total time">
            {!data.top_queries ? (
              <p className="text-sm text-muted">
                Not available while the project is {p.status}.
              </p>
            ) : !data.top_queries.available ? (
              <p className="text-sm text-muted">
                Enable <span className="font-mono">pg_stat_statements</span> in{" "}
                <Link
                  to="/projects/$id/settings"
                  params={{ id: p.id }}
                  className="text-accent hover:underline"
                >
                  Settings → Extensions
                </Link>{" "}
                to see which queries take the most time.
              </p>
            ) : data.top_queries.items.length === 0 ? (
              <p className="text-sm text-muted">No queries recorded yet.</p>
            ) : (
              <Table head={["Query", "Calls", "Total", "Mean", "Rows"]}>
                {data.top_queries.items.map((q, i) => (
                  <tr key={i}>
                    <td
                      className="max-w-[36rem] truncate px-3 py-1.5 font-mono text-xs"
                      title={q.query}
                    >
                      {q.query}
                    </td>
                    <td className="px-3 py-1.5 text-right tabular-nums">
                      {q.calls.toLocaleString()}
                    </td>
                    <td className="px-3 py-1.5 text-right tabular-nums">
                      {q.total_ms.toFixed(1)} ms
                    </td>
                    <td className="px-3 py-1.5 text-right tabular-nums">
                      {q.mean_ms.toFixed(2)} ms
                    </td>
                    <td className="px-3 py-1.5 text-right tabular-nums">
                      {q.rows.toLocaleString()}
                    </td>
                  </tr>
                ))}
              </Table>
            )}
          </Card>
        )}
      </MetricCharts>
      <div className="mt-4">
        <ReapedCard p={p} />
      </div>
    </>
  );
}

export const nodeCharts: ChartDef[] = [
  {
    title: "CPU",
    metrics: [{ metric: "cpu_percent", name: "busy" }],
    format: fmt.percent,
  },
  {
    title: "Load average (1 min)",
    metrics: [{ metric: "load1", name: "load" }],
    format: fmt.count,
  },
  {
    title: "Memory",
    metrics: [
      { metric: "mem_used_bytes", name: "used" },
      { metric: "mem_total_bytes", name: "total" },
    ],
    format: fmt.bytes,
  },
  {
    title: "Disk",
    metrics: [
      { metric: "disk_used_bytes", name: "used" },
      { metric: "disk_total_bytes", name: "total" },
    ],
    format: fmt.bytes,
  },
  {
    title: "Disk I/O",
    metrics: [
      { metric: "disk_read_bps", name: "read" },
      { metric: "disk_write_bps", name: "write" },
    ],
    format: fmt.bytesRate,
  },
];
