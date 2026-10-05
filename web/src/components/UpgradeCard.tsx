import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import {
  api,
  errorMessage,
  type Move,
  type Project,
  type UpgradePreflight,
} from "../api/client";
import { formatBytes } from "../lib/format";
import { sessionQuery } from "../lib/session";
import { useOperationStream } from "../lib/useOperationStream";
import { OperationLog } from "./OperationLog";
import {
  Alert,
  Badge,
  Button,
  Field,
  KeyValues,
  Panel,
  Select,
  SidePanel,
  StatusBadge,
} from "./ui";

const checkLabel: Record<UpgradePreflight["checks"][number]["name"], string> = {
  version: "Version",
  target: "Target",
  replication: "Copy",
  schema: "Schema",
  extensions: "Extensions",
};
const checkTone = { ok: "ok", warning: "warn", blocked: "danger" } as const;
const checkMark = { ok: "OK", warning: "Warning", blocked: "Blocked" } as const;

function duration(seconds: number): string {
  if (seconds < 90) return `about ${seconds} s`;
  return `about ${Math.round(seconds / 60)} min`;
}

/**
 * The major upgrade wizard (V3 §2.4): pick a newer Postgres major, read the
 * preflight (the schema is test-restored on the new version), then follow
 * the logical move live.
 */
export function UpgradeCard({ p }: { p: Project }) {
  const qc = useQueryClient();
  const current = p.instance?.pg_version;
  const profiles = useQuery({ queryKey: ["profiles"], queryFn: api.profiles });
  const newer = (profiles.data?.pg_versions ?? []).filter(
    (v) => current !== undefined && v > current,
  );
  const [open, setOpen] = useState(false);
  const [to, setTo] = useState<number | undefined>();
  const target = to ?? newer[newer.length - 1];
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [opId, setOpId] = useState<string | undefined>();
  const stream = useOperationStream(opId);
  const pf = useQuery({
    queryKey: ["upgrade", p.id, target],
    queryFn: () => api.upgradePreflight(p.id, target!),
    enabled: open && !opId && target !== undefined,
    retry: false,
  });

  const upgrade = async () => {
    setBusy(true);
    setErr(null);
    try {
      const op = await api.upgrade(p.id, target!);
      setOpId(op.id);
      await qc.invalidateQueries({ queryKey: ["project", p.id] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  if (current === undefined) return null;

  if (opId) {
    return (
      <Panel
        title="Major upgrade"
        actions={stream.status && <StatusBadge status={stream.status} />}
      >
        <OperationLog log={stream.log} live={!stream.done} />
        {stream.status === "succeeded" && (
          <p className="mt-3 text-sm text-ok-text" data-testid="upgrade-done">
            Upgraded to Postgres {target}. The connection strings are unchanged;
            the old copy is kept for 48 hours.
          </p>
        )}
        {stream.status === "failed" && (
          <div className="mt-3">
            <Alert title="Upgrade failed">
              {stream.error}. The project stays on Postgres {current} with no
              data lost.
            </Alert>
          </div>
        )}
      </Panel>
    );
  }

  return (
    <Panel title="Postgres version">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="text-sm">
          <span className="font-medium" data-testid="pg-version">
            Postgres {current}
          </span>
          {p.instance?.pg_release && (
            <span className="text-muted">
              {" "}
              (release {p.instance.pg_release})
            </span>
          )}
          <span className="block text-muted">
            {newer.length > 0
              ? `Postgres ${newer[newer.length - 1]} is available. Upgrading copies the data by logical replication; writes pause for a few seconds.`
              : "This is the newest supported version. Minor releases are applied in the platform's weekly maintenance window."}
          </span>
        </div>
        {newer.length > 0 &&
          (p.status === "active" || p.status === "upgrading") && (
            <Button onClick={() => setOpen(true)}>Upgrade…</Button>
          )}
      </div>
      <SidePanel
        open={open}
        onOpenChange={setOpen}
        size="large"
        title={`Upgrade from Postgres ${current}`}
        footer={
          <>
            <Button onClick={() => setOpen(false)}>Cancel</Button>
            <Button
              variant="primary"
              onClick={upgrade}
              busy={busy}
              disabled={!pf.data?.eligible}
            >
              Upgrade now
            </Button>
          </>
        }
      >
        <div className="flex flex-col gap-4">
          {newer.length > 1 && (
            <Field label="Upgrade to">
              {(id) => (
                <Select
                  id={id}
                  value={String(target)}
                  onChange={(e) => setTo(Number(e.target.value))}
                >
                  {[...newer].reverse().map((v) => (
                    <option key={v} value={v}>
                      Postgres {v}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
          )}
          {pf.isLoading && (
            <p className="text-sm text-muted">
              Checking the upgrade, including a trial restore of the schema on
              Postgres {target}…
            </p>
          )}
          {pf.isError && <Alert>{errorMessage(pf.error)}</Alert>}
          {pf.data && (
            <>
              <ul
                className="flex flex-col gap-2 text-sm"
                data-testid="upgrade-checks"
              >
                {pf.data.checks.map((c) => (
                  <li
                    key={c.name}
                    className="flex items-start gap-3"
                    data-testid={`upgrade-check-${c.name}`}
                    data-status={c.status}
                  >
                    <Badge tone={checkTone[c.status]}>
                      {checkMark[c.status]}
                    </Badge>
                    <span>
                      <span className="font-medium">{checkLabel[c.name]}</span>
                      <span className="block text-muted">{c.message}</span>
                    </span>
                  </li>
                ))}
              </ul>
              {pf.data.eligible && (
                <Alert
                  tone="accent"
                  title={`Estimated write pause: ${duration(pf.data.estimated_downtime_seconds)}`}
                >
                  The database is {formatBytes(pf.data.size_bytes)}.{" "}
                  {pf.data.copy_mode === "dump"
                    ? `It is copied while writes wait, because logical replication can't be used: ${pf.data.fallback_reason}.`
                    : "It is copied by logical replication while the project keeps serving; writes only pause for the switch."}{" "}
                  Test your application against Postgres {target} first (a
                  branch is a quick way); a major version can change query plans
                  and defaults.
                </Alert>
              )}
            </>
          )}
          {err && <Alert>{err}</Alert>}
        </div>
      </SidePanel>
    </Panel>
  );
}

const phaseLabel: Record<Move["phase"], string> = {
  preparing: "Preparing",
  copying: "Copying",
  streaming: "Streaming changes",
  cutover: "Switching",
  done: "Done",
  failed: "Failed",
};

/**
 * Recent moves of the project between instances (promotion, demotion, node
 * moves, upgrades), with live progress, and for the platform admin a way to
 * move it to another node (V3 §2.3).
 */
export function MovesCard({ p }: { p: Project }) {
  const qc = useQueryClient();
  const { data: session } = useQuery(sessionQuery);
  const platformAdmin = session?.user?.platform_role === "platform_admin";
  const moving =
    p.status === "moving" ||
    p.status === "upgrading" ||
    p.status === "promoting" ||
    p.status === "demoting";
  const moves = useQuery({
    queryKey: ["moves", p.id],
    queryFn: () => api.projectMoves(p.id),
    refetchInterval: moving ? 2000 : false,
  });
  const nodes = useQuery({
    queryKey: ["nodes"],
    queryFn: api.nodes,
    enabled: platformAdmin,
    retry: false,
  });
  const kindOK = (role: string) =>
    p.tier === "dedicated"
      ? role === "dedicated" || role === "both"
      : role === "shared" || role === "both";
  const targets = (nodes.data?.items ?? []).filter(
    (n) => kindOK(n.role) && n.agent.registered && n.id !== p.instance?.node_id,
  );
  const [nodeId, setNodeId] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [opId, setOpId] = useState<string | undefined>();
  const stream = useOperationStream(opId);

  const move = async () => {
    setBusy(true);
    setErr(null);
    try {
      const op = await api.moveProject(p.id, { node_id: nodeId });
      setOpId(op.id);
      await qc.invalidateQueries({ queryKey: ["project", p.id] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  const items = moves.data?.items ?? [];
  if (items.length === 0 && !platformAdmin) return null;
  const latest = items[0];

  return (
    <Panel
      title="Moves"
      actions={opId && stream.status && <StatusBadge status={stream.status} />}
    >
      {latest && latest.phase !== "done" && latest.phase !== "failed" && (
        <div className="mb-3" data-testid="move-progress">
          <KeyValues
            items={[
              ["Phase", phaseLabel[latest.phase]],
              ...(latest.tables_total
                ? ([
                    [
                      "Tables copied",
                      `${latest.tables_ready ?? 0} of ${latest.tables_total}`,
                    ],
                  ] as [string, string][])
                : []),
              ...(latest.lag_bytes !== undefined && latest.lag_bytes !== null
                ? ([["Behind by", formatBytes(latest.lag_bytes)]] as [
                    string,
                    string,
                  ][])
                : []),
            ]}
          />
        </div>
      )}
      {items.length > 0 && (
        <table className="w-full text-sm" data-testid="moves">
          <thead className="text-left text-muted">
            <tr>
              <th className="py-1 font-normal">Started</th>
              <th className="py-1 font-normal">How</th>
              <th className="py-1 font-normal">Result</th>
              <th className="py-1 font-normal">Writes paused</th>
            </tr>
          </thead>
          <tbody>
            {items.slice(0, 5).map((m) => (
              <tr key={m.id} className="border-t border-line">
                <td className="py-1.5">
                  {new Date(m.started_at).toLocaleString()}
                </td>
                <td className="py-1.5" title={m.fallback_reason ?? undefined}>
                  {m.mode === "logical"
                    ? "Logical replication"
                    : "Dump and restore"}
                </td>
                <td className="py-1.5">{phaseLabel[m.phase]}</td>
                <td className="py-1.5">
                  {m.freeze_ms !== undefined && m.freeze_ms !== null
                    ? `${(m.freeze_ms / 1000).toFixed(1)} s`
                    : "—"}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {platformAdmin && !opId && p.status === "active" && (
        <div className="mt-3 flex flex-wrap items-end gap-3">
          <Field
            label="Move to node"
            hint="Platform admin. Copies by logical replication; writes pause for a few seconds."
          >
            {(id) => (
              <Select
                id={id}
                value={nodeId}
                onChange={(e) => setNodeId(e.target.value)}
              >
                <option value="">Choose a node…</option>
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
          <Button onClick={move} busy={busy} disabled={!nodeId}>
            Move
          </Button>
        </div>
      )}
      {opId && (
        <div className="mt-3">
          <OperationLog log={stream.log} live={!stream.done} />
          {stream.status === "failed" && (
            <Alert title="Move failed">
              {stream.error}. The project stays where it was with no data lost.
            </Alert>
          )}
        </div>
      )}
      {err && <Alert>{err}</Alert>}
    </Panel>
  );
}
