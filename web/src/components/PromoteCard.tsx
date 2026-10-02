import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, errorMessage, type DedicatedRequest, type Project } from "../api/client";
import { formatBytes } from "../lib/format";
import { useOperationStream } from "../lib/useOperationStream";
import { OperationLog } from "./OperationLog";
import { Alert, Button, Card, Field, Input, Select, StatusBadge } from "./ui";

function duration(seconds: number): string {
  if (seconds < 90) return `about ${seconds} s`;
  return `about ${Math.round(seconds / 60)} min`;
}

/**
 * The promotion wizard (spec §6.6, §14 M5): pick where and how big, see the
 * size and the expected write freeze, then follow the operation live.
 */
export function PromoteCard({ p }: { p: Project }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [nodeId, setNodeId] = useState("");
  const [profile, setProfile] = useState("");
  const [volume, setVolume] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [opId, setOpId] = useState<string | undefined>();
  const [reason, setReason] = useState("");
  const [request, setRequest] = useState<DedicatedRequest | null>(null);
  const stream = useOperationStream(opId);
  const estimate = useQuery({ queryKey: ["promote", p.id], queryFn: () => api.promotionEstimate(p.id), enabled: open });
  const profiles = useQuery({ queryKey: ["profiles"], queryFn: api.profiles, enabled: open });
  const nodes = useQuery({ queryKey: ["nodes"], queryFn: api.nodes, enabled: open });
  const targets = (nodes.data?.items ?? []).filter((n) => (n.role === "dedicated" || n.role === "both") && n.agent.registered);

  const promote = async () => {
    setBusy(true);
    setErr(null);
    try {
      const r = await api.promote(p.id, {
        node_id: nodeId || undefined,
        profile: profile || undefined,
        volume_gb: volume ? Number(volume) : undefined,
        reason: reason || undefined,
      });
      // Beyond the organisation's dedicated allowance it becomes a request
      // the platform admin decides (V2 §10.6).
      if ("project_name" in r) {
        setRequest(r);
        setOpen(false);
      } else setOpId(r.id);
      await qc.invalidateQueries({ queryKey: ["project", p.id] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  // Shared projects only; once started it stays to show the outcome, even
  // after the project has become dedicated.
  if (!opId && (p.tier !== "shared" || (p.status !== "active" && p.status !== "promoting"))) return null;

  if (request) {
    return (
      <Card title="Promote to dedicated">
        <Alert tone="ok" title="Request sent">
          This is beyond your organisation&rsquo;s dedicated allowance, so the platform admin decides. You&rsquo;ll get an email either way; the promotion starts as
          soon as it&rsquo;s approved.
        </Alert>
      </Card>
    );
  }

  if (opId) {
    return (
      <Card title="Promotion" actions={stream.status && <StatusBadge status={stream.status} />}>
        <OperationLog log={stream.log} live={!stream.done} />
        {stream.status === "succeeded" && (
          <p className="mt-3 text-sm text-ok" data-testid="promote-done">
            Promoted. The connection strings are unchanged; the shared copy is kept read-only for 48 hours.
          </p>
        )}
        {stream.status === "failed" && (
          <div className="mt-3">
            <Alert title="Promotion failed">{stream.error}. The project stays on the shared tier with no data lost.</Alert>
          </div>
        )}
      </Card>
    );
  }

  return (
    <Card title="Promote to dedicated">
      {!open ? (
        <div className="flex flex-wrap items-center justify-between gap-3">
          <p className="text-sm text-muted">
            Move this project to its own Postgres instance with continuous backups and point-in-time recovery. The URL and password stay the same.
          </p>
          <Button onClick={() => setOpen(true)}>Promote…</Button>
        </div>
      ) : (
        <div className="flex flex-col gap-4">
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
            <Field label="Target node">
              {(id) => (
                <Select id={id} value={nodeId} onChange={(e) => setNodeId(e.target.value)}>
                  <option value="">Least loaded</option>
                  {targets.map((n) => (
                    <option key={n.id} value={n.id}>
                      {n.name}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
            <Field label="Instance size">
              {(id) => (
                <Select id={id} value={profile} onChange={(e) => setProfile(e.target.value)}>
                  {(profiles.data?.items ?? []).map((pr) => (
                    <option key={pr.name} value={pr.name === profiles.data?.default_profile ? "" : pr.name}>
                      {pr.name}: {pr.cpus} CPU, {pr.memory_mb / 1024} GB
                    </option>
                  ))}
                </Select>
              )}
            </Field>
            <Field label="Volume size (GB)">
              {(id) => (
                <Input
                  id={id}
                  type="number"
                  min={1}
                  value={volume}
                  placeholder={String(profiles.data?.default_volume_gb ?? 20)}
                  onChange={(e) => setVolume(e.target.value)}
                />
              )}
            </Field>
          </div>
          <Field label="Why (if it needs approval)" hint="Beyond your organisation's dedicated allowance, this becomes a request to the platform admin.">
            {(id) => <Input id={id} value={reason} onChange={(e) => setReason(e.target.value)} maxLength={500} />}
          </Field>
          {estimate.data && (
            <Alert tone="accent" title={`Estimated write freeze: ${duration(estimate.data.estimated_downtime_seconds)}`}>
              <span data-testid="promote-estimate">
                The database is {formatBytes(estimate.data.size_bytes)}. During the freeze, apps on the pooled URL wait rather than fail; session
                connections are dropped once and reconnect.
              </span>
            </Alert>
          )}
          {estimate.isError && <Alert>{errorMessage(estimate.error)}</Alert>}
          <ol className="list-decimal pl-5 text-sm text-muted">
            <li>A dedicated instance starts with the same role and password.</li>
            <li>Writes freeze; the data is copied and every table's rows and sequences are checked.</li>
            <li>The pooler route moves to the new instance and clients continue, with the same URL.</li>
            <li>The shared copy stays read-only for 48 hours, then is dropped. A failure before the switch changes nothing.</li>
          </ol>
          {nodes.data && targets.length === 0 && <Alert tone="warn">No node with an agent takes dedicated instances (see Nodes).</Alert>}
          {err && <Alert>{err}</Alert>}
          <div className="flex gap-2">
            <Button variant="primary" onClick={promote} busy={busy} disabled={nodes.data !== undefined && targets.length === 0}>
              Promote now
            </Button>
            <Button onClick={() => setOpen(false)}>Cancel</Button>
          </div>
        </div>
      )}
    </Card>
  );
}
