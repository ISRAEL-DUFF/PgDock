import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { api, errorMessage, type InvitationCreated, type PersonalCredentials, type ProjectRole } from "../api/client";
import { Alert, Badge, Button, CopyField, Page, Panel, Field, Input, Select, Table, PageSkeleton } from "../components/ui";
import { formatDate } from "../lib/format";
import { sessionQuery } from "../lib/session";
import { AuditView } from "./Audit";
import { InvitationResult } from "./Org";
import { useProject } from "./ProjectOverview";

const roles: { id: ProjectRole; label: string; hint: string }[] = [
  { id: "admin", label: "Admin", hint: "everything, including members and deletion" },
  { id: "developer", label: "Developer", hint: "read/write data and schema, backups" },
  { id: "read_only", label: "Read-only", hint: "read data, read-only console and login" },
];

/** Project → Members, with "Get my credentials" (V2 §3.3, §3.5). */
export function ProjectMembersPage() {
  const { data: p } = useProject();
  const { data: session } = useQuery(sessionQuery);
  const qc = useQueryClient();
  const members = useQuery({ queryKey: ["project", p?.id, "members"], queryFn: () => api.projectMembers(p!.id), enabled: !!p });
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<ProjectRole>("developer");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [invited, setInvited] = useState<InvitationCreated | null>(null);
  const [added, setAdded] = useState(false);
  if (!p) return <PageSkeleton />;
  const admin = p.my_role === "admin";
  const refresh = () => qc.invalidateQueries({ queryKey: ["project", p.id, "members"] });
  const act = async (f: () => Promise<unknown>) => {
    setErr(null);
    try {
      await f();
      await refresh();
    } catch (e) {
      setErr(errorMessage(e));
    }
  };
  const add = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    setInvited(null);
    setAdded(false);
    try {
      const r = await api.addProjectMember(p.id, { email, role });
      if (r.invitation) setInvited(r.invitation);
      else setAdded(true);
      setEmail("");
      await refresh();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Page title="Members" description="Who can use this project, with what role, and their personal database logins." testId="project-members">
      <MyCredentials projectId={p.id} />
      <Panel title="Members">
        {members.data && (
          <Table head={["Member", "Role", "Login", ""]}>
            {members.data.items.map((m) => (
              <tr key={m.user_id} data-testid={`project-member-${m.email}`}>
                <td className="px-3 py-2">
                  <div className="font-medium">{m.name || m.email}</div>
                  {m.name && <div className="text-xs text-muted">{m.email}</div>}
                </td>
                <td className="px-3 py-2">
                  {m.implicit ? (
                    <span className="flex items-center gap-2">
                      admin <Badge>org {m.org_role}</Badge>
                    </span>
                  ) : admin && m.user_id !== session?.user?.id ? (
                    <Select
                      aria-label={`Role of ${m.email}`}
                      value={m.role}
                      onChange={(e) => act(() => api.setProjectRole(p.id, m.user_id, e.target.value as ProjectRole))}
                    >
                      {roles.map((r) => (
                        <option key={r.id} value={r.id}>
                          {r.label}
                        </option>
                      ))}
                    </Select>
                  ) : (
                    m.role.replace("_", "-")
                  )}
                </td>
                <td className="px-3 py-2 text-xs">{m.has_credentials ? "personal login" : "—"}</td>
                <td className="px-3 py-2 text-right">
                  {admin && !m.implicit && m.user_id !== session?.user?.id && (
                    <Button className="text-xs" variant="danger" onClick={() => act(() => api.removeProjectMember(p.id, m.user_id))}>
                      Remove
                    </Button>
                  )}
                </td>
              </tr>
            ))}
          </Table>
        )}
        {admin && (
          <form className="mt-4 flex flex-wrap items-end gap-2" onSubmit={add} data-testid="add-member">
            <div className="min-w-56 flex-1">
              <Field label="Add someone by email" hint="Organisation members are added now; anyone else gets an invitation.">
                {(id) => <Input id={id} type="email" required value={email} onChange={(e) => setEmail(e.target.value)} />}
              </Field>
            </div>
            <Select aria-label="Project role" value={role} onChange={(e) => setRole(e.target.value as ProjectRole)}>
              {roles.map((r) => (
                <option key={r.id} value={r.id} title={r.hint}>
                  {r.label}
                </option>
              ))}
            </Select>
            <Button type="submit" variant="primary" busy={busy}>
              Add
            </Button>
          </form>
        )}
        {added && (
          <div className="mt-3">
            <Alert tone="ok">Added.</Alert>
          </div>
        )}
        {invited && (
          <div className="mt-3">
            <InvitationResult created={invited} />
          </div>
        )}
        {err && (
          <div className="mt-3">
            <Alert>{err}</Alert>
          </div>
        )}
      </Panel>
      {admin && <AuditView embedded title="Project audit log" scope={`project:${p.id}`} load={(q) => api.projectAudit(p.id, q)} />}
    </Page>
  );
}

/** The member's own database login on this project (V2 §3.5). */
function MyCredentials({ projectId }: { projectId: string }) {
  const q = useQuery({ queryKey: ["project", projectId, "my-credentials"], queryFn: () => api.myCredentials(projectId) });
  const qc = useQueryClient();
  const [creds, setCreds] = useState<PersonalCredentials | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const issue = async () => {
    setBusy(true);
    setErr(null);
    try {
      setCreds(await api.issueMyCredentials(projectId));
      await qc.invalidateQueries({ queryKey: ["project", projectId] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  const info = q.data;
  return (
    <Panel title="Your database login">
      <div className="flex flex-col gap-3" data-testid="my-credentials">
        <p className="text-sm text-muted">
          Your own login to this database, separate from the app's password, so removing you never means rotating it. Your role gives{" "}
          <strong>{info?.access === "read_only" ? "read-only" : "read/write"}</strong> access.
          {info?.exists && ` Created ${formatDate(info.created_at)}${info.rotated_at ? `, last rotated ${formatDate(info.rotated_at)}` : ""}.`}
        </p>
        {creds ? (
          <>
            <Alert tone="warn">Shown once. Copy the password now; you can rotate it any time.</Alert>
            <CopyField label="User" value={creds.role} testId="my-db-user" />
            <CopyField label="Password" value={creds.password} secret testId="my-db-password" />
            <CopyField label="Pooled URL (transaction mode)" value={creds.connection.pooled_url} secret testId="my-pooled-url" />
            <CopyField label="Session URL" value={creds.connection.session_url} secret testId="my-session-url" />
          </>
        ) : (
          <Button className="self-start" variant="primary" busy={busy} onClick={issue} data-testid="get-credentials">
            {info?.exists ? "Rotate my password" : "Get my credentials"}
          </Button>
        )}
        {err && <Alert>{err}</Alert>}
      </div>
    </Panel>
  );
}
