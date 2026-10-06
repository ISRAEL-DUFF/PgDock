import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { Inbox } from "lucide-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { api, errorMessage, type AdminOrg, type DedicatedRequest, type Plan } from "../api/client";
import {
  Alert,
  Badge,
  Button,
  Panel,
  Field,
  Input,
  Dialog,
  PageHeading,
  Select,
  SidePanel,
  Table,
  TableSkeleton,
  PageSkeleton,
  EmptyState,
} from "../components/ui";
import { formatBytes, formatDate } from "../lib/format";
import { setCurrentOrg } from "../lib/org";
import { OrgBillingPanel } from "../components/AdminPayments";
import { formatQuantity, LIMIT_LABELS, monthStart } from "../lib/usage";

function statusBadge(status: string) {
  return <Badge tone={status === "active" ? "ok" : status === "suspended" ? "danger" : "warn"}>{status}</Badge>;
}

/** Admin → Organisations: names, plans, sizes, statuses; no tenant content (V2 §2.4). */
export function AdminOrgsPage() {
  const [q, setQ] = useState("");
  const orgs = useQuery({ queryKey: ["admin", "orgs", q], queryFn: () => api.adminOrgs(q || undefined) });
  const usage = useQuery({ queryKey: ["admin", "usage"], queryFn: () => api.platformUsage({ from: monthStart().toISOString() }) });
  const storage: Record<string, number> = {};
  for (const r of usage.data?.items ?? []) if (r.metric === "shared_storage_gb_hours") storage[r.org_id] = r.quantity;
  return (
    <>
      <PageHeading title="Organisations" description="Every organisation on this PGDock: plan, size, and status. What is inside them is theirs." />
      <div className="mb-3">
        <Input placeholder="Search name or slug" value={q} onChange={(e) => setQ(e.target.value)} className="max-w-xs" aria-label="Search organisations" />
      </div>
      {orgs.isPending && <TableSkeleton cols={5} />}
      {orgs.data && (
        <Table head={["Organisation", "Plan", "Members", "Projects", "Size", "Storage this month", "Status"]}>
          {orgs.data.items.map((o) => (
            <tr key={o.id} data-testid={`admin-org-${o.slug}`}>
              <td className="px-3 py-2">
                <Link to="/admin/orgs/$id" params={{ id: o.id }} className="font-medium hover:underline">
                  {o.name}
                </Link>
                <div className="text-xs text-muted">
                  {o.slug}
                  {o.personal ? " · personal" : ""}
                </div>
              </td>
              <td className="px-3 py-2">{o.plan}</td>
              <td className="px-3 py-2">{o.member_count}</td>
              <td className="px-3 py-2">
                {o.project_count}
                {!!o.org_target_projects && (
                  <span className="ml-1 text-xs text-muted" title="Their backups go to one of the organisation's own targets, which you don't see.">
                    ({o.org_target_projects} on an org target)
                  </span>
                )}
              </td>
              <td className="px-3 py-2 text-xs">{formatBytes(o.size_bytes)}</td>
              <td className="px-3 py-2 font-mono text-xs">{formatQuantity(storage[o.id] ?? 0)} GB-h</td>
              <td className="px-3 py-2">
                {statusBadge(o.status)} {o.outbound_disabled && <Badge tone="warn">outbound off</Badge>}
              </td>
            </tr>
          ))}
        </Table>
      )}
    </>
  );
}

/** Step-up auth fields, for destructive admin actions (spec §7.2). */
function useReauth() {
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const fields = (
    <>
      <Field label="Your password">{(id) => <Input id={id} type="password" value={password} onChange={(e) => setPassword(e.target.value)} />}</Field>
      <Field label="Your authenticator code">{(id) => <Input id={id} inputMode="numeric" value={code} onChange={(e) => setCode(e.target.value)} />}</Field>
    </>
  );
  return { fields, ready: !!password && code.length >= 6, run: () => api.reauth({ password, code }), reset: () => (setPassword(""), setCode("")) };
}

/** Admin → one organisation: plan, overrides, allowance, cluster, suspension, break-glass. */
export function AdminOrgPage() {
  const { id } = useParams({ from: "/app/admin/orgs/$id" });
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["admin", "org", id], queryFn: () => api.adminOrg(id) });
  const plans = useQuery({ queryKey: ["admin", "plans"], queryFn: api.plans });
  const clusters = useQuery({ queryKey: ["admin", "clusters"], queryFn: api.sharedClusters });
  const [err, setErr] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  if (q.isPending) return <PageSkeleton />;
  if (q.isError) return <Alert>{errorMessage(q.error)}</Alert>;
  const o = q.data;
  const refresh = async () => {
    await qc.invalidateQueries({ queryKey: ["admin"] });
  };
  const save = async (b: Parameters<typeof api.adminUpdateOrg>[1]) => {
    setErr(null);
    setSaved(false);
    try {
      qc.setQueryData(["admin", "org", id], await api.adminUpdateOrg(id, b));
      setSaved(true);
      await refresh();
    } catch (e) {
      setErr(errorMessage(e));
    }
  };
  return (
    <>
      <PageHeading
        title={
          <span className="flex items-center gap-2">
            {o.org.name} {statusBadge(o.org.status)}
          </span>
        }
        description={`${o.org.member_count} members · ${o.org.project_count} projects · ${formatBytes(o.org.size_bytes)} · since ${formatDate(o.org.created_at)}`}
      />
      <div className="flex max-w-3xl flex-col gap-4">
        {o.org.status === "suspended" && <Alert title="Suspended">{o.org.suspended_reason}</Alert>}
        {err && <Alert>{err}</Alert>}
        {saved && <Alert tone="ok">Saved.</Alert>}
        <PlanCard org={o} plans={plans.data?.items ?? []} onSave={save} />
        <AllowanceCard org={o} onSave={save} />
        <Panel title="Outbound traffic">
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={o.org.outbound_disabled}
              onChange={(e) => save({ outbound_disabled: e.target.checked })}
              data-testid="outbound-disabled"
            />
            Disable webhooks and HTTP jobs for this organisation, without suspending its databases (V2 §10.7)
          </label>
          <OutboundCard org={id} />
        </Panel>
        <Panel title="Shared cluster">
          <p className="mb-3 text-sm text-muted">
            An organisation can have its own shared cluster: its new projects are placed there, and no other organisation's ever are. Pick an empty cluster.
          </p>
          {o.clusters.length > 0 ? (
            <div className="flex flex-col gap-2">
              {o.clusters.map((c) => (
                <div key={c.id} className="flex items-center justify-between text-sm">
                  <span>
                    {c.node_name} · {c.project_count} projects
                  </span>
                  <Button className="text-xs" onClick={() => api.setOrgCluster(id, c.id, false).then(refresh, (e) => setErr(errorMessage(e)))}>
                    Release
                  </Button>
                </div>
              ))}
            </div>
          ) : (
            <ClusterPicker
              options={(clusters.data?.items ?? []).filter((c) => !c.org_id)}
              onPick={(cid) => api.setOrgCluster(id, cid, true).then(refresh, (e) => setErr(errorMessage(e)))}
            />
          )}
        </Panel>
        <OrgBillingPanel org={id} />
        <SuspendCard org={o} onDone={refresh} />
        <BreakGlassCard org={o} onDone={refresh} />
      </div>
    </>
  );
}

function ClusterPicker({ options, onPick }: { options: { id: string; node_name: string; project_count: number }[]; onPick: (id: string) => void }) {
  const [pick, setPick] = useState("");
  return (
    <div className="flex gap-2">
      <Select aria-label="Cluster" value={pick} onChange={(e) => setPick(e.target.value)}>
        <option value="">Choose a cluster…</option>
        {options.map((c) => (
          <option key={c.id} value={c.id}>
            {c.node_name} ({c.project_count} projects)
          </option>
        ))}
      </Select>
      <Button disabled={!pick} onClick={() => onPick(pick)}>
        Reserve
      </Button>
    </div>
  );
}

function PlanCard({
  org,
  plans,
  onSave,
}: {
  org: AdminOrg;
  plans: Plan[];
  onSave: (b: { plan_id?: string; limit_overrides?: Record<string, number | null> }) => void;
}) {
  const [overrides, setOverrides] = useState<Record<string, string>>(() =>
    Object.fromEntries(Object.entries(org.limit_overrides).map(([k, v]) => [k, v == null ? "unlimited" : String(v)])),
  );
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const out: Record<string, number | null> = {};
    for (const [k, v] of Object.entries(overrides)) {
      if (v.trim() === "") continue;
      out[k] = v.trim() === "unlimited" ? null : Number(v);
    }
    onSave({ limit_overrides: out });
  };
  return (
    <Panel title="Plan and limits">
      <div className="flex flex-col gap-3">
        <Field label="Plan">
          {(id) => (
            <Select id={id} value={org.plan_id} onChange={(e) => onSave({ plan_id: e.target.value })} data-testid="org-plan">
              {plans.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </Select>
          )}
        </Field>
        <form onSubmit={submit} className="flex flex-col gap-2">
          <p className="text-sm text-muted">Overrides for this organisation only: a number, &ldquo;unlimited&rdquo;, or blank for the plan&rsquo;s.</p>
          <div className="grid gap-2 sm:grid-cols-2">
            {Object.entries(LIMIT_LABELS).map(([k, meta]) => (
              <Field key={k} label={`${meta.label}${meta.unit ? ` (${meta.unit})` : ""}`} hint={`Plan: ${org.limits[k] ?? "unlimited"}`}>
                {(id) => (
                  <Input
                    id={id}
                    value={overrides[k] ?? ""}
                    placeholder={String(org.limits[k] ?? "unlimited")}
                    onChange={(e) => setOverrides((o) => ({ ...o, [k]: e.target.value }))}
                  />
                )}
              </Field>
            ))}
          </div>
          <Button type="submit" className="self-start">
            Save overrides
          </Button>
        </form>
      </div>
    </Panel>
  );
}

function AllowanceCard({ org, onSave }: { org: AdminOrg; onSave: (b: { dedicated_allowance: AdminOrg["dedicated_allowance"] }) => void }) {
  const a = org.dedicated_allowance;
  const [v, setV] = useState({ instances: a.instances, cpus: a.cpus, memory_mb: a.memory_mb, disk_gb: a.disk_gb });
  return (
    <Panel title="Dedicated allowance">
      {a.unlimited ? (
        <p className="text-sm text-muted">The Unlimited plan needs no allowance.</p>
      ) : (
        <form
          className="flex flex-wrap items-end gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            onSave({ dedicated_allowance: v });
          }}
        >
          {(
            [
              ["instances", "Instances"],
              ["cpus", "vCPU"],
              ["memory_mb", "RAM (MB)"],
              ["disk_gb", "Disk (GB)"],
            ] as const
          ).map(([k, label]) => (
            <Field key={k} label={label}>
              {(id) => (
                <Input id={id} type="number" min={0} className="w-28" value={v[k]} onChange={(e) => setV((x) => ({ ...x, [k]: Number(e.target.value) }))} />
              )}
            </Field>
          ))}
          <Button type="submit">Save allowance</Button>
        </form>
      )}
    </Panel>
  );
}

function SuspendCard({ org, onDone }: { org: AdminOrg; onDone: () => Promise<void> }) {
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const re = useReauth();
  const suspended = org.org.status === "suspended";
  const run = async (e?: FormEvent) => {
    e?.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      if (suspended) await api.reinstateOrg(org.org.id);
      else {
        await re.run();
        await api.suspendOrg(org.org.id, reason);
      }
      setOpen(false);
      re.reset();
      await onDone();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Panel title="Suspension" tone="danger">
      <p className="mb-3 text-sm text-muted">
        Suspending takes every project offline (pooler routes and logins off), pauses scheduled backups after one last backup, and emails the owners. Data is
        kept; reinstating reverses it all.
      </p>
      {err && <Alert>{err}</Alert>}
      {suspended ? (
        <Button variant="primary" busy={busy} onClick={() => run()} data-testid="reinstate-org">
          Reinstate
        </Button>
      ) : (
        <Button variant="danger" onClick={() => setOpen(true)} disabled={org.org.status !== "active"} data-testid="suspend-org">
          Suspend…
        </Button>
      )}
      <Dialog title={`Suspend ${org.org.name}`} open={open} onOpenChange={setOpen}>
        <form className="flex flex-col gap-3" onSubmit={run}>
          <Field label="Reason (shown to its members)">
            {(id) => <Input id={id} required value={reason} onChange={(e) => setReason(e.target.value)} maxLength={1000} />}
          </Field>
          {re.fields}
          {err && <Alert>{err}</Alert>}
          <Button type="submit" variant="danger" busy={busy} disabled={!reason.trim() || !re.ready}>
            Suspend
          </Button>
        </form>
      </Dialog>
    </Panel>
  );
}

function BreakGlassCard({ org, onDone }: { org: AdminOrg; onDone: () => Promise<void> }) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState("");
  const [minutes, setMinutes] = useState(60);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const re = useReauth();
  const active = org.break_glass[0];
  const enter = async () => {
    setCurrentOrg(org.org.id);
    await qc.invalidateQueries();
    await navigate({ to: "/projects" });
  };
  const start = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      await re.run();
      await api.startBreakGlass(org.org.id, reason, minutes);
      setOpen(false);
      re.reset();
      await onDone();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Panel title="Break-glass access" tone="warn">
      <p className="mb-3 text-sm text-muted">
        For support or an incident: act as an admin of this organisation for up to 4 hours. Every owner is emailed at once, everyone in the organisation sees a
        banner, every action is flagged in its audit log and yours, and any owner can end it.
      </p>
      {active ? (
        <div className="flex flex-wrap items-center justify-between gap-2 text-sm" data-testid="break-glass-active">
          <span>
            Open until {new Date(active.expires_at).toLocaleString()}: {active.reason}
          </span>
          <Button variant="primary" onClick={enter} data-testid="break-glass-enter">
            Open the organisation
          </Button>
        </div>
      ) : (
        <Button onClick={() => setOpen(true)} disabled={org.org.personal && org.org.status !== "active"} data-testid="break-glass-start">
          Start break-glass…
        </Button>
      )}
      <Dialog title={`Break-glass access to ${org.org.name}`} open={open} onOpenChange={setOpen}>
        <form className="flex flex-col gap-3" onSubmit={start}>
          <Field label="Reason (the owners see it)">
            {(id) => <Input id={id} required value={reason} onChange={(e) => setReason(e.target.value)} maxLength={1000} />}
          </Field>
          <Field label="Duration">
            {(id) => (
              <Select id={id} value={minutes} onChange={(e) => setMinutes(Number(e.target.value))}>
                {[15, 30, 60, 120, 240].map((m) => (
                  <option key={m} value={m}>
                    {m < 60 ? `${m} minutes` : `${m / 60} hour${m === 60 ? "" : "s"}`}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          {re.fields}
          {err && <Alert>{err}</Alert>}
          <Button type="submit" variant="danger" busy={busy} disabled={!reason.trim() || !re.ready}>
            Start and email the owners
          </Button>
        </form>
      </Dialog>
    </Panel>
  );
}

/** Admin → Plans: quota plan templates (V2 §10.3). */
export function AdminPlansPage() {
  const q = useQuery({ queryKey: ["admin", "plans"], queryFn: api.plans });
  const qc = useQueryClient();
  const [editing, setEditing] = useState<Plan | "new" | null>(null);
  return (
    <>
      <PageHeading
        title="Plans"
        description="Quota templates. Assign one to an organisation, and override single limits there."
        actions={
          <Button variant="primary" onClick={() => setEditing("new")}>
            New plan
          </Button>
        }
      />
      {q.data && (
        <Table head={["Plan", "Organisations", ...q.data.keys.slice(0, 5).map((k) => (LIMIT_LABELS[k]?.label ?? k) + (LIMIT_LABELS[k]?.unit ? ` (${LIMIT_LABELS[k].unit})` : "")), ""]}>
          {q.data.items.map((p) => (
            <tr key={p.id} data-testid={`plan-${p.name}`}>
              <td className="px-3 py-2 font-medium">{p.name}</td>
              <td className="px-3 py-2">{p.org_count}</td>
              {q.data.keys.slice(0, 5).map((k) => (
                <td key={k} className="px-3 py-2 text-xs">
                  {p.limits[k] ?? "—"}
                </td>
              ))}
              <td className="px-3 py-2 text-right">
                <Button className="text-xs" onClick={() => setEditing(p)}>
                  Edit
                </Button>
              </td>
            </tr>
          ))}
        </Table>
      )}
      {editing && (
        <PlanModal
          plan={editing === "new" ? null : editing}
          keys={q.data?.keys ?? []}
          onClose={() => setEditing(null)}
          onSaved={async () => {
            setEditing(null);
            await qc.invalidateQueries({ queryKey: ["admin"] });
          }}
        />
      )}
    </>
  );
}

function PlanModal({ plan, keys, onClose, onSaved }: { plan: Plan | null; keys: string[]; onClose: () => void; onSaved: () => Promise<void> }) {
  const [name, setName] = useState(plan?.name ?? "");
  const [limits, setLimits] = useState<Record<string, string>>(() => Object.fromEntries(Object.entries(plan?.limits ?? {}).map(([k, v]) => [k, String(v)])));
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    const out: Record<string, number> = {};
    for (const [k, v] of Object.entries(limits)) if (v.trim() !== "") out[k] = Number(v);
    try {
      if (plan) await api.updatePlan(plan.id, { name, limits: out });
      else await api.createPlan({ name, limits: out });
      await onSaved();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <SidePanel
      title={plan ? `Edit ${plan.name}` : "New plan"}
      description="Leave a limit blank for unlimited."
      open
      onOpenChange={(o) => !o && onClose()}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" form="plan-form" variant="primary" busy={busy}>
            Save
          </Button>
        </>
      }
    >
      <form id="plan-form" className="flex flex-col gap-4" onSubmit={submit}>
        <Field label="Name">{(id) => <Input id={id} required value={name} onChange={(e) => setName(e.target.value)} maxLength={64} />}</Field>
        <div className="grid gap-3 sm:grid-cols-2">
          {keys.map((k) => (
            <Field key={k} label={`${LIMIT_LABELS[k]?.label ?? k}${LIMIT_LABELS[k]?.unit ? ` (${LIMIT_LABELS[k].unit})` : ""}`}>
              {(id) => <Input id={id} type="number" min={0} value={limits[k] ?? ""} onChange={(e) => setLimits((l) => ({ ...l, [k]: e.target.value }))} />}
            </Field>
          ))}
        </div>
        {err && <Alert>{err}</Alert>}
      </form>
    </SidePanel>
  );
}

/** Admin → Dedicated requests (V2 §10.6). */
export function DedicatedRequestsPage() {
  const [status, setStatus] = useState("pending");
  const q = useQuery({ queryKey: ["admin", "dedicated-requests", status], queryFn: () => api.dedicatedRequests(status || undefined) });
  const [deciding, setDeciding] = useState<{ r: DedicatedRequest; approve: boolean } | null>(null);
  const qc = useQueryClient();
  return (
    <>
      <PageHeading title="Dedicated requests" description="Promotions beyond an organisation's dedicated allowance wait here for you." />
      <div className="mb-3">
        <Select aria-label="Status" value={status} onChange={(e) => setStatus(e.target.value)}>
          <option value="pending">Pending</option>
          <option value="">All</option>
        </Select>
      </div>
      {q.data && q.data.items.length === 0 && (
        <EmptyState title="Nothing waiting" icon={<Inbox />}>
          Promotions beyond an allowance appear here for you to approve.
        </EmptyState>
      )}
      {q.data && q.data.items.length > 0 && (
        <Table head={["Organisation", "Project", "Size", "Reason", "Asked", ""]}>
          {q.data.items.map((r) => (
            <tr key={r.id} data-testid="dedicated-request">
              <td className="px-3 py-2">{r.org_name}</td>
              <td className="px-3 py-2">
                {r.project_name}
                <div className="text-xs text-muted">{r.requested_by}</div>
              </td>
              <td className="px-3 py-2 text-xs">
                {r.profile}, {r.volume_gb} GB
              </td>
              <td className="px-3 py-2 text-xs">{r.reason ?? "—"}</td>
              <td className="px-3 py-2 text-xs text-muted">{formatDate(r.created_at)}</td>
              <td className="px-3 py-2 text-right">
                {r.status === "pending" ? (
                  <div className="flex justify-end gap-1">
                    <Button className="text-xs" variant="primary" onClick={() => setDeciding({ r, approve: true })}>
                      Approve
                    </Button>
                    <Button className="text-xs" onClick={() => setDeciding({ r, approve: false })}>
                      Reject
                    </Button>
                  </div>
                ) : (
                  <Badge tone={r.status === "approved" ? "ok" : "muted"}>{r.status}</Badge>
                )}
              </td>
            </tr>
          ))}
        </Table>
      )}
      {deciding && (
        <DecideModal
          {...deciding}
          onClose={() => setDeciding(null)}
          onDone={async () => {
            setDeciding(null);
            await qc.invalidateQueries({ queryKey: ["admin"] });
          }}
        />
      )}
    </>
  );
}

function DecideModal({ r, approve, onClose, onDone }: { r: DedicatedRequest; approve: boolean; onClose: () => void; onDone: () => Promise<void> }) {
  const [note, setNote] = useState("");
  const [raise, setRaise] = useState(true);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      if (approve) await api.approveDedicatedRequest(r.id, { note: note || undefined, raise_allowance: raise });
      else await api.rejectDedicatedRequest(r.id, { note: note || undefined });
      await onDone();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog title={`${approve ? "Approve" : "Reject"}: ${r.project_name}`} open onOpenChange={(o) => !o && onClose()}>
      <form className="flex flex-col gap-3" onSubmit={submit}>
        {approve && (
          <p className="text-sm text-muted">
            The promotion starts now ({r.profile}, {r.volume_gb} GB). The requester is emailed.
          </p>
        )}
        <Field label="Note to the requester (optional)">{(id) => <Input id={id} value={note} onChange={(e) => setNote(e.target.value)} />}</Field>
        {approve && (
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={raise} onChange={(e) => setRaise(e.target.checked)} /> Raise the organisation&rsquo;s allowance to include it
          </label>
        )}
        {err && <Alert>{err}</Alert>}
        <Button type="submit" variant={approve ? "primary" : "danger"} busy={busy}>
          {approve ? "Approve and promote" : "Reject"}
        </Button>
      </form>
    </Dialog>
  );
}

/** The internal hosts an org may reach and its requests by host (V2 §10.7). */
function OutboundCard({ org }: { org: string }) {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["admin", "outbound", org], queryFn: () => api.orgOutbound(org) });
  const [hosts, setHosts] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  if (!q.data) return q.isError ? <Alert>{errorMessage(q.error)}</Alert> : null;
  const value = hosts ?? q.data.allowlist.join("\n");
  const save = async () => {
    setErr(null);
    try {
      qc.setQueryData(["admin", "outbound", org], await api.setOrgOutbound(org, value.split(/[\s,]+/).filter(Boolean)));
      setHosts(null);
    } catch (e) {
      setErr(errorMessage(e));
    }
  };
  return (
    <div className="mt-4 flex flex-col gap-3 text-sm">
      <Field
        label="Internal hosts this organisation may reach"
        hint="One per line. Listed hosts may be private or loopback addresses and may use plain http://. Link-local and cloud metadata addresses can never be allowed."
      >
        {(id) => (
          <textarea
            id={id}
            className="h-20 rounded-md border border-line bg-surface p-2 font-mono text-xs"
            value={value}
            onChange={(e) => setHosts(e.target.value)}
            data-testid="outbound-allowlist"
          />
        )}
      </Field>
      {err && <Alert>{err}</Alert>}
      <Button className="self-start text-xs" onClick={save} disabled={hosts === null}>
        Save allow-list
      </Button>
      <p className="text-xs font-medium text-muted">Requests in the last 30 days, by destination host</p>
      {q.data.hosts.length === 0 ? (
        <p className="text-muted">None.</p>
      ) : (
        <Table head={["Host", "Requests", "Failed", "Last"]}>
          {q.data.hosts.map((h) => (
            <tr key={h.host}>
              <td className="px-3 py-1.5 font-mono text-xs">{h.host}</td>
              <td className="px-3 py-1.5 tabular-nums">{h.requests}</td>
              <td className="px-3 py-1.5 tabular-nums">{h.failures}</td>
              <td className="px-3 py-1.5 text-xs">{h.last_day}</td>
            </tr>
          ))}
        </Table>
      )}
    </div>
  );
}
