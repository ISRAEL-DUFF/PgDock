import { Link } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { api, errorMessage, type Project, type ProjectCredentials } from "../api/client";
import { ConfirmDestroy } from "../components/ConfirmDelete";
import { ProvisionProgress } from "../components/ProvisionProgress";
import { useOperationToast } from "../components/Toasts";
import { GitBranch, GitBranchPlus } from "lucide-react";
import {
  Alert,
  Badge,
  Button,
  Dialog,
  EmptyState,
  Field,
  FormRow,
  Input,
  KeyValues,
  Page,
  Panel,
  Section,
  Select,
  SidePanel,
  StatusBadge,
  Table,
  TableSkeleton,
} from "../components/ui";
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
    <span className={soon ? "text-warn-text" : "text-muted"} title={formatDate(at)} data-testid="branch-expiry">
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
    <Page
      title="Branches"
      description={
        <>
          Throwaway copies of {p.name} on the shared tier, each with its own connection strings: try a migration, give a pull request its own database, or debug
          against real data. Resets keep a branch's URL and password; webhooks and scheduled jobs are not copied.
          {p.sensitive_data && " This project contains sensitive data, so branches copy the schema only unless a project admin asks for the data."}
        </>
      }
      actions={
        canBranch(p) && (
          <Button variant="primary" icon={<GitBranchPlus className="h-3.5 w-3.5" />} onClick={() => setCreating(true)} disabled={p.status !== "active"}>
            New branch
          </Button>
        )
      }
      testId="project-branches"
    >
      {q.isPending && <TableSkeleton rows={3} cols={5} />}
      {q.isError && <Alert>{errorMessage(q.error)}</Alert>}
      {q.data && q.data.items.length === 0 && (
        <EmptyState title="No branches" icon={<GitBranch />}>
          Create one to try a change without touching {p.name}.
        </EmptyState>
      )}
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
    </Page>
  );
}

function CreateBranchDialog({ p, onClose, onCreated }: { p: Project; onClose: () => void; onCreated: (c: ProjectCredentials) => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [source, setSource] = useState<"backup" | "live">(p.last_backup_at ? "backup" : "live");
  const [schemaOnly, setSchemaOnly] = useState(!!p.sensitive_data);
  const [ttl, setTtl] = useState(168);
  const [copyFiles, setCopyFiles] = useState(false);
  const services = useQuery({ queryKey: ["services", p.id], queryFn: () => api.backendServices(p.id), retry: false });
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const sensitiveFull = !!p.sensitive_data && !schemaOnly;
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      const c = await api.createBranch(p.id, { name, source, schema_only: schemaOnly, ttl_hours: ttl, copy_files: services.data?.enabled ? copyFiles : undefined });
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
    <SidePanel
      open
      onOpenChange={(o) => !o && onClose()}
      title={`Create a branch of ${p.name}`}
      description="A copy on the shared tier, with its own connection strings."
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" form="create-branch" variant="primary" busy={busy} disabled={!name.trim()}>
            Create branch
          </Button>
        </>
      }
    >
      <form id="create-branch" className="flex flex-col gap-5" onSubmit={submit}>
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
        {services.data?.enabled && (
          <div className="flex flex-col gap-1 text-sm" data-testid="branch-services">
            <p className="text-xs text-muted">
              {p.name} has backend services: the branch gets its own API URL and keys, and {p.name}'s settings without its auth secrets (SMTP, SMS, OAuth and captcha), which you set on the branch.
            </p>
            <label className="flex items-center gap-2">
              <input type="checkbox" checked={copyFiles} onChange={(e) => setCopyFiles(e.target.checked)} data-testid="branch-copy-files" />
              Copy its stored files too (in the background; they count toward file storage)
            </label>
          </div>
        )}
        <p className="text-xs text-muted">Webhooks and scheduled jobs are not copied to the branch. It counts toward your organisation's branch quota.</p>
        {err && <Alert>{err}</Alert>}
      </form>
    </SidePanel>
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
    <Page
      title={
        <span className="flex items-center gap-3">
          {b.name} <Badge tone="accent">branch</Badge> {b.status !== "active" && <Badge tone="warn">{b.status}</Badge>}
        </span>
      }
      description={
        parent.data ? (
          <>
            A branch of{" "}
            <Link to="/projects/$id/branches" params={{ id: parent.data.id }} className="text-accent-text underline underline-offset-2 hover:no-underline">
              {parent.data.name}
            </Link>
            .
          </>
        ) : undefined
      }
      testId="branch-controls"
    >
      <Panel title="This branch">
        <KeyValues
          items={[
            ["Contents", contents(b)],
            [
              "Expires",
              <span key="e">
                <Expiry b={b} />
                {b.branch?.expires_at && <span className="ml-2 text-xs text-muted">({formatDate(b.branch.expires_at)})</span>}
              </span>,
            ],
            ["Backups", b.branch?.backups ? "nightly" : "none (branches are disposable)"],
          ]}
        />
      </Panel>
      {can && (
        <Panel title="Manage">
          <FormRow
            label="Reset from the parent"
            description="Replaces the branch's data with a fresh copy of its parent. The database name, URL, password and everyone's logins stay the same; clients wait at the pooler while it runs."
          >
            <div>
              <Button onClick={() => setResetting(true)} disabled={b.status !== "active" && b.status !== "error"} data-testid="reset-branch">
                Reset branch…
              </Button>
            </div>
          </FormRow>
          <FormRow label="Expiry" description="Branches are deleted when they expire.">
            <div className="flex flex-wrap items-center gap-2">
              <Select aria-label="Expire" value={extend} onChange={(e) => setExtend(Number(e.target.value))} data-testid="extend-ttl">
                {TTLS.map((t) => (
                  <option key={t.hours} value={t.hours}>
                    {t.hours ? `${t.label} from now` : t.label}
                  </option>
                ))}
              </Select>
              <Button
                busy={busy === "extend"}
                data-testid="extend-branch"
                onClick={() =>
                  run("extend", async () => {
                    await api.updateProject(b.id, extend ? { expires_at: new Date(Date.now() + extend * 3600 * 1000).toISOString() } : { no_expiry: true });
                  })
                }
              >
                Set expiry
              </Button>
            </div>
          </FormRow>
          <FormRow label="Detach" description="Keeps the data and credentials; it then counts as a project and can be promoted.">
            <div>
              <Button
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
          </FormRow>
        </Panel>
      )}
      {can && (
        <Section title="Danger zone">
          <Panel tone="danger">
            <FormRow label="Delete branch" description={b.branch?.backups ? "A final backup is taken first." : "Branches take no final backup."}>
              <div>
                <Button variant="danger" onClick={() => setDeleting(true)}>
                  Delete branch…
                </Button>
              </div>
            </FormRow>
          </Panel>
        </Section>
      )}
      {err && <Alert>{err}</Alert>}
      {resetting && (
        <Dialog
          open
          onOpenChange={(o) => !o && setResetting(false)}
          title={`Reset ${b.name}`}
          description={`Everything in ${b.name} is replaced with a copy of its parent${b.branch?.source === "live" ? " as it is now" : "'s latest backup"}.`}
          footer={
            <>
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
            </>
          }
        />
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
    </Page>
  );
}
