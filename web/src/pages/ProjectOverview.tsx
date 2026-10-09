import { Link, Outlet, useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import {
  ApiRequestError,
  api,
  errorMessage,
  type InstanceSummary,
  type Project,
} from "../api/client";
import { StorageBanner } from "../components/TenancyCards";
import { VersionBanner } from "../components/VersionBanner";
import { LifecycleBanner } from "../components/FreeTier";
import { SquareTerminal, Table2 } from "lucide-react";
import { LineChart, type ChartSeries } from "../components/LineChart";
import { fmt } from "../components/Metrics";
import {
  Alert,
  Badge,
  Button,
  CopyField,
  EmptyState,
  KeyValues,
  Page,
  Panel,
  StateBadge,
  Stat,
  StatusBadge,
  PageSkeleton,
} from "../components/ui";
import { formatDate, relativeTime } from "../lib/format";

export function useProject() {
  const { id } = useParams({ from: "/app/projects/$id" });
  return useQuery({
    queryKey: ["project", id],
    queryFn: () => api.project(id),
    refetchInterval: (q) => {
      const d = q.state.data;
      if (d && d.status !== "active") return 2000;
      // Paused or archived: notice a resume within a few seconds.
      if (d && (d.lifecycle ?? "active") !== "active") return 5000;
      return 30_000;
    },
  });
}

export function ProjectLayout() {
  const q = useProject();
  if (q.isPending) return <PageSkeleton />;
  if (q.isError) {
    // Another organisation's project and a missing one look the same (V2 §4.2).
    if (q.error instanceof ApiRequestError && q.error.status === 404)
      return (
        <div data-testid="project-not-found">
          <EmptyState title="Project not found">
            It doesn't exist, or you don't have access to it.
          </EmptyState>
        </div>
      );
    return <Alert>{errorMessage(q.error)}</Alert>;
  }
  const p = q.data;
  // Navigation is the shell's rail and section menus (docs/ui-redesign.md).
  return (
    <>
      <LifecycleBanner p={p} />
      <StorageBanner p={p} />
      <VersionBanner p={p} />
      <Outlet />
    </>
  );
}

/** The small charts on the overview: the last 24 hours, two metrics. */
function OverviewCharts({ p }: { p: Project }) {
  const q = useQuery({
    queryKey: ["metrics", "project", p.id, "24h"],
    queryFn: () => api.projectMetrics(p.id, "24h"),
    refetchInterval: 60_000,
    enabled: p.status === "active",
  });
  const now = q.dataUpdatedAt || Date.now();
  const by = new Map((q.data?.series ?? []).map((s) => [s.metric, s.points]));
  const charts: {
    title: string;
    series: ChartSeries[];
    format: (v: number) => string;
  }[] = [
    {
      title: "Database size",
      series: [
        {
          name: "size",
          color: "var(--accent)",
          points: by.get("size_bytes") ?? [],
        },
      ],
      format: fmt.bytes,
    },
    {
      title: "Connections",
      series: [
        {
          name: "active",
          color: "var(--accent)",
          points: by.get("connections_active") ?? [],
        },
        {
          name: "pooler clients",
          color: "var(--ok)",
          points: by.get("pooler_clients") ?? [],
        },
      ],
      format: fmt.count,
    },
  ];
  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      {charts.map((c) => {
        const latest = c.series[0].points.at(-1)?.value;
        return (
          <Panel
            key={c.title}
            title={c.title}
            description="Last 24 hours"
            actions={
              <Link
                to="/projects/$id/metrics"
                params={{ id: p.id }}
                className="text-[12px] text-muted hover:text-fg"
              >
                {latest != null ? c.format(latest) : "Reports"}
              </Link>
            }
          >
            <LineChart
              series={c.series}
              from={now - 86400_000}
              to={now}
              format={c.format}
              label={c.title}
              compact
            />
          </Panel>
        );
      })}
    </div>
  );
}

export function ProjectOverviewPage() {
  const { data: p } = useProject();
  const ops = useQuery({
    queryKey: ["operations", { project: p?.id }],
    queryFn: () =>
      api.operations({ org: p!.org_id, project_id: p!.id, limit: 8 }),
    enabled: !!p,
    refetchInterval: 5000,
  });
  if (!p) return null;
  const s = p.settings;
  return (
    <Page
      title={
        <span className="flex items-center gap-3">
          {p.name} <StatusBadge status={p.status} />
        </span>
      }
      description={
        <span className="flex flex-wrap items-center gap-2">
          <span className="font-mono">{p.db_name}</span>
          {p.my_role && <Badge>{p.my_role.replace("_", "-")}</Badge>}
          {p.parent_project_id && <Badge tone="accent">branch</Badge>}
          {p.sensitive_data && <Badge tone="warn">sensitive data</Badge>}
          {p.description && <span>· {p.description}</span>}
        </span>
      }
      actions={
        <>
          <Link to="/projects/$id/tables" params={{ id: p.id }}>
            <Button icon={<Table2 className="h-3.5 w-3.5" />}>
              Table Editor
            </Button>
          </Link>
          <Link to="/projects/$id/sql" params={{ id: p.id }}>
            <Button icon={<SquareTerminal className="h-3.5 w-3.5" />}>
              SQL Editor
            </Button>
          </Link>
        </>
      }
      testId="project-overview"
    >
      {p.retired_copy_until &&
        (p.tier === "dedicated" ? (
          <Alert tone="accent" title="Promoted to the dedicated tier">
            The previous shared copy is kept read-only until{" "}
            {formatDate(p.retired_copy_until)}, then dropped.
          </Alert>
        ) : (
          <Alert tone="accent" title="Demoted to the shared tier">
            The previous dedicated instance is kept stopped until{" "}
            {formatDate(p.retired_copy_until)}, then destroyed, which releases
            it from the organisation&rsquo;s dedicated allowance.
          </Alert>
        ))}
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Stat
          label="Tier"
          value={p.tier === "dedicated" ? "Dedicated" : "Shared"}
          hint={p.tier === "dedicated" ? "Its own instance" : "Shared cluster"}
        />
        <Stat
          label="Max connections"
          value={s.connection_limit}
          hint={`pool of ${s.pool_size}`}
        />
        <Stat
          label="Last backup"
          value={p.last_backup_at ? relativeTime(p.last_backup_at) : "never"}
          hint={
            p.last_backup_at
              ? formatDate(p.last_backup_at)
              : "Back up from Database → Backups"
          }
        />
        <Stat
          label="SQL console"
          value={s.console_read_only ? "Read-only" : "Read/write"}
          hint={`statement timeout ${s.statement_timeout || "unset"}`}
        />
      </div>
      <OverviewCharts p={p} />
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Panel
          title="Connect"
          actions={
            <Link
              to="/projects/$id/connect"
              params={{ id: p.id }}
              className="text-[12px] text-accent-text underline underline-offset-2 hover:no-underline"
            >
              Snippets
            </Link>
          }
        >
          <div className="flex flex-col gap-3">
            <CopyField
              label="Pooled URL (password not shown)"
              value={p.connection.pooled_url}
            />
            <CopyField label="Session URL" value={p.connection.session_url} />
          </div>
        </Panel>
        <Panel
          title="Recent operations"
          actions={
            <Link
              to="/projects/$id/logs"
              params={{ id: p.id }}
              className="text-[12px] text-accent-text underline underline-offset-2 hover:no-underline"
            >
              All logs
            </Link>
          }
          bodyClassName="px-0 py-0"
        >
          {ops.data && ops.data.items.length > 0 ? (
            <ul className="divide-y divide-line">
              {ops.data.items.map((o) => (
                <li
                  key={o.id}
                  className="flex items-center gap-3 px-5 py-2 text-[13px]"
                >
                  <Link
                    to="/operations/$id"
                    params={{ id: o.id }}
                    className="flex-1 hover:underline"
                  >
                    {o.kind}
                  </Link>
                  <StatusBadge status={o.status} />
                  <span className="w-24 text-right text-[12px] text-muted">
                    {relativeTime(o.created_at)}
                  </span>
                </li>
              ))}
            </ul>
          ) : (
            <p className="px-5 py-4 text-[13px] text-muted">None yet.</p>
          )}
        </Panel>
      </div>
      {p.tier === "dedicated" && p.instance && (
        <InstancePanel projectId={p.id} instance={p.instance} />
      )}
    </Page>
  );
}

/** A dedicated project's own instance (spec §4.2), with start/stop/restart. */
export function InstancePanel({
  projectId,
  instance: i,
}: {
  projectId: string;
  instance: InstanceSummary;
}) {
  const qc = useQueryClient();
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const act = async (action: "start" | "stop" | "restart") => {
    if (
      action === "stop" &&
      !window.confirm(
        "Stop the instance? Clients cannot connect until it starts again.",
      )
    )
      return;
    setBusy(action);
    setErr(null);
    try {
      await api.instanceAction(projectId, action);
      await qc.invalidateQueries({ queryKey: ["project", projectId] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(null);
    }
  };
  return (
    <Panel
      title="Instance"
      actions={
        <>
          <StateBadge state={i.status} />
          {i.status === "stopped" ? (
            <Button
              size="tiny"
              busy={busy === "start"}
              onClick={() => act("start")}
            >
              Start
            </Button>
          ) : (
            <>
              <Button
                size="tiny"
                busy={busy === "restart"}
                onClick={() => act("restart")}
              >
                Restart
              </Button>
              <Button
                size="tiny"
                busy={busy === "stop"}
                onClick={() => act("stop")}
              >
                Stop
              </Button>
            </>
          )}
        </>
      }
    >
      <KeyValues
        testId="instance-card"
        items={[
          [
            "Node",
            <Link
              key="n"
              to="/nodes/$id"
              params={{ id: i.node_id }}
              className="hover:underline"
            >
              {i.node_name}
            </Link>,
          ],
          [
            "Size",
            `${i.profile} · ${i.cpus} CPU · ${(i.memory_mb ?? 0) / 1024} GB memory · ${i.volume_gb} GB volume`,
          ],
          ["Engine", "PostgreSQL 18 with WAL-G continuous archiving"],
        ]}
      />
      {err && (
        <div className="mt-3">
          <Alert>{err}</Alert>
        </div>
      )}
    </Panel>
  );
}
