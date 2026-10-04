import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";
import { api, errorMessage, type InvitationCreated, type Org, type OrgMember, type OrgRole, type ProjectRole } from "../api/client";
import { Alert, Badge, Button, Panel, CopyField, EmptyState, Field, Input, Dialog, PageHeading, Select, Spinner, Table } from "../components/ui";
import { formatDate, relativeTime } from "../lib/format";
import { canManageOrg, setCurrentOrg, useCurrentOrg } from "../lib/org";
import { StorageTargetsPanel } from "../components/StorageTargets";
import { sessionQuery } from "../lib/session";
import { OrgTokensCard } from "../components/Tokens";

const orgRoles: OrgRole[] = ["owner", "admin", "member"];
const projectRoles: { id: ProjectRole; label: string }[] = [
  { id: "admin", label: "Admin" },
  { id: "developer", label: "Developer" },
  { id: "read_only", label: "Read-only" },
];

/** Org → Members (V2 §3.3). */
export function OrgMembersPage() {
  const { org } = useCurrentOrg();
  const { data: session } = useQuery(sessionQuery);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const members = useQuery({ queryKey: ["org", org?.id, "members"], queryFn: () => api.orgMembers(org!.id), enabled: !!org });
  const manage = canManageOrg(org);
  const invitations = useQuery({ queryKey: ["org", org?.id, "invitations"], queryFn: () => api.orgInvitations(org!.id), enabled: !!org && manage });
  const [inviting, setInviting] = useState(false);
  const [transferring, setTransferring] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  if (!org) return <Spinner />;
  const owner = org.role === "owner";
  const refresh = () => qc.invalidateQueries({ queryKey: ["org", org.id] });

  const act = async (f: () => Promise<unknown>) => {
    setErr(null);
    try {
      await f();
      await refresh();
    } catch (e) {
      setErr(errorMessage(e));
    }
  };

  return (
    <>
      <PageHeading
        title="Members"
        description={`People in ${org.name}. Owners and admins manage everything; members see the projects they're added to.`}
        actions={
          <div className="flex gap-2">
            {owner && (
              <Button onClick={() => setTransferring(true)} data-testid="transfer-ownership">
                Transfer ownership
              </Button>
            )}
            {!org.personal && (
              <Button
                onClick={() =>
                  act(async () => {
                    await api.leaveOrg(org.id);
                    await qc.invalidateQueries({ queryKey: ["orgs"] });
                    const mine = (await api.orgs()).items.find((o) => o.personal);
                    if (mine) setCurrentOrg(mine.id);
                    await navigate({ to: "/projects" });
                  })
                }
              >
                Leave
              </Button>
            )}
            {manage && (
              <Button variant="primary" onClick={() => setInviting(true)} data-testid="invite-member">
                Invite
              </Button>
            )}
          </div>
        }
      />
      {err && (
        <div className="mb-3">
          <Alert>{err}</Alert>
        </div>
      )}
      {members.isPending && <Spinner />}
      {members.data && (
        <Table head={["Member", "Role", "Projects", "2FA", "Last active", ""]}>
          {members.data.items.map((m) => (
            <tr key={m.user_id} data-testid={`member-${m.email}`}>
              <td className="px-3 py-2">
                <div className="font-medium">{m.name || m.email}</div>
                {m.name && <div className="text-xs text-muted">{m.email}</div>}
                {m.user_id === session?.user?.id && <Badge tone="accent">you</Badge>}
              </td>
              <td className="px-3 py-2">
                {manage && m.user_id !== session?.user?.id ? (
                  <Select
                    aria-label={`Role of ${m.email}`}
                    value={m.role}
                    onChange={(e) => act(() => api.setOrgRole(org.id, m.user_id, e.target.value as OrgRole))}
                  >
                    {orgRoles
                      .filter((r) => owner || r !== "owner" || m.role === "owner")
                      .map((r) => (
                        <option key={r}>{r}</option>
                      ))}
                  </Select>
                ) : (
                  m.role
                )}
              </td>
              <td className="px-3 py-2 text-xs">
                {m.role === "member"
                  ? m.projects.length
                    ? m.projects.map((p) => `${p.project_name} (${p.role.replace("_", "-")})`).join(", ")
                    : "none"
                  : "all (admin)"}
              </td>
              <td className="px-3 py-2">{m.totp_enabled ? <Badge tone="ok">on</Badge> : <Badge tone="warn">not set up</Badge>}</td>
              <td className="px-3 py-2 text-xs text-muted">{m.last_active_at ? relativeTime(m.last_active_at) : "—"}</td>
              <td className="px-3 py-2 text-right">
                {manage && m.user_id !== session?.user?.id && (owner || m.role !== "owner") && (
                  <Button
                    className="text-xs"
                    variant="danger"
                    onClick={() => act(() => api.removeOrgMember(org.id, m.user_id))}
                    data-testid={`remove-${m.email}`}
                  >
                    Remove
                  </Button>
                )}
              </td>
            </tr>
          ))}
        </Table>
      )}
      {manage && invitations.data && invitations.data.items.length > 0 && (
        <div className="mt-6">
          <h2 className="mb-2 font-semibold">Pending invitations</h2>
          <Table head={["Email", "Role", "Invited by", "Expires", ""]}>
            {invitations.data.items.map((i) => (
              <tr key={i.id}>
                <td className="px-3 py-2">{i.email}</td>
                <td className="px-3 py-2">{i.role}</td>
                <td className="px-3 py-2 text-xs text-muted">{i.invited_by}</td>
                <td className="px-3 py-2 text-xs text-muted">{formatDate(i.expires_at)}</td>
                <td className="px-3 py-2 text-right">
                  <Button className="text-xs" onClick={() => act(() => api.revokeOrgInvitation(org.id, i.id))}>
                    Revoke
                  </Button>
                </td>
              </tr>
            ))}
          </Table>
        </div>
      )}
      <InviteModal
        orgId={org.id}
        canInviteOwners={owner}
        open={inviting}
        onClose={() => {
          setInviting(false);
          void refresh();
        }}
      />
      <TransferOwnershipModal
        orgId={org.id}
        members={(members.data?.items ?? []).filter((m) => m.user_id !== session?.user?.id)}
        open={transferring}
        onClose={() => {
          setTransferring(false);
          void refresh();
          void qc.invalidateQueries({ queryKey: ["orgs"] });
        }}
      />
    </>
  );
}

/** The result of an invitation: the link to pass on if email fails. */
export function InvitationResult({ created }: { created: InvitationCreated }) {
  return (
    <div className="flex flex-col gap-3" data-testid="invitation-created">
      {created.email_sent ? (
        <Alert tone="ok">Invitation emailed to {created.invitation.email}.</Alert>
      ) : (
        <Alert tone="warn">The email could not be sent ({created.email_error}). Send them this link yourself.</Alert>
      )}
      <CopyField label="Invitation link (valid 7 days, once)" value={created.url} testId="invitation-url" />
    </div>
  );
}

function InviteModal({ orgId, canInviteOwners, open, onClose }: { orgId: string; canInviteOwners: boolean; open: boolean; onClose: () => void }) {
  const projects = useQuery({ queryKey: ["projects", orgId], queryFn: () => api.projects(orgId), enabled: open });
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<OrgRole>("member");
  const [access, setAccess] = useState<Record<string, ProjectRole | "">>({});
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [created, setCreated] = useState<InvitationCreated | null>(null);
  const close = () => {
    setEmail("");
    setRole("member");
    setAccess({});
    setCreated(null);
    setErr(null);
    onClose();
  };
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      const picks = Object.entries(access).filter(([, r]) => r) as [string, ProjectRole][];
      setCreated(
        await api.inviteOrgMember(orgId, { email, role, projects: role === "member" ? picks.map(([project_id, r]) => ({ project_id, role: r })) : [] }),
      );
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog title="Invite to the organisation" open={open} onOpenChange={(o) => !o && close()}>
      {created ? (
        <div className="flex flex-col gap-3">
          <InvitationResult created={created} />
          <Button variant="primary" onClick={close}>
            Done
          </Button>
        </div>
      ) : (
        <form className="flex flex-col gap-4" onSubmit={submit}>
          <Field label="Email">{(id) => <Input id={id} type="email" required value={email} onChange={(e) => setEmail(e.target.value)} autoFocus />}</Field>
          <Field label="Organisation role" hint="Owners and admins are admins of every project; members only of the projects you pick.">
            {(id) => (
              <Select id={id} value={role} onChange={(e) => setRole(e.target.value as OrgRole)}>
                {orgRoles
                  .filter((r) => canInviteOwners || r !== "owner")
                  .map((r) => (
                    <option key={r}>{r}</option>
                  ))}
              </Select>
            )}
          </Field>
          {role === "member" && (projects.data?.items.length ?? 0) > 0 && (
            <fieldset className="flex flex-col gap-2">
              <legend className="mb-1 text-sm font-medium">Project access</legend>
              {projects.data!.items.map((p) => (
                <label key={p.id} className="flex items-center justify-between gap-2 text-sm">
                  <span>{p.name}</span>
                  <Select
                    aria-label={`Access to ${p.name}`}
                    value={access[p.id] ?? ""}
                    onChange={(e) => setAccess((a) => ({ ...a, [p.id]: e.target.value as ProjectRole | "" }))}
                  >
                    <option value="">No access</option>
                    {projectRoles.map((r) => (
                      <option key={r.id} value={r.id}>
                        {r.label}
                      </option>
                    ))}
                  </Select>
                </label>
              ))}
            </fieldset>
          )}
          {err && <Alert>{err}</Alert>}
          <div className="flex justify-end gap-2">
            <Button onClick={close}>Cancel</Button>
            <Button type="submit" variant="primary" busy={busy}>
              Send invitation
            </Button>
          </div>
        </form>
      )}
    </Dialog>
  );
}

function TransferOwnershipModal({ orgId, members, open, onClose }: { orgId: string; members: OrgMember[]; open: boolean; onClose: () => void }) {
  const [to, setTo] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      await api.reauth({ password, code });
      await api.transferOwnership(orgId, to);
      onClose();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog title="Transfer ownership" open={open} onOpenChange={(o) => !o && onClose()}>
      <form className="flex flex-col gap-4" onSubmit={submit}>
        <p className="text-sm text-muted">They become an owner and you become an admin.</p>
        <Field label="New owner">
          {(id) => (
            <Select id={id} required value={to} onChange={(e) => setTo(e.target.value)}>
              <option value="">Choose a member…</option>
              {members.map((m) => (
                <option key={m.user_id} value={m.user_id}>
                  {m.name || m.email}
                </option>
              ))}
            </Select>
          )}
        </Field>
        <Field label="Your password">{(id) => <Input id={id} type="password" value={password} onChange={(e) => setPassword(e.target.value)} />}</Field>
        <Field label="Authenticator code">{(id) => <Input id={id} inputMode="numeric" value={code} onChange={(e) => setCode(e.target.value)} />}</Field>
        {err && <Alert>{err}</Alert>}
        <div className="flex justify-end gap-2">
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={!to || !password || code.length < 6}>
            Transfer
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** Org → Settings (V2 §13). */
export function OrgSettingsPage() {
  const { org } = useCurrentOrg();
  const qc = useQueryClient();
  const [name, setName] = useState<string | null>(null);
  const [slug, setSlug] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  if (!org) return <Spinner />;
  if (!canManageOrg(org)) return <EmptyState title="Only owners and admins change organisation settings" />;
  const save = async (patch: Parameters<typeof api.updateOrg>[1]) => {
    setBusy(true);
    setErr(null);
    try {
      await api.updateOrg(org.id, patch);
      setName(null);
      setSlug(null);
      await qc.invalidateQueries({ queryKey: ["orgs"] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <PageHeading title="Organisation" description={org.personal ? "Your personal organisation. Invite others into it like any other." : undefined} />
      <div className="flex max-w-2xl flex-col gap-4">
        <Panel title="Name">
          <form
            className="flex flex-col gap-3"
            onSubmit={(e) => {
              e.preventDefault();
              void save({ name: name ?? undefined, slug: slug ?? undefined });
            }}
          >
            <Field label="Name">{(id) => <Input id={id} value={name ?? org.name} onChange={(e) => setName(e.target.value)} maxLength={64} />}</Field>
            <Field label="Slug">{(id) => <Input id={id} value={slug ?? org.slug} onChange={(e) => setSlug(e.target.value)} className="font-mono" />}</Field>
            <Button type="submit" busy={busy} className="self-start" disabled={name === null && slug === null}>
              Save
            </Button>
          </form>
        </Panel>
        <Panel title="Projects">
          <label className="flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              checked={org.members_can_create_projects}
              onChange={(e) => void save({ members_can_create_projects: e.target.checked })}
              className="mt-1"
              data-testid="members-can-create"
            />
            <span>
              <span className="font-medium">Members can create projects</span>
              <span className="block text-muted">They become the admin of what they create.</span>
            </span>
          </label>
          <label className="mt-3 flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              checked={!!org.sensitive_by_default}
              onChange={(e) => void save({ sensitive_by_default: e.target.checked })}
              className="mt-1"
              data-testid="sensitive-by-default"
            />
            <span>
              <span className="font-medium">New projects contain sensitive data</span>
              <span className="block text-muted">Their branches copy the schema only unless a project admin asks for the data.</span>
            </span>
          </label>
        </Panel>
        <Panel title="Plan">
          <p className="text-sm">{org.plan} plan. Ask the platform admin to change it.</p>
        </Panel>
        <OrgTokensCard orgId={org.id} />
        <Panel title="Backup storage">
          <StorageTargetsPanel org={org.id} />
        </Panel>
        {err && <Alert>{err}</Alert>}
        {org.role === "owner" && !org.personal && org.status === "active" && <DeleteOrgCard org={org} />}
      </div>
    </>
  );
}

/** Delete the organisation, after a 7-day grace period (V2 §10.10). */
function DeleteOrgCard({ org }: { org: Org }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [confirm, setConfirm] = useState("");
  const [all, setAll] = useState(false);
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      await api.reauth({ password, code });
      await api.deleteOrg(org.id, { confirm, delete_projects: all });
      setOpen(false);
      await qc.invalidateQueries();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Panel title="Delete organisation">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-muted">
          Its projects go offline at once, and are deleted (each with a final backup kept for 30 days) after 7 days. Until then any owner can cancel.
        </p>
        <Button variant="danger" onClick={() => setOpen(true)} data-testid="delete-org">
          Delete…
        </Button>
      </div>
      <Dialog title={`Delete ${org.name}`} open={open} onOpenChange={(o) => !o && (() => setOpen(false))()}>
        <form className="flex flex-col gap-3" onSubmit={submit}>
          <Field label={`Type ${org.name} to confirm`}>{(id) => <Input id={id} value={confirm} onChange={(e) => setConfirm(e.target.value)} />}</Field>
          {org.project_count > 0 && (
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} /> Delete its {org.project_count} project
              {org.project_count === 1 ? "" : "s"} too
            </label>
          )}
          <Field label="Your password">{(id) => <Input id={id} type="password" value={password} onChange={(e) => setPassword(e.target.value)} />}</Field>
          <Field label="Authenticator code">{(id) => <Input id={id} inputMode="numeric" value={code} onChange={(e) => setCode(e.target.value)} />}</Field>
          {err && <Alert>{err}</Alert>}
          <Button type="submit" variant="danger" busy={busy} disabled={confirm !== org.name || (org.project_count > 0 && !all) || !password || code.length < 6}>
            Delete in 7 days
          </Button>
        </form>
      </Dialog>
    </Panel>
  );
}
