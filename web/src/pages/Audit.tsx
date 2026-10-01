import { useInfiniteQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api, errorMessage } from "../api/client";
import { Alert, Button, EmptyState, Input, PageHeader, Select, Spinner, StatusBadge, Table } from "../components/ui";
import { formatDate } from "../lib/format";

export function AuditPage() {
  const [action, setAction] = useState("");
  const [outcome, setOutcome] = useState("");
  const q = useInfiniteQuery({
    queryKey: ["audit", { action, outcome }],
    queryFn: ({ pageParam }) => api.audit({ action: action || undefined, outcome: outcome || undefined, before: pageParam, limit: 100 }),
    initialPageParam: undefined as number | undefined,
    getNextPageParam: (last) => last.next_before ?? undefined,
  });
  const items = q.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <>
      <PageHeader title="Audit log" subtitle="Every change, sign-in, and refused request. Append-only." />
      <div className="mb-3 flex flex-wrap gap-2">
        <Input placeholder="Action prefix, e.g. project. or auth.login" value={action} onChange={(e) => setAction(e.target.value)} className="max-w-xs" aria-label="Filter by action" />
        <Select value={outcome} onChange={(e) => setOutcome(e.target.value)} aria-label="Filter by outcome">
          <option value="">All outcomes</option>
          <option value="success">success</option>
          <option value="failure">failure</option>
          <option value="denied">denied</option>
        </Select>
      </div>
      {q.isPending && <Spinner />}
      {q.isError && <Alert>{errorMessage(q.error)}</Alert>}
      {q.data && items.length === 0 && <EmptyState title="Nothing logged yet" />}
      {items.length > 0 && (
        <Table head={["When", "Action", "Outcome", "Operator", "Target", "IP"]}>
          {items.map((a) => (
            <tr key={a.id}>
              <td className="px-3 py-1.5 whitespace-nowrap text-muted">{formatDate(a.created_at)}</td>
              <td className="px-3 py-1.5 font-mono text-xs" title={JSON.stringify(a.detail)}>
                {a.action}
              </td>
              <td className="px-3 py-1.5">
                <StatusBadge status={a.outcome} />
              </td>
              <td className="px-3 py-1.5 text-muted">{a.operator_email ?? "—"}</td>
              <td className="px-3 py-1.5 font-mono text-xs text-muted">{a.target_id ? `${a.target_type}:${a.target_id.slice(0, 8)}` : "—"}</td>
              <td className="px-3 py-1.5 font-mono text-xs text-muted">{a.ip ?? "—"}</td>
            </tr>
          ))}
        </Table>
      )}
      {q.hasNextPage && (
        <div className="mt-3">
          <Button onClick={() => q.fetchNextPage()} busy={q.isFetchingNextPage}>
            Load more
          </Button>
        </div>
      )}
    </>
  );
}
