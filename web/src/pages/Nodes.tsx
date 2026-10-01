import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { api, errorMessage, type Node, type NodeCreated } from "../api/client";
import { ConfirmDestroy } from "../components/ConfirmDelete";
import { useOperationToast } from "../components/Toasts";
import { Alert, Badge, Button, Card, CodeBlock, Field, Input, PageHeader, Select, Spinner, StateBadge, Table } from "../components/ui";
import { formatBytes, formatDate, relativeTime } from "../lib/format";
import { MetricCharts } from "../components/Metrics";
import { nodeCharts } from "./ProjectMetrics";

type Role = "shared" | "dedicated" | "both";

export function agentBadge(n: Node) {
  if (n.status === "removed") return <Badge>removed</Badge>;
  if (!n.agent.registered) return <Badge tone="warn">not registered</Badge>;
  if (n.agent.reachable) return <StateBadge state="healthy" />;
  return (
    <span title={n.agent.error}>
      <Badge tone="danger">unreachable</Badge>
    </span>
  );
}

type Metrics = {
  cpus?: number;
  load1?: number;
  mem_total_bytes?: number;
  mem_available_bytes?: number;
  disk_total_bytes?: number;
  disk_free_bytes?: number;
};

function usage(used: number, total: number) {
  const pct = total ? Math.round((used / total) * 100) : 0;
  return (
    <div className="flex items-center gap-2">
      <div className="h-1.5 w-24 overflow-hidden rounded bg-surface-2">
        <div className={pct > 85 ? "h-full bg-danger" : "h-full bg-accent"} style={{ width: `${pct}%` }} />
      </div>
      <span className="text-xs text-muted">
        {formatBytes(used)} of {formatBytes(total)} ({pct}%)
      </span>
    </div>
  );
}

/** Nodes list with "Add node" (spec §8.1 /nodes). */
export function NodesPage() {
  const q = useQuery({ queryKey: ["nodes"], queryFn: api.nodes, refetchInterval: 5000 });
  const [adding, setAdding] = useState(false);
  return (
    <>
      <PageHeader
        title="Nodes"
        subtitle="Hosts running Postgres, and the agent on each that runs instances, backups, restores, and imports."
        actions={
          !adding && (
            <Button variant="primary" onClick={() => setAdding(true)}>
              Add node
            </Button>
          )
        }
      />
      {adding && <AddNode onClose={() => setAdding(false)} />}
      {q.isPending && <Spinner />}
      {q.isError && <Alert>{errorMessage(q.error)}</Alert>}
      {q.data && (
        <Table head={["Node", "Role", "Address", "Agent", "Disk", "Last seen"]}>
          {q.data.items.map((n) => {
            const m = (n.agent.metrics ?? {}) as Metrics;
            return (
              <tr key={n.id} data-testid="node-row">
                <td className="px-3 py-2">
                  <Link to="/nodes/$id" params={{ id: n.id }} className="font-medium hover:underline">
                    {n.name}
                  </Link>
                </td>
                <td className="px-3 py-2">
                  <Badge>{n.role}</Badge>
                </td>
                <td className="px-3 py-2 font-mono text-xs text-muted">{n.private_addr}</td>
                <td className="px-3 py-2" data-testid="agent-status">
                  {agentBadge(n)}
                </td>
                <td className="px-3 py-2">{m.disk_total_bytes ? usage(m.disk_total_bytes - (m.disk_free_bytes ?? 0), m.disk_total_bytes) : "—"}</td>
                <td className="px-3 py-2 text-muted">{relativeTime(n.last_heartbeat)}</td>
              </tr>
            );
          })}
        </Table>
      )}
    </>
  );
}

function AddNode({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [addr, setAddr] = useState("");
  const [role, setRole] = useState<Role>("dedicated");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [created, setCreated] = useState<NodeCreated | null>(null);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      setCreated(await api.createNode({ name, private_addr: addr, role }));
      await qc.invalidateQueries({ queryKey: ["nodes"] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Card title="Add a node" className="mb-4 max-w-2xl" actions={<Button variant="ghost" className="text-xs" onClick={onClose}>Close</Button>}>
      {created ? (
        <div className="flex flex-col gap-3 text-sm">
          <p>
            On <strong>{created.node.name}</strong>, with Docker installed and the <code className="font-mono">pgdock-agent</code> binary, run this
            within 24 hours (until {formatDate(created.expires_at)}). The token works once.
          </p>
          <CodeBlock code={created.command} />
          <p className="text-xs text-muted">The node shows as healthy here once its agent registers.</p>
        </div>
      ) : (
        <form className="flex flex-col gap-3" onSubmit={submit}>
          <div className="grid gap-3 sm:grid-cols-3">
            <Field label="Node name" hint="Lowercase, e.g. node-b">
              {(id) => <Input id={id} required value={name} onChange={(e) => setName(e.target.value)} className="font-mono" />}
            </Field>
            <Field label="Private address" hint="Poolers reach its Postgres here">
              {(id) => <Input id={id} required value={addr} onChange={(e) => setAddr(e.target.value)} className="font-mono" placeholder="10.0.0.12" />}
            </Field>
            <Field label="Role">
              {(id) => (
                <Select id={id} value={role} onChange={(e) => setRole(e.target.value as Role)}>
                  <option value="dedicated">dedicated</option>
                  <option value="shared">shared</option>
                  <option value="both">both</option>
                </Select>
              )}
            </Field>
          </div>
          {err && <Alert>{err}</Alert>}
          <div>
            <Button type="submit" variant="primary" busy={busy}>
              Add node and get the registration command
            </Button>
          </div>
        </form>
      )}
    </Card>
  );
}

/** One node: health, role, instances, shared cluster, removal (spec §8.1 /nodes/:id). */
export function NodeDetailPage() {
  const { id } = useParams({ from: "/app/nodes/$id" });
  const qc = useQueryClient();
  const toast = useOperationToast();
  const navigate = useNavigate();
  const q = useQuery({ queryKey: ["node", id], queryFn: () => api.node(id), refetchInterval: 5000 });
  const [err, setErr] = useState<string | null>(null);
  const [mem, setMem] = useState("2048");
  const [busy, setBusy] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [token, setToken] = useState<string | null>(null);
  if (q.isPending) return <Spinner />;
  if (q.isError) return <Alert>{errorMessage(q.error)}</Alert>;
  const { node: n, instances } = q.data;
  const m = (n.agent.metrics ?? {}) as Metrics;
  const hasShared = instances.some((i) => i.kind === "shared");

  const setRole = async (role: Role) => {
    setErr(null);
    try {
      await api.updateNode(n.id, role);
      await qc.invalidateQueries({ queryKey: ["node", id] });
    } catch (e) {
      setErr(errorMessage(e));
    }
  };
  const addShared = async () => {
    setBusy(true);
    setErr(null);
    try {
      const op = await api.createSharedCluster(n.id, Number(mem));
      toast(op.id, `Shared cluster · ${n.name}`);
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <PageHeader title={<span className="flex items-center gap-2">{n.name} {agentBadge(n)}</span>} subtitle={<span className="font-mono">{n.private_addr}</span>} />
      {err && (
        <div className="mb-4">
          <Alert>{err}</Alert>
        </div>
      )}
      <div className="grid gap-4 lg:grid-cols-2">
        <Card title="Health">
          <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
            <dt className="text-muted">Agent</dt>
            <dd>
              {n.agent.version ?? "—"} {n.agent.address && <span className="font-mono text-xs text-muted">at {n.agent.address}</span>}
            </dd>
            <dt className="text-muted">Docker</dt>
            <dd data-testid="node-docker">{n.agent.docker ?? "—"}</dd>
            <dt className="text-muted">Instance image</dt>
            <dd className="font-mono text-xs">{n.agent.image ?? "—"}</dd>
            <dt className="text-muted">CPU</dt>
            <dd>{m.cpus ? `${m.cpus} cores, load ${m.load1?.toFixed(2)}` : "—"}</dd>
            <dt className="text-muted">Memory</dt>
            <dd>{m.mem_total_bytes ? usage(m.mem_total_bytes - (m.mem_available_bytes ?? 0), m.mem_total_bytes) : "—"}</dd>
            <dt className="text-muted">Disk</dt>
            <dd>{m.disk_total_bytes ? usage(m.disk_total_bytes - (m.disk_free_bytes ?? 0), m.disk_total_bytes) : "—"}</dd>
            <dt className="text-muted">Last seen</dt>
            <dd>{relativeTime(n.last_heartbeat)}</dd>
          </dl>
          {n.agent.error && (
            <div className="mt-3">
              <Alert tone="warn">{n.agent.error}</Alert>
            </div>
          )}
        </Card>
        <Card title="Placement">
          <div className="flex flex-col gap-3 text-sm">
            <Field label="Role" hint="Which tiers new projects may be placed here with. Existing instances stay.">
              {(fid) => (
                <Select id={fid} value={n.role} onChange={(e) => setRole(e.target.value as Role)} className="self-start">
                  <option value="shared">shared</option>
                  <option value="dedicated">dedicated</option>
                  <option value="both">both</option>
                </Select>
              )}
            </Field>
            {(n.role === "shared" || n.role === "both") && !hasShared && n.agent.registered && (
              <div className="flex flex-wrap items-end gap-2">
                <Field label="Run a shared cluster here (memory, MB)">
                  {(fid) => <Input id={fid} type="number" min={512} value={mem} onChange={(e) => setMem(e.target.value)} className="w-32" />}
                </Field>
                <Button onClick={addShared} busy={busy}>
                  Create shared cluster
                </Button>
              </div>
            )}
            <div className="flex gap-2">
              <Button
                className="text-xs"
                onClick={async () => {
                  setErr(null);
                  try {
                    setToken((await api.nodeToken(n.id)).command);
                  } catch (e) {
                    setErr(errorMessage(e));
                  }
                }}
              >
                {n.agent.registered ? "Re-register agent…" : "Registration command…"}
              </Button>
              <Button variant="danger" className="text-xs" onClick={() => setRemoving(true)} disabled={instances.length > 0}>
                Remove node
              </Button>
            </div>
            {token && <CodeBlock code={token} />}
          </div>
        </Card>
      </div>
      <h2 className="mt-6 mb-2 text-sm font-semibold">Metrics</h2>
      <MetricCharts queryKey={["metrics", "node", n.id]} fetch={(r) => api.nodeMetrics(n.id, r)} charts={nodeCharts} />
      <h2 className="mt-6 mb-2 text-sm font-semibold">Instances</h2>
      {instances.length === 0 ? (
        <p className="text-sm text-muted">Nothing runs here yet.</p>
      ) : (
        <Table head={["Kind", "Status", "Size", "Address", "Projects", "Created"]}>
          {instances.map((i) => (
            <tr key={i.id} data-testid="instance-row">
              <td className="px-3 py-2">
                <Badge tone={i.kind === "dedicated" ? "accent" : "muted"}>{i.kind}</Badge>
              </td>
              <td className="px-3 py-2">
                <StateBadge state={i.status} />
                {i.error && <p className="mt-1 max-w-xs truncate text-xs text-danger" title={i.error}>{i.error}</p>}
              </td>
              <td className="px-3 py-2 text-xs text-muted">
                {i.profile ?? "—"}
                {i.cpus ? ` · ${i.cpus} CPU` : ""}
                {i.memory_mb ? ` · ${i.memory_mb / 1024} GB` : ""}
                {i.volume_gb ? ` · ${i.volume_gb} GB disk` : ""}
              </td>
              <td className="px-3 py-2 font-mono text-xs text-muted">{i.address}</td>
              <td className="px-3 py-2">{i.projects}</td>
              <td className="px-3 py-2 text-muted">{formatDate(i.created_at)}</td>
            </tr>
          ))}
        </Table>
      )}
      <ConfirmDestroy
        open={removing}
        onClose={() => setRemoving(false)}
        title={`Remove ${n.name}`}
        name={n.name}
        description="Its agent's certificate stops being trusted and nothing new is placed here. Instances must be gone first."
        action="Remove node"
        run={async () => {
          await api.removeNode(n.id);
          await qc.invalidateQueries({ queryKey: ["nodes"] });
          await navigate({ to: "/nodes" });
        }}
      />
    </>
  );
}
