import { Link, Outlet, useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { api, errorMessage } from "../api/client";
import { Alert, Badge, Card, CopyField, PageHeader, Spinner, StatusBadge, Table } from "../components/ui";
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
  if (q.isError) return <Alert>{errorMessage(q.error)}</Alert>;
  const p = q.data;
  const tabs = [
    { to: "/projects/$id", label: "Overview", exact: true },
    { to: "/projects/$id/connect", label: "Connect" },
    { to: "/projects/$id/settings", label: "Settings" },
  ] as const;
  return (
    <>
      <PageHeader
        title={
          <span className="flex items-center gap-2">
            {p.name} <StatusBadge status={p.status} />
          </span>
        }
        subtitle={<span className="font-mono">{p.db_name}</span>}
      />
      <nav aria-label="Project" className="mb-5 flex gap-1 border-b border-line">
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
    queryFn: () => api.operations({ project_id: p!.id, limit: 10 }),
    enabled: !!p,
    refetchInterval: 5000,
  });
  if (!p) return null;
  const s = p.settings;
  return (
    <div className="grid gap-4 lg:grid-cols-2">
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
