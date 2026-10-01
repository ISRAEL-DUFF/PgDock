import { useNavigate } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type FormEvent } from "react";
import { api, errorMessage, type Project, type ProjectCredentials } from "../api/client";
import { ConfirmDestroy } from "../components/ConfirmDelete";
import { CredentialPanel } from "../components/Credentials";
import { PromoteCard } from "../components/PromoteCard";
import { useOperationToast } from "../components/Toasts";
import { Alert, Button, Card, Field, Input } from "../components/ui";
import { formatBytes, parseBytes } from "../lib/format";
import { useOperationStream } from "../lib/useOperationStream";
import { useProject } from "./ProjectOverview";

export function ProjectSettingsPage() {
  const { data: p } = useProject();
  if (!p) return null;
  return (
    <div className="flex max-w-3xl flex-col gap-4">
      <GeneralCard p={p} />
      <GuardrailsCard p={p} />
      <RotateCard p={p} />
      <PromoteCard p={p} />
      <DangerCard p={p} />
    </div>
  );
}

function GeneralCard({ p }: { p: Project }) {
  const qc = useQueryClient();
  const [name, setName] = useState(p.name);
  const [description, setDescription] = useState(p.description ?? "");
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setMsg(null);
    try {
      await api.updateProject(p.id, { name, description });
      await qc.invalidateQueries({ queryKey: ["project", p.id] });
      setMsg({ ok: true, text: "Saved." });
    } catch (e) {
      setMsg({ ok: false, text: errorMessage(e) });
    } finally {
      setBusy(false);
    }
  };
  return (
    <Card title="General">
      <form className="flex flex-col gap-3" onSubmit={submit}>
        <Field label="Name" hint="Renaming keeps the database name and connection strings.">
          {(id) => <Input id={id} required maxLength={64} value={name} onChange={(e) => setName(e.target.value)} />}
        </Field>
        <Field label="Description">
          {(id) => <Input id={id} maxLength={1000} value={description} onChange={(e) => setDescription(e.target.value)} />}
        </Field>
        {msg && <Alert tone={msg.ok ? "ok" : "danger"}>{msg.text}</Alert>}
        <div>
          <Button type="submit" busy={busy} disabled={name === p.name && description === (p.description ?? "")}>
            Save
          </Button>
        </div>
      </form>
    </Card>
  );
}

function GuardrailsCard({ p }: { p: Project }) {
  const qc = useQueryClient();
  const toast = useOperationToast();
  const s = p.settings;
  const [form, setForm] = useState({
    connection_limit: String(s.connection_limit),
    pool_size: String(s.pool_size),
    statement_timeout: s.statement_timeout,
    idle: s.idle_in_transaction_session_timeout,
    disk: formatBytes(s.disk_warn_bytes),
    readOnly: s.console_read_only,
  });
  useEffect(() => {
    setForm({
      connection_limit: String(s.connection_limit),
      pool_size: String(s.pool_size),
      statement_timeout: s.statement_timeout,
      idle: s.idle_in_transaction_session_timeout,
      disk: formatBytes(s.disk_warn_bytes),
      readOnly: s.console_read_only,
    });
  }, [s.connection_limit, s.pool_size, s.statement_timeout, s.idle_in_transaction_session_timeout, s.disk_warn_bytes, s.console_read_only]);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const disk = parseBytes(form.disk);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setMsg(null);
    try {
      const res = await api.updateProject(p.id, {
        settings: {
          connection_limit: Number(form.connection_limit),
          pool_size: Number(form.pool_size),
          statement_timeout: form.statement_timeout.trim(),
          idle_in_transaction_session_timeout: form.idle.trim(),
          disk_warn_bytes: disk ?? undefined,
          console_read_only: form.readOnly,
        },
      });
      if (res.operation) toast(res.operation.id, `Apply settings · ${p.name}`);
      await qc.invalidateQueries({ queryKey: ["project", p.id] });
      setMsg({ ok: true, text: res.operation ? "Saved; applying to the database and pooler." : "Saved." });
    } catch (e) {
      setMsg({ ok: false, text: errorMessage(e) });
    } finally {
      setBusy(false);
    }
  };
  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement>) =>
    setForm((f) => ({ ...f, [k]: e.target.type === "checkbox" ? e.target.checked : e.target.value }));

  return (
    <Card title="Guardrails">
      <form className="grid grid-cols-1 gap-4 sm:grid-cols-2" onSubmit={submit}>
        <Field label="Max backend connections">
          {(id) => <Input id={id} type="number" min={1} max={1000} value={form.connection_limit} onChange={set("connection_limit")} />}
        </Field>
        <Field label="Pooler pool size">
          {(id) => <Input id={id} type="number" min={1} max={1000} value={form.pool_size} onChange={set("pool_size")} />}
        </Field>
        <Field label="Statement timeout" hint="e.g. 60s, 500ms, 5min; empty to unset.">
          {(id) => <Input id={id} value={form.statement_timeout} onChange={set("statement_timeout")} className="font-mono" />}
        </Field>
        <Field label="Idle in transaction timeout">
          {(id) => <Input id={id} value={form.idle} onChange={set("idle")} className="font-mono" />}
        </Field>
        <Field label="Disk warning" error={disk === null ? "Use a size like 1 GiB or 500 MB." : null}>
          {(id) => <Input id={id} value={form.disk} onChange={set("disk")} />}
        </Field>
        <label className="flex items-center gap-2 self-end pb-2 text-sm">
          <input type="checkbox" checked={form.readOnly} onChange={set("readOnly")} />
          SQL console is read-only
        </label>
        {msg && (
          <div className="sm:col-span-2">
            <Alert tone={msg.ok ? "ok" : "danger"}>{msg.text}</Alert>
          </div>
        )}
        <div className="sm:col-span-2">
          <Button type="submit" busy={busy} disabled={disk === null}>
            Save guardrails
          </Button>
        </div>
      </form>
    </Card>
  );
}

function RotateCard({ p }: { p: Project }) {
  const [creds, setCreds] = useState<ProjectCredentials | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const stream = useOperationStream(creds?.operation.id);
  const rotate = async () => {
    setBusy(true);
    setErr(null);
    try {
      setCreds(await api.rotatePassword(p.id));
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Card title="Password">
      {!creds ? (
        <div className="flex flex-col gap-3">
          <p className="text-sm text-muted">Rotating issues a new password. The old one stops working as soon as the rotation finishes.</p>
          {err && <Alert>{err}</Alert>}
          <div>
            <Button onClick={rotate} busy={busy} disabled={p.status !== "active"}>
              Rotate password
            </Button>
          </div>
        </div>
      ) : (
        <div className="flex flex-col gap-3">
          {stream.status === "failed" && <Alert title="Rotation failed">{stream.error}. The old password still works.</Alert>}
          <CredentialPanel creds={creds} ready={stream.status === "succeeded"} onDismiss={() => setCreds(null)} />
        </div>
      )}
    </Card>
  );
}

function DangerCard({ p }: { p: Project }) {
  const [open, setOpen] = useState(false);
  const [finalBackup, setFinalBackup] = useState(true);
  const toast = useOperationToast();
  const navigate = useNavigate();
  const qc = useQueryClient();
  return (
    <Card title="Danger zone" className="border-danger/40">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-muted">
          Deleting drops the database and its role and removes the connection strings. A final backup is kept for 30 days.
        </p>
        <Button variant="danger" onClick={() => setOpen(true)}>
          Delete project
        </Button>
      </div>
      <ConfirmDestroy
        open={open}
        onClose={() => setOpen(false)}
        title={`Delete ${p.name}`}
        name={p.name}
        description="Confirm with the project name, your password, and an authenticator code."
        action="Delete project"
        run={async () => {
          const op = await api.deleteProject(p.id, p.name, !finalBackup);
          toast(op.id, `Delete · ${p.name}`);
          await qc.invalidateQueries({ queryKey: ["projects"] });
          await navigate({ to: "/projects" });
        }}
      >
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={finalBackup} onChange={(e) => setFinalBackup(e.target.checked)} />
          Take a final backup first (kept 30 days)
        </label>
      </ConfirmDestroy>
    </Card>
  );
}
