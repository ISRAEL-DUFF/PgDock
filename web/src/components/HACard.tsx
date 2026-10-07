import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, errorMessage, type HAStatus, type Project } from "../api/client";
import { formatBytes, formatDate } from "../lib/format";
import { useOperationStream } from "../lib/useOperationStream";
import { CostEstimate } from "./CostEstimate";
import { OperationLog } from "./OperationLog";
import {
  Alert,
  Badge,
  Button,
  Field,
  Panel,
  Select,
  SidePanel,
  StatusBadge,
  Switch,
  Table,
} from "./ui";

const roleLabel: Record<HAStatus["members"][number]["role"], string> = {
  leader: "Primary",
  replica: "Standby",
  sync_standby: "Synchronous standby",
  starting: "Starting",
  stopped: "Stopped",
  unknown: "Unknown",
};

function ms(n?: number | null): string {
  if (n == null) return "—";
  return n < 1000 ? `${n} ms` : `${(n / 1000).toFixed(1)} s`;
}

/**
 * High availability for a dedicated project (V3 §2.2): a standby on
 * another node that takes over within a minute, planned switchovers,
 * synchronous replication, the failover history, and the month's measured
 * availability (§2.7).
 */
export function HACard({ p }: { p: Project }) {
  const qc = useQueryClient();
  const busyStatus = p.status !== "active";
  const ha = useQuery({
    queryKey: ["ha", p.id],
    queryFn: () => api.projectHA(p.id),
    refetchInterval: 5000,
    enabled: p.tier === "dedicated",
  });
  const nodes = useQuery({
    queryKey: ["nodes"],
    queryFn: api.nodes,
    retry: false,
  });
  const [open, setOpen] = useState(false);
  const [nodeId, setNodeId] = useState("");
  const [sync, setSync] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [opId, setOpId] = useState<string | undefined>();
  const stream = useOperationStream(opId);

  const run = async (what: string, f: () => Promise<{ id: string }>) => {
    setBusy(what);
    setErr(null);
    try {
      const op = await f();
      setOpId(op.id);
      setOpen(false);
      await qc.invalidateQueries({ queryKey: ["project", p.id] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(null);
    }
  };
  const setSynchronous = async (on: boolean) => {
    setBusy("sync");
    setErr(null);
    try {
      await api.updateHA(p.id, on);
      await qc.invalidateQueries({ queryKey: ["ha", p.id] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(null);
    }
  };

  if (p.tier !== "dedicated") return null;
  const st = ha.data;
  const targets = (nodes.data?.items ?? []).filter(
    (n) =>
      (n.role === "dedicated" || n.role === "both") &&
      n.agent.registered &&
      n.id !== p.instance?.node_id,
  );
  const a = st?.availability;

  return (
    <Panel
      title="High availability"
      actions={
        opId && stream.status ? (
          <StatusBadge status={stream.status} />
        ) : st?.enabled ? (
          <Badge tone="ok">On</Badge>
        ) : (
          <Badge tone="muted">Off</Badge>
        )
      }
    >
      <div className="flex flex-col gap-4 text-sm" data-testid="ha">
        {!st?.enabled && !opId && (
          <div className="flex flex-wrap items-center justify-between gap-3">
            <p className="text-muted">
              A standby on another node takes over within a minute if this one
              fails, with the same URL. Turning it on restarts the database once
              (a few seconds, clients wait), then builds the standby from the
              latest backup.
            </p>
            <Button onClick={() => setOpen(true)} disabled={busyStatus}>
              Enable HA…
            </Button>
          </div>
        )}
        {st && st.members.length > 0 && (
          <Table head={["Member", "Node", "State", "Behind", "Timeline"]}>
            {st.members.map((m) => (
              <tr key={m.id} data-testid="ha-member" data-role={m.role}>
                <td className="px-3 py-2">
                  <Badge tone={m.role === "leader" ? "accent" : "muted"}>
                    {roleLabel[m.role]}
                  </Badge>
                </td>
                <td className="px-3 py-2">{m.node_name}</td>
                <td className="px-3 py-2 text-muted">{m.state ?? "—"}</td>
                <td className="px-3 py-2">
                  {m.role === "leader"
                    ? "—"
                    : m.lag_bytes != null
                      ? formatBytes(m.lag_bytes)
                      : "unknown"}
                </td>
                <td className="px-3 py-2 text-muted">{m.timeline ?? "—"}</td>
              </tr>
            ))}
          </Table>
        )}
        {st?.enabled && (
          <div className="flex flex-wrap items-center gap-4">
            <label className="flex items-center gap-2">
              <Switch
                checked={st.synchronous}
                onCheckedChange={setSynchronous}
                disabled={busy === "sync"}
                aria-label="Synchronous replication"
              />
              <span>
                <span className="font-medium">Synchronous replication</span>
                <span className="block text-xs text-muted">
                  No committed write is lost on failover; each commit waits for
                  the standby.
                </span>
              </span>
            </label>
            <div className="ml-auto flex gap-2">
              {st.etcd_move_available && (
                <Button
                  onClick={() => run("etcd", () => api.moveProjectEtcd(p.id))}
                  busy={busy === "etcd"}
                  disabled={busyStatus}
                  title={`Its Patroni state is in ${st.etcd_region}'s etcd cluster; move it to its own region's (writes pause once, for a few seconds).`}
                >
                  Move to the region's etcd
                </Button>
              )}
              <Button
                onClick={() => run("switchover", () => api.switchover(p.id))}
                busy={busy === "switchover"}
                disabled={busyStatus}
              >
                Switch over
              </Button>
              <Button
                variant="danger"
                onClick={() => run("disable", () => api.disableHA(p.id))}
                busy={busy === "disable"}
                disabled={busyStatus}
              >
                Turn off
              </Button>
            </div>
          </div>
        )}
        {a && (
          <div data-testid="ha-availability">
            <span className="font-medium">Availability in {a.month}: </span>
            {a.percent != null
              ? `${a.percent.toFixed(3)}%`
              : "not measured yet"}
            <span className="text-muted">
              {" "}
              ({a.unavailable_minutes} unavailable of {a.measured_minutes}{" "}
              measured minutes; the SLA is 99.9%)
            </span>
            {a.exclusions && a.exclusions.length > 0 && (
              <ul
                className="mt-1 text-xs text-muted"
                data-testid="ha-exclusions"
              >
                {a.exclusions.map((x) => (
                  <li key={x.incident_id}>
                    {x.minutes} minutes excluded for announced maintenance:{" "}
                    {x.title}
                    {x.scheduled_start
                      ? ` (${formatDate(x.scheduled_start)})`
                      : ""}
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
        {st && st.failovers.length > 0 && (
          <details>
            <summary className="cursor-pointer text-muted">
              Failovers and switchovers ({st.failovers.length}), last{" "}
              {formatDate(st.failovers[0].occurred_at)}
            </summary>
            <Table head={["When", "Kind", "From", "To", "Writes paused"]}>
              {st.failovers.map((f, i) => (
                <tr key={i}>
                  <td className="px-3 py-2 text-muted">
                    {formatDate(f.occurred_at)}
                  </td>
                  <td className="px-3 py-2">{f.kind}</td>
                  <td className="px-3 py-2">{f.from_node ?? "—"}</td>
                  <td className="px-3 py-2">{f.to_node ?? "—"}</td>
                  <td className="px-3 py-2">{ms(f.duration_ms)}</td>
                </tr>
              ))}
            </Table>
          </details>
        )}
        {opId && <OperationLog log={stream.log} live={!stream.done} />}
        {stream.status === "failed" && <Alert>{stream.error}</Alert>}
        {err && <Alert>{err}</Alert>}
      </div>
      <SidePanel
        open={open}
        onOpenChange={setOpen}
        title="Enable high availability"
        footer={
          <>
            <Button onClick={() => setOpen(false)}>Cancel</Button>
            <Button
              variant="primary"
              busy={busy === "enable"}
              onClick={() =>
                run("enable", () =>
                  api.enableHA(p.id, {
                    node_id: nodeId || undefined,
                    synchronous: sync || undefined,
                  }),
                )
              }
            >
              Enable HA
            </Button>
          </>
        }
      >
        <div className="flex flex-col gap-4 text-sm">
          <Field
            label="Standby node"
            hint="On a different node from the primary."
          >
            {(id) => (
              <Select
                id={id}
                value={nodeId}
                onChange={(e) => setNodeId(e.target.value)}
              >
                <option value="">Least loaded</option>
                {targets.map((n) => (
                  <option
                    key={n.id}
                    value={n.id}
                    disabled={n.status !== "healthy"}
                  >
                    {n.name}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <label className="flex items-start gap-2">
            <input
              type="checkbox"
              className="mt-1"
              checked={sync}
              onChange={(e) => setSync(e.target.checked)}
            />
            <span>
              <span className="font-medium">Synchronous replication</span>
              <span className="block text-muted">
                No committed data is lost on failover, at the cost of write
                latency. You can change it later.
              </span>
            </span>
          </label>
          <ol className="list-decimal pl-5 text-muted">
            <li>
              The database restarts under Patroni; clients on the pooled URL
              wait a few seconds.
            </li>
            <li>
              A standby on the other node is restored from the latest backup and
              streams changes.
            </li>
            <li>
              If the primary fails, the standby takes over within a minute; the
              URL stays the same.
            </li>
          </ol>
          <CostEstimate
            org={p.org_id}
            what="HA"
            req={
              p.instance?.cpus != null
                ? {
                    cpus: p.instance.cpus,
                    memory_mb: p.instance.memory_mb ?? 0,
                    disk_gb: p.instance.volume_gb ?? 0,
                    standby_only: true,
                    synchronous: sync,
                  }
                : null
            }
          />
          {nodes.data && targets.length === 0 && (
            <Alert tone="warn">
              HA needs a second node that takes dedicated instances.
            </Alert>
          )}
        </div>
      </SidePanel>
    </Panel>
  );
}
