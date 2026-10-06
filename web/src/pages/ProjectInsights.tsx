import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import {
  ApiRequestError,
  api,
  errorMessage,
  type IndexSuggestion,
  type InsightPlan,
  type InsightQuery,
  type InsightRange,
  type InsightSort,
} from "../api/client";
import { LineChart } from "../components/LineChart";
import { useOperationToast } from "../components/Toasts";
import { SchemaReview } from "../components/tableEditor/SchemaReview";
import {
  Alert,
  Badge,
  Button,
  CodeBlock,
  Page,
  Panel,
  Segmented,
  SidePanel,
  Spinner,
  Table,
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "../components/ui";
import { formatBytes, relativeTime } from "../lib/format";
import { useProject } from "./ProjectOverview";

const ranges: { value: InsightRange; label: string }[] = [
  { value: "1h", label: "1h" },
  { value: "24h", label: "24h" },
  { value: "7d", label: "7d" },
  { value: "30d", label: "30d" },
];
const rangeMs: Record<InsightRange, number> = {
  "1h": 3600_000,
  "24h": 86400_000,
  "7d": 7 * 86400_000,
  "30d": 30 * 86400_000,
};

const ms = (v: number) =>
  v >= 1000
    ? `${(v / 1000).toFixed(v >= 10_000 ? 0 : 1)} s`
    : `${v >= 10 ? Math.round(v) : v.toFixed(2)} ms`;
const count = (v: number) => v.toLocaleString();

/** The upsell when the project's plan has no insights (403 plan_required). */
function Gate({ error, children }: { error: unknown; children: ReactNode }) {
  if (error instanceof ApiRequestError && error.code === "plan_required") {
    return (
      <Panel title="Query insights">
        <p className="text-sm text-muted" data-testid="insights-plan-required">
          {error.message}. Upgrade the organisation&apos;s plan under Billing,
          or promote the project to dedicated.
        </p>
      </Panel>
    );
  }
  if (error) return <Alert>{errorMessage(error)}</Alert>;
  return <>{children}</>;
}

/** Project → Query insights (V3 §8): top queries, slow queries, indexes,
 * bloat and locks. */
export function ProjectInsightsPage() {
  const { data: p } = useProject();
  const [tab, setTab] = useState("queries");
  if (!p) return null;
  return (
    <Page
      title="Query insights"
      description="Which queries take the time, how they run, and the indexes that would help. Statistics are collected every 5 minutes."
      testId="project-insights"
    >
      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="queries">Queries</TabsTrigger>
          <TabsTrigger value="slow">Slow queries</TabsTrigger>
          <TabsTrigger value="indexes">Indexes</TabsTrigger>
          <TabsTrigger value="bloat">Bloat</TabsTrigger>
          <TabsTrigger value="locks">Locks</TabsTrigger>
        </TabsList>
        <div className="pt-4">
          <TabsContent value="queries">
            <QueriesTab projectId={p.id} />
          </TabsContent>
          <TabsContent value="slow">
            <SlowTab projectId={p.id} />
          </TabsContent>
          <TabsContent value="indexes">
            <IndexesTab projectId={p.id} />
          </TabsContent>
          <TabsContent value="bloat">
            <BloatTab projectId={p.id} />
          </TabsContent>
          <TabsContent value="locks">
            <LocksTab projectId={p.id} />
          </TabsContent>
        </div>
      </Tabs>
    </Page>
  );
}

function QueriesTab({ projectId }: { projectId: string }) {
  const [range, setRange] = useState<InsightRange>("24h");
  const [sort, setSort] = useState<InsightSort>("total");
  const [open, setOpen] = useState<InsightQuery | null>(null);
  const q = useQuery({
    queryKey: ["insights", projectId, "queries", range, sort],
    queryFn: () => api.insightQueries(projectId, range, sort),
    retry: false,
  });
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <Segmented
          value={sort}
          onChange={setSort}
          options={[
            { value: "total", label: "Total time" },
            { value: "mean", label: "Mean time" },
            { value: "calls", label: "Calls" },
            { value: "rows", label: "Rows" },
          ]}
        />
        <Segmented value={range} onChange={setRange} options={ranges} />
      </div>
      <Gate error={q.error}>
        {q.isPending ? (
          <Spinner />
        ) : q.data && q.data.items.length === 0 ? (
          <p className="text-sm text-muted">
            No queries recorded in this range yet. Statistics arrive every 5
            minutes.
          </p>
        ) : (
          <Table
            head={["Query", "Calls", "Total", "Mean", "Max", "Rows", "Share"]}
          >
            {(q.data?.items ?? []).map((it) => (
              <tr
                key={it.query_id}
                className="cursor-pointer"
                onClick={() => setOpen(it)}
                data-testid={`insight-query-${it.query_id}`}
              >
                <td className="max-w-[28rem] px-3 py-2">
                  <div className="truncate font-mono text-xs" title={it.query}>
                    {it.query}
                  </div>
                </td>
                <td className="px-3 py-2 tabular-nums">{count(it.calls)}</td>
                <td className="px-3 py-2 tabular-nums">{ms(it.total_ms)}</td>
                <td className="px-3 py-2 tabular-nums">{ms(it.mean_ms)}</td>
                <td className="px-3 py-2 tabular-nums">{ms(it.max_ms)}</td>
                <td className="px-3 py-2 tabular-nums">{count(it.rows)}</td>
                <td className="px-3 py-2 tabular-nums">
                  {(it.share * 100).toFixed(1)}%
                </td>
              </tr>
            ))}
          </Table>
        )}
      </Gate>
      {open && (
        <QueryPanel
          projectId={projectId}
          query={open}
          range={range}
          onClose={() => setOpen(null)}
        />
      )}
    </div>
  );
}

function QueryPanel({
  projectId,
  query,
  range,
  onClose,
}: {
  projectId: string;
  query: InsightQuery;
  range: InsightRange;
  onClose: () => void;
}) {
  const d = useQuery({
    queryKey: ["insights", projectId, "query", query.query_id, range],
    queryFn: () => api.insightQuery(projectId, query.query_id, range),
  });
  const [plan, setPlan] = useState<InsightPlan | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const explain = async (generic: boolean) => {
    setBusy(true);
    setErr(null);
    try {
      setPlan(await api.explainQuery(projectId, query.query_id, generic));
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  const now = d.dataUpdatedAt || Date.now();
  const points = d.data?.series ?? [];
  return (
    <SidePanel
      open
      onOpenChange={(o) => !o && onClose()}
      title="Query"
      size="xlarge"
      testId="insight-query-panel"
    >
      <div className="flex flex-col gap-4">
        <CodeBlock code={query.query} wrap />
        <div className="grid grid-cols-2 gap-3 text-[13px] sm:grid-cols-4">
          <div>
            <div className="text-muted">Calls</div>
            {count(query.calls)}
          </div>
          <div>
            <div className="text-muted">Mean</div>
            {ms(query.mean_ms)}
          </div>
          <div>
            <div className="text-muted">Max</div>
            {ms(query.max_ms)}
          </div>
          <div>
            <div className="text-muted">Cache hits</div>
            {(query.hit_ratio * 100).toFixed(1)}%
          </div>
        </div>
        {d.data && (
          <>
            <Panel title="Mean time">
              <LineChart
                series={[
                  {
                    name: "mean",
                    color: "var(--accent)",
                    points: points.map((pt) => ({
                      ts: pt.ts,
                      value: pt.mean_ms,
                    })),
                  },
                ]}
                from={now - rangeMs[range]}
                to={now}
                format={ms}
                label="Mean time"
              />
            </Panel>
            <Panel title="Calls">
              <LineChart
                series={[
                  {
                    name: "calls",
                    color: "var(--ok)",
                    points: points.map((pt) => ({
                      ts: pt.ts,
                      value: pt.calls,
                    })),
                  },
                ]}
                from={now - rangeMs[range]}
                to={now}
                format={count}
                label="Calls"
              />
            </Panel>
            {d.data.example && (
              <div className="flex flex-col gap-1">
                <span className="text-xs text-muted">
                  Latest example
                  {d.data.example_at
                    ? `, ${relativeTime(d.data.example_at)}`
                    : ""}
                </span>
                <CodeBlock code={d.data.example} wrap />
              </div>
            )}
          </>
        )}
        <div className="flex gap-2">
          <Button
            variant="primary"
            busy={busy}
            onClick={() => void explain(false)}
            data-testid="insight-explain"
          >
            {query.has_example
              ? "Explain the example"
              : "Explain (generic plan)"}
          </Button>
          {query.has_example && (
            <Button busy={busy} onClick={() => void explain(true)}>
              Generic plan
            </Button>
          )}
        </div>
        {err && <Alert>{err}</Alert>}
        {plan && <PlanView plan={plan} />}
      </div>
    </SidePanel>
  );
}

type PlanNode = {
  "Node Type": string;
  "Relation Name"?: string;
  Schema?: string;
  Alias?: string;
  "Index Name"?: string;
  "Join Type"?: string;
  Filter?: string;
  "Index Cond"?: string;
  "Total Cost": number;
  "Plan Rows": number;
  Plans?: PlanNode[];
};

/** EXPLAIN's plan as a tree (V3 §8). */
function PlanView({ plan }: { plan: InsightPlan }) {
  const root = (plan.plan as { Plan: PlanNode }[])[0]?.Plan;
  return (
    <Panel
      title={
        plan.generic
          ? "Generic plan (parameters unknown)"
          : "Plan for the example"
      }
      actions={
        <span className="text-xs text-muted">
          cost {plan.total_cost.toFixed(0)}
        </span>
      }
    >
      <div
        data-testid="insight-plan"
        className="flex flex-col gap-1 font-mono text-xs"
      >
        {root && <PlanTree node={root} depth={0} />}
      </div>
      {plan.seq_scans.length > 0 && (
        <p className="mt-2 text-xs text-muted">
          Reads whole tables: {plan.seq_scans.join(", ")}. See the Indexes tab.
        </p>
      )}
    </Panel>
  );
}

function PlanTree({ node, depth }: { node: PlanNode; depth: number }) {
  const rel = node["Relation Name"]
    ? ` on ${node.Schema ? `${node.Schema}.` : ""}${node["Relation Name"]}`
    : "";
  const idx = node["Index Name"] ? ` using ${node["Index Name"]}` : "";
  const seq = node["Node Type"] === "Seq Scan";
  return (
    <>
      <div
        style={{ paddingLeft: depth * 16 }}
        className="flex flex-wrap gap-x-2"
      >
        <span className={seq ? "text-warn" : undefined}>
          {depth > 0 ? "→ " : ""}
          {node["Join Type"] ? `${node["Join Type"]} ` : ""}
          {node["Node Type"]}
          {rel}
          {idx}
        </span>
        <span className="text-muted">
          cost {node["Total Cost"].toFixed(0)}, ~
          {Math.round(node["Plan Rows"]).toLocaleString()} rows
        </span>
        {(node.Filter || node["Index Cond"]) && (
          <span className="text-muted">
            {node["Index Cond"]
              ? `cond ${node["Index Cond"]}`
              : `filter ${node.Filter}`}
          </span>
        )}
      </div>
      {(node.Plans ?? []).map((c, i) => (
        <PlanTree key={i} node={c} depth={depth + 1} />
      ))}
    </>
  );
}

const sourceLabel = {
  running: "Seen running",
  snapshot: "Between snapshots",
  reaper: "Cancelled by the reaper",
} as const;

function SlowTab({ projectId }: { projectId: string }) {
  const [range, setRange] = useState<InsightRange>("24h");
  const q = useQuery({
    queryKey: ["insights", projectId, "slow", range],
    queryFn: () => api.slowQueries(projectId, range),
    retry: false,
  });
  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center justify-between">
        <p className="text-xs text-muted">
          {q.data ? `Statements over ${ms(q.data.threshold_ms)}.` : " "}
        </p>
        <Segmented value={range} onChange={setRange} options={ranges} />
      </div>
      <Gate error={q.error}>
        {q.isPending ? (
          <Spinner />
        ) : q.data && q.data.items.length === 0 ? (
          <p className="text-sm text-muted">
            No slow statements in this range.
          </p>
        ) : (
          <Table head={["When", "Duration", "Query", "Role", "Source"]}>
            {(q.data?.items ?? []).map((s, i) => (
              <tr key={i} data-testid="slow-query">
                <td className="whitespace-nowrap px-3 py-2">
                  {relativeTime(s.seen_at)}
                </td>
                <td className="px-3 py-2 tabular-nums">{ms(s.duration_ms)}</td>
                <td className="max-w-[28rem] px-3 py-2">
                  <div className="truncate font-mono text-xs" title={s.query}>
                    {s.query}
                  </div>
                </td>
                <td className="px-3 py-2">{s.role || "—"}</td>
                <td className="px-3 py-2 text-muted">
                  {sourceLabel[s.source]}
                </td>
              </tr>
            ))}
          </Table>
        )}
      </Gate>
    </div>
  );
}

const reasonLabel = {
  seq_scan_filter: "Top queries filter on it with a full scan",
  unindexed_foreign_key: "Foreign key without an index",
} as const;

function IndexesTab({ projectId }: { projectId: string }) {
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["insights", projectId, "indexes"],
    queryFn: () => api.insightIndexes(projectId),
    retry: false,
  });
  const [review, setReview] = useState<IndexSuggestion | null>(null);
  const r = q.data;
  return (
    <Gate error={q.error}>
      {q.isPending || !r ? (
        <Spinner />
      ) : (
        <div className="flex flex-col gap-4">
          {r.hypopg === "available" && (
            <Alert tone="accent">
              Enable the <span className="font-mono">hypopg</span> extension
              (Database → Extensions) to see the planner&apos;s estimate for
              each suggestion.
            </Alert>
          )}
          <Panel title="Suggested indexes">
            {r.suggestions.length === 0 ? (
              <p className="text-sm text-muted">
                No suggestions: the top queries and foreign keys are covered.
              </p>
            ) : (
              <div className="flex flex-col gap-4">
                {r.suggestions.map((s) => (
                  <div
                    key={s.statement}
                    className="flex flex-col gap-2 rounded-md border border-line p-3"
                    data-testid={`suggestion-${s.table}-${s.columns.join("-")}`}
                  >
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <span className="font-medium">
                        {s.schema}.{s.table} ({s.columns.join(", ")})
                      </span>
                      <div className="flex gap-2">
                        {s.reasons.map((x) => (
                          <Badge key={x} tone="muted">
                            {reasonLabel[x]}
                          </Badge>
                        ))}
                      </div>
                    </div>
                    <p className="text-xs text-muted">
                      {Math.round(s.table_rows).toLocaleString()} rows,{" "}
                      {s.seq_scans.toLocaleString()} full scans
                      {s.query_ids.length > 0
                        ? `; helps ${s.query_ids.length} of the top queries`
                        : ""}
                      .
                      {s.estimate &&
                        ` Estimated cost ${s.estimate.cost_before.toFixed(0)} → ${s.estimate.cost_after.toFixed(0)} (${Math.round(s.estimate.improvement * 100)}% less)${s.estimate.uses_index ? "" : ", though the planner wouldn't use it"}.`}
                    </p>
                    <CodeBlock code={s.statement} wrap />
                    <div>
                      <Button
                        size="small"
                        variant="primary"
                        onClick={() => setReview(s)}
                      >
                        Review, create, or save as migration
                      </Button>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </Panel>
          {r.heavy_seq_scans.length > 0 && (
            <Panel title="Large tables read in full">
              <Table
                head={[
                  "Table",
                  "Rows",
                  "Full scans",
                  "Rows read",
                  "Index scans",
                ]}
              >
                {r.heavy_seq_scans.map((t) => (
                  <tr key={`${t.schema}.${t.table}`}>
                    <td className="px-3 py-2 font-mono text-xs">
                      {t.schema}.{t.table}
                    </td>
                    <td className="px-3 py-2 tabular-nums">
                      {Math.round(t.rows).toLocaleString()}
                    </td>
                    <td className="px-3 py-2 tabular-nums">
                      {t.seq_scans.toLocaleString()}
                    </td>
                    <td className="px-3 py-2 tabular-nums">
                      {t.seq_tup_read.toLocaleString()}
                    </td>
                    <td className="px-3 py-2 tabular-nums">
                      {t.idx_scans.toLocaleString()}
                    </td>
                  </tr>
                ))}
              </Table>
            </Panel>
          )}
          <Panel
            title="Unused indexes"
            actions={
              r.stats_since ? (
                <span className="text-xs text-muted">
                  never scanned since{" "}
                  {new Date(r.stats_since).toLocaleDateString()}
                </span>
              ) : undefined
            }
          >
            {r.unused.length === 0 ? (
              <p className="text-sm text-muted">Every index has been used.</p>
            ) : (
              <IndexTable items={r.unused.map((i) => ({ ...i, note: "" }))} />
            )}
          </Panel>
          <Panel title="Duplicate indexes">
            {r.duplicates.length === 0 ? (
              <p className="text-sm text-muted">No duplicates.</p>
            ) : (
              <IndexTable
                items={r.duplicates.map((i) => ({
                  ...i,
                  note: i.exact ? `identical to ${i.of}` : `covered by ${i.of}`,
                }))}
              />
            )}
          </Panel>
        </div>
      )}
      {review && (
        <SchemaReview
          projectId={projectId}
          change={review.change}
          title={`Index ${review.schema}.${review.table} (${review.columns.join(", ")})`}
          success="Index created"
          onClose={() => setReview(null)}
          onApplied={() => {
            setReview(null);
            void qc.invalidateQueries({ queryKey: ["insights", projectId] });
          }}
        />
      )}
    </Gate>
  );
}

function IndexTable({
  items,
}: {
  items: {
    schema: string;
    table: string;
    name: string;
    definition: string;
    bytes: number;
    note: string;
  }[];
}) {
  return (
    <Table head={["Index", "Table", "Size", ""]}>
      {items.map((i) => (
        <tr key={`${i.schema}.${i.name}`} data-testid={`index-${i.name}`}>
          <td className="px-3 py-2">
            <div className="font-mono text-xs">{i.name}</div>
            <div className="truncate text-xs text-muted" title={i.definition}>
              {i.definition}
            </div>
          </td>
          <td className="px-3 py-2 font-mono text-xs">
            {i.schema}.{i.table}
          </td>
          <td className="px-3 py-2 tabular-nums">{formatBytes(i.bytes)}</td>
          <td className="px-3 py-2 text-xs text-muted">{i.note}</td>
        </tr>
      ))}
    </Table>
  );
}

function BloatTab({ projectId }: { projectId: string }) {
  const toast = useOperationToast();
  const q = useQuery({
    queryKey: ["insights", projectId, "bloat"],
    queryFn: () => api.insightBloat(projectId),
    retry: false,
  });
  const [err, setErr] = useState<string | null>(null);
  const reclaim = async (schema: string, table: string) => {
    if (
      !window.confirm(
        `VACUUM FULL ${schema}.${table}? The table is locked (no reads or writes) until it finishes.`,
      )
    )
      return;
    setErr(null);
    try {
      const op = await api.reclaimSpace(projectId, schema, table);
      toast(op.id, `Reclaiming space in ${schema}.${table}`);
    } catch (e) {
      setErr(errorMessage(e));
    }
  };
  return (
    <Gate error={q.error}>
      {q.isPending ? (
        <Spinner />
      ) : (q.data?.items ?? []).length === 0 ? (
        <p className="text-sm text-muted">
          No table over 1 MB, or none analysed yet.
        </p>
      ) : (
        <div className="flex flex-col gap-3">
          <p className="text-xs text-muted">
            Estimated from row counts and widths; autovacuum reuses most dead
            space, and reclaiming returns it to the disk.
          </p>
          <Table
            head={[
              "Table",
              "Size",
              "Estimated bloat",
              "Dead rows",
              "Last vacuum",
              "",
            ]}
          >
            {q.data!.items.map((b) => (
              <tr
                key={`${b.schema}.${b.table}`}
                data-testid={`bloat-${b.table}`}
              >
                <td className="px-3 py-2 font-mono text-xs">
                  {b.schema}.{b.table}
                </td>
                <td className="px-3 py-2 tabular-nums">
                  {formatBytes(b.bytes)}
                </td>
                <td className="px-3 py-2 tabular-nums">
                  {formatBytes(b.bloat_bytes)} (
                  {Math.round(b.bloat_ratio * 100)}%)
                </td>
                <td className="px-3 py-2 tabular-nums">
                  {b.dead_rows.toLocaleString()}
                </td>
                <td className="px-3 py-2">
                  {b.last_autovacuum || b.last_vacuum
                    ? relativeTime((b.last_autovacuum ?? b.last_vacuum)!)
                    : "never"}
                </td>
                <td className="px-3 py-2 text-right">
                  {b.bloat_ratio >= 0.2 && (
                    <Button
                      size="small"
                      onClick={() => void reclaim(b.schema, b.table)}
                    >
                      Reclaim space
                    </Button>
                  )}
                </td>
              </tr>
            ))}
          </Table>
          {err && <Alert>{err}</Alert>}
        </div>
      )}
    </Gate>
  );
}

function LocksTab({ projectId }: { projectId: string }) {
  const q = useQuery({
    queryKey: ["insights", projectId, "locks"],
    queryFn: () => api.insightLocks(projectId),
    retry: false,
    refetchInterval: 5000,
  });
  return (
    <Gate error={q.error}>
      {q.isPending ? (
        <Spinner />
      ) : (q.data?.items ?? []).length === 0 ? (
        <p className="text-sm text-muted" data-testid="no-locks">
          Nothing is waiting on a lock. Refreshes every 5 seconds.
        </p>
      ) : (
        <div className="flex flex-col gap-3">
          {q.data!.items.map((b) => (
            <Panel
              key={b.blocked.pid}
              title={`Session ${b.blocked.pid} waiting ${ms(b.blocked.running_ms)}`}
              actions={<span className="text-xs text-muted">{b.lock}</span>}
            >
              <div
                className="flex flex-col gap-2 text-[13px]"
                data-testid="lock-block"
              >
                <CodeBlock code={b.blocked.query} wrap />
                {b.blockers.map((x) => (
                  <div
                    key={x.pid}
                    className="rounded-md border border-line p-2"
                  >
                    <div className="text-xs text-muted">
                      Held by session {x.pid} ({x.role || "?"}
                      {x.platform ? ", PGDock" : ""}), {x.state}
                      {x.in_transaction_ms > 0
                        ? `, in a transaction for ${ms(x.in_transaction_ms)}`
                        : ""}
                    </div>
                    <div
                      className="mt-1 truncate font-mono text-xs"
                      title={x.query}
                    >
                      {x.query}
                    </div>
                  </div>
                ))}
              </div>
            </Panel>
          ))}
        </div>
      )}
    </Gate>
  );
}
