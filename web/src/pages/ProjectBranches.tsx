import { Link } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { api, errorMessage, type Project, type ProjectCredentials } from "../api/client";
import { ConfirmDestroy } from "../components/ConfirmDelete";
import { ProvisionProgress } from "../components/ProvisionProgress";
import { useOperationToast } from "../components/Toasts";
import { Alert, Badge, Button, Card, EmptyState, Field, Input, Modal, Select, Spinner, StatusBadge, Table } from "../components/ui";
import { formatDate, timeUntil } from "../lib/format";
import { useProject } from "./ProjectOverview";

/** TTL choices for the create dialog, in hours (0 keeps it). */
const TTLS: { hours: number; label: string }[] = [
  { hours: 1, label: "1 hour" },
  { hours: 24, label: "1 day" },
  { hours: 72, label: "3 days" },
  { hours: 168, label: "7 days" },
  { hours: 720, label: "30 days" },
  { hours: 0, label: "Keep until deleted" },
];

const canBranch = (p: Project) => p.my_role === "admin" || p.my_role === "developer";

function contents(b: Project) {
  if (!b.branch) return "";
  return `${b.branch.source === "live" ? "Live copy" : "Latest backup"}, ${b.branch.schema_only ? "schema only" : "schema and data"}`;
}

function Expiry({ b }: { b: Project }) {
  const at = b.branch?.expires_at;
  if (!at) return <span className="text-muted">never</span>;
  const soon = new Date(at).getTime() - Date.now() < 24 * 3600 * 1000;
  return (
    <span className={soon ? "text-warn" : "text-muted"} title={formatDate(at)} data-testid="branch-expiry">
      {timeUntil(at)}
    </span>
  );
}

/** Project → Branches (V2 §8): a project's branches, or a branch's own controls. */
export function ProjectBranchesPage() {
  const { data: p } = useProject();
  if (!p) return null;
  return p.parent_project_id ? <BranchControls b={p} /> : <BranchList p={p} />;
}

function BranchList({ p }: { p: Project }) {
  const q = useQuery({
    queryKey: ["branches", p.id],
    queryFn: () => api.branches(p.id),
    refetchInterval: (q) => (q.state.data?.items.some((b) => b.status !== "active") ? 2000 : 15_000),
  });
  const [creating, setCreating] = useState(false);
  const [created, setCreated] = useState<ProjectCredentials | null>(null);
  if (created) return <ProvisionProgress creds={created} progressTitle="Branch" />;
  return (
    <div className="flex flex-col gap-4">
      <Card
        title="Branches"
        actions={
          canBranch(p) && (
            <Button variant="primary" className="text-xs" onClick={() => setCreating(true)} disabled={p.status !== "active"}>
              New branch
            </Button>
          )
        }
      >
        <p className="text-sm text-muted">
          A branch is a throwaway copy of {p.name} on the shared tier, with its own connection strings: try a migration, give a pull request its
          own database, or debug against real data. Resets keep its URL and password. Webhooks and scheduled jobs are not copied.
          {p.sensitive_data && " This project contains sensitive data, so branches copy the schema only unless a project admin asks for the data."}
        </p>
      </Card>
      {q.isPending && <Spinner />}
      {q.isError && <Alert>{errorMessage(q.error)}</Alert>}
      {q.data && q.data.items.length === 0 && <EmptyState title="No branches" />}
      {q.data && q.data.items.length > 0 && (
        <Table head={["Branch", "Status", "Contents", "Expires", "Created"]}>
          {q.data.items.map((b) => (
            <tr key={b.id} data-testid="branch-row">
              <td className="px-3 py-2">
                <Link to="/projects/$id/branches" params={{ id: b.id }} className="font-medium hover:underline">
                  {b.name}
                </Link>
                <div className="font-mono text-xs text-muted">{b.db_name}</div>
              </td>
              <td className="px-3 py-2">
                <StatusBadge status={b.status} />
              </td>
              <td className="px-3 py-2 text-muted">{contents(b)}</td>
              <td className="px-3 py-2">
                <Expiry b={b} />
              </td>
              <td className="px-3 py-2 text-muted">{formatDate(b.created_at)}</td>
            </tr>
          ))}
        </Table>
      )}
      {creating && <CreateBranchDialog p={p} onClose={() => setCreating(false)} onCreated={setCreated} />}
    </div>
  );
}

function CreateBranchDialog({ p, onClose, onCreated }: { p: Project; onClose: () => void; onCreated: (c: ProjectCredentials) => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [source, setSource] = useState<"backup" | "live">(p.last_backup_at ? "backup" : "live");
  const [schemaOnly, setSchemaOnly] = useState(!!p.sensitive_data);
  const [ttl, setTtl] = useState(168);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const sensitiveFull = !!p.sensitive_data && !schemaOnly;
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      const c = await api.createBranch(p.id, { name, source, schema_only: schemaOnly, ttl_hours: ttl });
      await qc.invalidateQueries({ queryKey: ["branches", p.id] });
      await qc.invalidateQueries({ queryKey: ["projects"] });
      onCreated(c);
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal title={`Branch ${p.name}`} open onClose={onClose}>
      <form className="flex flex-col gap-4" onSubmit={submit}>
        <Field label="Branch name">
          {(id) => <Input id={id} required maxLength={64} value={name} onChange={(e) => setName(e.target.value)} placeholder="feature-login" autoFocus />}
        </Field>
        <fieldset className="flex flex-col gap-1 text-sm">
          <legend className="mb-1 font-medium">Copy from</legend>
          <label className="flex items-center gap-2">
            <input type="radio" name="source" checked={source === "backup"} onChange={() => setSource("backup")} disabled={!p.last_backup_at} />
            The latest backup{p.last_backup_at ? ` (${formatDate(p.last_backup_at)}; no load on ${p.name})` : " (none yet)"}
          </label>
          <label className="flex items-center gap-2">
            <input type="radio" name="source" checked={source === "live"} onChange={() => setSource("live")} />
            Live: a fresh dump of {p.name} now
          </label>
        </fieldset>
        <fieldset className="flex flex-col gap-1 text-sm">
          <legend className="mb-1 font-medium">Contents</legend>
          <label className="flex items-center gap-2">
            <input type="radio" name="contents" checked={!schemaOnly} onChange={() => setSchemaOnly(false)} />
            Schema and data
          </label>
          <label className="flex items-center gap-2">
            <input type="radio" name="contents" checked={schemaOnly} onChange={() => setSchemaOnly(true)} data-testid="schema-only" />
            Schema only
          </label>
        </fieldset>
        {sensitiveFull && (
          <Alert tone="warn">{p.name} contains sensitive data. A full copy needs the project admin role, and the branch will hold that data too.</Alert>
        )}
        <Field label="Delete it after">
          {(id) => (
            <Select id={id} value={ttl} onChange={(e) => setTtl(Number(e.target.value))} data-testid="branch-ttl">
              {TTLS.map((t) => (
                <option key={t.hours} value={t.hours}>
                  {t.label}
                </option>
              ))}
            </Select>
          )}
        </Field>
        <p className="text-xs text-muted">Webhooks and scheduled jobs are not copied to the branch. It counts toward your organisation's branch quota.</p>
        {err && <Alert>{err}</Alert>}
        <div className="flex justify-end gap-2">
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={!name.trim()}>
            Create branch
          </Button>
        </div>
      </form>
    </Modal>
  );
}

/** A branch's own controls: reset, extend, detach, delete. */
function BranchControls({ b }: { b: Project }) {
  const qc = useQueryClient();
  const toast = useOperationToast();
  const parent = useQuery({ queryKey: ["project", b.parent_project_id], queryFn: () => api.project(b.parent_project_id!), retry: false });
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [extend, setExtend] = useState(168);
  const [deleting, setDeleting] = useState(false);
  const [resetting, setResetting] = useState(false);
  const can = canBranch(b);
  const refresh = async () => {
    await qc.invalidateQueries({ queryKey: ["project", b.id] });
    await qc.invalidateQueries({ queryKey: ["projects"] });
    if (b.parent_project_id) await qc.invalidateQueries({ queryKey: ["branches", b.parent_project_id] });
  };
  const run = async (what: string, f: () => Promise<void>) => {
    setBusy(what);
    setErr(null);
    try {
      await f();
      await refresh();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(null);
    }
  };
  return (
    <div className="flex flex-col gap-4" data-testid="branch-controls">
      <Card title="This is a branch">
        <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
          <dt className="text-muted">Parent</dt>
          <dd>
            {parent.data ? (
              <Link to="/projects/$id/branches" params={{ id: parent.data.id }} className="hover:underline">
                {parent.data.name}
              </Link>
            ) : (
              "—"
            )}
          </dd>
          <dt className="text-muted">Contents</dt>
          <dd>{contents(b)}</dd>
          <dt className="text-muted">Expires</dt>
          <dd>
            <Expiry b={b} />
            {b.branch?.expires_at && <span className="ml-2 text-xs text-muted">({formatDate(b.branch.expires_at)})</span>}
          </dd>
          <dt className="text-muted">Backups</dt>
          <dd>{b.branch?.backups ? "nightly" : "none (branches are disposable)"}</dd>
        </dl>
      </Card>
      {can && (
        <Card title="Reset from the parent">
          <p className="text-sm text-muted">
            Replaces the branch's data with a fresh copy of its parent. The database name, URL, password and everyone's logins stay the same, so CI
            and .env files keep working. Clients wait at the pooler while it runs.
          </p>
          <div className="mt-3">
            <Button onClick={() => setResetting(true)} disabled={b.status !== "active" && b.status !== "error"} data-testid="reset-branch">
              Reset branch…
            </Button>
          </div>
        </Card>
      )}
      {can && (
        <Card title="Keep it longer">
          <div className="flex flex-wrap items-end gap-2">
            <Field label="Expire">
              {(id) => (
                <Select id={id} value={extend} onChange={(e) => setExtend(Number(e.target.value))} data-testid="extend-ttl">
                  {TTLS.map((t) => (
                    <option key={t.hours} value={t.hours}>
                      {t.hours ? `${t.label} from now` : t.label}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
            <Button
              busy={busy === "extend"}
              data-testid="extend-branch"
              onClick={() =>
                run("extend", async () => {
                  await api.updateProject(
                    b.id,
                    extend ? { expires_at: new Date(Date.now() + extend * 3600 * 1000).toISOString() } : { no_expiry: true },
                  );
                })
              }
            >
              Set expiry
            </Button>
            <Button
              variant="ghost"
              busy={busy === "detach"}
              onClick={() =>
                run("detach", async () => {
                  await api.detachBranch(b.id);
                })
              }
            >
              Detach into a standalone project
            </Button>
          </div>
          <p className="mt-2 text-xs text-muted">Detaching keeps the data and credentials; it then counts as a project and can be promoted.</p>
        </Card>
      )}
      {can && (
        <div>
          <Button variant="danger" onClick={() => setDeleting(true)}>
            Delete branch…
          </Button>
        </div>
      )}
      {err && <Alert>{err}</Alert>}
      {resetting && (
        <Modal title={`Reset ${b.name}`} open onClose={() => setResetting(false)}>
          <div className="flex flex-col gap-3 text-sm">
            <p>Everything in {b.name} is replaced with a copy of its parent{b.branch?.source === "live" ? " as it is now" : "'s latest backup"}.</p>
            <div className="flex justify-end gap-2">
              <Button onClick={() => setResetting(false)}>Cancel</Button>
              <Button
                variant="primary"
                busy={busy === "reset"}
                data-testid="confirm-reset"
                onClick={() =>
                  run("reset", async () => {
                    const op = await api.resetBranch(b.id);
                    toast(op.id, `Reset · ${b.name}`);
                    setResetting(false);
                  })
                }
              >
                Reset branch
              </Button>
            </div>
          </div>
        </Modal>
      )}
      <ConfirmDestroy
        open={deleting}
        onClose={() => setDeleting(false)}
        title={`Delete ${b.name}`}
        name={b.name}
        description={`Drops the branch's database and its connection strings. ${b.branch?.backups ? "A final backup is taken first." : "Branches take no final backup."}`}
        action="Delete branch"
        run={async () => {
          const op = await api.deleteProject(b.id, b.name, !b.branch?.backups);
          toast(op.id, `Delete · ${b.name}`);
          await refresh();
        }}
      />
      {b.status !== "active" && <Badge tone="warn">{b.status}</Badge>}
    </div>
  );
}
