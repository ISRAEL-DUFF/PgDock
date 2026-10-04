import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { api, errorMessage, type AdminUser, type InvitationCreated } from "../api/client";
import { Alert, Badge, Button, Field, Input, Dialog, PageHeading, SidePanel, Spinner, Table } from "../components/ui";
import { formatDate, relativeTime } from "../lib/format";
import { sessionQuery } from "../lib/session";
import { InvitationResult } from "./Org";

/** Admin → Users (V2 §2.4, §3.1, §3.6). */
export function AdminUsersPage() {
  const { data: session } = useQuery(sessionQuery);
  const qc = useQueryClient();
  const [search, setSearch] = useState("");
  const [pending, setPending] = useState(false);
  const users = useQuery({ queryKey: ["admin", "users", { search, pending }], queryFn: () => api.users({ q: search || undefined, pending }) });
  const invitations = useQuery({ queryKey: ["admin", "invitations"], queryFn: api.platformInvitations });
  const [inviting, setInviting] = useState(false);
  const [resetting, setResetting] = useState<AdminUser | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const act = async (f: () => Promise<unknown>) => {
    setErr(null);
    try {
      await f();
      await qc.invalidateQueries({ queryKey: ["admin"] });
    } catch (e) {
      setErr(errorMessage(e));
    }
  };
  return (
    <>
      <PageHeading
        title="Users"
        description="Every account on this PGDock. You see who they are, not what is in their organisations."
        actions={
          <Button variant="primary" onClick={() => setInviting(true)} data-testid="invite-user">
            Invite someone
          </Button>
        }
      />
      <div className="mb-3 flex flex-wrap items-center gap-3">
        <Input placeholder="Search email or name" value={search} onChange={(e) => setSearch(e.target.value)} className="max-w-xs" aria-label="Search users" />
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={pending} onChange={(e) => setPending(e.target.checked)} /> Waiting for approval
        </label>
      </div>
      {err && (
        <div className="mb-3">
          <Alert>{err}</Alert>
        </div>
      )}
      {users.isPending && <Spinner />}
      {users.data && (
        <Table head={["User", "State", "2FA", "Orgs", "Last active", ""]}>
          {users.data.items.map((u) => (
            <tr key={u.id} data-testid={`user-${u.email}`}>
              <td className="px-3 py-2">
                <div className="font-medium">{u.name || u.email}</div>
                <div className="text-xs text-muted">
                  {u.name ? u.email + " · " : ""}
                  {u.platform_role === "platform_admin" ? "platform admin" : "user"} · joined {formatDate(u.created_at)}
                </div>
              </td>
              <td className="px-3 py-2">
                {u.disabled ? (
                  <Badge tone="danger">disabled</Badge>
                ) : !u.email_verified ? (
                  <Badge tone="warn">unverified</Badge>
                ) : !u.approved ? (
                  <Badge tone="warn">awaiting approval</Badge>
                ) : (
                  <Badge tone="ok">active</Badge>
                )}
              </td>
              <td className="px-3 py-2">{u.totp_enabled ? "on" : "—"}</td>
              <td className="px-3 py-2">{u.org_count}</td>
              <td className="px-3 py-2 text-xs text-muted">{u.last_active_at ? relativeTime(u.last_active_at) : "—"}</td>
              <td className="px-3 py-2">
                <div className="flex justify-end gap-1">
                  {!u.approved && !u.disabled && (
                    <Button className="text-xs" variant="primary" onClick={() => act(() => api.updateUser(u.id, { approved: true }))}>
                      Approve
                    </Button>
                  )}
                  {u.totp_enabled && u.id !== session?.user?.id && (
                    <Button className="text-xs" onClick={() => setResetting(u)}>
                      Reset 2FA
                    </Button>
                  )}
                  {u.id !== session?.user?.id && (
                    <Button
                      className="text-xs"
                      variant={u.disabled ? "primary" : "danger"}
                      onClick={() => act(() => api.updateUser(u.id, { disabled: !u.disabled }))}
                    >
                      {u.disabled ? "Enable" : "Disable"}
                    </Button>
                  )}
                </div>
              </td>
            </tr>
          ))}
        </Table>
      )}
      {invitations.data && invitations.data.items.length > 0 && (
        <div className="mt-6">
          <h2 className="mb-2 font-semibold">Pending platform invitations</h2>
          <Table head={["Email", "Invited", "Expires", ""]}>
            {invitations.data.items.map((i) => (
              <tr key={i.id}>
                <td className="px-3 py-2">{i.email}</td>
                <td className="px-3 py-2 text-xs text-muted">{formatDate(i.created_at)}</td>
                <td className="px-3 py-2 text-xs text-muted">{formatDate(i.expires_at)}</td>
                <td className="px-3 py-2 text-right">
                  <Button className="text-xs" onClick={() => act(() => api.revokePlatformInvitation(i.id))}>
                    Revoke
                  </Button>
                </td>
              </tr>
            ))}
          </Table>
        </div>
      )}
      <PlatformInviteModal
        open={inviting}
        onClose={() => {
          setInviting(false);
          void qc.invalidateQueries({ queryKey: ["admin"] });
        }}
      />
      <ResetTotpModal user={resetting} onClose={() => setResetting(null)} />
    </>
  );
}

function PlatformInviteModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [email, setEmail] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [created, setCreated] = useState<InvitationCreated | null>(null);
  const close = () => {
    setEmail("");
    setCreated(null);
    setErr(null);
    onClose();
  };
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      setCreated(await api.invitePlatform(email));
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <SidePanel
      title="Invite someone to PGDock"
      description="They get an account and a personal organisation, and no access to anyone else's."
      open={open}
      onOpenChange={(o) => !o && close()}
      footer={
        created ? (
          <Button variant="primary" onClick={close}>
            Done
          </Button>
        ) : (
          <>
            <Button onClick={close}>Cancel</Button>
            <Button type="submit" form="platform-invite" variant="primary" busy={busy}>
              Send invitation
            </Button>
          </>
        )
      }
    >
      {created ? (
        <InvitationResult created={created} />
      ) : (
        <form id="platform-invite" className="flex flex-col gap-4" onSubmit={submit}>
          <Field label="Email">{(id) => <Input id={id} type="email" required value={email} onChange={(e) => setEmail(e.target.value)} autoFocus />}</Field>
          {err && <Alert>{err}</Alert>}
        </form>
      )}
    </SidePanel>
  );
}

function ResetTotpModal({ user, onClose }: { user: AdminUser | null; onClose: () => void }) {
  const qc = useQueryClient();
  const [confirmed, setConfirmed] = useState(false);
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!user) return;
    setBusy(true);
    setErr(null);
    try {
      await api.reauth({ password, code });
      await api.resetUserTotp(user.id);
      await qc.invalidateQueries({ queryKey: ["admin"] });
      setConfirmed(false);
      setPassword("");
      setCode("");
      onClose();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog title="Reset two-factor authentication" open={!!user} onOpenChange={(o) => !o && onClose()}>
      <form className="flex flex-col gap-4" onSubmit={submit}>
        <p className="text-sm text-muted">
          {user?.email} will set up a new authenticator at their next sign-in and is emailed now. Only do this after confirming who they are some other way.
        </p>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={confirmed} onChange={(e) => setConfirmed(e.target.checked)} /> I've confirmed their identity out of band
        </label>
        <Field label="Your password">{(id) => <Input id={id} type="password" value={password} onChange={(e) => setPassword(e.target.value)} />}</Field>
        <Field label="Your authenticator code">{(id) => <Input id={id} inputMode="numeric" value={code} onChange={(e) => setCode(e.target.value)} />}</Field>
        {err && <Alert>{err}</Alert>}
        <div className="flex justify-end gap-2">
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="danger" busy={busy} disabled={!confirmed || !password || code.length < 6}>
            Reset 2FA
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
