import { Link, useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api, errorMessage } from "../api/client";
import { OperationLog } from "../components/OperationLog";
import { Alert, Card, EmptyState, PageHeader, Select, Spinner, StatusBadge, Table } from "../components/ui";
import { formatDate, relativeTime } from "../lib/format";
import { useOperationStream } from "../lib/useOperationStream";

export function OperationsPage() {
  const [status, setStatus] = useState("");
  const q = useQuery({
    queryKey: ["operations", { status }],
    queryFn: () => api.operations({ status: status || undefined, limit: 100 }),
    refetchInterval: 5000,
  });
  return (
    <>
      <PageHeader title="Operations" subtitle="Every long action runs as an operation with a step log." />
      <div className="mb-3">
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
      <PageHeader
        title={
          <span className="flex items-center gap-2">
            {o.kind} <StatusBadge status={status} />
          </span>
        }
        subtitle={<span className="font-mono text-xs">{o.id}</span>}
      />
      <div className="flex flex-col gap-4">
        <Card>
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
        </Card>
        <Card title={finished ? "Log" : "Live log"}>
          <OperationLog log={log} live={!finished} />
        </Card>
      </div>
    </>
  );
}
