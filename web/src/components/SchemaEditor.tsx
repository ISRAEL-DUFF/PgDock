import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type ReactNode } from "react";
import { api, errorMessage, type MigrationFormat, type SchemaChange, type SchemaPlan, type TableInfo } from "../api/client";
import { Alert, Badge, Button, CodeBlock, CopyButton, Field, Input, Modal, Select, Spinner, Table, cx } from "./ui";

const riskTone = { info: "muted", warning: "warn", danger: "danger" } as const;

/** Preview a schema change: its DDL, risk notes, and Run / Copy SQL / Save
 * as migration (V2 §4.3). */
export function SchemaPreview({ projectId, change, onClose, onApplied }: { projectId: string; change: SchemaChange; onClose: () => void; onApplied: () => void }) {
  const plan = useQuery({ queryKey: ["schema-plan", projectId, change], queryFn: () => api.previewSchema(projectId, change), retry: false });
  const prefs = useQuery({ queryKey: ["editor-prefs", projectId], queryFn: () => api.editorPreferences(projectId) });
  const [format, setFormat] = useState<MigrationFormat | null>(null);
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<ReactNode>(null);
  const qc = useQueryClient();
  const fmt = format ?? prefs.data?.migration_format ?? "sql";
  const sql = (p: SchemaPlan) => p.statements.map((s) => s.sql.replace(/;?$/, ";")).join("\n");

  const run = async (p: SchemaPlan) => {
    setBusy(true);
    setErr(null);
    try {
      await api.applySchema(projectId, change, p.hash, p.confirm ? confirm : undefined);
      await qc.invalidateQueries({ queryKey: ["schema", projectId] });
      await qc.invalidateQueries({ queryKey: ["table-info", projectId] });
      await qc.invalidateQueries({ queryKey: ["rows", projectId] });
      onApplied();
    } catch (e) {
      const body = (e as { body?: { sql_error?: { message: string; hint?: string }; statement?: string } }).body;
      setErr(
        body?.sql_error ? (
          <>
            {body.sql_error.message}
            {body.sql_error.hint && <span className="block text-xs">{body.sql_error.hint}</span>}
            {body.statement && <code className="mt-1 block font-mono text-xs">{body.statement}</code>}
          </>
        ) : (
          errorMessage(e)
        ),
      );
    } finally {
      setBusy(false);
    }
  };
  const download = async () => {
    setErr(null);
    try {
      const m = await api.schemaMigration(projectId, change, fmt);
      await qc.invalidateQueries({ queryKey: ["editor-prefs", projectId] });
      const url = URL.createObjectURL(new Blob([m.content], { type: "text/plain" }));
      const a = document.createElement("a");
      a.href = url;
      a.download = m.filename;
      a.click();
      URL.revokeObjectURL(url);
    } catch (e) {
      setErr(errorMessage(e));
    }
  };

  return (
    <Modal title="Preview schema change" open onClose={onClose}>
      {plan.isPending ? (
        <Spinner />
      ) : plan.isError ? (
        <div className="flex flex-col gap-3">
          <Alert>{errorMessage(plan.error)}</Alert>
          <Button className="self-end" onClick={onClose}>
            Back
          </Button>
        </div>
      ) : (
        <div className="flex flex-col gap-3" data-testid="schema-preview">
          <div>
            <h3 className="mb-1 text-sm font-medium">SQL</h3>
            <CodeBlock code={sql(plan.data)} />
          </div>
          {plan.data.risks.length > 0 && (
            <ul className="flex flex-col gap-1.5" data-testid="schema-risks">
              {plan.data.risks.map((r, i) => (
                <li
                  key={i}
                  className={cx(
                    "rounded-md border px-3 py-2 text-sm",
                    r.level === "danger" ? "border-danger/40 bg-danger/10" : r.level === "warning" ? "border-warn/40 bg-warn/10" : "border-line bg-surface-2",
                  )}
                  data-testid={`risk-${r.level}`}
                >
                  <Badge tone={riskTone[r.level]}>{r.level}</Badge> {r.message}
                </li>
              ))}
            </ul>
          )}
          {plan.data.confirm && (
            <Field label={`Type ${plan.data.confirm} to confirm`}>
              {(id) => <Input id={id} value={confirm} onChange={(e) => setConfirm(e.target.value)} className="font-mono" data-testid="schema-confirm" />}
            </Field>
          )}
          {err && <Alert>{err}</Alert>}
          <div className="flex flex-wrap items-center justify-end gap-2">
            <CopyButton value={sql(plan.data)} label="Copy SQL" />
            <Select aria-label="Migration format" value={fmt} onChange={(e) => setFormat(e.target.value as MigrationFormat)} className="text-xs" data-testid="migration-format">
              <option value="sql">Plain SQL</option>
              <option value="goose">goose</option>
              <option value="dbmate">dbmate</option>
            </Select>
            <Button onClick={() => void download()} data-testid="save-migration">
              Save as migration
            </Button>
            <Button variant="primary" busy={busy} disabled={!!plan.data.confirm && confirm !== plan.data.confirm} onClick={() => void run(plan.data)} data-testid="schema-run">
              Run
            </Button>
          </div>
        </div>
      )}
    </Modal>
  );
}

const ACTIONS = ["NO ACTION", "RESTRICT", "CASCADE", "SET NULL", "SET DEFAULT"] as const;
const COMMON_TYPES = ["text", "integer", "bigint", "numeric", "boolean", "uuid", "jsonb", "timestamptz", "date", "varchar(255)", "text[]"];

type Form = { title: string; fields: ReactNode; build: () => SchemaChange | null };

/** A small form that ends in a preview. */
function ChangeForm({ form, onPreview, onClose }: { form: Form; onPreview: (c: SchemaChange) => void; onClose: () => void }) {
  return (
    <Modal title={form.title} open onClose={onClose}>
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          const c = form.build();
          if (c) onPreview(c);
        }}
      >
        {form.fields}
        <div className="flex justify-end gap-2">
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" data-testid="schema-form-preview">
            Preview
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function TypeInput({ value, onChange, id }: { value: string; onChange: (v: string) => void; id: string }) {
  return (
    <>
      <Input id={id} list="pg-types" value={value} onChange={(e) => onChange(e.target.value)} className="font-mono" required />
      <datalist id="pg-types">
        {COMMON_TYPES.map((t) => (
          <option key={t} value={t} />
        ))}
      </datalist>
    </>
  );
}

/** Add column / alter column / rename column. */
function useColumnForm(t: TableInfo) {
  const [kind, setKind] = useState<"add_column" | "alter_column" | "rename_column" | "drop_column" | null>(null);
  const [col, setCol] = useState("");
  const [name, setName] = useState("");
  const [type, setType] = useState("text");
  const [def, setDef] = useState("");
  const [nullable, setNullable] = useState(true);
  const [using, setUsing] = useState("");
  const open = (k: NonNullable<typeof kind>, column?: string) => {
    const c = t.columns.find((x) => x.name === column);
    setKind(k);
    setCol(column ?? "");
    setName(k === "rename_column" ? (column ?? "") : "");
    setType(c?.type ?? "text");
    setDef(c?.default ?? "");
    setNullable(c?.nullable ?? true);
    setUsing("");
  };
  const base = { schema: t.schema, table: t.name };
  const form: Form | null = !kind
    ? null
    : kind === "add_column"
      ? {
          title: `Add a column to ${t.name}`,
          fields: (
            <>
              <Field label="Name">{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} required className="font-mono" autoFocus />}</Field>
              <Field label="Type">{(id) => <TypeInput id={id} value={type} onChange={setType} />}</Field>
              <Field label="Default" hint="An SQL expression, e.g. now(), 0, or 'draft'. Empty for none.">
                {(id) => <Input id={id} value={def} onChange={(e) => setDef(e.target.value)} className="font-mono" />}
              </Field>
              <label className="flex items-center gap-2 text-sm">
                <input type="checkbox" checked={!nullable} onChange={(e) => setNullable(!e.target.checked)} /> NOT NULL
              </label>
            </>
          ),
          build: () => ({ ...base, kind, column: { name, type, nullable, default: def || undefined } }),
        }
      : kind === "alter_column"
        ? {
            title: `Change ${t.name}.${col}`,
            fields: (
              <>
                <Field label="Type">{(id) => <TypeInput id={id} value={type} onChange={setType} />}</Field>
                <Field label="USING" hint="Optional: how to convert existing values, e.g. id::bigint.">
                  {(id) => <Input id={id} value={using} onChange={(e) => setUsing(e.target.value)} className="font-mono" />}
                </Field>
                <Field label="Default" hint="Empty to drop the default.">
                  {(id) => <Input id={id} value={def} onChange={(e) => setDef(e.target.value)} className="font-mono" />}
                </Field>
                <label className="flex items-center gap-2 text-sm">
                  <input type="checkbox" checked={!nullable} onChange={(e) => setNullable(!e.target.checked)} /> NOT NULL
                </label>
              </>
            ),
            build: () => {
              const c = t.columns.find((x) => x.name === col)!;
              const ch: SchemaChange = { ...base, kind, column_name: col };
              if (type.trim() !== c.type) ch.type = type.trim();
              if (using.trim()) ch.using = using.trim();
              if ((def || null) !== (c.default ?? null)) {
                if (def) ch.default = def;
                else ch.drop_default = true;
              }
              if (nullable !== c.nullable) ch.nullable = nullable;
              return ch;
            },
          }
        : kind === "rename_column"
          ? {
              title: `Rename ${t.name}.${col}`,
              fields: <Field label="New name">{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} required className="font-mono" autoFocus />}</Field>,
              build: () => ({ ...base, kind, column_name: col, new_name: name }),
            }
          : null;
  return { open, form, kind, col, close: () => setKind(null) };
}

/** Columns, constraints, indexes and the table itself, each with its
 * changes (V2 §4.3). */
export function StructureEditor({ projectId, info, canEdit, onRenamed, onDropped }: { projectId: string; info: TableInfo; canEdit: boolean; onRenamed: (name: string) => void; onDropped: () => void }) {
  const [preview, setPreview] = useState<{ change: SchemaChange; after?: () => void } | null>(null);
  const [form, setForm] = useState<Form | null>(null);
  const cf = useColumnForm(info);
  const t = info;
  const base = { schema: t.schema, table: t.name };
  const show = (change: SchemaChange, after?: () => void) => {
    setForm(null);
    cf.close();
    setPreview({ change, after });
  };

  const constraintForm = () => {
    let kind = "add_unique";
    let cols = "";
    let expr = "";
    let refTable = "";
    let refCols = "";
    let onDelete = "NO ACTION";
    let notValid = false;
    const F = () => {
      const [k, setK] = useState(kind);
      const [c, setC] = useState(cols);
      const [x, setX] = useState(expr);
      const [rt, setRt] = useState(refTable);
      const [rc, setRc] = useState(refCols);
      const [od, setOd] = useState(onDelete);
      const [nv, setNv] = useState(notValid);
      useEffect(() => {
        kind = k;
        cols = c;
        expr = x;
        refTable = rt;
        refCols = rc;
        onDelete = od;
        notValid = nv;
      });
      return (
        <>
          <Field label="Kind">
            {(id) => (
              <Select id={id} value={k} onChange={(e) => setK(e.target.value)}>
                <option value="add_unique">Unique</option>
                <option value="add_check">Check</option>
                <option value="add_foreign_key">Foreign key</option>
              </Select>
            )}
          </Field>
          {k !== "add_check" && <Field label="Columns" hint="Comma-separated.">{(id) => <Input id={id} value={c} onChange={(e) => setC(e.target.value)} className="font-mono" required />}</Field>}
          {k === "add_check" && <Field label="Check expression">{(id) => <Input id={id} value={x} onChange={(e) => setX(e.target.value)} className="font-mono" placeholder="price >= 0" required />}</Field>}
          {k === "add_foreign_key" && (
            <>
              <Field label="References table">{(id) => <Input id={id} value={rt} onChange={(e) => setRt(e.target.value)} className="font-mono" required />}</Field>
              <Field label="Referenced columns">{(id) => <Input id={id} value={rc} onChange={(e) => setRc(e.target.value)} className="font-mono" required />}</Field>
              <Field label="On delete">
                {(id) => (
                  <Select id={id} value={od} onChange={(e) => setOd(e.target.value)}>
                    {ACTIONS.map((a) => (
                      <option key={a}>{a}</option>
                    ))}
                  </Select>
                )}
              </Field>
            </>
          )}
          {k !== "add_unique" && (
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={nv} onChange={(e) => setNv(e.target.checked)} /> NOT VALID (don't check existing rows now)
            </label>
          )}
        </>
      );
    };
    const split = (s: string) => s.split(",").map((x) => x.trim()).filter(Boolean);
    setForm({
      title: `Add a constraint to ${t.name}`,
      fields: <F />,
      build: () =>
        kind === "add_check"
          ? { ...base, kind: "add_check", expression: expr, not_valid: notValid }
          : kind === "add_unique"
            ? { ...base, kind: "add_unique", key_columns: split(cols) }
            : { ...base, kind: "add_foreign_key", key_columns: split(cols), ref_table: refTable, ref_columns: split(refCols), on_delete: onDelete as SchemaChange["on_delete"], not_valid: notValid },
    });
  };

  const indexForm = () => {
    let cols = "";
    let method = "btree";
    let unique = false;
    let where = "";
    const F = () => {
      const [c, setC] = useState(cols);
      const [m, setM] = useState(method);
      const [u, setU] = useState(unique);
      const [w, setW] = useState(where);
      useEffect(() => {
        cols = c;
        method = m;
        unique = u;
        where = w;
      });
      return (
        <>
          <Field label="Columns" hint="Comma-separated, in order.">{(id) => <Input id={id} value={c} onChange={(e) => setC(e.target.value)} className="font-mono" required autoFocus />}</Field>
          <Field label="Method">
            {(id) => (
              <Select id={id} value={m} onChange={(e) => setM(e.target.value)}>
                {["btree", "gin", "gist", "brin", "hash"].map((x) => (
                  <option key={x}>{x}</option>
                ))}
              </Select>
            )}
          </Field>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={u} onChange={(e) => setU(e.target.checked)} /> Unique
          </label>
          <Field label="Only where (partial index)" hint="Optional, e.g. deleted_at IS NULL.">
            {(id) => <Input id={id} value={w} onChange={(e) => setW(e.target.value)} className="font-mono" />}
          </Field>
          <p className="text-xs text-muted">Built CONCURRENTLY, so writes carry on while it builds.</p>
        </>
      );
    };
    setForm({
      title: `Create an index on ${t.name}`,
      fields: <F />,
      build: () => ({ ...base, kind: "create_index", key_columns: cols.split(",").map((x) => x.trim()).filter(Boolean), method: method as SchemaChange["method"], unique, where: where || undefined }),
    });
  };

  const renameTable = () => {
    let name = t.name;
    const F = () => {
      const [n, setN] = useState(name);
      useEffect(() => {
        name = n;
      });
      return <Field label="New name">{(id) => <Input id={id} value={n} onChange={(e) => setN(e.target.value)} className="font-mono" required autoFocus />}</Field>;
    };
    setForm({ title: `Rename ${t.name}`, fields: <F />, build: () => ({ ...base, kind: "rename_table", new_name: name }) });
  };

  const isTable = t.kind === "table" || t.kind === "partitioned_table";
  const btn = (label: string, onClick: () => void, testId?: string, danger?: boolean) =>
    canEdit && (
      <Button className={cx("px-1.5 py-0.5 text-[11px]", danger && "text-danger")} onClick={onClick} data-testid={testId}>
        {label}
      </Button>
    );
  return (
    <div className="flex flex-col gap-4" data-testid="structure-editor">
      {!canEdit && <p className="text-xs text-muted">Read-only: you can preview and export migrations, but not run schema changes.</p>}
      <section>
        <div className="mb-1 flex items-center justify-between">
          <h3 className="text-sm font-medium">Columns</h3>
          {isTable && (
            <Button className="text-xs" onClick={() => cf.open("add_column")} data-testid="add-column">
              Add column
            </Button>
          )}
        </div>
        <Table head={["Column", "Type", "Nullable", "Default", ""]}>
          {t.columns.map((c) => (
            <tr key={c.name} data-testid={`column-${c.name}`}>
              <td className="px-3 py-1 font-mono text-xs">
                {c.name} {t.primary_key.includes(c.name) && <Badge tone="accent">pk</Badge>} {c.generated && <Badge>generated</Badge>} {c.identity && <Badge>identity</Badge>}
              </td>
              <td className="px-3 py-1 font-mono text-xs">{c.type}</td>
              <td className="px-3 py-1 text-xs">{c.nullable ? "yes" : "no"}</td>
              <td className="px-3 py-1 font-mono text-xs text-muted">{c.default ?? ""}</td>
              <td className="px-3 py-1 text-right whitespace-nowrap">
                {isTable && (
                  <>
                    {btn("Change", () => cf.open("alter_column", c.name), `alter-${c.name}`)} {btn("Rename", () => cf.open("rename_column", c.name))}{" "}
                    {btn("Drop", () => show({ ...base, kind: "drop_column", column_name: c.name }), `drop-${c.name}`, true)}
                  </>
                )}
              </td>
            </tr>
          ))}
        </Table>
      </section>
      {isTable && (
        <section>
          <div className="mb-1 flex items-center justify-between">
            <h3 className="text-sm font-medium">Constraints</h3>
            <Button className="text-xs" onClick={constraintForm}>
              Add constraint
            </Button>
          </div>
          {t.constraints.length === 0 ? (
            <p className="text-xs text-muted">None.</p>
          ) : (
            <Table head={["Name", "Kind", "Definition", ""]}>
              {t.constraints.map((c) => (
                <tr key={c.name}>
                  <td className="px-3 py-1 font-mono text-xs">{c.name}</td>
                  <td className="px-3 py-1 text-xs">{c.kind.replace("_", " ")}</td>
                  <td className="px-3 py-1 font-mono text-xs text-muted">{c.definition}</td>
                  <td className="px-3 py-1 text-right">{c.kind !== "primary_key" && btn("Drop", () => show({ ...base, kind: "drop_constraint", name: c.name }), undefined, true)}</td>
                </tr>
              ))}
            </Table>
          )}
        </section>
      )}
      <section>
        <div className="mb-1 flex items-center justify-between">
          <h3 className="text-sm font-medium">Indexes</h3>
          {isTable && (
            <Button className="text-xs" onClick={indexForm} data-testid="create-index">
              Create index
            </Button>
          )}
        </div>
        {t.indexes.length === 0 ? (
          <p className="text-xs text-muted">None.</p>
        ) : (
          <Table head={["Index", "Definition", ""]}>
            {t.indexes.map((i) => (
              <tr key={i.name}>
                <td className="px-3 py-1 font-mono text-xs">
                  {i.name} {i.primary ? <Badge tone="accent">primary</Badge> : i.unique ? <Badge>unique</Badge> : null}
                </td>
                <td className="px-3 py-1 font-mono text-xs text-muted">{i.definition}</td>
                <td className="px-3 py-1 text-right">{!i.primary && btn("Drop", () => show({ schema: t.schema, kind: "drop_index", name: i.name }), undefined, true)}</td>
              </tr>
            ))}
          </Table>
        )}
      </section>
      {isTable && (
        <section className="flex gap-2">
          <Button className="text-xs" onClick={renameTable}>
            Rename table
          </Button>
          <Button className="text-xs text-danger" onClick={() => show({ ...base, kind: "drop_table" }, onDropped)} data-testid="drop-table">
            Drop table
          </Button>
        </section>
      )}
      {(form ?? cf.form) && <ChangeForm form={(form ?? cf.form)!} onPreview={(c) => show(c, c.kind === "rename_table" ? () => onRenamed(c.new_name!) : undefined)} onClose={() => (setForm(null), cf.close())} />}
      {preview && (
        <SchemaPreview
          projectId={projectId}
          change={preview.change}
          onClose={() => setPreview(null)}
          onApplied={() => {
            const after = preview.after;
            setPreview(null);
            after?.();
          }}
        />
      )}
    </div>
  );
}

type NewCol = { name: string; type: string; nullable: boolean; default: string; primary_key: boolean };

/** New table, schema or enum, ending in a preview. */
export function NewObjectButtons({ projectId, schemas, onCreated }: { projectId: string; schemas: string[]; onCreated: (schema: string, table?: string) => void }) {
  const [open, setOpen] = useState<"table" | "schema" | "enum" | null>(null);
  const [schema, setSchema] = useState(schemas[0] ?? "public");
  const [name, setName] = useState("");
  const [cols, setCols] = useState<NewCol[]>([{ name: "id", type: "bigint generated always as identity", nullable: false, default: "", primary_key: true }]);
  const [values, setValues] = useState("");
  const [change, setChange] = useState<SchemaChange | null>(null);
  const reset = () => {
    setOpen(null);
    setName("");
    setValues("");
    setCols([{ name: "id", type: "bigint generated always as identity", nullable: false, default: "", primary_key: true }]);
  };
  const build = (): SchemaChange =>
    open === "schema"
      ? { kind: "create_schema", name }
      : open === "enum"
        ? { kind: "create_enum", schema, name, values: values.split(",").map((v) => v.trim()).filter(Boolean) }
        : {
            kind: "create_table",
            schema,
            table: name,
            columns: cols.filter((c) => c.name).map((c) => ({ name: c.name, type: c.type, nullable: c.nullable, default: c.default || undefined, primary_key: c.primary_key })),
          };
  const set = (i: number, patch: Partial<NewCol>) => setCols((all) => all.map((c, j) => (j === i ? { ...c, ...patch } : c)));
  return (
    <>
      <div className="flex flex-wrap gap-1">
        <Button className="px-1.5 py-0.5 text-[11px]" onClick={() => setOpen("table")} data-testid="new-table">
          New table
        </Button>
        <Button className="px-1.5 py-0.5 text-[11px]" onClick={() => setOpen("schema")}>
          New schema
        </Button>
        <Button className="px-1.5 py-0.5 text-[11px]" onClick={() => setOpen("enum")}>
          New enum
        </Button>
      </div>
      {open && !change && (
        <Modal title={open === "table" ? "New table" : open === "schema" ? "New schema" : "New enum type"} open onClose={reset}>
          <form
            className="flex flex-col gap-3"
            onSubmit={(e) => {
              e.preventDefault();
              setChange(build());
            }}
          >
            {open !== "schema" && (
              <Field label="Schema">
                {(id) => (
                  <Select id={id} value={schema} onChange={(e) => setSchema(e.target.value)}>
                    {schemas.map((s) => (
                      <option key={s}>{s}</option>
                    ))}
                  </Select>
                )}
              </Field>
            )}
            <Field label="Name">{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} className="font-mono" required autoFocus data-testid="new-name" />}</Field>
            {open === "enum" && <Field label="Values" hint="Comma-separated, in order.">{(id) => <Input id={id} value={values} onChange={(e) => setValues(e.target.value)} className="font-mono" required />}</Field>}
            {open === "table" && (
              <div className="flex flex-col gap-1">
                <span className="text-sm font-medium">Columns</span>
                {cols.map((c, i) => (
                  <div key={i} className="flex flex-wrap items-center gap-1" data-testid="new-column">
                    <Input aria-label="Column name" value={c.name} onChange={(e) => set(i, { name: e.target.value })} className="w-28 font-mono text-xs" placeholder="name" />
                    <Input aria-label="Column type" list="pg-types" value={c.type} onChange={(e) => set(i, { type: e.target.value })} className="w-44 font-mono text-xs" placeholder="type" />
                    <Input aria-label="Column default" value={c.default} onChange={(e) => set(i, { default: e.target.value })} className="w-24 font-mono text-xs" placeholder="default" />
                    <label className="flex items-center gap-1 text-[11px]">
                      <input type="checkbox" checked={!c.nullable} onChange={(e) => set(i, { nullable: !e.target.checked })} /> not null
                    </label>
                    <label className="flex items-center gap-1 text-[11px]">
                      <input type="checkbox" checked={c.primary_key} onChange={(e) => set(i, { primary_key: e.target.checked })} /> pk
                    </label>
                    <button type="button" aria-label="Remove column" className="text-muted hover:text-danger" onClick={() => setCols((all) => all.filter((_, j) => j !== i))}>
                      ×
                    </button>
                  </div>
                ))}
                <datalist id="pg-types">
                  {COMMON_TYPES.map((t) => (
                    <option key={t} value={t} />
                  ))}
                </datalist>
                <Button className="self-start text-xs" onClick={() => setCols((all) => [...all, { name: "", type: "text", nullable: true, default: "", primary_key: false }])} data-testid="new-table-add-column">
                  Add column
                </Button>
              </div>
            )}
            <div className="flex justify-end gap-2">
              <Button onClick={reset}>Cancel</Button>
              <Button type="submit" variant="primary" data-testid="schema-form-preview">
                Preview
              </Button>
            </div>
          </form>
        </Modal>
      )}
      {change && (
        <SchemaPreview
          projectId={projectId}
          change={change}
          onClose={() => setChange(null)}
          onApplied={() => {
            const c = change;
            setChange(null);
            reset();
            onCreated(c.schema ?? c.name ?? "public", c.kind === "create_table" ? c.table : undefined);
          }}
        />
      )}
    </>
  );
}
