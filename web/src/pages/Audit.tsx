import { useInfiniteQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api, errorMessage, type AuditList, type AuditQuery } from "../api/client";
import { useCurrentOrg } from "../lib/org";
import { Alert, Button, EmptyState, Input, PageHeading, Select, Spinner, StatusBadge, Table } from "../components/ui";
import { formatDate } from "../lib/format";

/** The platform audit log (platform admin). */
export function AuditPage() {
  return (
    <AuditView
      title="Platform audit log"
      subtitle="Platform-level actions, sign-ins, and break-glass access. Append-only."
      scope="platform"
      load={api.platformAudit}
    />
  );
}

/** The current organisation's audit log (owners and admins). */
export function OrgAuditPage() {
  const { org } = useCurrentOrg();
  if (!org) return <Spinner />;
  return <AuditView title="Audit log" subtitle={`Everything done in ${org.name}. Append-only.`} scope={org.id} load={(p) => api.orgAudit(org.id, p)} />;
}

export function AuditView({
  title,
  subtitle,
  scope,
  load,
  embedded,
}: {
  title: string;
  subtitle?: string;
  scope: string;
  load: (p: AuditQuery) => Promise<AuditList>;
  embedded?: boolean;
}) {
  const [action, setAction] = useState("");
  const [outcome, setOutcome] = useState("");
  const q = useInfiniteQuery({
    queryKey: ["audit", scope, { action, outcome }],
    queryFn: ({ pageParam }) => load({ action: action || undefined, outcome: outcome || undefined, before: pageParam, limit: 100 }),
    initialPageParam: undefined as number | undefined,
    getNextPageParam: (last) => last.next_before ?? undefined,
  });
  const items = q.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <>
      {embedded ? <h2 className="mb-2 text-[15px]">{title}</h2> : <PageHeading title={title} description={subtitle} />}
      <div className="mb-3 flex flex-wrap gap-2">
        <Input
          placeholder="Action prefix, e.g. project. or auth.login"
          value={action}
          onChange={(e) => setAction(e.target.value)}
          className="max-w-xs"
          aria-label="Filter by action"
        />
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
        <Table head={["When", "Action", "Outcome", "Who", "Target", "IP"]}>
          {items.map((a) => (
            <tr key={a.id}>
              <td className="px-3 py-1.5 whitespace-nowrap text-muted">{formatDate(a.created_at)}</td>
              <td className="px-3 py-1.5 font-mono text-xs" title={JSON.stringify(a.detail)}>
                {a.action}
                {a.break_glass && <span className="ml-2 rounded bg-danger px-1 text-white">break-glass</span>}
              </td>
              <td className="px-3 py-1.5">
                <StatusBadge status={a.outcome} />
              </td>
              <td className="px-3 py-1.5 text-muted">{a.user_email ?? (a.actor_kind === "system" ? "PGDock" : "—")}</td>
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
