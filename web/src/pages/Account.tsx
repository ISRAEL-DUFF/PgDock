import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";
import { ApiRequestError, api, errorMessage } from "../api/client";
import { Alert, Badge, Button, Card, Field, Input, Modal, PageHeader, Spinner, Table } from "../components/ui";
import { TokensCard } from "../components/Tokens";
import { formatDate, relativeTime } from "../lib/format";
import { setCurrentOrg } from "../lib/org";
import { refreshSession, sessionQuery } from "../lib/session";

/** Profile, password, two-factor, sessions, tokens, and invitations (V2 §13). */
export function AccountPage() {
  const { data: session } = useQuery(sessionQuery);
  if (!session?.user) return <Spinner />;
  return (
    <>
      <PageHeader title="Your account" subtitle={session.user.email} />
      <div className="flex max-w-3xl flex-col gap-4">
        <InvitationsCard />
        <ProfileCard />
        <PasswordCard />
        <RecoveryCard />
        <SessionsCard />
        <TokensCard />
      </div>
    </>
  );
}

function InvitationsCard() {
  const q = useQuery({ queryKey: ["me", "invitations"], queryFn: api.myInvitations });
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [err, setErr] = useState<string | null>(null);
  if (!q.data?.items.length) return null;
  return (
    <Card title="Invitations">
      <div id="invitations" className="flex flex-col gap-2">
        {q.data.items.map((i) => (
          <div key={i.id} className="flex flex-wrap items-center justify-between gap-2 text-sm">
            <span>
              <strong>{i.org_name ?? "PGDock"}</strong> — {i.role}
              {i.projects.length > 0 && `, ${i.projects.length} project${i.projects.length === 1 ? "" : "s"}`}, from {i.invited_by}
            </span>
            <Button
              variant="primary"
              className="text-xs"
              onClick={async () => {
                setErr(null);
                try {
                  const r = await api.acceptMyInvitation(i.id);
                  await qc.invalidateQueries();
                  if (r.org_id) {
                    setCurrentOrg(r.org_id);
                    await navigate({ to: "/projects" });
                  }
                } catch (e) {
                  setErr(errorMessage(e));
                }
              }}
            >
              Accept
            </Button>
          </div>
        ))}
        {err && <Alert>{err}</Alert>}
      </div>
    </Card>
  );
}

function ProfileCard() {
  const { data: session } = useQuery(sessionQuery);
  const qc = useQueryClient();
  const [name, setName] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const value = name ?? session?.user?.name ?? "";
  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      await api.updateMe({ name: value });
      setName(null);
      await refreshSession(qc);
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Card title="Profile">
      <form className="flex flex-col gap-3" onSubmit={save}>
        <Field label="Name">{(id) => <Input id={id} value={value} onChange={(e) => setName(e.target.value)} maxLength={100} />}</Field>
        <p className="text-xs text-muted">Platform role: {session?.user?.platform_role === "platform_admin" ? "platform admin" : "user"}</p>
        {err && <Alert>{err}</Alert>}
        <Button type="submit" busy={busy} className="self-start" disabled={name === null}>
          Save
        </Button>
      </form>
    </Card>
  );
}

function PasswordCard() {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setMsg(null);
    try {
      await api.changePassword({ current_password: current, new_password: next });
      setCurrent("");
      setNext("");
      setMsg({ ok: true, text: "Password changed. Your other sessions were signed out." });
    } catch (e) {
      setMsg({ ok: false, text: e instanceof ApiRequestError && e.code === "invalid_credentials" ? "The current password is wrong." : errorMessage(e) });
    } finally {
      setBusy(false);
    }
  };
  return (
    <Card title="Password">
      <form className="flex flex-col gap-3" onSubmit={save}>
        <Field label="Current password">
          {(id) => <Input id={id} type="password" required autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} />}
        </Field>
        <Field label="New password" hint="At least 12 characters.">
          {(id) => <Input id={id} type="password" required minLength={12} autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} />}
        </Field>
        {msg && <Alert tone={msg.ok ? "ok" : "danger"}>{msg.text}</Alert>}
        <Button type="submit" busy={busy} className="self-start" disabled={next.length < 12 || !current}>
          Change password
        </Button>
      </form>
    </Card>
  );
}

function RecoveryCard() {
  const q = useQuery({ queryKey: ["me", "recovery"], queryFn: api.recoveryCodes });
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [codes, setCodes] = useState<string[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const regenerate = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      await api.reauth({ password, code });
      setCodes((await api.regenerateRecoveryCodes()).codes);
      setPassword("");
      setCode("");
      await qc.invalidateQueries({ queryKey: ["me", "recovery"] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  const left = q.data?.remaining ?? 0;
  return (
    <Card title="Two-factor recovery codes" actions={<Button className="text-xs" onClick={() => setOpen(true)}>New codes</Button>}>
      <p className="text-sm">
        {left} of 10 unused codes left.{" "}
        {left <= 3 && <span className="text-warn">Make new ones before you run out.</span>}
      </p>
      <Modal title="New recovery codes" open={open} onClose={() => { setOpen(false); setCodes(null); }}>
        {codes ? (
          <div className="flex flex-col gap-3">
            <p className="text-sm text-muted">Your old codes no longer work. Save these; they are shown only once.</p>
            <ul className="grid grid-cols-2 gap-1 rounded-md border border-line bg-surface-2 p-3 font-mono text-sm" data-testid="new-recovery-codes">
              {codes.map((c) => <li key={c}>{c}</li>)}
            </ul>
            <Button variant="primary" onClick={() => { setOpen(false); setCodes(null); }}>Done</Button>
          </div>
        ) : (
          <form className="flex flex-col gap-3" onSubmit={regenerate}>
            <p className="text-sm text-muted">Replaces all your recovery codes. Confirm with your password and a code.</p>
            <Field label="Password">{(id) => <Input id={id} type="password" value={password} onChange={(e) => setPassword(e.target.value)} />}</Field>
            <Field label="Authenticator code">{(id) => <Input id={id} inputMode="numeric" value={code} onChange={(e) => setCode(e.target.value)} />}</Field>
            {err && <Alert>{err}</Alert>}
            <Button type="submit" variant="primary" busy={busy} disabled={!password || code.length < 6}>Make new codes</Button>
          </form>
        )}
      </Modal>
    </Card>
  );
}

function SessionsCard() {
  const q = useQuery({ queryKey: ["me", "sessions"], queryFn: api.mySessions });
  const qc = useQueryClient();
  const navigate = useNavigate();
  return (
    <Card title="Sessions">
      {q.data && (
        <Table head={["Device", "IP", "Signed in", "Last seen", ""]}>
          {q.data.items.map((s) => (
            <tr key={s.id}>
              <td className="max-w-xs truncate px-3 py-1.5 text-xs" title={s.user_agent ?? ""}>
                {s.user_agent ?? "—"} {s.current && <Badge tone="accent">this one</Badge>}
              </td>
              <td className="px-3 py-1.5 font-mono text-xs">{s.ip ?? "—"}</td>
              <td className="px-3 py-1.5 text-xs text-muted">{formatDate(s.created_at)}</td>
              <td className="px-3 py-1.5 text-xs text-muted">{relativeTime(s.last_seen_at)}</td>
              <td className="px-3 py-1.5 text-right">
                <Button
                  className="text-xs"
                  onClick={async () => {
                    await api.revokeSession(s.id);
                    if (s.current) {
                      qc.clear();
                      await navigate({ to: "/login" });
                    } else await qc.invalidateQueries({ queryKey: ["me", "sessions"] });
                  }}
                >
                  {s.current ? "Sign out" : "Revoke"}
                </Button>
              </td>
            </tr>
          ))}
        </Table>
      )}
    </Card>
  );
}
