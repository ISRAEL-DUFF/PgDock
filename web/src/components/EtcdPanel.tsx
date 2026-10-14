import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, ApiRequestError, errorMessage } from "../api/client";
import { relativeTime } from "../lib/format";
import { useOperationToast } from "./Toasts";
import { Alert, Badge, Button, Panel, StateBadge, Table } from "./ui";

/**
 * A region's etcd cluster HA instances keep their Patroni state in (V3
 * §2.2, V3.1 §3): one member on each of three nodes in three failure
 * domains, so losing any one keeps a quorum. Each region has its own.
 */
export function EtcdPanel() {
  const qc = useQueryClient();
  const toast = useOperationToast();
  const regions = useQuery({
    queryKey: ["regions"],
    queryFn: api.regions,
    retry: false,
  });
  const [region, setRegion] = useState<string | undefined>(undefined);
  const q = useQuery({
    queryKey: ["etcd", region ?? ""],
    queryFn: () => api.etcd(region),
    refetchInterval: 15000,
    retry: false,
  });
  const nodes = useQuery({ queryKey: ["nodes"], queryFn: api.nodes });
  const [picked, setPicked] = useState<string[]>([]);
  const [replacing, setReplacing] = useState<string | null>(null);
  const [replaceTo, setReplaceTo] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  // 503: no dedicated tier on this server, so no HA.
  if (q.error instanceof ApiRequestError && q.error.status === 503) return null;
  if (q.isError) return <Alert>{errorMessage(q.error)}</Alert>;
  if (!q.data) return null;
  const c = q.data;
  const current = c.region ?? region;
  const memberNodes = new Set(c.members.map((m) => m.node_id));
  const candidates = (nodes.data?.items ?? []).filter(
    (n) =>
      n.agent.registered &&
      n.role !== "pooler" &&
      (!current || n.region === current) &&
      !memberNodes.has(n.id),
  );
  const replace = async (node: string) => {
    setBusy(true);
    setErr(null);
    try {
      const op = await api.replaceEtcdMember(node, replaceTo || undefined);
      toast(op.id, "Replace etcd member");
      setReplacing(null);
      await qc.invalidateQueries({ queryKey: ["etcd"] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  const setup = async () => {
    setBusy(true);
    setErr(null);
    try {
      const op = await api.setupEtcd(picked);
      toast(op.id, `etcd cluster · ${current ?? ""}`);
      await qc.invalidateQueries({ queryKey: ["etcd"] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Panel
      title="etcd cluster for HA"
      actions={
        <span className="flex items-center gap-2">
          {(regions.data?.items.length ?? 0) > 1 && (
            <select
              aria-label="etcd region"
              className="rounded border border-border bg-surface px-2 py-1 text-xs"
              value={current ?? ""}
              onChange={(e) => {
                setRegion(e.target.value);
                setPicked([]);
              }}
            >
              {regions.data!.items.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.name}
                </option>
              ))}
            </select>
          )}
          {c.members.length === 0 ? (
            <Badge tone="muted">Not set up</Badge>
          ) : c.ready ? (
            <Badge tone="ok">Ready</Badge>
          ) : (
            <Badge tone="danger">Degraded</Badge>
          )}
        </span>
      }
    >
      <div className="flex flex-col gap-3 text-sm" data-testid="etcd">
        {c.members.length === 0 ? (
          <>
            <p className="text-muted">
              HA dedicated projects keep their leader election in a three-member
              etcd cluster in their region. Pick three of the region's nodes, in
              three different failure domains (racks), so that losing any one
              keeps a quorum.
            </p>
            <div className="flex flex-wrap gap-3">
              {candidates.map((n) => (
                <label key={n.id} className="flex items-center gap-2">
                  <input
                    type="checkbox"
                    checked={picked.includes(n.id)}
                    disabled={!picked.includes(n.id) && picked.length >= 3}
                    onChange={(e) =>
                      setPicked(
                        e.target.checked
                          ? [...picked, n.id]
                          : picked.filter((x) => x !== n.id),
                      )
                    }
                  />
                  {n.name}
                </label>
              ))}
            </div>
            <div>
              <Button
                variant="primary"
                onClick={setup}
                busy={busy}
                disabled={picked.length !== 3}
              >
                Set up etcd on {picked.length} of 3 nodes
              </Button>
            </div>
          </>
        ) : (
          <Table head={["Member", "Node", "Health", "Checked", ""]}>
            {c.members.map((m) => (
              <tr key={m.node_id} data-testid="etcd-member">
                <td className="px-3 py-2 font-mono text-xs">{m.name}</td>
                <td className="px-3 py-2">{m.node_name}</td>
                <td className="px-3 py-2">
                  <StateBadge state={m.status} />
                  {m.error && (
                    <p
                      className="mt-1 max-w-xs truncate text-xs text-danger-text"
                      title={m.error}
                    >
                      {m.error}
                    </p>
                  )}
                </td>
                <td className="px-3 py-2 text-muted">
                  {relativeTime(m.checked_at ?? undefined)}
                </td>
                <td className="px-3 py-2 text-right">
                  {replacing === m.node_id ? (
                    <span className="flex items-center justify-end gap-2">
                      <select
                        aria-label="Replace on"
                        className="rounded border border-border bg-surface px-2 py-1 text-xs"
                        value={replaceTo}
                        onChange={(e) => setReplaceTo(e.target.value)}
                      >
                        <option value="">Best node</option>
                        {candidates.map((n) => (
                          <option key={n.id} value={n.id}>
                            {n.name}
                          </option>
                        ))}
                      </select>
                      <Button
                        size="small"
                        variant="primary"
                        busy={busy}
                        onClick={() => replace(m.node_id)}
                      >
                        Replace
                      </Button>
                    </span>
                  ) : (
                    <Button
                      size="small"
                      onClick={() => {
                        setReplacing(m.node_id);
                        setReplaceTo("");
                      }}
                    >
                      Replace…
                    </Button>
                  )}
                </td>
              </tr>
            ))}
          </Table>
        )}
        {!c.ready && c.reason && c.members.length > 0 && (
          <Alert tone="warn">{c.reason}</Alert>
        )}
        {err && <Alert>{err}</Alert>}
      </div>
    </Panel>
  );
}
