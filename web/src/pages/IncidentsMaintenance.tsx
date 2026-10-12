import { useQuery, useQueryClient } from "@tanstack/react-query";
import { CalendarClock } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api, errorMessage } from "../api/client";
import type { components } from "../api/schema";
import {
  Alert,
  Badge,
  Button,
  Dialog,
  EmptyState,
  Field,
  Input,
  Panel,
  Select,
  Table,
} from "../components/ui";
import { formatDate } from "../lib/format";

type Announcement = components["schemas"]["MaintenanceAnnouncement"];

/** The SLA's notice (V3.1 §4): minutes are excluded only from 72 hours after the announcement. */
export const noticeHours = 72;

/** A datetime-local value read as UTC. */
function utc(v: string): Date | null {
  if (!v) return null;
  const d = new Date(`${v}:00Z`);
  return Number.isNaN(d.getTime()) ? null : d;
}

/** Hours of notice a window starting at start gives, from now. */
export function noticeGiven(start: Date, now = new Date()): number {
  return (start.getTime() - now.getTime()) / 3_600_000;
}

function state(a: Announcement): {
  label: string;
  tone: "accent" | "muted" | "ok" | "warn";
} {
  if (a.draft) return { label: "proposed", tone: "warn" };
  if (a.cancelled_at && !a.announced_at)
    return { label: "discarded", tone: "muted" };
  if (a.cancelled_at) return { label: "cancelled", tone: "muted" };
  if (a.incident.resolved_at) return { label: "done", tone: "ok" };
  return { label: "upcoming", tone: "accent" };
}

/** Admin → Incidents → Scheduled maintenance (V3.1 §4): announcements
 * made 72 hours ahead exclude their window from HA projects' SLA. */
export function MaintenancePanel() {
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["maintenance-announcements"],
    queryFn: api.maintenanceAnnouncements,
    refetchInterval: 30_000,
  });
  const [open, setOpen] = useState(false);
  const [confirming, setConfirming] = useState<Announcement | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const discard = async (id: string) => {
    setErr(null);
    try {
      await api.discardMaintenanceDraft(id);
      await qc.invalidateQueries({ queryKey: ["maintenance-announcements"] });
    } catch (e) {
      setErr(errorMessage(e));
    }
  };
  const cancel = async (id: string) => {
    setErr(null);
    try {
      await api.cancelMaintenance(id);
      await qc.invalidateQueries({ queryKey: ["maintenance-announcements"] });
      await qc.invalidateQueries({ queryKey: ["incidents"] });
    } catch (e) {
      setErr(errorMessage(e));
    }
  };
  const items = q.data?.items ?? [];
  return (
    <Panel
      testId="maintenance-panel"
      title="Scheduled maintenance"
      description={`Announced at least ${noticeHours} hours ahead, a window's minutes don't count against HA projects' SLA. Owners and admins of the organisations it covers are emailed.`}
      actions={
        <Button onClick={() => setOpen(true)}>Schedule maintenance</Button>
      }
    >
      {err && (
        <div className="mb-3">
          <Alert>{err}</Alert>
        </div>
      )}
      {q.isError ? (
        <Alert>{errorMessage(q.error)}</Alert>
      ) : items.length === 0 ? (
        <EmptyState title="Nothing scheduled" icon={<CalendarClock />}>
          Announce planned work here, before the window, so it's excluded from
          the SLA.
        </EmptyState>
      ) : (
        <Table
          head={[
            "Window (UTC)",
            "Title",
            "Covers",
            "Announced",
            "SLA excluded from",
            "",
          ]}
        >
          {items.map((a) => {
            const st = state(a);
            return (
              <tr key={a.incident.id} data-testid="maintenance-row">
                <td className="px-3 py-2 text-xs">
                  {formatDate(a.scheduled_start)} –{" "}
                  {formatDate(a.scheduled_end)}
                </td>
                <td className="px-3 py-2 font-medium">
                  <span className="mr-2">{a.incident.title}</span>
                  <Badge tone={st.tone}>{st.label}</Badge>
                </td>
                <td className="px-3 py-2 text-xs text-muted">
                  {a.scope_projects.length + a.scope_nodes.length > 0
                    ? `${a.scope_projects.length} projects, ${a.scope_nodes.length} nodes`
                    : (a.incident.region ?? "all regions")}
                </td>
                <td className="px-3 py-2 text-xs text-muted">
                  {formatDate(a.announced_at)}
                </td>
                <td className="px-3 py-2 text-xs">
                  {formatDate(a.excluded_from)}
                  {a.short_notice && (
                    <span className="ml-1">
                      <Badge tone="warn">short notice</Badge>
                    </span>
                  )}
                </td>
                <td className="px-3 py-2 text-right">
                  {a.draft && (
                    <>
                      <Button
                        variant="ghost"
                        className="text-xs"
                        onClick={() => setConfirming(a)}
                        data-testid="confirm-draft"
                      >
                        Confirm…
                      </Button>
                      <Button
                        variant="ghost"
                        className="text-xs"
                        onClick={() => void discard(a.incident.id)}
                        data-testid="discard-draft"
                      >
                        Discard
                      </Button>
                    </>
                  )}
                  {st.label === "upcoming" && (
                    <Button
                      variant="ghost"
                      className="text-xs"
                      onClick={() => void cancel(a.incident.id)}
                    >
                      Cancel
                    </Button>
                  )}
                </td>
              </tr>
            );
          })}
        </Table>
      )}
      <ScheduleMaintenance open={open} onOpenChange={setOpen} />
      <ConfirmDraft draft={confirming} onClose={() => setConfirming(null)} />
    </Panel>
  );
}

/** The email an announcement sends, as the server renders it (V4.1 §8.3). */
function EmailPreview({
  preview,
}: {
  preview: components["schemas"]["MaintenancePreview"] | undefined;
}) {
  if (!preview) return null;
  return (
    <div
      className="flex flex-col gap-1 text-[13px]"
      data-testid="email-preview"
    >
      <div className="text-muted">
        Emailed to {preview.addresses} people in {preview.organisations}{" "}
        organisations.
      </div>
      <div className="font-medium">{preview.subject}</div>
      <pre className="max-h-64 overflow-auto whitespace-pre-wrap rounded-md border border-line bg-surface-2 p-2.5 text-xs">
        {preview.body}
      </pre>
    </div>
  );
}

/** Confirm a draft PGDock proposed: announce it, emailing what's shown. */
function ConfirmDraft({
  draft,
  onClose,
}: {
  draft: Announcement | null;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const preview = useQuery({
    queryKey: ["maintenance-preview", draft?.incident.id],
    queryFn: () => api.previewMaintenance({ incident_id: draft!.incident.id }),
    enabled: !!draft,
  });
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const confirm = async () => {
    if (!draft) return;
    setBusy(true);
    setErr(null);
    try {
      await api.confirmMaintenanceDraft(draft.incident.id);
      await qc.invalidateQueries({ queryKey: ["maintenance-announcements"] });
      await qc.invalidateQueries({ queryKey: ["incidents"] });
      onClose();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={!!draft}
      onOpenChange={(o) => !o && onClose()}
      title="Confirm proposed maintenance"
      description={`Announces it now: the status page shows it as upcoming, and the window is excluded from the SLA from ${noticeHours} hours after now.`}
      testId="confirm-draft-dialog"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" busy={busy} onClick={() => void confirm()}>
            Announce and email
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3">
        {draft && (
          <p className="text-[13px]">
            {formatDate(draft.scheduled_start)} –{" "}
            {formatDate(draft.scheduled_end)}, {draft.scope_projects.length}{" "}
            projects
            {draft.proposed_for === "minor_upgrade" &&
              " behind on their Postgres minor release"}
            .
          </p>
        )}
        {preview.isError && <Alert>{errorMessage(preview.error)}</Alert>}
        <EmailPreview preview={preview.data} />
        {err && <Alert>{err}</Alert>}
      </div>
    </Dialog>
  );
}

function ScheduleMaintenance({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
}) {
  const qc = useQueryClient();
  const regions = useQuery({
    queryKey: ["regions"],
    queryFn: api.regions,
    enabled: open,
  });
  const [name, setName] = useState("Scheduled maintenance");
  const [region, setRegion] = useState("");
  const [start, setStart] = useState("");
  const [hours, setHours] = useState(2);
  const [projects, setProjects] = useState("");
  const [body, setBody] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [done, setDone] = useState<Announcement | null>(null);
  const from = utc(start);
  const notice = from ? noticeGiven(from) : null;
  const ids = projects
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean);
  const previewReq = from
    ? {
        title: name,
        body: body || undefined,
        region: region || undefined,
        start: from.toISOString(),
        end: new Date(from.getTime() + hours * 3_600_000).toISOString(),
        project_ids: ids.length ? ids : undefined,
      }
    : null;
  const preview = useQuery({
    queryKey: ["maintenance-preview", previewReq],
    queryFn: () => api.previewMaintenance(previewReq!),
    enabled: open && !!previewReq && !done,
    retry: false,
  });
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!from) return;
    setBusy(true);
    setErr(null);
    try {
      const ids = projects
        .split(/[\s,]+/)
        .map((s) => s.trim())
        .filter(Boolean);
      const a = await api.announceMaintenance({
        title: name,
        body: body || undefined,
        region: region || undefined,
        start: from.toISOString(),
        end: new Date(from.getTime() + hours * 3_600_000).toISOString(),
        project_ids: ids.length ? ids : undefined,
      });
      setDone(a);
      await qc.invalidateQueries({ queryKey: ["maintenance-announcements"] });
      await qc.invalidateQueries({ queryKey: ["incidents"] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  const close = (o: boolean) => {
    if (!o) {
      setDone(null);
      setErr(null);
    }
    onOpenChange(o);
  };
  return (
    <Dialog
      open={open}
      onOpenChange={close}
      title="Schedule maintenance"
      description="Posted to the status page as upcoming and emailed now. A window can't be edited once announced: reschedule by cancelling it and announcing again."
      testId="schedule-maintenance"
      footer={
        done ? (
          <Button variant="primary" onClick={() => close(false)}>
            Done
          </Button>
        ) : (
          <>
            <Button variant="ghost" onClick={() => close(false)}>
              Cancel
            </Button>
            <Button
              type="submit"
              form="schedule-maintenance-form"
              variant="primary"
              busy={busy}
              disabled={!from}
            >
              Announce
            </Button>
          </>
        )
      }
    >
      {done ? (
        <div
          className="flex flex-col gap-2 text-[13px]"
          data-testid="maintenance-announced"
        >
          <p>
            Announced for {formatDate(done.scheduled_start)}.{" "}
            {done.emailed ?? 0} people were emailed.
          </p>
          {done.short_notice ? (
            <Alert tone="warn" title="Short notice">
              Minutes before {formatDate(done.excluded_from)} still count
              against the SLA.
            </Alert>
          ) : (
            <p className="text-muted">
              The whole window is excluded from the SLA.
            </p>
          )}
        </div>
      ) : (
        <form
          id="schedule-maintenance-form"
          className="flex flex-col gap-3"
          onSubmit={submit}
        >
          <Field label="Title">
            {(id) => (
              <Input
                id={id}
                required
                maxLength={200}
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            )}
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Starts (UTC)">
              {(id) => (
                <Input
                  id={id}
                  type="datetime-local"
                  required
                  value={start}
                  onChange={(e) => setStart(e.target.value)}
                />
              )}
            </Field>
            <Field label="Hours" hint="At most 24">
              {(id) => (
                <Input
                  id={id}
                  type="number"
                  min={1}
                  max={24}
                  required
                  value={hours}
                  onChange={(e) => setHours(Number(e.target.value))}
                />
              )}
            </Field>
          </div>
          {notice != null && notice < noticeHours && (
            <Alert tone="warn" title={`Less than ${noticeHours} hours' notice`}>
              {notice <= 0
                ? "The window must start in the future."
                : `Only ${Math.floor(notice)} hours ahead: minutes before ${noticeHours} hours from now still count against the SLA.`}
            </Alert>
          )}
          <Field label="Region">
            {(id) => (
              <Select
                id={id}
                value={region}
                onChange={(e) => setRegion(e.target.value)}
              >
                <option value="">All regions</option>
                {regions.data?.items.map((r) => (
                  <option key={r.id} value={r.id}>
                    {r.name} ({r.id})
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <Field
            label="Only these projects"
            hint="Project IDs, comma separated; empty covers the whole region"
          >
            {(id) => (
              <Input
                id={id}
                value={projects}
                onChange={(e) => setProjects(e.target.value)}
              />
            )}
          </Field>
          <Field label="What will happen">
            {(id) => (
              <textarea
                id={id}
                maxLength={4000}
                className="min-h-20 w-full rounded-md border border-line-strong bg-surface-2 p-2.5 text-[13px] text-fg placeholder:text-muted focus:border-accent focus:ring-2 focus:ring-accent-soft focus:outline-none"
                placeholder="Planned maintenance; affected databases may pause briefly while it runs."
                value={body}
                onChange={(e) => setBody(e.target.value)}
              />
            )}
          </Field>
          <EmailPreview preview={preview.data} />
          {err && <Alert>{err}</Alert>}
        </form>
      )}
    </Dialog>
  );
}
