import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, errorMessage, type DemotePreflight, type Project } from "../api/client";
import { formatBytes } from "../lib/format";
import { useOperationStream } from "../lib/useOperationStream";
import { OperationLog } from "./OperationLog";
import { Alert, Badge, Button, Panel, SidePanel, Field, Select, StatusBadge } from "./ui";

const checkLabel: Record<DemotePreflight["checks"][number]["name"], string> = {
  size: "Size",
  extensions: "Extensions",
  roles: "Roles",
  allowance: "Dedicated allowance",
  connections: "Connections",
  settings: "Settings",
  capacity: "Capacity",
};

const checkTone = { ok: "ok", warning: "warn", blocked: "danger" } as const;
const checkMark = { ok: "OK", warning: "Warning", blocked: "Blocked" } as const;

function duration(seconds: number): string {
  if (seconds < 90) return `about ${seconds} s`;
  return `about ${Math.round(seconds / 60)} min`;
}

/**
 * The demotion wizard (V2 §5): the preflight checklist, what resets, and
 * the confirmation, then the operation live.
 */
export function DemoteCard({ p }: { p: Project }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [nodeId, setNodeId] = useState("");
  const [consoleWritable, setConsoleWritable] = useState(false);
  const [accepted, setAccepted] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [opId, setOpId] = useState<string | undefined>();
  const stream = useOperationStream(opId);
  const body = { node_id: nodeId || undefined, console_writable: consoleWritable || undefined };
  const pf = useQuery({
    queryKey: ["demote", p.id, nodeId, consoleWritable],
    queryFn: () => api.demotePreflight(p.id, body),
    enabled: open && !opId,
  });
  const nodes = useQuery({ queryKey: ["nodes"], queryFn: api.nodes, enabled: open, retry: false });
  const targets = (nodes.data?.items ?? []).filter((n) => (n.role === "shared" || n.role === "both") && n.agent.registered);
  const warnings = (pf.data?.checks ?? []).filter((c) => c.status === "warning");

  const demote = async () => {
    setBusy(true);
    setErr(null);
    try {
      const op = await api.demote(p.id, { ...body, accept_warnings: warnings.length > 0 ? accepted : undefined });
      setOpId(op.id);
      await qc.invalidateQueries({ queryKey: ["project", p.id] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  // Dedicated projects only; once started it stays to show the outcome.
  if (!opId && (p.tier !== "dedicated" || (p.status !== "active" && p.status !== "demoting"))) return null;

  if (opId) {
    return (
      <Panel title="Demotion" actions={stream.status && <StatusBadge status={stream.status} />}>
        <OperationLog log={stream.log} live={!stream.done} />
        {stream.status === "succeeded" && (
          <p className="mt-3 text-sm text-ok" data-testid="demote-done">
            Demoted. The connection strings and passwords are unchanged; the stopped dedicated instance is kept for 48 hours, then destroyed.
          </p>
        )}
        {stream.status === "failed" && (
          <div className="mt-3">
            <Alert title="Demotion failed">{stream.error}. The project stays on its dedicated instance with no data lost.</Alert>
          </div>
        )}
      </Panel>
    );
  }

  return (
    <Panel title="Move back to shared">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-muted">
          Demote this project to the shared tier to free its instance. The URL and every password stay the same; point-in-time recovery ends.
        </p>
        <Button onClick={() => setOpen(true)}>Demote…</Button>
      </div>
      <SidePanel
        open={open}
        onOpenChange={setOpen}
        size="large"
        title="Move back to shared"
        footer={
          <>
            <Button onClick={() => setOpen(false)}>Cancel</Button>
            <Button variant="primary" onClick={demote} busy={busy} disabled={!pf.data?.eligible || (warnings.length > 0 && !accepted)}>
              Demote now
            </Button>
          </>
        }
      >
        <div className="flex flex-col gap-4">
          {targets.length > 1 && (
            <Field label="Target shared cluster">
              {(id) => (
                <Select id={id} value={nodeId} onChange={(e) => setNodeId(e.target.value)}>
                  <option value="">Most free capacity</option>
                  {targets.map((n) => (
                    <option key={n.id} value={n.id}>
                      {n.name}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
          )}
          {pf.isLoading && <p className="text-sm text-muted">Checking eligibility…</p>}
          {pf.isError && <Alert>{errorMessage(pf.error)}</Alert>}
          {pf.data && (
            <>
              <ul className="flex flex-col gap-2 text-sm" data-testid="demote-checks">
                {pf.data.checks.map((c) => (
                  <li key={c.name} className="flex items-start gap-3" data-testid={`demote-check-${c.name}`} data-status={c.status}>
                    <Badge tone={checkTone[c.status]}>{checkMark[c.status]}</Badge>
                    <span>
                      <span className="font-medium">{checkLabel[c.name]}</span>
                      <span className="block text-muted">{c.message}</span>
                    </span>
                  </li>
                ))}
              </ul>
              {pf.data.eligible && (
                <Alert tone="accent" title={`Estimated write freeze: ${duration(pf.data.estimated_downtime_seconds)}`}>
                  The database is {formatBytes(pf.data.size_bytes)}
                  {pf.data.target && <> and moves to node {pf.data.target.node_name}</>}. During the freeze, apps on the pooled URL wait rather than fail;
                  session connections are dropped once and reconnect.
                </Alert>
              )}
              {pf.data.resets.length > 0 && (
                <div className="text-sm">
                  <p className="font-medium">Guardrails reset to the shared defaults</p>
                  <ul className="list-disc pl-5 text-muted" data-testid="demote-resets">
                    {pf.data.resets.map((r) => (
                      <li key={r}>{r}</li>
                    ))}
                  </ul>
                </div>
              )}
              <Alert tone="warn" title="Point-in-time recovery ends at the demotion">
                The project switches to nightly logical backups, the first taken right away. Existing base backups stay restorable, labelled &ldquo;dedicated
                (pre-demotion)&rdquo;, until their retention ends. The stopped instance is kept for {pf.data.retain_hours} hours as a rollback option, then
                destroyed, which releases it from your dedicated allowance.
              </Alert>
              {p.settings.console_read_only && (
                <label className="flex items-start gap-2 text-sm">
                  <input type="checkbox" className="mt-1" checked={consoleWritable} onChange={(e) => setConsoleWritable(e.target.checked)} />
                  <span>
                    <span className="font-medium">Make the SQL console read-write</span>
                    <span className="block text-muted">The shared default. Left unticked, it stays read-only.</span>
                  </span>
                </label>
              )}
              {warnings.length > 0 && (
                <label className="flex items-start gap-2 text-sm">
                  <input type="checkbox" className="mt-1" checked={accepted} onChange={(e) => setAccepted(e.target.checked)} data-testid="demote-accept" />
                  <span className="font-medium">I&rsquo;ve read the warnings above and want to go ahead</span>
                </label>
              )}
            </>
          )}
          {err && <Alert>{err}</Alert>}
        </div>
      </SidePanel>
    </Panel>
  );
}
