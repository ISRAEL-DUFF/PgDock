import { useQueryClient } from "@tanstack/react-query";
import { useCurrentOrg } from "../lib/org";
import { useState, type FormEvent } from "react";
import { api, errorMessage, type ImportPreflight, type ProjectCredentials } from "../api/client";
import { ProvisionProgress } from "../components/ProvisionProgress";
import { Alert, Badge, Button, Card, Field, Input, PageHeader, Table } from "../components/ui";
import { formatBytes } from "../lib/format";

/** Import from an existing database (spec §6.8). */
export function ImportProjectPage() {
  const qc = useQueryClient();
  const { org } = useCurrentOrg();
  const [source, setSource] = useState("");
  const [pf, setPf] = useState<ImportPreflight | null>(null);
  const [schemas, setSchemas] = useState<string[]>([]);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState<"preflight" | "import" | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [creds, setCreds] = useState<ProjectCredentials | null>(null);

  const preflight = async (e: FormEvent) => {
    e.preventDefault();
    setBusy("preflight");
    setErr(null);
    setPf(null);
    try {
      const r = await api.importPreflight(source.trim());
      setPf(r);
      setSchemas(r.default_schemas);
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(null);
    }
  };

  const start = async (e: FormEvent) => {
    e.preventDefault();
    setBusy("import");
    setErr(null);
    try {
      setCreds(await api.createImport({ org_id: org?.id, source_url: source.trim(), name, schemas }));
      void qc.invalidateQueries({ queryKey: ["projects"] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(null);
    }
  };

  if (creds) return <ProvisionProgress creds={creds} progressTitle="Import" />;

  const refs = pf?.role_references.filter((r) => schemas.includes(r.schema)) ?? [];
  const toggle = (s: string) => setSchemas((cur) => (cur.includes(s) ? cur.filter((x) => x !== s) : [...cur, s].sort()));

  return (
    <>
      <PageHeader title="Import a database" subtitle="Copy an existing Postgres database (Supabase included) into a new project. The source is only read." />
      <div className="flex max-w-3xl flex-col gap-4">
        <Card title="1. Source">
          <form className="flex flex-col gap-3" onSubmit={preflight}>
            <Field
              label="Source connection string"
              hint="Used only while the import runs: held in memory, never stored, and redacted from logs. For Supabase, use the direct or session pooler string (port 5432)."
            >
              {(id) => (
                <Input
                  id={id}
                  type="password"
                  required
                  value={source}
                  onChange={(e) => {
                    setSource(e.target.value);
                    setPf(null);
                  }}
                  className="font-mono"
                  placeholder="postgresql://postgres:…@db.abcd.supabase.co:5432/postgres"
                  autoComplete="off"
                />
              )}
            </Field>
            <div>
              <Button type="submit" busy={busy === "preflight"} disabled={!source.trim()}>
                Run preflight
              </Button>
            </div>
          </form>
        </Card>
        {err && <Alert>{err}</Alert>}
        {pf && (
          <>
            <Card title="2. Preflight" actions={pf.supabase && <Badge tone="accent">Supabase project</Badge>}>
              <dl className="mb-4 grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
                <dt className="text-muted">Postgres</dt>
                <dd data-testid="preflight-version">{pf.server_version} → 18</dd>
                <dt className="text-muted">Size</dt>
                <dd>{formatBytes(pf.size_bytes)}</dd>
              </dl>
              {pf.warnings.length > 0 && (
                <div className="mb-4">
                  <Alert tone="warn" title="Check before importing">
                    <ul className="list-disc pl-4">
                      {pf.warnings.map((w) => (
                        <li key={w}>{w}</li>
                      ))}
                    </ul>
                  </Alert>
                </div>
              )}
              <h3 className="mb-2 text-sm font-semibold">Schemas to import</h3>
              <div className="mb-4 flex flex-col gap-1.5">
                {pf.schemas.map((s) => (
                  <label key={s.name} className="flex items-center gap-2 text-sm">
                    <input type="checkbox" checked={schemas.includes(s.name)} onChange={() => toggle(s.name)} aria-label={`Import schema ${s.name}`} />
                    <span className="font-mono">{s.name}</span>
                    <span className="text-xs text-muted">
                      {s.tables} table{s.tables === 1 ? "" : "s"}
                    </span>
                    {s.managed && <Badge>managed by Supabase</Badge>}
                  </label>
                ))}
              </div>
              {pf.extensions.length > 0 && (
                <>
                  <h3 className="mb-2 text-sm font-semibold">Extensions</h3>
                  <div className="mb-4 flex flex-wrap gap-1.5">
                    {pf.extensions.map((x) => (
                      <Badge key={x.name} tone={x.allowed ? "ok" : "warn"}>
                        {x.name} {x.version}
                        {!x.allowed && " · not on the allow-list"}
                      </Badge>
                    ))}
                  </div>
                </>
              )}
              {refs.length > 0 && (
                <>
                  <h3 className="mb-1 text-sm font-semibold">References to Supabase roles</h3>
                  <p className="mb-2 text-xs text-muted">
                    Kept as written, but PGDock has no <code>anon</code>, <code>authenticated</code> or <code>service_role</code> logins: your app
                    connects as the project role, which owns the tables. Review these after the import.
                  </p>
                  <Table head={["Kind", "Table", "Name", "Roles"]}>
                    {refs.map((r, i) => (
                      <tr key={i}>
                        <td className="px-3 py-1.5">{r.kind}</td>
                        <td className="px-3 py-1.5 font-mono text-xs">
                          {r.schema}.{r.table}
                        </td>
                        <td className="px-3 py-1.5">{r.name}</td>
                        <td className="px-3 py-1.5 font-mono text-xs">{r.roles.join(", ")}</td>
                      </tr>
                    ))}
                  </Table>
                </>
              )}
            </Card>
            <Card title="3. New project">
              <form className="flex flex-col gap-3" onSubmit={start}>
                <Field label="Project name">
                  {(id) => <Input id={id} required maxLength={64} value={name} onChange={(e) => setName(e.target.value)} />}
                </Field>
                <p className="text-xs text-muted">
                  Stop writes to the source first (or accept that later writes are not copied): this is a one-time copy.
                </p>
                <div>
                  <Button type="submit" variant="primary" busy={busy === "import"} disabled={!name.trim() || schemas.length === 0}>
                    Import {schemas.length} schema{schemas.length === 1 ? "" : "s"}
                  </Button>
                </div>
              </form>
            </Card>
          </>
        )}
      </div>
    </>
  );
}
