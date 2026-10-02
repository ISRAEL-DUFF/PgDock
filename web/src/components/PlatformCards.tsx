import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { api, errorMessage, type MailSettingsRequest, type SignupSettings } from "../api/client";
import { formatDate } from "../lib/format";
import { Alert, Button, Card, Field, Input, Select } from "./ui";

/**
 * Platform SMTP (V2 §3.1): saved only after a test message goes out, since
 * verification, password resets, and invitations depend on it.
 */
export function MailSettingsForm({ defaultTo, onSaved, submitLabel = "Send a test and save" }: { defaultTo?: string; onSaved?: () => void; submitLabel?: string }) {
  const q = useQuery({ queryKey: ["settings", "mail"], queryFn: api.mailSettings });
  const qc = useQueryClient();
  const cur = q.data;
  const [form, setForm] = useState<Partial<MailSettingsRequest>>({});
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [ok, setOk] = useState(false);
  const v = {
    host: form.host ?? cur?.host ?? "",
    port: form.port ?? cur?.port,
    username: form.username ?? cur?.username ?? "",
    from: form.from ?? cur?.from ?? "",
    tls: form.tls ?? cur?.tls ?? "starttls",
    test_to: form.test_to ?? defaultTo ?? "",
  };
  const set = (k: keyof MailSettingsRequest) => (e: { target: { value: string } }) =>
    setForm((f) => ({ ...f, [k]: k === "port" ? (e.target.value ? Number(e.target.value) : undefined) : e.target.value }));

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    setOk(false);
    try {
      await api.saveMailSettings({
        host: v.host.trim(),
        port: v.port,
        username: v.username || undefined,
        password: password ? password : undefined,
        from: v.from.trim(),
        tls: v.tls as MailSettingsRequest["tls"],
        test_to: v.test_to.trim(),
      });
      setPassword("");
      setOk(true);
      await qc.invalidateQueries({ queryKey: ["settings", "mail"] });
      onSaved?.();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form className="flex flex-col gap-3" onSubmit={submit} data-testid="mail-form">
      <div className="grid gap-3 sm:grid-cols-[1fr_8rem]">
        <Field label="SMTP host">{(id) => <Input id={id} required value={v.host} onChange={set("host")} placeholder="smtp.example.com" className="font-mono" />}</Field>
        <Field label="Port" hint="Blank: by TLS mode">
          {(id) => <Input id={id} type="number" min={1} max={65535} value={v.port ?? ""} onChange={set("port")} />}
        </Field>
      </div>
      <Field label="Encryption">
        {(id) => (
          <Select id={id} value={v.tls} onChange={set("tls")}>
            <option value="starttls">STARTTLS (port 587)</option>
            <option value="tls">TLS (port 465)</option>
            <option value="none">None (a relay on this host only)</option>
          </Select>
        )}
      </Field>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Username">{(id) => <Input id={id} value={v.username} onChange={set("username")} autoComplete="off" />}</Field>
        <Field label="Password" hint={cur?.has_password ? "Leave blank to keep the saved one." : undefined}>
          {(id) => <Input id={id} type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" />}
        </Field>
      </div>
      <Field label="From address">{(id) => <Input id={id} required value={v.from} onChange={set("from")} placeholder="PGDock <pgdock@example.com>" />}</Field>
      <Field label="Send the test to">{(id) => <Input id={id} type="email" required value={v.test_to} onChange={set("test_to")} />}</Field>
      {err && <Alert>{err}</Alert>}
      {ok && <Alert tone="ok">Test sent and settings saved. Check the inbox.</Alert>}
      <Button type="submit" variant="primary" busy={busy} className="self-start">
        {submitLabel}
      </Button>
    </form>
  );
}

export function MailCard() {
  const q = useQuery({ queryKey: ["settings", "mail"], queryFn: api.mailSettings });
  const [editing, setEditing] = useState(false);
  const m = q.data;
  return (
    <Card title="Email (SMTP)" actions={m?.configured && !editing && <Button className="text-xs" onClick={() => setEditing(true)}>Change</Button>}>
      {m && (!m.configured || editing) ? (
        <>
          {!m.configured && (
            <div className="mb-3">
              <Alert tone="warn">Email is not set up: nobody can verify an address, reset a password, or receive an invitation.</Alert>
            </div>
          )}
          <MailSettingsForm onSaved={() => setEditing(false)} />
        </>
      ) : (
        <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm" data-testid="mail-summary">
          <dt className="text-muted">Server</dt>
          <dd className="font-mono text-xs">
            {m?.host}:{m?.port} ({m?.tls})
          </dd>
          <dt className="text-muted">From</dt>
          <dd>{m?.from}</dd>
        </dl>
      )}
    </Card>
  );
}

const modes: { id: SignupSettings["mode"]; label: string; hint: string }[] = [
  { id: "invite_only", label: "Invite-only", hint: "Accounts come only from invitations (yours or an organisation's)." },
  { id: "approval", label: "Approval required", hint: "Anyone can sign up; you approve each account under Users." },
  { id: "open", label: "Open", hint: "Anyone can sign up and start straight away." },
];

export function SignupCard() {
  const q = useQuery({ queryKey: ["settings", "signup"], queryFn: api.signupSettings });
  const qc = useQueryClient();
  const [mode, setMode] = useState<SignupSettings["mode"] | null>(null);
  const [domains, setDomains] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const cur = q.data;
  const m = mode ?? cur?.mode ?? "invite_only";
  const d = domains ?? (cur?.domains ?? []).join(", ");
  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      await api.saveSignupSettings({ mode: m, domains: d.split(/[\s,]+/).filter(Boolean) });
      setMode(null);
      setDomains(null);
      await qc.invalidateQueries({ queryKey: ["settings", "signup"] });
      await qc.invalidateQueries({ queryKey: ["session"] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Card title="Sign-up">
      <form className="flex flex-col gap-3" onSubmit={save}>
        <fieldset className="flex flex-col gap-2">
          {modes.map((o) => (
            <label key={o.id} className="flex items-start gap-2 text-sm">
              <input type="radio" name="signup-mode" checked={m === o.id} onChange={() => setMode(o.id)} className="mt-1" />
              <span>
                <span className="font-medium">{o.label}</span> <span className="text-muted">— {o.hint}</span>
              </span>
            </label>
          ))}
        </fieldset>
        {m !== "invite_only" && (
          <Field label="Allowed email domains" hint="Comma-separated; leave empty to allow any.">
            {(id) => <Input id={id} value={d} onChange={(e) => setDomains(e.target.value)} placeholder="example.com, example.org" />}
          </Field>
        )}
        {err && <Alert>{err}</Alert>}
        <Button type="submit" busy={busy} className="self-start" disabled={mode === null && domains === null}>
          Save
        </Button>
      </form>
    </Card>
  );
}

export function TermsCard() {
  const q = useQuery({ queryKey: ["terms"], queryFn: api.terms });
  const qc = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [terms, setTerms] = useState("");
  const [privacy, setPrivacy] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const t = q.data;
  const start = () => {
    setTerms(t?.terms_md ?? "");
    setPrivacy(t?.privacy_md ?? "");
    setEditing(true);
  };
  const publish = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      await api.publishTerms({ terms_md: terms, privacy_md: privacy });
      setEditing(false);
      await qc.invalidateQueries({ queryKey: ["terms"] });
      await qc.invalidateQueries({ queryKey: ["session"] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Card title="Terms of use and privacy" actions={!editing && <Button className="text-xs" onClick={start}>Publish a new version</Button>}>
      {editing ? (
        <form className="flex flex-col gap-3" onSubmit={publish}>
          <Field label="Terms of use (Markdown)">
            {(id) => <textarea id={id} className="h-48 rounded-md border border-line bg-surface p-2 font-mono text-xs" value={terms} onChange={(e) => setTerms(e.target.value)} />}
          </Field>
          <Field label="Privacy notice (Markdown)">
            {(id) => <textarea id={id} className="h-32 rounded-md border border-line bg-surface p-2 font-mono text-xs" value={privacy} onChange={(e) => setPrivacy(e.target.value)} />}
          </Field>
          <p className="text-xs text-muted">Everyone, you included, accepts the new version at their next visit.</p>
          {err && <Alert>{err}</Alert>}
          <div className="flex gap-2">
            <Button type="submit" variant="primary" busy={busy}>
              Publish
            </Button>
            <Button onClick={() => setEditing(false)}>Cancel</Button>
          </div>
        </form>
      ) : (
        <p className="text-sm">
          Version {t?.version ?? "—"}, published {formatDate(t?.published_at)}.
        </p>
      )}
    </Card>
  );
}
