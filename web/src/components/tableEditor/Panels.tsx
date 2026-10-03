import { Plus, X } from "lucide-react";
import { useState } from "react";
import type { DbSchema, SchemaChange, TableInfo } from "../../api/client";
import { editorKind, prettyJSON, readOnlyReason, type Val } from "../../lib/tableEditor/cells";
import {
  addColumnChange,
  asChange,
  blankColumn,
  createTableChange,
  draftFromColumn,
  draftFromTable,
  editColumnChanges,
  editTableChanges,
  newTableColumns,
  validateTable,
  type ColumnDraft,
  type TableDraft,
} from "../../lib/tableEditor/columnForm";
import { Alert, Button, Input, SectionLabel, Select, SidePanel, cx } from "../ui";
import { ColumnSettings, DefaultInput, ForeignKeyEditor, PrimaryKeyBox, ToggleRow, TypePicker } from "./ColumnFields";

const enumsIn = (tree: DbSchema | undefined, schema: string) => [
  ...(tree?.schemas.find((s) => s.name === schema)?.enums.map((e) => e.name) ?? []),
  ...(tree?.schemas.filter((s) => s.name !== schema).flatMap((s) => s.enums.map((e) => `${s.name}.${e.name}`)) ?? []),
];

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <label className="grid grid-cols-1 gap-1.5 md:grid-cols-[10rem_1fr] md:gap-4">
      <span className="pt-1.5 text-[13px] text-fg-light">{label}</span>
      <span className="flex flex-col gap-1">
        {children}
        {hint && <span className="text-[11px] text-muted">{hint}</span>}
      </span>
    </label>
  );
}

/** Create a table, or edit one: its name, description and columns, as in
 * Studio's side panel. Saving opens the review. */
export function TablePanel({
  schema,
  tree,
  info,
  onClose,
  onReview,
}: {
  schema: string;
  tree: DbSchema | undefined;
  /** The table being edited; none to create one. */
  info?: TableInfo;
  onClose: () => void;
  onReview: (change: SchemaChange, after: { table: string }) => void;
}) {
  const [t, setT] = useState<TableDraft>(() => (info ? draftFromTable(info) : { schema, name: "", comment: "", columns: newTableColumns() }));
  const [errors, setErrors] = useState<string[]>([]);
  const setCol = (key: string, patch: Partial<ColumnDraft>) => setT((d) => ({ ...d, columns: d.columns.map((c) => (c.key === key ? { ...c, ...patch } : c)) }));
  const enums = enumsIn(tree, t.schema);
  const save = () => {
    const errs = validateTable(t, !info);
    setErrors(errs);
    if (errs.length) return;
    if (!info) {
      onReview(createTableChange(t), { table: t.name.trim() });
      return;
    }
    const change = asChange(info, editTableChanges(info, t));
    if (!change) {
      setErrors(["Nothing has changed."]);
      return;
    }
    onReview(change, { table: t.name.trim() });
  };
  return (
    <SidePanel
      open
      onOpenChange={(o) => !o && onClose()}
      size="xlarge"
      testId="table-panel"
      title={
        info ? (
          <>
            Update table <code className="font-mono">{info.name}</code>
          </>
        ) : (
          <>
            Create a new table under <code className="font-mono">{t.schema}</code>
          </>
        )
      }
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" onClick={save} data-testid="table-panel-save">
            Save
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-6">
        <div className="flex flex-col gap-4">
          <Field label="Name">
            <Input value={t.name} onChange={(e) => setT({ ...t, name: e.target.value })} className="font-mono" autoFocus={!info} aria-label="Name" data-testid="table-name" />
          </Field>
          <Field label="Description" hint="Optional">
            <Input value={t.comment} onChange={(e) => setT({ ...t, comment: e.target.value })} aria-label="Description" />
          </Field>
        </div>
        <div className="border-t border-line" />
        <section className="flex flex-col gap-2">
          <div className="flex items-center justify-between">
            <SectionLabel>Columns</SectionLabel>
          </div>
          <div className="grid grid-cols-[1fr_11rem_1fr_3rem_4.5rem] items-center gap-2 px-1 text-[11px] text-muted">
            <span>Name</span>
            <span>Type</span>
            <span>Default value</span>
            <span className="text-center">Primary</span>
            <span />
          </div>
          {t.columns.map((c) => (
            <div key={c.key} className="flex flex-col gap-2 rounded-md border border-line bg-surface-2/40 p-2" data-testid="column-row">
              <div className="grid grid-cols-[1fr_11rem_1fr_3rem_4.5rem] items-center gap-2">
                <Input value={c.name} onChange={(e) => setCol(c.key, { name: e.target.value })} placeholder="column_name" className="font-mono text-[12px]" aria-label="Column name" />
                <TypePicker value={c.type} onChange={(type) => setCol(c.key, { type })} enums={enums} label="Column type" />
                <DefaultInput col={c} onChange={(p) => setCol(c.key, p)} />
                <span className="flex justify-center">
                  <PrimaryKeyBox col={c} onChange={(p) => setCol(c.key, p)} disabled={!!info} />
                </span>
                <span className="flex items-center justify-end gap-0.5">
                  <ColumnSettings col={c} onChange={(p) => setCol(c.key, p)} existing={!!c.original} />
                  <button
                    type="button"
                    aria-label={`Remove ${c.name || "the column"}`}
                    className="rounded p-1 text-muted hover:bg-surface-3 hover:text-danger"
                    onClick={() => setT((d) => ({ ...d, columns: d.columns.filter((x) => x.key !== c.key) }))}
                  >
                    <X className="h-4 w-4" />
                  </button>
                </span>
              </div>
              <ForeignKeyEditor col={c} schema={t.schema} tree={tree} onChange={(ref) => setCol(c.key, { ref })} />
            </div>
          ))}
          <Button className="self-start" icon={<Plus className="h-3.5 w-3.5" />} onClick={() => setT((d) => ({ ...d, columns: [...d.columns, blankColumn()] }))} data-testid="table-panel-add-column">
            Add column
          </Button>
        </section>
        {errors.length > 0 && (
          <Alert>
            {errors.map((e) => (
              <span key={e} className="block">
                {e}
              </span>
            ))}
          </Alert>
        )}
      </div>
    </SidePanel>
  );
}

/** Add a column, or edit one, as Studio's column panel. */
export function ColumnPanel({
  info,
  tree,
  column,
  onClose,
  onReview,
}: {
  info: TableInfo;
  tree: DbSchema | undefined;
  /** The column being edited; none to add one. */
  column?: string;
  onClose: () => void;
  onReview: (change: SchemaChange) => void;
}) {
  const [c, setC] = useState<ColumnDraft>(() => (column ? draftFromColumn(info, column) : blankColumn()));
  const [err, setErr] = useState<string | null>(null);
  const set = (patch: Partial<ColumnDraft>) => setC((x) => ({ ...x, ...patch }));
  const save = () => {
    if (!c.name.trim()) return setErr("Give the column a name.");
    if (c.ref && (!c.ref.table || !c.ref.column)) return setErr("Finish the foreign key, or remove it.");
    if (!column) return onReview(addColumnChange(info, c));
    const change = asChange(info, editColumnChanges(info, c));
    if (!change) return setErr("Nothing has changed.");
    onReview(change);
  };
  return (
    <SidePanel
      open
      onOpenChange={(o) => !o && onClose()}
      size="large"
      testId="column-panel"
      title={
        column ? (
          <>
            Update column <code className="font-mono">{column}</code>
          </>
        ) : (
          <>
            Add new column to <code className="font-mono">{info.name}</code>
          </>
        )
      }
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" onClick={save} data-testid="column-panel-save">
            Save
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-6">
        <div className="flex flex-col gap-4">
          <Field label="Name" hint="Recommended to use lowercase and underscores">
            <Input value={c.name} onChange={(e) => set({ name: e.target.value })} className="font-mono" aria-label="Name" autoFocus data-testid="column-name" />
          </Field>
          <Field label="Description" hint="Optional">
            <Input value={c.comment} onChange={(e) => set({ comment: e.target.value })} aria-label="Description" />
          </Field>
        </div>
        <div className="border-t border-line" />
        <div className="flex flex-col gap-4">
          <SectionLabel>Data type</SectionLabel>
          <Field label="Type">
            <TypePicker value={c.type} onChange={(type) => set({ type })} enums={enumsIn(tree, info.schema)} />
          </Field>
          <Field label="">
            <ToggleRow label="Define as array" hint="Allow the column to hold a list of the type" checked={c.isArray} onChange={(v) => set({ isArray: v, identity: v ? false : c.identity })} />
          </Field>
          <Field label="Default value" hint="Used when a row is inserted without a value for this column">
            <DefaultInput col={c} onChange={set} disabled={!!column && c.identity} />
          </Field>
        </div>
        <div className="border-t border-line" />
        <div className="flex flex-col gap-3">
          <SectionLabel>Foreign keys</SectionLabel>
          <ForeignKeyEditor col={c} schema={info.schema} tree={tree} onChange={(ref) => set({ ref })} />
        </div>
        <div className="border-t border-line" />
        <div className="flex flex-col gap-4">
          <SectionLabel>Constraints</SectionLabel>
          <ToggleRow label="Allow nullable" hint="Allow the column to assume a NULL value if no value is provided" checked={c.nullable && !c.primaryKey} disabled={c.primaryKey} onChange={(v) => set({ nullable: v })} testId="column-nullable" />
          <ToggleRow label="Is unique" hint="Enforce values in the column to be unique across rows" checked={c.unique || c.primaryKey} disabled={c.primaryKey} onChange={(v) => set({ unique: v })} />
          <Field label="Check constraint" hint="An expression each row must satisfy">
            <Input value={c.check} onChange={(e) => set({ check: e.target.value })} placeholder="length(name) < 50" className="font-mono text-[12px]" aria-label="Check constraint" />
          </Field>
        </div>
        {err && <Alert>{err}</Alert>}
      </div>
    </SidePanel>
  );
}

/** One input per column for inserting or editing a row; JSON gets a
 * larger editor, generated columns are shown but left alone. */
export function RowPanel({
  info,
  row,
  onClose,
  onSave,
}: {
  info: TableInfo;
  /** The row being edited, by column; none to insert one. */
  row?: Record<string, Val>;
  onClose: () => void;
  onSave: (values: Record<string, Val>) => Promise<string | null>;
}) {
  // Inserts leave untouched columns out, so they get their defaults.
  const [values, setValues] = useState<Record<string, Val>>(() => {
    if (!row) return {};
    const out = { ...row };
    for (const c of info.columns) if (editorKind(c) === "json" && out[c.name] != null) out[c.name] = prettyJSON(out[c.name]);
    return out;
  });
  const [touched, setTouched] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const set = (name: string, v: Val) => {
    setValues((x) => ({ ...x, [name]: v }));
    setTouched((t) => new Set(t).add(name));
  };
  const save = async () => {
    for (const c of info.columns) {
      if (!touched.has(c.name)) continue;
      const v = values[c.name];
      if (v != null && (c.base_type === "json" || c.base_type === "jsonb")) {
        try {
          JSON.parse(v);
        } catch {
          return setErr(`${c.name} isn't valid JSON.`);
        }
      }
    }
    const out: Record<string, Val> = {};
    for (const name of touched) out[name] = values[name] ?? null;
    if (row && Object.keys(out).length === 0) return onClose();
    setBusy(true);
    setErr(null);
    const e = await onSave(out);
    setBusy(false);
    if (e) setErr(e);
  };
  return (
    <SidePanel
      open
      onOpenChange={(o) => !o && onClose()}
      size="large"
      testId="row-panel"
      title={row ? <>Update row from <code className="font-mono">{info.name}</code></> : <>Add new row to <code className="font-mono">{info.name}</code></>}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" busy={busy} onClick={() => void save()} data-testid="row-panel-save">
            Save
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-5">
        {info.columns.map((c) => {
          const ro = readOnlyReason(c, info);
          const kind = editorKind(c);
          const v = values[c.name];
          const isNull = row ? v == null : touched.has(c.name) && v == null;
          const placeholder = !row && !touched.has(c.name) ? (c.default ? `Default: ${c.default}` : c.identity ? "Automatically generated" : "NULL") : isNull ? "NULL" : "";
          const input =
            kind === "boolean" || kind === "enum" ? (
              <Select aria-label={c.name} value={v ?? ""} onChange={(e) => set(c.name, e.target.value === "" ? null : e.target.value)} disabled={!!ro} data-testid={`row-field-${c.name}`}>
                <option value="">{!row && !touched.has(c.name) && c.default ? `Default: ${c.default}` : "NULL"}</option>
                {(kind === "boolean" ? ["t", "f"] : c.enum_values!).map((x) => (
                  <option key={x} value={x}>
                    {kind === "boolean" ? (x === "t" ? "true" : "false") : x}
                  </option>
                ))}
              </Select>
            ) : kind === "json" ? (
              <textarea
                aria-label={c.name}
                rows={6}
                value={v ?? ""}
                placeholder={placeholder}
                disabled={!!ro}
                onChange={(e) => set(c.name, e.target.value === "" ? null : e.target.value)}
                className="w-full rounded-md border border-line-strong bg-surface-2 p-2 font-mono text-[12px] focus:border-accent focus:outline-none"
                data-testid={`row-field-${c.name}`}
              />
            ) : (
              <Input
                aria-label={c.name}
                value={v ?? ""}
                placeholder={placeholder}
                disabled={!!ro}
                onChange={(e) => set(c.name, e.target.value)}
                className={cx(kind !== "text" && "font-mono text-[12px]")}
                data-testid={`row-field-${c.name}`}
              />
            );
          return (
            <div key={c.name} className="grid grid-cols-1 gap-1.5 md:grid-cols-[12rem_1fr] md:gap-4">
              <div className="flex flex-col pt-1.5">
                <span className="font-mono text-[13px]">{c.name}</span>
                <span className="font-mono text-[11px] text-muted">{c.type}</span>
              </div>
              <div className="flex flex-col gap-1">
                {input}
                <div className="flex items-center gap-2 text-[11px] text-muted">
                  {ro ? <span>{ro}</span> : c.comment ? <span>{c.comment}</span> : null}
                  {!ro && c.nullable && kind !== "boolean" && kind !== "enum" && (
                    <button type="button" className="ml-auto hover:text-fg" onClick={() => set(c.name, null)}>
                      Set to NULL
                    </button>
                  )}
                </div>
              </div>
            </div>
          );
        })}
        {err && <Alert>{err}</Alert>}
      </div>
    </SidePanel>
  );
}
