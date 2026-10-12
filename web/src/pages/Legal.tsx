import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";
import { api, errorMessage, type LegalDocument } from "../api/client";
import {
  Alert,
  Badge,
  Button,
  Field,
  Input,
  Page,
  PageSkeleton,
  Panel,
  Select,
  SidePanel,
  Table,
  TableSkeleton,
} from "../components/ui";
import { formatDate } from "../lib/format";
import { useCurrentOrg } from "../lib/org";

const KIND_LABEL: Record<string, string> = {
  sla: "Service level agreement",
  dpa: "Data processing agreement",
  order_form: "Order form",
};
const textarea =
  "h-72 w-full rounded-md border border-line bg-surface p-2 font-mono text-xs";

function DocumentText({ d }: { d: LegalDocument }) {
  return (
    <div
      tabIndex={0}
      className="max-h-96 overflow-y-auto rounded-md border border-line bg-surface-2 p-3 text-xs whitespace-pre-wrap"
      data-testid={`legal-text-${d.kind}`}
    >
      {d.body_md}
    </div>
  );
}

/** Org → Legal (V3 §7.3): the SLA, the DPA and the organisation's order
 * form in effect, which an owner accepts on its behalf. */
export function OrgLegalPage() {
  const { org } = useCurrentOrg();
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["org", org?.id, "legal"],
    queryFn: () => api.orgLegal(org!.id),
    enabled: !!org,
  });
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  if (!org || q.isPending) return <PageSkeleton />;
  if (q.error) return <Alert>{errorMessage(q.error)}</Alert>;
  const owner = org.role === "owner";
  const accept = async (id: string) => {
    setBusy(id);
    setErr(null);
    try {
      qc.setQueryData(
        ["org", org.id, "legal"],
        await api.acceptLegal(org.id, id),
      );
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(null);
    }
  };
  return (
    <Page
      title="Legal"
      description="The agreements between your organisation and PGDock, alongside the terms of use, privacy notice and acceptable use policy each person accepts. An owner accepts them for the organisation; a new version needs accepting again."
      testId="org-legal"
    >
      {err && <Alert>{err}</Alert>}
      {q.data.items.map(({ document: d, accepted_at, accepted_by }) => (
        <Panel
          key={d.id}
          title={
            <span className="flex items-center gap-2">
              {d.title}
              <Badge tone="muted">version {d.version}</Badge>
              {accepted_at ? (
                <Badge tone="ok">accepted</Badge>
              ) : (
                <Badge tone="warn">not accepted</Badge>
              )}
            </span>
          }
          description={
            accepted_at
              ? `Accepted ${formatDate(accepted_at)}${accepted_by ? ` by ${accepted_by}` : ""}.`
              : `${KIND_LABEL[d.kind]}, published ${formatDate(d.published_at)}.`
          }
          testId={`legal-${d.kind}`}
          footer={
            !accepted_at &&
            (owner ? (
              <Button
                variant="primary"
                busy={busy === d.id}
                onClick={() => void accept(d.id)}
                data-testid={`accept-${d.kind}`}
              >
                Accept for {org.name}
              </Button>
            ) : (
              <span className="text-xs text-muted">
                An owner of the organisation accepts it.
              </span>
            ))
          }
        >
          <DocumentText d={d} />
        </Panel>
      ))}
    </Page>
  );
}

/** A banner for owners while a document in effect isn't accepted. */
export function LegalBanner({ orgId }: { orgId: string }) {
  const q = useQuery({
    queryKey: ["org", orgId, "legal"],
    queryFn: () => api.orgLegal(orgId),
  });
  if (!q.data?.outstanding) return null;
  return (
    <Alert tone="warn" title="Agreements to accept">
      A new or updated service level agreement, data processing agreement or
      order form is waiting for an owner to accept it.{" "}
      <Link
        to="/org/legal"
        className="underline"
        data-testid="legal-banner-link"
      >
        Review on the Legal page
      </Link>
      .
    </Alert>
  );
}

/** Platform → Legal: publish new versions of the SLA and DPA and see who
 * accepted each. */
export function AdminLegalPage() {
  const q = useQuery({
    queryKey: ["admin", "legal"],
    queryFn: api.legalVersions,
  });
  const [publishing, setPublishing] = useState(false);
  const [openId, setOpenId] = useState<string | null>(null);
  return (
    <Page
      title="Legal documents"
      description="The service level agreement and data processing agreement organisations accept. The defaults are templates: have them reviewed before charging anyone. Order forms are on each organisation's page; the terms, privacy notice and acceptable use policy are in Platform settings."
      testId="admin-legal"
      actions={
        <Button
          variant="primary"
          onClick={() => setPublishing(true)}
          data-testid="legal-publish"
        >
          Publish a new version
        </Button>
      }
    >
      {q.isPending && <TableSkeleton cols={4} />}
      {q.error && <Alert>{errorMessage(q.error)}</Alert>}
      {q.data && (
        <Table head={["Document", "Version", "Published", "Accepted by"]}>
          {q.data.items.map((v) => (
            <tr
              key={v.id}
              className="cursor-pointer"
              onClick={() => setOpenId(v.id)}
              data-testid={`legal-version-${v.kind}-${v.version}`}
            >
              <td className="px-3 py-2">{v.title}</td>
              <td className="px-3 py-2">{v.version}</td>
              <td className="px-3 py-2 text-xs text-muted">
                {formatDate(v.published_at)}
              </td>
              <td className="px-3 py-2">{v.acceptances} organisations</td>
            </tr>
          ))}
        </Table>
      )}
      <PublishLegal open={publishing} onClose={() => setPublishing(false)} />
      <LegalVersion id={openId} onClose={() => setOpenId(null)} />
    </Page>
  );
}

function PublishLegal({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const [kind, setKind] = useState<"sla" | "dpa">("sla");
  const [title, setTitle] = useState(KIND_LABEL.sla);
  const [body, setBody] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const load = async (k: "sla" | "dpa") => {
    setKind(k);
    setTitle(KIND_LABEL[k]);
    const cur = (await api.legal()).items.find((d) => d.kind === k);
    setBody(cur?.body_md ?? "");
  };
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      await api.publishLegal({ kind, title, body_md: body });
      await qc.invalidateQueries({ queryKey: ["admin", "legal"] });
      onClose();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <SidePanel
      open={open}
      onOpenChange={(o) => {
        if (o) void load(kind);
        else onClose();
      }}
      title="Publish a new version"
      description="Organisations accept the new version; until they do, their banner asks an owner to."
      size="large"
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button
            type="submit"
            form="legal-form"
            variant="primary"
            busy={busy}
            disabled={!title.trim() || !body.trim()}
          >
            Publish
          </Button>
        </>
      }
    >
      <form id="legal-form" className="flex flex-col gap-4" onSubmit={submit}>
        <Field label="Document">
          {(id) => (
            <Select
              id={id}
              value={kind}
              onChange={(e) => void load(e.target.value as "sla" | "dpa")}
            >
              <option value="sla">{KIND_LABEL.sla}</option>
              <option value="dpa">{KIND_LABEL.dpa}</option>
            </Select>
          )}
        </Field>
        <Field label="Title">
          {(id) => (
            <Input
              id={id}
              value={title}
              onChange={(e) => setTitle(e.target.value)}
            />
          )}
        </Field>
        <Field label="Text (Markdown)">
          {(id) => (
            <textarea
              id={id}
              className={textarea}
              value={body}
              onChange={(e) => setBody(e.target.value)}
            />
          )}
        </Field>
        {err && <Alert>{err}</Alert>}
      </form>
    </SidePanel>
  );
}

function LegalVersion({
  id,
  onClose,
}: {
  id: string | null;
  onClose: () => void;
}) {
  const q = useQuery({
    queryKey: ["admin", "legal", id],
    queryFn: () => api.legalDocument(id!),
    enabled: !!id,
  });
  const d = q.data;
  return (
    <SidePanel
      open={!!id}
      onOpenChange={(o) => !o && onClose()}
      title={
        d ? `${d.document.title}, version ${d.document.version}` : "Document"
      }
      size="large"
    >
      {d ? (
        <div className="flex flex-col gap-4">
          <DocumentText d={d.document} />
          <h3 className="text-[14px]">Accepted by</h3>
          {d.acceptances.length === 0 ? (
            <p className="text-sm text-muted">No organisation yet.</p>
          ) : (
            <Table head={["Organisation", "Accepted", "By"]}>
              {d.acceptances.map((a) => (
                <tr key={a.org_id}>
                  <td className="px-3 py-2">{a.org_name}</td>
                  <td className="px-3 py-2 text-xs text-muted">
                    {formatDate(a.accepted_at)}
                  </td>
                  <td className="px-3 py-2 text-xs">{a.accepted_by ?? "—"}</td>
                </tr>
              ))}
            </Table>
          )}
        </div>
      ) : (
        <TableSkeleton rows={3} cols={1} />
      )}
    </SidePanel>
  );
}

/** An organisation's order form (custom terms or prices), on the admin's
 * organisation page. */
export function OrderFormCard({ org }: { org: string }) {
  const [editing, setEditing] = useState(false);
  const [title, setTitle] = useState("Order form");
  const [body, setBody] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [done, setDone] = useState<LegalDocument | null>(null);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      setDone(await api.publishOrderForm(org, { title, body_md: body }));
      setEditing(false);
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Panel
      title="Order form"
      description="Terms or prices agreed with this organisation. Its owners accept each version on their Legal page."
      actions={
        !editing && (
          <Button
            className="text-xs"
            onClick={() => setEditing(true)}
            data-testid="order-form-new"
          >
            Publish a version
          </Button>
        )
      }
    >
      {editing ? (
        <form className="flex flex-col gap-3" onSubmit={submit}>
          <Field label="Title">
            {(id) => (
              <Input
                id={id}
                value={title}
                onChange={(e) => setTitle(e.target.value)}
              />
            )}
          </Field>
          <Field label="Text (Markdown)">
            {(id) => (
              <textarea
                id={id}
                className={textarea}
                value={body}
                onChange={(e) => setBody(e.target.value)}
              />
            )}
          </Field>
          {err && <Alert>{err}</Alert>}
          <div className="flex gap-2">
            <Button
              type="submit"
              variant="primary"
              busy={busy}
              disabled={!title.trim() || !body.trim()}
            >
              Publish
            </Button>
            <Button onClick={() => setEditing(false)}>Cancel</Button>
          </div>
        </form>
      ) : (
        <p className="text-sm text-muted">
          {done
            ? `Version ${done.version} published ${formatDate(done.published_at)}.`
            : "Publishing a version replaces the one in effect."}
        </p>
      )}
    </Panel>
  );
}
