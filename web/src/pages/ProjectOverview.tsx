import { Link, Outlet, useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { ApiRequestError, api, errorMessage, type InstanceSummary } from "../api/client";
import { StorageBanner } from "../components/TenancyCards";
import { Alert, Badge, Button, Card, CopyField, EmptyState, PageHeader, Spinner, StateBadge, StatusBadge, Table } from "../components/ui";
import { formatBytes, formatDate, relativeTime } from "../lib/format";

export function useProject() {
  const { id } = useParams({ from: "/app/projects/$id" });
  return useQuery({
    queryKey: ["project", id],
    queryFn: () => api.project(id),
    refetchInterval: (q) => (q.state.data && q.state.data.status !== "active" ? 2000 : 30_000),
  });
}

export function ProjectLayout() {
  const q = useProject();
  if (q.isPending) return <Spinner />;
  if (q.isError) {
    // Another organisation's project and a missing one look the same (V2 §4.2).
    if (q.error instanceof ApiRequestError && q.error.status === 404)
      return (
        <div data-testid="project-not-found">
          <EmptyState title="Project not found">It doesn't exist, or you don't have access to it.</EmptyState>
        </div>
      );
    return <Alert>{errorMessage(q.error)}</Alert>;
  }
  const p = q.data;
  const admin = p.my_role === "admin";
  const tabs = [
    { to: "/projects/$id", label: "Overview", exact: true },
    { to: "/projects/$id/connect", label: "Connect" },
    { to: "/projects/$id/sql", label: "SQL" },
    { to: "/projects/$id/tables", label: "Tables" },
    { to: "/projects/$id/backups", label: "Backups" },
    { to: "/projects/$id/branches", label: p.parent_project_id ? "Branch" : "Branches" },
    ...(admin || p.my_role === "developer"
      ? ([
          { to: "/projects/$id/webhooks", label: "Webhooks" },
          { to: "/projects/$id/jobs", label: "Jobs" },
        ] as const)
      : []),
    { to: "/projects/$id/metrics", label: "Metrics" },
    { to: "/projects/$id/members", label: "Members" },
    ...(admin ? [{ to: "/projects/$id/settings", label: "Settings" } as const] : []),
  ] as const;
  return (
    <>
      <PageHeader
        title={
          <span className="flex items-center gap-2">
            {p.name} <StatusBadge status={p.status} />
          </span>
        }
        subtitle={
          <span className="flex items-center gap-2">
            <span className="font-mono">{p.db_name}</span>
            {p.my_role && <Badge>{p.my_role.replace("_", "-")}</Badge>}
            {p.parent_project_id && <Badge tone="accent">branch</Badge>}
            {p.sensitive_data && <Badge tone="warn">sensitive data</Badge>}
          </span>
        }
      />
      <StorageBanner p={p} />
      <nav aria-label="Project" className="mb-5 flex gap-1 overflow-x-auto border-b border-line">
        {tabs.map((t) => (
          <Link
            key={t.to}
            to={t.to}
            params={{ id: p.id }}
            activeOptions={{ exact: "exact" in t }}
            className="-mb-px border-b-2 border-transparent px-3 py-2 text-sm text-muted hover:text-fg"
            activeProps={{ className: "!border-accent !text-fg font-medium" }}
          >
            {t.label}
          </Link>
        ))}
      </nav>
      <Outlet />
    </>
  );
}

export function ProjectOverviewPage() {
  const { data: p } = useProject();
  const ops = useQuery({
    queryKey: ["operations", { project: p?.id }],
    queryFn: () => api.operations({ org: p!.org_id, project_id: p!.id, limit: 10 }),
    enabled: !!p,
    refetchInterval: 5000,
  });
  if (!p) return null;
  const s = p.settings;
  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      {p.retired_copy_until && (
        <div className="lg:col-span-2">
          {p.tier === "dedicated" ? (
            <Alert tone="accent" title="Promoted to the dedicated tier">
              The previous shared copy is kept read-only until {formatDate(p.retired_copy_until)}, then dropped.
            </Alert>
          ) : (
            <Alert tone="accent" title="Demoted to the shared tier">
              The previous dedicated instance is kept stopped until {formatDate(p.retired_copy_until)}, then destroyed, which releases it from the
              organisation&rsquo;s dedicated allowance.
            </Alert>
          )}
        </div>
      )}
      <Card title="Details">
        <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
          <dt className="text-muted">Tier</dt>
          <dd>
            <Badge>{p.tier === "dedicated" ? "Dedicated" : "Shared"}</Badge>
          </dd>
          <dt className="text-muted">Owner role</dt>
          <dd className="font-mono text-xs">{p.owner_role}</dd>
          <dt className="text-muted">Created</dt>
          <dd>{formatDate(p.created_at)}</dd>
          {p.description && (
            <>
              <dt className="text-muted">Description</dt>
              <dd>{p.description}</dd>
            </>
          )}
        </dl>
      </Card>
      <Card title="Connect" actions={<Link to="/projects/$id/connect" params={{ id: p.id }} className="text-xs text-accent hover:underline">Snippets</Link>}>
        <div className="flex flex-col gap-3">
          <CopyField label="Pooled URL (password not shown)" value={p.connection.pooled_url} />
          <CopyField label="Session URL" value={p.connection.session_url} />
        </div>
      </Card>
      <Card title="Guardrails" actions={<Link to="/projects/$id/settings" params={{ id: p.id }} className="text-xs text-accent hover:underline">Edit</Link>}>
        <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
          <dt className="text-muted">Max connections</dt>
          <dd>{s.connection_limit}</dd>
          <dt className="text-muted">Pool size</dt>
          <dd>{s.pool_size}</dd>
          <dt className="text-muted">Statement timeout</dt>
          <dd>{s.statement_timeout || "unset"}</dd>
          <dt className="text-muted">Idle in transaction</dt>
          <dd>{s.idle_in_transaction_session_timeout || "unset"}</dd>
          <dt className="text-muted">Disk warning</dt>
          <dd>{formatBytes(s.disk_warn_bytes)}</dd>
          <dt className="text-muted">SQL console</dt>
          <dd>{s.console_read_only ? "read-only" : "read/write"}</dd>
        </dl>
      </Card>
      {p.tier === "dedicated" && p.instance && <InstanceCard projectId={p.id} instance={p.instance} />}
      <Card title="Recent operations">
        {ops.data && ops.data.items.length > 0 ? (
          <Table head={["Kind", "Status", "When"]}>
            {ops.data.items.map((o) => (
              <tr key={o.id}>
                <td className="px-3 py-1.5">
                  <Link to="/operations/$id" params={{ id: o.id }} className="hover:underline">
                    {o.kind}
                  </Link>
                </td>
                <td className="px-3 py-1.5">
                  <StatusBadge status={o.status} />
                </td>
                <td className="px-3 py-1.5 text-muted">{relativeTime(o.created_at)}</td>
              </tr>
            ))}
          </Table>
        ) : (
          <p className="text-sm text-muted">None yet.</p>
        )}
      </Card>
    </div>
  );
}

/** A dedicated project's own instance (spec §4.2), with start/stop/restart. */
function InstanceCard({ projectId, instance: i }: { projectId: string; instance: InstanceSummary }) {
  const qc = useQueryClient();
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const act = async (action: "start" | "stop" | "restart") => {
    if (action === "stop" && !window.confirm("Stop the instance? Clients cannot connect until it starts again.")) return;
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
    <Card title="Instance" actions={<StateBadge state={i.status} />}>
      <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm" data-testid="instance-card">
        <dt className="text-muted">Node</dt>
        <dd>
          <Link to="/nodes/$id" params={{ id: i.node_id }} className="hover:underline">
            {i.node_name}
          </Link>
        </dd>
        <dt className="text-muted">Size</dt>
        <dd>
          {i.profile} · {i.cpus} CPU · {(i.memory_mb ?? 0) / 1024} GB memory · {i.volume_gb} GB volume
        </dd>
        <dt className="text-muted">Engine</dt>
        <dd>PostgreSQL 18 with WAL-G continuous archiving</dd>
      </dl>
      <div className="mt-3 flex gap-2">
        {i.status === "stopped" ? (
          <Button className="text-xs" busy={busy === "start"} onClick={() => act("start")}>
            Start
          </Button>
        ) : (
          <>
            <Button className="text-xs" busy={busy === "restart"} onClick={() => act("restart")}>
              Restart
            </Button>
            <Button className="text-xs" busy={busy === "stop"} onClick={() => act("stop")}>
              Stop
            </Button>
          </>
        )}
      </div>
      {err && (
        <div className="mt-3">
          <Alert>{err}</Alert>
        </div>
      )}
    </Card>
  );
}
