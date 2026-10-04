import { Link, useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api, errorMessage } from "../api/client";
import { OperationLog } from "../components/OperationLog";
import { Alert, Panel, EmptyState, PageHeading, Select, Spinner, StatusBadge, Table } from "../components/ui";
import { formatDate, relativeTime } from "../lib/format";
import { useCurrentOrg } from "../lib/org";
import { sessionQuery } from "../lib/session";
import { useOperationStream } from "../lib/useOperationStream";

export function OperationsPage() {
  const [status, setStatus] = useState("");
  const [platform, setPlatform] = useState(false);
  const { org } = useCurrentOrg();
  const { data: session } = useQuery(sessionQuery);
  const q = useQuery({
    queryKey: ["operations", { status, org: org?.id, platform }],
    queryFn: () => api.operations({ status: status || undefined, limit: 100, ...(platform ? { platform: "true" } : { org: org?.id }) }),
    refetchInterval: 5000,
    enabled: !!org,
  });
  return (
    <>
      <PageHeading title="Operations" description="Every long action runs as an operation with a step log." />
      <div className="mb-3 flex flex-wrap items-center gap-3">
        {session?.user?.platform_role === "platform_admin" && (
          <Select value={platform ? "platform" : "org"} onChange={(e) => setPlatform(e.target.value === "platform")} aria-label="Scope">
            <option value="org">{org?.name ?? "This organisation"}</option>
            <option value="platform">Platform (nodes, restore tests, self-backups)</option>
          </Select>
        )}
        <Select value={status} onChange={(e) => setStatus(e.target.value)} aria-label="Filter by status">
          <option value="">All statuses</option>
          {["queued", "running", "succeeded", "failed"].map((s) => (
            <option key={s}>{s}</option>
          ))}
        </Select>
      </div>
      {q.isPending && <Spinner />}
      {q.isError && <Alert>{errorMessage(q.error)}</Alert>}
      {q.data && q.data.items.length === 0 && <EmptyState title="No operations yet" />}
      {q.data && q.data.items.length > 0 && (
        <Table head={["Kind", "Status", "Attempts", "Started", "Finished"]}>
          {q.data.items.map((o) => (
            <tr key={o.id} className="hover:bg-surface-2">
              <td className="px-3 py-2">
                <Link to="/operations/$id" params={{ id: o.id }} className="font-medium hover:underline">
                  {o.kind}
                </Link>
              </td>
              <td className="px-3 py-2">
                <StatusBadge status={o.status} />
              </td>
              <td className="px-3 py-2 text-muted">{o.attempts}</td>
              <td className="px-3 py-2 text-muted" title={formatDate(o.created_at)}>
                {relativeTime(o.created_at)}
              </td>
              <td className="px-3 py-2 text-muted">{o.finished_at ? relativeTime(o.finished_at) : "—"}</td>
            </tr>
          ))}
        </Table>
      )}
    </>
  );
}

export function OperationDetailPage() {
  const { id } = useParams({ from: "/app/operations/$id" });
  const q = useQuery({ queryKey: ["operation", id], queryFn: () => api.operation(id) });
  const finished = q.data && (q.data.status === "succeeded" || q.data.status === "failed");
  const stream = useOperationStream(q.data && !finished ? id : null);
  if (q.isPending) return <Spinner />;
  if (q.isError) return <Alert>{errorMessage(q.error)}</Alert>;
  const o = q.data;
  const log = finished ? o.log : stream.log;
  const status = (finished ? o.status : stream.status) ?? o.status;
  const error = finished ? o.error : stream.error;
  return (
    <>
      <PageHeading
        title={
          <span className="flex items-center gap-2">
            {o.kind} <StatusBadge status={status} />
          </span>
        }
        description={<span className="font-mono text-xs">{o.id}</span>}
      />
      <div className="flex flex-col gap-4">
        <Panel>
          <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
            {o.project_id && (
              <>
                <dt className="text-muted">Project</dt>
                <dd>
                  <Link to="/projects/$id" params={{ id: o.project_id }} className="font-mono text-xs text-accent hover:underline">
                    {o.project_id}
                  </Link>
                </dd>
              </>
            )}
            <dt className="text-muted">Created</dt>
            <dd>{formatDate(o.created_at)}</dd>
            <dt className="text-muted">Finished</dt>
            <dd>{formatDate(o.finished_at)}</dd>
            <dt className="text-muted">Attempts</dt>
            <dd>{finished ? o.attempts : stream.attempts || o.attempts}</dd>
          </dl>
          {status === "failed" && error && (
            <div className="mt-3">
              <Alert title="Failed">{error}</Alert>
            </div>
          )}
        </Panel>
        <Panel title={finished ? "Log" : "Live log"}>
          <OperationLog log={log} live={!finished} />
        </Panel>
      </div>
    </>
  );
}
