import { Link } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { api, errorMessage, type Backup, type Project, type ProjectCredentials } from "../api/client";
import { ConfirmDestroy } from "../components/ConfirmDelete";
import { ProvisionProgress } from "../components/ProvisionProgress";
import { ProjectStorageCard } from "../components/StorageTargets";
import { useOperationToast } from "../components/Toasts";
import { Alert, Badge, Button, Card, EmptyState, Field, Input, Modal, Spinner, StatusBadge, Table } from "../components/ui";
import { formatBytes, formatDate, relativeTime } from "../lib/format";
import { useCurrentOrg } from "../lib/org";
import { useProject } from "./ProjectOverview";

const kindLabels: Record<string, string> = {
  logical: "Nightly / manual",
  base: "Base backup (WAL-G)",
  final: "Final (before delete)",
  safety: "Safety (before restore)",
};

/** True when the latest backup is missing or older than 26 hours (spec §8.3). */
export function backupIsStale(lastBackupAt: string | null | undefined, now = Date.now()): boolean {
  return !lastBackupAt || now - new Date(lastBackupAt).getTime() > 26 * 3600 * 1000;
}

export function ProjectBackupsPage() {
  const { data: p } = useProject();
  const qc = useQueryClient();
  const { orgs } = useCurrentOrg();
  // Org owners can download a backup as a pg_dump file (V2 §10.10).
  const canExport = !!p && orgs.find((o) => o.id === p.org_id)?.role === "owner";
  const toast = useOperationToast();
  const overview = useQuery({ queryKey: ["backups", "overview", p?.org_id], queryFn: () => api.backupOverview(p?.org_id), enabled: !!p });
  const list = useQuery({
    queryKey: ["backups", { project: p?.id }],
    queryFn: () => api.backups({ org: p!.org_id, project_id: p!.id, limit: 200 }),
    enabled: !!p,
    refetchInterval: (q) => (q.state.data?.items.some((b) => b.status === "running") ? 2000 : 15_000),
  });
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [restoring, setRestoring] = useState<Backup | null>(null);
  const [created, setCreated] = useState<ProjectCredentials | null>(null);

  if (!p) return null;
  if (created) return <ProvisionProgress creds={created} progressTitle="Restore" />;

  const ov = overview.data;
  const ready = ov?.storage_configured && ov.key.exists;
  const backupNow = async () => {
    setBusy(true);
    setErr(null);
    try {
      const op = await api.backupNow(p.id);
      toast(op.id, `Backup · ${p.name}`);
      await qc.invalidateQueries({ queryKey: ["backups"] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex flex-col gap-4">
      {ov && !ready && (
        <Alert tone="warn" title="Backups are not configured">
          Set up S3 storage and the backup key in{" "}
          <Link to="/settings" className="underline">
            Settings
          </Link>{" "}
          first.
        </Alert>
      )}
      <Card
        title="Backups"
        actions={
          <Button variant="primary" className="text-xs" onClick={backupNow} busy={busy} disabled={!ready || p.status !== "active"}>
            Back up now
          </Button>
        }
      >
        <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
          <dt className="text-muted">Last backup</dt>
          <dd data-testid="last-backup">
            {p.last_backup_at ? (
              <span className={backupIsStale(p.last_backup_at) ? "text-warn" : undefined}>
                {formatDate(p.last_backup_at)} ({relativeTime(p.last_backup_at)})
              </span>
            ) : (
              <span className="text-warn">never</span>
            )}
          </dd>
          {ov && (
            <>
              <dt className="text-muted">Schedule</dt>
              <dd>
                {p.tier === "dedicated" ? "Daily base backup with WAL archived continuously" : "Nightly logical dump"}, starting{" "}
                {String(ov.window_hour_utc).padStart(2, "0")}:00 UTC (spread over the following hours)
              </dd>
              <dt className="text-muted">Retention</dt>
              <dd>
                {p.tier === "dedicated"
                  ? "7 full base backups (about 7 days of point-in-time recovery)"
                  : `${ov.retention_daily} daily + ${ov.retention_weekly} weekly; final and safety backups 30 days`}
              </dd>
              <dt className="text-muted">Encryption</dt>
              <dd>
                {p.tier === "dedicated" ? "OpenPGP (WAL-G)" : "Encrypted before upload"}, with the instance backup key unless the project has its own (Storage below)
                {ov.key.fingerprint && <span className="ml-1 font-mono text-xs text-muted">(key {ov.key.fingerprint})</span>}
              </dd>
            </>
          )}
        </dl>
        {err && (
          <div className="mt-3">
            <Alert>{err}</Alert>
          </div>
        )}
      </Card>
      <ProjectStorageCard p={p} canManage={p.my_role === "admin"} />
      {p.tier === "dedicated" && <PITRCard p={p} onCreated={setCreated} />}
      {list.isPending && <Spinner />}
      {list.isError && <Alert>{errorMessage(list.error)}</Alert>}
      {list.data && list.data.items.length === 0 && <EmptyState title="No backups yet">The first nightly backup runs tonight, or back up now.</EmptyState>}
      {list.data && list.data.items.length > 0 && (
        <Table head={["Taken", "Kind", "Status", "Stored on", "Size", "Expires", ""]}>
          {list.data.items.map((b) => (
            <tr key={b.id} data-testid="backup-row">
              <td className="px-3 py-2">{formatDate(b.finished_at ?? b.started_at)}</td>
              <td className="px-3 py-2">
                <Badge tone={b.kind === "logical" ? "muted" : "accent"}>
                  {/* A shared project's base backups are from before its demotion (V2 §5.4). */}
                  {b.kind === "base" && p?.tier === "shared" ? "Dedicated (pre-demotion)" : (kindLabels[b.kind] ?? b.kind)}
                </Badge>
              </td>
              <td className="px-3 py-2">
                <StatusBadge status={b.status} />
                {b.error && <p className="mt-1 max-w-xs truncate text-xs text-danger" title={b.error}>{b.error}</p>}
              </td>
              <td className="px-3 py-2 text-muted" data-testid="backup-storage">
                {b.storage_target ?? "—"}
                {b.encryption === "project" && (
                  <span className="ml-1" title={`Encrypted with the project's key ${b.key_fingerprint ?? ""}`}>
                    <Badge tone="accent">project key</Badge>
                  </span>
                )}
              </td>
              <td className="px-3 py-2 text-muted">{b.size_bytes != null ? formatBytes(b.size_bytes) : "—"}</td>
              <td className="px-3 py-2 text-muted">{b.expires_at ? formatDate(b.expires_at) : "by retention"}</td>
              <td className="px-3 py-2 text-right">
                {b.status === "succeeded" && canExport && b.kind !== "base" && (
                  <a
                    className="mr-2 text-xs text-accent hover:underline"
                    href={`/api/v1/backups/${b.id}/download`}
                    download
                    data-testid="backup-download"
                    title="A pg_dump file, for pg_restore"
                  >
                    Download
                  </a>
                )}
                {b.status === "succeeded" && (
                  <Button className="text-xs" onClick={() => setRestoring(b)}>
                    Restore
                  </Button>
                )}
              </td>
            </tr>
          ))}
        </Table>
      )}
      {restoring && <RestoreDialog p={p} backup={restoring} onClose={() => setRestoring(null)} onCreated={setCreated} />}
    </div>
  );
}

/** Restore into a new project (default) or in place (spec §6.5). */
export function RestoreDialog({
  p,
  backup,
  onClose,
  onCreated,
}: {
  p: Pick<Project, "id" | "name"> | null;
  backup: Backup;
  onClose: () => void;
  onCreated: (c: ProjectCredentials) => void;
}) {
  const qc = useQueryClient();
  const toast = useOperationToast();
  const [mode, setMode] = useState<"new" | "in_place">("new");
  const [name, setName] = useState(`${p?.name ?? backup.project_name ?? "Project"} restored`);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const when = formatDate(backup.finished_at ?? backup.started_at);

  const restoreNew = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      const r = await api.restore(backup.id, { mode: "new", name });
      await qc.invalidateQueries({ queryKey: ["projects"] });
      if (r.credentials) onCreated(r.credentials);
      onClose();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  // Base backups (dedicated) restore into a new project only.
  const canInPlace = p && backup.kind !== "base";
  if (mode === "in_place" && p) {
    return (
      <ConfirmDestroy
        open
        onClose={onClose}
        title={`Restore ${p.name} in place`}
        name={p.name}
        description={`Replaces everything in ${p.name} with the backup from ${when}. A safety backup is taken first, and clients are held at the pooler while it runs.`}
        action="Restore in place"
        run={async () => {
          const r = await api.restore(backup.id, { mode: "in_place", confirm: p.name });
          toast(r.operation.id, `Restore in place · ${p.name}`);
          await qc.invalidateQueries({ queryKey: ["project", p.id] });
          await qc.invalidateQueries({ queryKey: ["backups"] });
        }}
      >
        <Button variant="ghost" className="self-start px-0 text-xs" onClick={() => setMode("new")}>
          ← Restore into a new project instead
        </Button>
      </ConfirmDestroy>
    );
  }

  return (
    <Modal title="Restore backup" open onClose={onClose}>
      <form className="flex flex-col gap-4" onSubmit={restoreNew}>
        <p className="text-sm text-muted">
          Backup from {when}. Restoring into a new project leaves {p ? p.name : "everything else"} untouched, so you can inspect the result before
          switching your app over.
        </p>
        <Field label="New project name">
          {(id) => <Input id={id} required maxLength={64} value={name} onChange={(e) => setName(e.target.value)} autoFocus />}
        </Field>
        {err && <Alert>{err}</Alert>}
        <div className="flex flex-wrap items-center justify-between gap-2">
          {canInPlace ? (
            <Button variant="ghost" className="px-0 text-xs text-danger" onClick={() => setMode("in_place")}>
              Restore in place instead…
            </Button>
          ) : (
            <span />
          )}
          <div className="flex gap-2">
            <Button onClick={onClose}>Cancel</Button>
            <Button type="submit" variant="primary" busy={busy} disabled={!name.trim()}>
              Restore into new project
            </Button>
          </div>
        </div>
      </form>
    </Modal>
  );
}

/** "YYYY-MM-DDTHH:MM:SS" in local time, for datetime-local inputs. */
export function toLocalInput(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

/** Point-in-time recovery of a dedicated project into a new one (spec §6.5). */
function PITRCard({ p, onCreated }: { p: Project; onCreated: (c: ProjectCredentials) => void }) {
  const qc = useQueryClient();
  const [when, setWhen] = useState("");
  const [name, setName] = useState(`${p.name} restored`);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const w = p.pitr_window;
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      const c = await api.pitr(p.id, { name, target_time: when ? new Date(when).toISOString() : undefined });
      await qc.invalidateQueries({ queryKey: ["projects"] });
      onCreated(c);
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Card title="Point-in-time recovery">
      {!w ? (
        <p className="text-sm text-muted">Available once the first base backup has finished.</p>
      ) : (
        <form className="flex flex-col gap-3" onSubmit={submit}>
          <p className="text-sm text-muted" data-testid="pitr-window">
            Any moment from {formatDate(w.from)} until now can be restored into a new dedicated project on the same node. This project is not
            changed.
          </p>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <Field label="Restore to (your local time)" hint="Leave empty for the latest point.">
              {(id) => (
                <Input
                  id={id}
                  type="datetime-local"
                  step={1}
                  min={toLocalInput(new Date(w.from))}
                  max={toLocalInput(new Date())}
                  value={when}
                  onChange={(e) => setWhen(e.target.value)}
                />
              )}
            </Field>
            <Field label="New project name">
              {(id) => <Input id={id} required maxLength={64} value={name} onChange={(e) => setName(e.target.value)} />}
            </Field>
          </div>
          {err && <Alert>{err}</Alert>}
          <div>
            <Button type="submit" variant="primary" busy={busy}>
              Restore to this point
            </Button>
          </div>
        </form>
      )}
    </Card>
  );
}
