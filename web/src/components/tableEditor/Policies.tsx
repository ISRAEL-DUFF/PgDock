import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api, errorMessage, type TableInfo } from "../../api/client";
import { guessOwnerColumn, policyProblem, policySQL, policyTemplates, type PolicyTemplate } from "../../lib/policies";
import { Alert, Button, Dialog, Input, Select, Spinner } from "../ui";

/** The policy helper (V4 §3.6): pick a template, see the SQL, apply it. */
export function PolicyDialog({ projectId, info, onClose, onApplied }: { projectId: string; info: TableInfo; onClose: () => void; onApplied: () => void }) {
  const svc = useQuery({ queryKey: ["services", projectId], queryFn: () => api.backendServices(projectId) });
  const [template, setTemplate] = useState<PolicyTemplate>("owner");
  const [ownerColumn, setOwnerColumn] = useState(() => guessOwnerColumn(info.columns) ?? "");
  const [orgColumn, setOrgColumn] = useState(() => info.columns.find((c) => c.name === "org_id")?.name ?? "");
  const [membersTable, setMembersTable] = useState("members");
  const [membersOrgColumn, setMembersOrgColumn] = useState("org_id");
  const [membersUserColumn, setMembersUserColumn] = useState("user_id");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const roles = svc.data?.roles;
  const input = {
    schema: info.schema,
    table: info.name,
    template,
    roles: { anon: roles?.anon ?? "", user: roles?.user ?? "" },
    ownerColumn,
    orgColumn,
    membersTable,
    membersOrgColumn,
    membersUserColumn,
  };
  const problem = policyProblem(input);
  const sql = problem ? "" : policySQL(input);
  const apply = async () => {
    setBusy(true);
    setErr(null);
    try {
      const r = await api.sql(projectId, { query: sql, query_id: crypto.randomUUID() });
      if (r.error) setErr(r.error.message + (r.error.hint ? ` (${r.error.hint})` : ""));
      else onApplied();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  const columnSelect = (label: string, value: string, set: (v: string) => void) => (
    <label className="flex flex-col gap-1 text-[13px] text-fg-light">
      {label}
      <Select aria-label={label} value={value} onChange={(e) => set(e.target.value)} className="font-mono">
        <option value="">Choose a column</option>
        {info.columns.map((c) => (
          <option key={c.name} value={c.name}>
            {c.name} ({c.type})
          </option>
        ))}
      </Select>
    </label>
  );
  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onClose()}
      title={`Row-level security for ${info.name}`}
      description="Turns on row-level security and adds policies for the publishable key's roles. Review the SQL; it runs as the project owner."
      testId="policy-dialog"
      className="w-[min(44rem,calc(100vw-2rem))]"
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" busy={busy} disabled={!sql || !roles} onClick={() => void apply()} data-testid="policy-apply">
            Apply policies
          </Button>
        </>
      }
    >
      {svc.isPending ? (
        <Spinner />
      ) : !svc.data?.enabled || !roles ? (
        <Alert tone="warn">Turn on backend services (Settings → API) first: the policies are written for its request roles.</Alert>
      ) : (
        <div className="flex flex-col gap-3">
          <label className="flex flex-col gap-1 text-[13px] text-fg-light">
            Template
            <Select aria-label="Template" value={template} onChange={(e) => setTemplate(e.target.value as PolicyTemplate)} data-testid="policy-template">
              {policyTemplates.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.label}
                </option>
              ))}
            </Select>
            <span className="text-[12px] text-muted">{policyTemplates.find((t) => t.id === template)?.hint}</span>
          </label>
          {template === "org_members" ? (
            <>
              {columnSelect("Organisation column", orgColumn, setOrgColumn)}
              <div className="grid grid-cols-3 gap-2">
                <label className="flex flex-col gap-1 text-[13px] text-fg-light">
                  Membership table
                  <Input aria-label="Membership table" value={membersTable} onChange={(e) => setMembersTable(e.target.value)} className="font-mono" />
                </label>
                <label className="flex flex-col gap-1 text-[13px] text-fg-light">
                  Its organisation column
                  <Input aria-label="Membership organisation column" value={membersOrgColumn} onChange={(e) => setMembersOrgColumn(e.target.value)} className="font-mono" />
                </label>
                <label className="flex flex-col gap-1 text-[13px] text-fg-light">
                  Its user column
                  <Input aria-label="Membership user column" value={membersUserColumn} onChange={(e) => setMembersUserColumn(e.target.value)} className="font-mono" />
                </label>
              </div>
            </>
          ) : (
            columnSelect("Owner column (the user's id)", ownerColumn, setOwnerColumn)
          )}
          {problem ? (
            <p className="text-[12px] text-muted">{problem}</p>
          ) : (
            <pre className="max-h-64 overflow-auto rounded bg-surface-2 p-2.5 font-mono text-[11px] whitespace-pre-wrap" data-testid="policy-sql">
              {sql}
            </pre>
          )}
          {err && <Alert>{err}</Alert>}
        </div>
      )}
    </Dialog>
  );
}
