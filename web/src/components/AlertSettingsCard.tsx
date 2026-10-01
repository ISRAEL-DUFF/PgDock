import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type FormEvent } from "react";
import { api, errorMessage, type AlertSettingsRequest } from "../api/client";
import { useOperationToast } from "./Toasts";
import { Alert, Button, Card, Field, Input, Select, Spinner, StatusBadge, Table } from "./ui";
import { formatDate } from "../lib/format";

/** Alert channels: a webhook and optional SMTP email (spec §8.8). */
export function AlertSettingsCard() {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["settings", "alerts"], queryFn: api.alertSettings });
  const [f, setF] = useState({ webhook: "", secret: "", email: false, host: "", port: "", tls: "starttls", user: "", password: "", from: "", to: "" });
  const [busy, setBusy] = useState<"save" | "test" | null>(null);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  useEffect(() => {
    const s = q.data;
    if (!s) return;
    setF((x) => ({
      ...x,
      webhook: s.webhook_url,
      email: !!s.smtp,
      host: s.smtp?.host ?? "",
      port: s.smtp ? String(s.smtp.port) : "",
      tls: s.smtp?.tls ?? "starttls",
      user: s.smtp?.username ?? "",
      from: s.smtp?.from ?? "",
      to: s.smtp?.to.join(", ") ?? "",
    }));
  }, [q.data]);
  if (q.isPending) return <Spinner />;
  if (q.isError) return <Alert>{errorMessage(q.error)}</Alert>;
  const s = q.data;
  const set = (k: keyof typeof f) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) =>
    setF((x) => ({ ...x, [k]: e.target.type === "checkbox" ? (e.target as HTMLInputElement).checked : e.target.value }));

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy("save");
    setMsg(null);
    const b: AlertSettingsRequest = { webhook_url: f.webhook.trim() };
    if (f.secret) b.webhook_secret = f.secret;
    if (f.email) {
      b.smtp = {
        host: f.host.trim(),
        port: f.port ? Number(f.port) : undefined,
        tls: f.tls as "starttls" | "tls" | "none",
        username: f.user.trim() || undefined,
        password: f.password || undefined,
        from: f.from.trim(),
        to: f.to.split(/[,\s]+/).filter(Boolean),
      };
    }
    try {
      qc.setQueryData(["settings", "alerts"], await api.saveAlertSettings(b));
      setF((x) => ({ ...x, secret: "", password: "" }));
      setMsg({ ok: true, text: "Saved." });
    } catch (err) {
      setMsg({ ok: false, text: errorMessage(err) });
    } finally {
      setBusy(null);
    }
  };
  const test = async () => {
    setBusy("test");
    setMsg(null);
    try {
      const r = await api.testAlerts();
      const bad = r.results.filter((x) => !x.ok);
      setMsg(
        bad.length === 0
          ? { ok: true, text: `Test sent via ${r.results.map((x) => x.channel).join(" and ")}.` }
          : { ok: false, text: bad.map((x) => `${x.channel}: ${x.error}`).join("; ") },
      );
    } catch (err) {
      setMsg({ ok: false, text: errorMessage(err) });
    } finally {
      setBusy(null);
    }
  };

  return (
    <Card title="Alerts">
      <form className="flex flex-col gap-4" onSubmit={save}>
        <p className="text-sm text-muted">
          Backup failed or overdue, restore test failed, node disk above 85%, node unreachable for 2 minutes, project over its disk warning, pooler
          down, and a failed isolation check. Each alert is sent when it fires and when it resolves.
        </p>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Webhook URL" hint="POSTed JSON; Slack-style and generic receivers work.">
            {(id) => <Input id={id} value={f.webhook} onChange={set("webhook")} placeholder="https://hooks.example.com/pgdock" />}
          </Field>
          <Field label="Signing secret" hint={s.has_webhook_secret ? "Set. Leave empty to keep it." : "Optional; signs X-PGDock-Signature."}>
            {(id) => <Input id={id} type="password" value={f.secret} onChange={set("secret")} autoComplete="new-password" />}
          </Field>
        </div>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={f.email} onChange={set("email")} /> Also send email (SMTP)
        </label>
        {f.email && (
          <div className="grid gap-3 sm:grid-cols-3">
            <Field label="SMTP host">{(id) => <Input id={id} value={f.host} onChange={set("host")} placeholder="smtp.example.com" />}</Field>
            <Field label="Port">{(id) => <Input id={id} type="number" value={f.port} onChange={set("port")} placeholder="587" />}</Field>
            <Field label="Encryption">
              {(id) => (
                <Select id={id} value={f.tls} onChange={set("tls")}>
                  <option value="starttls">STARTTLS (587)</option>
                  <option value="tls">TLS (465)</option>
                  <option value="none">None (local relay only)</option>
                </Select>
              )}
            </Field>
            <Field label="Username">{(id) => <Input id={id} value={f.user} onChange={set("user")} autoComplete="off" />}</Field>
            <Field label="Password" hint={s.smtp?.has_password ? "Set. Leave empty to keep it." : undefined}>
              {(id) => <Input id={id} type="password" value={f.password} onChange={set("password")} autoComplete="new-password" />}
            </Field>
            <Field label="From">{(id) => <Input id={id} value={f.from} onChange={set("from")} placeholder="PGDock <pgdock@example.com>" />}</Field>
            <div className="sm:col-span-3">
              <Field label="To" hint="Comma-separated addresses.">
                {(id) => <Input id={id} value={f.to} onChange={set("to")} placeholder="ops@example.com" />}
              </Field>
            </div>
          </div>
        )}
        {msg && <Alert tone={msg.ok ? "ok" : "danger"}>{msg.text}</Alert>}
        <div className="flex gap-2">
          <Button type="submit" variant="primary" busy={busy === "save"}>
            Save alerts
          </Button>
          <Button onClick={test} busy={busy === "test"} disabled={!s.webhook_url && !s.smtp}>
            Send test
          </Button>
        </div>
      </form>
    </Card>
  );
}

/** The weekly tenant-isolation checks (spec §7.1). */
export function IsolationChecksCard() {
  const qc = useQueryClient();
  const toast = useOperationToast();
  const q = useQuery({ queryKey: ["isolation-checks"], queryFn: api.isolationChecks, refetchInterval: 15_000 });
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const run = async () => {
    setBusy(true);
    setErr(null);
    try {
      const ops = await api.runIsolationChecks();
      for (const op of ops.items) toast(op.id, "Isolation check");
      await qc.invalidateQueries({ queryKey: ["isolation-checks"] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Card
      title="Tenant isolation"
      actions={
        <Button className="text-xs" onClick={run} busy={busy}>
          Check now
        </Button>
      }
    >
      <p className="mb-3 text-sm text-muted">
        Every {q.data?.every_days ?? 7} days each shared cluster is checked against the isolation checklist: cluster settings and pg_hba.conf, every
        project's role and database, and two throwaway tenants that try to reach each other. A failure raises an alert.
      </p>
      {err && <Alert>{err}</Alert>}
      {q.data && (
        <Table head={["Cluster", "Last check", "Result"]}>
          {q.data.items.map((c) => (
            <tr key={c.instance_id} data-testid="isolation-row">
              <td className="px-3 py-1.5 text-sm">{c.node_name}</td>
              <td className="px-3 py-1.5 text-xs text-muted">{c.last ? formatDate(c.last.finished_at ?? c.last.created_at) : "not yet"}</td>
              <td className="px-3 py-1.5 text-xs">
                {c.last ? <StatusBadge status={c.last.status} /> : "—"}
                {c.last?.error && <p className="mt-1 max-w-md text-danger">{c.last.error}</p>}
              </td>
            </tr>
          ))}
        </Table>
      )}
    </Card>
  );
}
