import { useQuery } from "@tanstack/react-query";
import { Activity } from "lucide-react";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { api, errorMessage, type Operation } from "../api/client";
import { OperationLog } from "../components/OperationLog";
import { Alert, KeyValues, Page, Panel, Select, StatusBadge, cx, EmptyState, TableSkeleton } from "../components/ui";
import { formatDate, relativeTime } from "../lib/format";
import { useOperationStream } from "../lib/useOperationStream";
import { useProject } from "./ProjectOverview";

const STATUSES = ["", "queued", "running", "succeeded", "failed", "cancelled"];

function took(o: Operation): string {
  if (!o.finished_at) return "";
  const s = (Date.parse(o.finished_at) - Date.parse(o.created_at)) / 1000;
  return s < 60 ? `${s.toFixed(1)} s` : `${Math.round(s / 60)} min`;
}

/** Project → Logs (docs/ui-redesign.md, phase 4): the project's
 * operations, newest first, and the selected one's step log, live while it
 * runs. */
export function ProjectLogsPage() {
  const { data: p } = useProject();
  const [status, setStatus] = useState("");
  const [picked, setPicked] = useState<string | null>(null);
  const ops = useQuery({
    queryKey: ["operations", { project: p?.id, status, logs: true }],
    queryFn: () => api.operations({ org: p!.org_id, project_id: p!.id, status: status || undefined, limit: 100 }),
    enabled: !!p,
    refetchInterval: 5000,
  });
  const items = ops.data?.items ?? [];
  const sel = items.find((o) => o.id === picked) ?? items[0];
  const stream = useOperationStream(sel?.id);
  if (!p) return null;
  return (
    <Page
      title="Logs"
      description="Everything PGDock has done to this project: provisioning, backups, restores, settings and moves, with each step's log."
      actions={
        <Select aria-label="Status" value={status} onChange={(e) => setStatus(e.target.value)}>
          {STATUSES.map((s) => (
            <option key={s} value={s}>
              {s ? s : "All statuses"}
            </option>
          ))}
        </Select>
      }
      testId="project-logs"
    >
      {ops.isPending ? (
        <TableSkeleton rows={6} cols={3} />
      ) : ops.isError ? (
        <Alert>{errorMessage(ops.error)}</Alert>
      ) : items.length === 0 ? (
        <EmptyState title={status ? `No ${status} operations` : "No operations yet"} icon={<Activity />}>
          Backups, restores and other long actions on this project show up here with their step logs.
        </EmptyState>
      ) : (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-[minmax(0,2fr)_minmax(0,3fr)]">
          <Panel bodyClassName="p-0">
            <ul className="max-h-[70vh] divide-y divide-line overflow-y-auto" data-testid="log-list">
              {items.map((o) => (
                <li key={o.id}>
                  <button
                    type="button"
                    onClick={() => setPicked(o.id)}
                    aria-current={o.id === sel?.id || undefined}
                    className={cx("flex w-full items-center gap-3 px-4 py-2.5 text-left text-[13px] hover:bg-surface-2", o.id === sel?.id && "bg-surface-3")}
                  >
                    <span className="flex-1 truncate">{o.kind}</span>
                    <StatusBadge status={o.status} />
                    <span className="w-20 text-right text-[12px] text-muted" title={formatDate(o.created_at)}>
                      {relativeTime(o.created_at)}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          </Panel>
          {sel && (
            <Panel
              title={sel.kind}
              actions={
                <Link to="/operations/$id" params={{ id: sel.id }} className="text-[12px] text-accent-text underline underline-offset-2 hover:no-underline">
                  Open
                </Link>
              }
              testId="log-detail"
            >
              <div className="flex flex-col gap-4">
                <KeyValues
                  items={[
                    ["Status", <StatusBadge key="s" status={stream.status ?? sel.status} />],
                    ["Created", formatDate(sel.created_at)],
                    ["Took", took(sel) || "—"],
                    ["Attempts", String(stream.attempts || sel.attempts)],
                  ]}
                />
                {(stream.error ?? sel.error) && <Alert>{stream.error ?? sel.error}</Alert>}
                <OperationLog log={stream.log} live={!stream.done} />
              </div>
            </Panel>
          )}
        </div>
      )}
    </Page>
  );
}
