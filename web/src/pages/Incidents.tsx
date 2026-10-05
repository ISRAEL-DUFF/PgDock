import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ExternalLink, Megaphone } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api, errorMessage, type Incident, type IncidentSeverity, type IncidentStatus } from "../api/client";
import { Alert, Badge, Button, Checkbox, Dialog, EmptyState, Field, Input, PageHeading, Panel, Select, Spinner, Table, type Tone } from "../components/ui";
import { formatDate, relativeTime } from "../lib/format";

const severities: IncidentSeverity[] = ["minor", "major", "critical", "maintenance"];
const statuses: IncidentStatus[] = ["investigating", "identified", "monitoring", "resolved"];

const severityTone: Record<IncidentSeverity, Tone> = {
  minor: "warn",
  major: "danger",
  critical: "danger",
  maintenance: "accent",
};

const textareaClass =
  "min-h-24 w-full rounded-md border border-line-strong bg-surface-2 p-2.5 text-[13px] text-fg placeholder:text-muted focus:border-accent focus:ring-2 focus:ring-accent-soft focus:outline-none";

function title(s: string) {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

/** Where the status page stands on this version of the incident. */
function PushState({ inc, configured }: { inc: Incident; configured: boolean }) {
  if (!configured) return null;
  if (inc.push_error)
    return (
      <span title={inc.push_error}>
        <Badge tone="danger">push failed, retrying</Badge>
      </span>
    );
  if (!inc.pushed_at) return <Badge tone="muted">sending to status page…</Badge>;
  return <Badge tone="ok">on status page</Badge>;
}

/** Admin → Incidents (V3 §2.6): create and update incidents, which are
 * pushed to the separately hosted status page. */
export function IncidentsPage() {
  const q = useQuery({
    queryKey: ["incidents"],
    queryFn: api.incidents,
    refetchInterval: 10_000,
  });
  const [creating, setCreating] = useState(false);
  const open = q.data?.items.filter((i) => !i.resolved_at) ?? [];
  const past = q.data?.items.filter((i) => i.resolved_at) ?? [];
  return (
    <>
      <PageHeading
        title="Incidents"
        description={
          q.data?.status_page_url ? (
            <>
              Posted to{" "}
              <a href={q.data.status_page_url} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-accent-text hover:underline">
                {q.data.status_page_url.replace(/^https?:\/\//, "")}
                <ExternalLink className="size-3" />
              </a>{" "}
              within seconds. Outages it detects from outside open and resolve there by themselves.
            </>
          ) : (
            "Incidents for the public status page."
          )
        }
        actions={
          <Button variant="primary" onClick={() => setCreating(true)} disabled={!q.data}>
            New incident
          </Button>
        }
      />
      {q.data && !q.data.status_page_configured && (
        <div className="mb-4">
          <Alert tone="warn" title="No status page connected">
            Incidents stay in PGDock until you set PGDOCK_STATUS_URL and PGDOCK_STATUS_PUSH_SECRET (docs/status-page.md).
          </Alert>
        </div>
      )}
      {q.isPending ? (
        <Spinner />
      ) : q.isError ? (
        <Alert>{errorMessage(q.error)}</Alert>
      ) : (
        <div className="flex flex-col gap-4">
          {open.length === 0 ? (
            <EmptyState title="No open incidents" icon={<Megaphone />}>
              Open one when customers should know about a problem or planned maintenance.
            </EmptyState>
          ) : (
            open.map((inc) => <OpenIncident key={inc.id} inc={inc} configured={q.data.status_page_configured} components={q.data.components} />)
          )}
          {past.length > 0 && (
            <Panel title="Resolved">
              <Table head={["Incident", "Severity", "Components", "Started", "Resolved", ""]}>
                {past.map((inc) => (
                  <tr key={inc.id} data-testid="past-incident">
                    <td className="px-3 py-2 font-medium">{inc.title}</td>
                    <td className="px-3 py-2">
                      <Badge tone={severityTone[inc.severity]}>{inc.severity}</Badge>
                    </td>
                    <td className="px-3 py-2 text-xs text-muted">{inc.components.join(", ")}</td>
                    <td className="px-3 py-2 text-xs text-muted" title={formatDate(inc.started_at)}>
                      {relativeTime(inc.started_at)}
                    </td>
                    <td className="px-3 py-2 text-xs text-muted" title={formatDate(inc.resolved_at)}>
                      {relativeTime(inc.resolved_at)}
                    </td>
                    <td className="px-3 py-2">
                      <PushState inc={inc} configured={q.data.status_page_configured} />
                    </td>
                  </tr>
                ))}
              </Table>
            </Panel>
          )}
        </div>
      )}
      {q.data && <CreateIncident open={creating} onOpenChange={setCreating} components={q.data.components} />}
    </>
  );
}

function ComponentPicker({ all, value, onChange }: { all: string[]; value: string[]; onChange: (v: string[]) => void }) {
  return (
    <div className="grid grid-cols-2 gap-1.5">
      {all.map((c) => (
        <label key={c} className="flex items-center gap-2 text-[13px]">
          <Checkbox checked={value.includes(c)} onCheckedChange={(on) => onChange(on ? [...value, c] : value.filter((x) => x !== c))} />
          <span className="font-mono text-xs">{c}</span>
        </label>
      ))}
    </div>
  );
}

function CreateIncident({ open, onOpenChange, components }: { open: boolean; onOpenChange: (o: boolean) => void; components: string[] }) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [picked, setPicked] = useState<string[]>([]);
  const [severity, setSeverity] = useState<IncidentSeverity>("minor");
  const [status, setStatus] = useState<IncidentStatus>("investigating");
  const [body, setBody] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      await api.createIncident({
        title: name,
        components: picked,
        severity,
        status,
        body,
      });
      await qc.invalidateQueries({ queryKey: ["incidents"] });
      setName("");
      setPicked([]);
      setBody("");
      onOpenChange(false);
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title="New incident"
      description="Published on the status page and emailed to its subscribers."
      testId="create-incident"
      footer={
        <>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button type="submit" form="create-incident-form" variant="primary" busy={busy} disabled={picked.length === 0}>
            Publish
          </Button>
        </>
      }
    >
      <form id="create-incident-form" className="flex flex-col gap-3" onSubmit={submit}>
        <Field label="Title" hint="What customers see, e.g. “Elevated connection errors in eu-central”">
          {(id) => <Input id={id} required maxLength={200} value={name} onChange={(e) => setName(e.target.value)} />}
        </Field>
        <div className="flex flex-col gap-1">
          <span className="text-[13px] text-fg-light">Affected components</span>
          <ComponentPicker all={components} value={picked} onChange={setPicked} />
        </div>
        <div className="grid grid-cols-2 gap-3">
          <Field label="Severity" hint={severity === "minor" || severity === "maintenance" ? "Shows as degraded" : "Shows as an outage"}>
            {(id) => (
              <Select id={id} value={severity} onChange={(e) => setSeverity(e.target.value as IncidentSeverity)}>
                {severities.map((s) => (
                  <option key={s} value={s}>
                    {s}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <Field label="Status">
            {(id) => (
              <Select id={id} value={status} onChange={(e) => setStatus(e.target.value as IncidentStatus)}>
                {statuses.map((s) => (
                  <option key={s} value={s}>
                    {s}
                  </option>
                ))}
              </Select>
            )}
          </Field>
        </div>
        <Field label="First update">
          {(id) => (
            <textarea
              id={id}
              required
              maxLength={5000}
              className={textareaClass}
              placeholder="We're looking into reports of failed connections."
              value={body}
              onChange={(e) => setBody(e.target.value)}
            />
          )}
        </Field>
        {err && <Alert>{err}</Alert>}
      </form>
    </Dialog>
  );
}

function OpenIncident({ inc, configured, components }: { inc: Incident; configured: boolean; components: string[] }) {
  const qc = useQueryClient();
  const next = statuses[Math.min(statuses.indexOf(inc.status) + 1, statuses.length - 1)];
  const [status, setStatus] = useState<IncidentStatus>(next);
  const [body, setBody] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);
  const post = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      await api.postIncidentUpdate(inc.id, { status, body });
      setBody("");
      await qc.invalidateQueries({ queryKey: ["incidents"] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  const updates = [...inc.updates].reverse();
  return (
    <Panel
      testId="open-incident"
      title={
        <span className="flex flex-wrap items-center gap-2">
          <Badge tone={severityTone[inc.severity]}>{inc.severity}</Badge>
          <span>{inc.title}</span>
        </span>
      }
      description={`${title(inc.status)} · ${inc.components.join(", ")} · started ${relativeTime(inc.started_at)}`}
      actions={
        <div className="flex items-center gap-2">
          <PushState inc={inc} configured={configured} />
          <Button variant="ghost" className="text-xs" onClick={() => setEditing(true)}>
            Edit
          </Button>
        </div>
      }
    >
      <ol className="mb-4 flex flex-col gap-3 border-l border-line pl-4">
        {updates.map((u) => (
          <li key={u.id} className="text-[13px]">
            <div className="flex flex-wrap items-baseline gap-2">
              <span className="font-medium">{title(u.status)}</span>
              <span className="text-xs text-muted" title={formatDate(u.posted_at)}>
                {relativeTime(u.posted_at)}
                {u.posted_by ? ` · ${u.posted_by}` : ""}
              </span>
            </div>
            <p className="mt-0.5 whitespace-pre-wrap text-fg-light">{u.body}</p>
          </li>
        ))}
      </ol>
      <form className="flex flex-col gap-2" onSubmit={post}>
        <textarea
          aria-label="Update"
          required
          maxLength={5000}
          className={textareaClass}
          placeholder={status === "resolved" ? "What was fixed, and whether anything is still affected." : "What you know now and what happens next."}
          value={body}
          onChange={(e) => setBody(e.target.value)}
        />
        <div className="flex flex-wrap items-center gap-2">
          <Select aria-label="Status" className="w-40" value={status} onChange={(e) => setStatus(e.target.value as IncidentStatus)}>
            {statuses.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </Select>
          <Button type="submit" variant={status === "resolved" ? "primary" : "default"} busy={busy}>
            {status === "resolved" ? "Post and resolve" : "Post update"}
          </Button>
        </div>
        {err && <Alert>{err}</Alert>}
      </form>
      <EditIncident inc={inc} components={components} open={editing} onOpenChange={setEditing} />
    </Panel>
  );
}

function EditIncident({ inc, components, open, onOpenChange }: { inc: Incident; components: string[]; open: boolean; onOpenChange: (o: boolean) => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState(inc.title);
  const [picked, setPicked] = useState<string[]>(inc.components);
  const [severity, setSeverity] = useState<IncidentSeverity>(inc.severity);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      await api.updateIncident(inc.id, {
        title: name,
        components: picked,
        severity,
      });
      await qc.invalidateQueries({ queryKey: ["incidents"] });
      onOpenChange(false);
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title="Edit incident"
      footer={
        <>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button type="submit" form={`edit-${inc.id}`} variant="primary" busy={busy} disabled={picked.length === 0}>
            Save
          </Button>
        </>
      }
    >
      <form id={`edit-${inc.id}`} className="flex flex-col gap-3" onSubmit={save}>
        <Field label="Title">{(id) => <Input id={id} required maxLength={200} value={name} onChange={(e) => setName(e.target.value)} />}</Field>
        <div className="flex flex-col gap-1">
          <span className="text-[13px] text-fg-light">Affected components</span>
          <ComponentPicker all={components} value={picked} onChange={setPicked} />
        </div>
        <Field label="Severity">
          {(id) => (
            <Select id={id} value={severity} onChange={(e) => setSeverity(e.target.value as IncidentSeverity)}>
              {severities.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </Select>
          )}
        </Field>
        {err && <Alert>{err}</Alert>}
      </form>
    </Dialog>
  );
}
