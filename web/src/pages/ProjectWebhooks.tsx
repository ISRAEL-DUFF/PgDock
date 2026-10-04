import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { api, errorMessage, type Project, type Webhook } from "../api/client";
import { Plus, Webhook as WebhookIcon } from "lucide-react";
import { Alert, Badge, Button, CopyField, EmptyState, Field, Input, Page, SidePanel, Table, TableSkeleton } from "../components/ui";
import { formatDate, relativeTime } from "../lib/format";
import { useProject } from "./ProjectOverview";

const EVENTS = ["INSERT", "UPDATE", "DELETE"] as const;

const statusTone = { healthy: "ok", failing: "warn", paused: "muted", broken: "danger" } as const;

export const canAutomate = (p: Project) => p.my_role === "admin" || p.my_role === "developer";

function list(s: string): string[] {
  return s
    .split(",")
    .map((x) => x.trim())
    .filter(Boolean);
}

function parseHeaders(s: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of s.split("\n")) {
    const i = line.indexOf(":");
    if (i > 0) out[line.slice(0, i).trim()] = line.slice(i + 1).trim();
  }
  return out;
}

/** Project → Webhooks (V2 §9.1): table changes POSTed to a URL. */
export function ProjectWebhooksPage() {
  const { data: p } = useProject();
  const q = useQuery({ queryKey: ["webhooks", p?.id], queryFn: () => api.webhooks(p!.id), enabled: !!p, refetchInterval: 5000 });
  const [creating, setCreating] = useState(false);
  const [secret, setSecret] = useState<{ name: string; secret: string } | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  if (!p) return null;
  const selected = q.data?.items.find((w) => w.id === open);
  return (
    <Page
      title="Database Webhooks"
      description={
        <>
          When rows of the chosen tables change, PGDock POSTs the change to your URL, signed with the webhook&rsquo;s secret. Changes are recorded in the same
          transaction, so a rolled-back change never sends anything, and each webhook&rsquo;s events arrive in commit order. Failed deliveries are retried for
          24 hours, then kept as dead letters you can replay.
        </>
      }
      actions={
        <Button variant="primary" icon={<Plus className="h-3.5 w-3.5" />} onClick={() => setCreating(true)}>
          New webhook
        </Button>
      }
      testId="project-webhooks"
    >
      {q.isPending && <TableSkeleton rows={3} cols={4} />}
      {q.isError && <Alert>{errorMessage(q.error)}</Alert>}
      {q.data && q.data.items.length === 0 && (
        <EmptyState title="No webhooks yet" icon={<WebhookIcon />}>
          Create one to send table changes to your app.
        </EmptyState>
      )}
      {q.data && q.data.items.length > 0 && (
        <Table head={["Webhook", "Status", "Tables", "Events", "Queued", "URL"]}>
          {q.data.items.map((w) => (
            <tr key={w.id} data-testid="webhook-row" className="cursor-pointer hover:bg-surface-2" onClick={() => setOpen(w.id)}>
              <td className="px-3 py-2 font-medium">{w.name}</td>
              <td className="px-3 py-2">
                <Badge tone={statusTone[w.status]}>{w.status}</Badge>
              </td>
              <td className="px-3 py-2 font-mono text-xs">{w.tables.join(", ")}</td>
              <td className="px-3 py-2 text-xs">{w.events.join(", ")}</td>
              <td className="px-3 py-2 tabular-nums" data-testid="webhook-backlog">
                {w.backlog}
              </td>
              <td className="max-w-xs truncate px-3 py-2 font-mono text-xs" title={w.url}>
                {w.url}
              </td>
            </tr>
          ))}
        </Table>
      )}
      {selected && (
        <WebhookDetail
          p={p}
          w={selected}
          secret={secret?.name === selected.name ? secret.secret : null}
          onSecret={(s) => setSecret({ name: selected.name, secret: s })}
          onSecretStored={() => setSecret(null)}
          onClose={() => {
            setOpen(null);
            setSecret(null);
          }}
        />
      )}
      <CreateWebhookDialog
        p={p}
        open={creating}
        onClose={() => setCreating(false)}
        onCreated={(name, s, id) => {
          setCreating(false);
          setSecret({ name, secret: s });
          setOpen(id);
        }}
      />
    </Page>
  );
}

function CreateWebhookDialog({
  p,
  open,
  onClose,
  onCreated,
}: {
  p: Project;
  open: boolean;
  onClose: () => void;
  onCreated: (name: string, secret: string, id: string) => void;
}) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [tables, setTables] = useState("");
  const [events, setEvents] = useState<string[]>(["INSERT", "UPDATE", "DELETE"]);
  const [columns, setColumns] = useState("");
  const [url, setURL] = useState("");
  const [headers, setHeaders] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      const cols = list(columns);
      const hdrs = parseHeaders(headers);
      const r = await api.createWebhook(p.id, {
        name,
        tables: list(tables),
        events: events as ("INSERT" | "UPDATE" | "DELETE")[],
        columns: cols.length ? cols : undefined,
        url,
        headers: Object.keys(hdrs).length ? hdrs : undefined,
        enabled: true,
      });
      await qc.invalidateQueries({ queryKey: ["webhooks", p.id] });
      onCreated(r.webhook.name, r.secret, r.webhook.id);
      setName("");
      setTables("");
      setColumns("");
      setURL("");
      setHeaders("");
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
      title="Create a new database webhook"
      footer={
        <>
          <Button type="button" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="create-webhook" variant="primary" busy={busy} disabled={events.length === 0}>
            Create webhook
          </Button>
        </>
      }
    >
      <form id="create-webhook" className="flex flex-col gap-4" onSubmit={submit}>
        <Field label="Name">{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} placeholder="orders-to-slack" required />}</Field>
        <Field label="Tables" hint="Comma-separated: orders, billing.invoices">
          {(id) => <Input id={id} value={tables} onChange={(e) => setTables(e.target.value)} required />}
        </Field>
        <fieldset className="flex gap-4 text-sm">
          <legend className="mb-1 text-xs font-medium text-muted">Events</legend>
          {EVENTS.map((ev) => (
            <label key={ev} className="flex items-center gap-1.5">
              <input
                type="checkbox"
                checked={events.includes(ev)}
                onChange={(e) => setEvents((cur) => (e.target.checked ? [...cur, ev] : cur.filter((x) => x !== ev)))}
              />
              {ev}
            </label>
          ))}
        </fieldset>
        <Field label="Only when these columns change (UPDATE)" hint="Optional, comma-separated">
          {(id) => <Input id={id} value={columns} onChange={(e) => setColumns(e.target.value)} placeholder="status" />}
        </Field>
        <Field label="URL" hint="https:// only, to a public address">
          {(id) => <Input id={id} value={url} onChange={(e) => setURL(e.target.value)} placeholder="https://example.com/hooks/orders" required />}
        </Field>
        <Field label="Headers" hint="Optional, one per line as Name: value; stored encrypted">
          {(id) => (
            <textarea
              id={id}
              className="h-20 rounded-md border border-line-strong bg-surface-2 p-2 font-mono text-xs focus:border-accent focus:outline-none"
              value={headers}
              onChange={(e) => setHeaders(e.target.value)}
              placeholder="Authorization: Bearer …"
            />
          )}
        </Field>
        {err && <Alert>{err}</Alert>}
      </form>
    </SidePanel>
  );
}

function WebhookDetail({
  p,
  w,
  secret,
  onSecret,
  onSecretStored,
  onClose,
}: {
  p: Project;
  w: Webhook;
  secret: string | null;
  onSecret: (s: string) => void;
  onSecretStored: () => void;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const [dead, setDead] = useState(false);
  const log = useQuery({ queryKey: ["deliveries", w.id, dead], queryFn: () => api.webhookDeliveries(p.id, w.id, dead), refetchInterval: 5000 });
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const act = async (what: string, f: () => Promise<void>) => {
    setBusy(what);
    setMsg(null);
    try {
      await f();
      await qc.invalidateQueries({ queryKey: ["webhooks", p.id] });
      await qc.invalidateQueries({ queryKey: ["deliveries", w.id] });
    } catch (e) {
      setMsg({ ok: false, text: errorMessage(e) });
    } finally {
      setBusy(null);
    }
  };
  return (
    <SidePanel
      open
      onOpenChange={(o) => !o && onClose()}
      size="large"
      testId="webhook-detail"
      title={
        <span className="flex items-center gap-2">
          {w.name} <Badge tone={statusTone[w.status]}>{w.status}</Badge>
        </span>
      }
      description={<span className="font-mono">{w.url}</span>}
    >
      <div className="flex flex-col gap-4 text-sm">
        {secret && (
          <Alert tone="ok" title={`Signing secret for ${w.name}`}>
            <div className="flex flex-col gap-2" data-testid="webhook-secret">
              <span>Shown once: store it where your receiver checks the PGDock-Signature header.</span>
              <CopyField label="Secret" value={secret} secret testId="webhook-secret-value" />
              <Button className="self-start text-xs" onClick={onSecretStored}>
                I&rsquo;ve stored it
              </Button>
            </div>
          </Alert>
        )}
        {w.status_reason && <Alert tone={w.status === "broken" ? "danger" : "warn"}>{w.status_reason}</Alert>}
        <div className="flex flex-wrap gap-2">
          <Button
            className="text-xs"
            busy={busy === "test"}
            data-testid="webhook-test"
            onClick={() =>
              act("test", async () => {
                const r = await api.testWebhook(p.id, w.id);
                setMsg(
                  r.ok
                    ? { ok: true, text: `Test event delivered (HTTP ${r.status_code}, ${r.latency_ms} ms).` }
                    : { ok: false, text: `Test event failed: ${r.error ?? `HTTP ${r.status_code}`}` },
                );
              })
            }
          >
            Send test event
          </Button>
          <Button
            className="text-xs"
            busy={busy === "toggle"}
            onClick={() => act("toggle", async () => void (await api.updateWebhook(p.id, w.id, { enabled: !w.enabled })))}
          >
            {w.enabled ? "Pause" : w.status === "broken" ? "Reinstall triggers" : "Resume"}
          </Button>
          <Button
            className="text-xs"
            busy={busy === "rotate"}
            onClick={() =>
              act("rotate", async () => {
                const r = await api.rotateWebhookSecret(p.id, w.id);
                onSecret(r.secret);
              })
            }
          >
            Rotate secret
          </Button>
          <Button
            variant="danger"
            className="text-xs"
            busy={busy === "delete"}
            onClick={() =>
              confirm(`Delete the webhook ${w.name} and its queued events?`) &&
              act("delete", async () => {
                await api.deleteWebhook(p.id, w.id);
                onClose();
              })
            }
          >
            Delete
          </Button>
        </div>
        {msg && (
          <p className={msg.ok ? "text-ok-text" : "text-danger-text"} data-testid="webhook-message">
            {msg.text}
          </p>
        )}
        <div className="flex items-center justify-between">
          <label className="flex items-center gap-2 text-xs">
            <input type="checkbox" checked={dead} onChange={(e) => setDead(e.target.checked)} data-testid="dead-letters" />
            Dead letters only
          </label>
          {dead && (log.data?.items.length ?? 0) > 0 && (
            <Button
              className="text-xs"
              busy={busy === "replay"}
              onClick={() => act("replay", async () => void (await api.replayWebhook(p.id, w.id, { all: true })))}
            >
              Replay all
            </Button>
          )}
        </div>
        {log.data && log.data.items.length === 0 && <p className="text-muted">{dead ? "No dead letters." : "No deliveries in the last 7 days."}</p>}
        {log.data && log.data.items.length > 0 && (
          <Table head={["When", "Event", "Attempt", "Result", "Response", ""]}>
            {log.data.items.map((d) => (
              <tr key={d.id} data-testid="delivery-row">
                <td className="px-3 py-2 text-xs" title={formatDate(d.created_at)}>
                  {relativeTime(d.created_at)}
                </td>
                <td className="px-3 py-2 font-mono text-xs">{d.event_id}</td>
                <td className="px-3 py-2 tabular-nums">{d.attempt}</td>
                <td className="px-3 py-2">
                  <Badge tone={d.succeeded ? "ok" : d.dead_lettered ? "danger" : "warn"}>
                    {d.succeeded ? "delivered" : d.dead_lettered ? "dead letter" : "failed"}
                    {d.status_code ? ` · ${d.status_code}` : ""}
                  </Badge>
                  {d.latency_ms != null && <span className="ml-2 text-xs text-muted">{d.latency_ms} ms</span>}
                </td>
                <td className="max-w-xs truncate px-3 py-2 font-mono text-xs" title={d.error ?? d.response ?? ""}>
                  {d.error ?? d.response}
                </td>
                <td className="px-3 py-2">
                  {d.dead_lettered && !d.replayed_at && (
                    <Button className="text-xs" onClick={() => act("replay", async () => void (await api.replayWebhook(p.id, w.id, { ids: [d.id] })))}>
                      Replay
                    </Button>
                  )}
                </td>
              </tr>
            ))}
          </Table>
        )}
      </div>
    </SidePanel>
  );
}
