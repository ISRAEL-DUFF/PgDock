import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { api, errorMessage, type Job, type Project } from "../api/client";
import { Plus } from "lucide-react";
import { Alert, Badge, Button, CopyField, EmptyState, Field, Input, Page, Select, SidePanel, Spinner, Table } from "../components/ui";
import { formatDate, relativeTime, timeUntil } from "../lib/format";
import { useProject } from "./ProjectOverview";

const PRESETS: { cron: string; label: string }[] = [
  { cron: "*/5 * * * *", label: "Every 5 minutes" },
  { cron: "0 * * * *", label: "Every hour" },
  { cron: "0 3 * * *", label: "Every day at 03:00" },
  { cron: "0 9 * * 1-5", label: "Weekdays at 09:00" },
  { cron: "0 4 * * 0", label: "Sundays at 04:00" },
  { cron: "0 0 1 * *", label: "The 1st of each month" },
];

const runTone = { succeeded: "ok", failed: "danger", timed_out: "danger", skipped: "muted", running: "accent", queued: "muted" } as const;

function localZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

function parseHeaders(s: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of s.split("\n")) {
    const i = line.indexOf(":");
    if (i > 0) out[line.slice(0, i).trim()] = line.slice(i + 1).trim();
  }
  return out;
}

/** Project → Jobs (V2 §9.2): SQL or HTTP on a cron schedule. */
export function ProjectJobsPage() {
  const { data: p } = useProject();
  const q = useQuery({ queryKey: ["jobs", p?.id], queryFn: () => api.jobs(p!.id), enabled: !!p, refetchInterval: 5000 });
  const [creating, setCreating] = useState(false);
  const [secret, setSecret] = useState<{ name: string; secret: string } | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  if (!p) return null;
  const selected = q.data?.items.find((j) => j.id === open);
  return (
    <Page
      title="Scheduled jobs"
      description="Run SQL as the project owner, or call a URL, on a schedule: nightly clean-ups, refreshing a materialised view, pinging your app to send digests. Missed runs are not caught up, and jobs are skipped while the project moves between tiers or restores."
      actions={
        <Button variant="primary" icon={<Plus className="h-3.5 w-3.5" />} onClick={() => setCreating(true)}>
          New job
        </Button>
      }
      testId="project-jobs"
    >
      {q.isPending && <Spinner />}
      {q.isError && <Alert>{errorMessage(q.error)}</Alert>}
      {q.data && q.data.items.length === 0 && <EmptyState title="No jobs yet">Create one to run SQL or call a URL on a schedule.</EmptyState>}
      {q.data && q.data.items.length > 0 && (
        <Table head={["Job", "Kind", "Schedule", "Next run", "Last run"]}>
          {q.data.items.map((j) => (
            <tr key={j.id} data-testid="job-row" className="cursor-pointer hover:bg-surface-2" onClick={() => setOpen(j.id)}>
              <td className="px-3 py-2 font-medium">{j.name}</td>
              <td className="px-3 py-2">
                <Badge>{j.kind.toUpperCase()}</Badge>
              </td>
              <td className="px-3 py-2 font-mono text-xs">
                {j.cron} <span className="text-muted">({j.timezone})</span>
              </td>
              <td className="px-3 py-2 text-xs" title={formatDate(j.next_run_at)}>
                {j.enabled ? timeUntil(j.next_run_at) : <Badge tone="muted">paused</Badge>}
              </td>
              <td className="px-3 py-2" data-testid="job-last-run">
                {j.last_run ? (
                  <Badge tone={runTone[j.last_run.status]}>{j.last_run.status.replace("_", " ")}</Badge>
                ) : (
                  <span className="text-muted">never</span>
                )}
              </td>
            </tr>
          ))}
        </Table>
      )}
      {selected && (
        <JobDetail
          p={p}
          j={selected}
          secret={secret?.name === selected.name ? secret.secret : null}
          onSecretStored={() => setSecret(null)}
          onClose={() => {
            setOpen(null);
            setSecret(null);
          }}
        />
      )}
      <CreateJobDialog
        p={p}
        open={creating}
        onClose={() => setCreating(false)}
        onCreated={(j, s) => {
          setCreating(false);
          if (s) setSecret({ name: j.name, secret: s });
          setOpen(j.id);
        }}
      />
    </Page>
  );
}

function CreateJobDialog({ p, open, onClose, onCreated }: { p: Project; open: boolean; onClose: () => void; onCreated: (j: Job, secret?: string) => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [cron, setCron] = useState("0 3 * * *");
  const [tz, setTz] = useState(localZone());
  const [kind, setKind] = useState<"sql" | "http">("sql");
  const [sql, setSql] = useState("");
  const [method, setMethod] = useState<"GET" | "POST" | "PUT" | "PATCH" | "DELETE">("POST");
  const [url, setURL] = useState("");
  const [body, setBody] = useState("");
  const [headers, setHeaders] = useState("");
  const [timeout, setTimeout] = useState("");
  const [overlap, setOverlap] = useState<"skip" | "queue">("skip");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      const hdrs = parseHeaders(headers);
      const r = await api.createJob(p.id, {
        name,
        cron,
        timezone: tz,
        kind,
        sql: kind === "sql" ? sql : undefined,
        http: kind === "http" ? { method, url, body: body || undefined, headers: Object.keys(hdrs).length ? hdrs : undefined } : undefined,
        timeout_seconds: timeout ? Number(timeout) : undefined,
        overlap,
        enabled: true,
      });
      await qc.invalidateQueries({ queryKey: ["jobs", p.id] });
      onCreated(r.job, r.secret);
      setName("");
      setSql("");
      setURL("");
      setBody("");
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <SidePanel
      open={open}
      onOpenChange={(o) => !o && onClose()}
      size="large"
      title="Create a new scheduled job"
      footer={
        <>
          <Button type="button" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="create-job" variant="primary" busy={busy}>
            Create job
          </Button>
        </>
      }
    >
      <form id="create-job" className="flex flex-col gap-4" onSubmit={submit}>
        <Field label="Name">{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} placeholder="nightly-cleanup" required />}</Field>
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <Field label="Schedule (cron)" hint="minute hour day month weekday">
            {(id) => <Input id={id} className="font-mono" value={cron} onChange={(e) => setCron(e.target.value)} required />}
          </Field>
          <Field label="Time zone">{(id) => <Input id={id} value={tz} onChange={(e) => setTz(e.target.value)} />}</Field>
        </div>
        <div className="flex flex-wrap gap-1.5">
          {PRESETS.map((pr) => (
            <button
              type="button"
              key={pr.cron}
              className={"rounded border px-2 py-0.5 text-xs " + (cron === pr.cron ? "border-accent text-accent" : "border-line text-muted")}
              onClick={() => setCron(pr.cron)}
            >
              {pr.label}
            </button>
          ))}
        </div>
        <fieldset className="flex gap-4 text-sm">
          <legend className="mb-1 text-xs font-medium text-muted">Runs</legend>
          <label className="flex items-center gap-1.5">
            <input type="radio" checked={kind === "sql"} onChange={() => setKind("sql")} /> SQL, as the project owner
          </label>
          <label className="flex items-center gap-1.5">
            <input type="radio" checked={kind === "http"} onChange={() => setKind("http")} /> An HTTP request
          </label>
        </fieldset>
        {kind === "sql" ? (
          <Field label="SQL" hint="Runs in one transaction">
            {(id) => (
              <textarea
                id={id}
                className="h-28 rounded-md border border-line-strong bg-surface-2 p-2 font-mono text-xs focus:border-accent focus:outline-none"
                value={sql}
                onChange={(e) => setSql(e.target.value)}
                placeholder="DELETE FROM sessions WHERE expires_at < now()"
                required
              />
            )}
          </Field>
        ) : (
          <>
            <div className="grid grid-cols-[7rem_1fr] gap-3">
              <Field label="Method">
                {(id) => (
                  <Select id={id} value={method} onChange={(e) => setMethod(e.target.value as typeof method)}>
                    {(["GET", "POST", "PUT", "PATCH", "DELETE"] as const).map((m) => (
                      <option key={m}>{m}</option>
                    ))}
                  </Select>
                )}
              </Field>
              <Field label="URL">
                {(id) => <Input id={id} value={url} onChange={(e) => setURL(e.target.value)} placeholder="https://example.com/cron/digest" required />}
              </Field>
            </div>
            <Field label="Body" hint="Optional">
              {(id) => (
                <textarea
                  id={id}
                  className="h-16 rounded-md border border-line-strong bg-surface-2 p-2 font-mono text-xs focus:border-accent focus:outline-none"
                  value={body}
                  onChange={(e) => setBody(e.target.value)}
                />
              )}
            </Field>
            <Field label="Headers" hint="Optional, one per line as Name: value">
              {(id) => (
                <textarea
                  id={id}
                  className="h-14 rounded-md border border-line-strong bg-surface-2 p-2 font-mono text-xs focus:border-accent focus:outline-none"
                  value={headers}
                  onChange={(e) => setHeaders(e.target.value)}
                />
              )}
            </Field>
          </>
        )}
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <Field label="Timeout (seconds)" hint={kind === "sql" ? "Default 300, at most 3600" : "Default 30, at most 3600"}>
            {(id) => <Input id={id} type="number" min={1} max={3600} value={timeout} onChange={(e) => setTimeout(e.target.value)} />}
          </Field>
          <Field label="If the previous run is still going">
            {(id) => (
              <Select id={id} value={overlap} onChange={(e) => setOverlap(e.target.value as "skip" | "queue")}>
                <option value="skip">Skip this run</option>
                <option value="queue">Queue one run</option>
              </Select>
            )}
          </Field>
        </div>
        {err && <Alert>{err}</Alert>}
      </form>
    </SidePanel>
  );
}

function JobDetail({ p, j, secret, onSecretStored, onClose }: { p: Project; j: Job; secret: string | null; onSecretStored: () => void; onClose: () => void }) {
  const qc = useQueryClient();
  const runs = useQuery({ queryKey: ["job-runs", j.id], queryFn: () => api.jobRuns(p.id, j.id), refetchInterval: 3000 });
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const act = async (what: string, f: () => Promise<void>) => {
    setBusy(what);
    setErr(null);
    try {
      await f();
      await qc.invalidateQueries({ queryKey: ["jobs", p.id] });
      await qc.invalidateQueries({ queryKey: ["job-runs", j.id] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(null);
    }
  };
  return (
    <SidePanel
      open
      onOpenChange={(o) => !o && onClose()}
      size="large"
      testId="job-detail"
      title={
        <span className="flex items-center gap-2">
          {j.name} <Badge>{j.kind.toUpperCase()}</Badge>
        </span>
      }
      description={
        <span className="font-mono">
          {j.cron} ({j.timezone})
        </span>
      }
    >
      <div className="flex flex-col gap-4 text-sm">
        {secret && (
          <Alert tone="ok" title={`Signing secret for ${j.name}`}>
            <div className="flex flex-col gap-2">
              <span>Shown once: requests carry PGDock-Signature and PGDock-Job headers.</span>
              <CopyField label="Secret" value={secret} secret />
              <Button className="self-start text-xs" onClick={onSecretStored}>
                I&rsquo;ve stored it
              </Button>
            </div>
          </Alert>
        )}
        {j.kind === "sql" ? (
          <pre className="max-h-32 overflow-auto rounded bg-surface-2 p-2 font-mono text-xs">{j.sql}</pre>
        ) : (
          <p className="font-mono text-xs">
            {j.http?.method} {j.http?.url}
          </p>
        )}
        {j.enabled && j.upcoming.length > 0 && (
          <div>
            <p className="text-xs font-medium text-muted">Next runs</p>
            <ul className="text-xs" data-testid="job-upcoming">
              {j.upcoming.map((t) => (
                <li key={t}>{formatDate(t)}</li>
              ))}
            </ul>
          </div>
        )}
        <div className="flex flex-wrap gap-2">
          <Button
            className="text-xs"
            busy={busy === "run"}
            data-testid="job-run-now"
            onClick={() => act("run", async () => void (await api.runJob(p.id, j.id)))}
          >
            Run now
          </Button>
          <Button
            className="text-xs"
            busy={busy === "toggle"}
            onClick={() => act("toggle", async () => void (await api.updateJob(p.id, j.id, { enabled: !j.enabled })))}
          >
            {j.enabled ? "Pause" : "Resume"}
          </Button>
          <Button
            variant="danger"
            className="text-xs"
            busy={busy === "delete"}
            onClick={() =>
              confirm(`Delete the job ${j.name} and its history?`) &&
              act("delete", async () => {
                await api.deleteJob(p.id, j.id);
                onClose();
              })
            }
          >
            Delete
          </Button>
        </div>
        {err && <Alert>{err}</Alert>}
        {runs.data && runs.data.items.length === 0 && <p className="text-muted">No runs yet.</p>}
        {runs.data && runs.data.items.length > 0 && (
          <Table head={["Scheduled", "Trigger", "Status", "Duration", "Result"]}>
            {runs.data.items.map((r) => (
              <tr key={r.id} data-testid="job-run-row">
                <td className="px-3 py-2 text-xs" title={formatDate(r.scheduled_for)}>
                  {relativeTime(r.scheduled_for)}
                </td>
                <td className="px-3 py-2 text-xs">{r.trigger}</td>
                <td className="px-3 py-2">
                  <Badge tone={runTone[r.status]}>{r.status.replace("_", " ")}</Badge>
                </td>
                <td className="px-3 py-2 text-xs tabular-nums">
                  {r.started_at && r.finished_at ? `${Math.max(0, new Date(r.finished_at).getTime() - new Date(r.started_at).getTime())} ms` : "—"}
                </td>
                <td className="max-w-sm truncate px-3 py-2 font-mono text-xs" title={r.error ?? ""}>
                  {r.error ?? (r.rows_affected != null ? `${r.rows_affected} row(s)` : r.status_code ? `HTTP ${r.status_code}` : "")}
                </td>
              </tr>
            ))}
          </Table>
        )}
      </div>
    </SidePanel>
  );
}
