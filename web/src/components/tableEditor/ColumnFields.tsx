import { Check, ChevronsUpDown, Link2, Settings2, X } from "lucide-react";
import { useState } from "react";
import type { DbSchema } from "../../api/client";
import { defaultSuggestions } from "../../lib/tableEditor/cells";
import { FK_ACTIONS, TYPE_GROUPS, type ColumnDraft, type FkAction } from "../../lib/tableEditor/columnForm";
import {
  Button,
  Checkbox,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
  Input,
  Popover,
  PopoverContent,
  PopoverTrigger,
  Select,
  Switch,
  cx,
} from "../ui";

const IDENTITY = "__identity__";

/** The type picker: grouped, searchable, and any other type typed in
 * (varchar(255), a domain…). */
export function TypePicker({
  value,
  onChange,
  enums,
  disabled,
  label = "Type",
}: {
  value: string;
  onChange: (t: string) => void;
  enums: string[];
  disabled?: boolean;
  label?: string;
}) {
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState("");
  const f = q.trim().toLowerCase();
  const groups = [
    ...TYPE_GROUPS,
    ...(enums.length ? [{ label: "Enumerated types", types: enums.map((e) => ({ name: e, hint: "enum" })) }] : []),
  ]
    .map((g) => ({ ...g, types: g.types.filter((t) => !f || t.name.includes(f) || t.hint.toLowerCase().includes(f)) }))
    .filter((g) => g.types.length > 0);
  const known = groups.some((g) => g.types.some((t) => t.name === f));
  const pick = (t: string) => {
    onChange(t);
    setOpen(false);
    setQ("");
  };
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild disabled={disabled}>
        <button
          type="button"
          aria-label={label}
          className="flex h-[30px] w-full items-center justify-between gap-2 rounded-md border border-line-strong bg-surface-2 px-2.5 text-left font-mono text-[12px] text-fg disabled:opacity-50"
          data-testid="type-picker"
        >
          <span className="truncate">{value || "Choose a type"}</span>
          <ChevronsUpDown className="h-3.5 w-3.5 shrink-0 text-muted" />
        </button>
      </PopoverTrigger>
      <PopoverContent className="w-72 p-0">
        <Input
          autoFocus
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="Search types…"
          className="rounded-none border-0 border-b border-line bg-transparent focus:ring-0"
          onKeyDown={(e) => {
            if (e.key === "Enter" && f) {
              e.preventDefault();
              pick(q.trim());
            }
          }}
          aria-label="Search types"
        />
        <div className="max-h-72 overflow-y-auto p-1">
          {f && !known && (
            <button type="button" onClick={() => pick(q.trim())} className="flex w-full rounded px-2 py-1.5 text-left text-[12px] hover:bg-surface-3">
              Use <code className="ml-1 font-mono">{q.trim()}</code>
            </button>
          )}
          {groups.map((g) => (
            <div key={g.label} className="py-1">
              <p className="px-2 py-1 text-[11px] tracking-wider text-muted uppercase">{g.label}</p>
              {g.types.map((t) => (
                <button
                  key={t.name}
                  type="button"
                  onClick={() => pick(t.name)}
                  className="flex w-full items-center gap-2 rounded px-2 py-1.5 text-left hover:bg-surface-3"
                  data-testid={`type-${t.name}`}
                >
                  <code className="w-24 shrink-0 font-mono text-[12px]">{t.name}</code>
                  <span className="truncate text-[11px] text-muted">{t.hint}</span>
                  {t.name === value && <Check className="ml-auto h-3.5 w-3.5 text-accent" />}
                </button>
              ))}
            </div>
          ))}
        </div>
      </PopoverContent>
    </Popover>
  );
}

const isInteger = (t: string) => /^(int2|int4|int8|smallint|integer|bigint)$/i.test(t.trim());

/** A default value: an SQL expression, with Studio's suggestions for the
 * type, and identity for integer keys. */
export function DefaultInput({ col, onChange, disabled }: { col: ColumnDraft; onChange: (patch: Partial<ColumnDraft>) => void; disabled?: boolean }) {
  const sugg = [...(isInteger(col.type) && !col.isArray ? [{ value: IDENTITY, label: "Automatically generate as identity" }] : []), ...defaultSuggestions(col.type)];
  return (
    <div className="flex">
      <Input
        aria-label="Default value"
        value={col.identity ? "" : col.defaultValue}
        placeholder={col.identity ? "Identity" : "NULL"}
        disabled={disabled || col.identity}
        onChange={(e) => onChange({ defaultValue: e.target.value })}
        className={cx("font-mono text-[12px]", sugg.length > 0 && "rounded-r-none")}
      />
      {sugg.length > 0 && !disabled && (
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button type="button" aria-label="Suggested defaults" className="rounded-r-md border border-l-0 border-line-strong bg-surface-2 px-1.5 text-muted hover:text-fg">
              <ChevronsUpDown className="h-3.5 w-3.5" />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuLabel>Suggested values</DropdownMenuLabel>
            {col.identity && <DropdownMenuItem onSelect={() => onChange({ identity: false })}>No identity</DropdownMenuItem>}
            {sugg.map((s) => (
              <DropdownMenuItem
                key={s.value}
                onSelect={() => (s.value === IDENTITY ? onChange({ identity: true, defaultValue: "" }) : onChange({ identity: false, defaultValue: s.value }))}
              >
                <code className="font-mono text-[12px]">{s.label}</code>
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      )}
    </div>
  );
}

/** A column's foreign key: the referenced schema, table and column, and
 * what happens on update and delete. */
export function ForeignKeyEditor({
  col,
  schema,
  tree,
  onChange,
}: {
  col: ColumnDraft;
  schema: string;
  tree: DbSchema | undefined;
  onChange: (ref: ColumnDraft["ref"]) => void;
}) {
  if (!col.ref) {
    return (
      <Button size="tiny" variant="ghost" className="self-start" icon={<Link2 className="h-3.5 w-3.5" />} onClick={() => onChange({ schema, table: "", column: "", onDelete: "NO ACTION", onUpdate: "NO ACTION" })} data-testid="add-foreign-key">
        Add foreign key relation
      </Button>
    );
  }
  const ref = col.ref;
  const refSchema = ref.schema || schema;
  const tables = tree?.schemas.find((s) => s.name === refSchema)?.tables.filter((t) => t.kind === "table" || t.kind === "partitioned_table") ?? [];
  const columns = tables.find((t) => t.name === ref.table)?.columns ?? [];
  const set = (patch: Partial<NonNullable<ColumnDraft["ref"]>>) => onChange({ ...ref, ...patch });
  return (
    <div className="flex flex-col gap-2 rounded-md border border-line bg-surface-2/50 p-3" data-testid="foreign-key-editor">
      <div className="flex items-center justify-between">
        <span className="text-[12px] text-fg-light">Foreign key relation</span>
        <button type="button" aria-label="Remove foreign key" className="rounded p-0.5 text-muted hover:text-danger" onClick={() => onChange(null)}>
          <X className="h-3.5 w-3.5" />
        </button>
      </div>
      <div className="grid grid-cols-3 gap-2">
        <Select aria-label="Referenced schema" value={refSchema} onChange={(e) => set({ schema: e.target.value, table: "", column: "" })}>
          {tree?.schemas.map((s) => (
            <option key={s.name}>{s.name}</option>
          ))}
        </Select>
        <Select aria-label="Referenced table" value={ref.table} onChange={(e) => set({ table: e.target.value, column: "" })}>
          <option value="">Table…</option>
          {tables.map((t) => (
            <option key={t.name}>{t.name}</option>
          ))}
        </Select>
        <Select aria-label="Referenced column" value={ref.column} onChange={(e) => set({ column: e.target.value })} disabled={!ref.table}>
          <option value="">Column…</option>
          {columns.map((c) => (
            <option key={c.name} value={c.name}>
              {c.name} ({c.type})
            </option>
          ))}
        </Select>
      </div>
      <div className="grid grid-cols-2 gap-2">
        <label className="flex flex-col gap-1 text-[11px] text-muted">
          On update
          <Select aria-label="On update" value={ref.onUpdate} onChange={(e) => set({ onUpdate: e.target.value as FkAction })}>
            {FK_ACTIONS.map((a) => (
              <option key={a}>{a}</option>
            ))}
          </Select>
        </label>
        <label className="flex flex-col gap-1 text-[11px] text-muted">
          On delete
          <Select aria-label="On delete" value={ref.onDelete} onChange={(e) => set({ onDelete: e.target.value as FkAction })}>
            {FK_ACTIONS.map((a) => (
              <option key={a}>{a}</option>
            ))}
          </Select>
        </label>
      </div>
    </div>
  );
}

/** A labelled switch with a hint: the column's constraints. */
export function ToggleRow({ label, hint, checked, onChange, disabled, testId }: { label: string; hint?: string; checked: boolean; onChange: (v: boolean) => void; disabled?: boolean; testId?: string }) {
  return (
    <label className={cx("flex items-start justify-between gap-4", disabled && "opacity-50")}>
      <span className="flex flex-col">
        <span className="text-[13px]">{label}</span>
        {hint && <span className="text-[11px] text-muted">{hint}</span>}
      </span>
      <Switch checked={checked} onCheckedChange={onChange} disabled={disabled} aria-label={label} data-testid={testId} />
    </label>
  );
}

/** The ⚙ beside a column in the table panel: nullability, uniqueness,
 * arrays, a check and a description. */
export function ColumnSettings({ col, onChange, existing }: { col: ColumnDraft; onChange: (patch: Partial<ColumnDraft>) => void; existing: boolean }) {
  return (
    <Popover>
      <PopoverTrigger asChild>
        <button type="button" aria-label={`Settings for ${col.name || "the column"}`} className="rounded p-1 text-muted hover:bg-surface-3 hover:text-fg" data-testid="column-settings">
          <Settings2 className="h-4 w-4" strokeWidth={1.6} />
        </button>
      </PopoverTrigger>
      <PopoverContent align="end" className="flex w-72 flex-col gap-3">
        <label className="flex flex-col gap-1 text-[12px] text-fg-light">
          Description
          <Input value={col.comment} onChange={(e) => onChange({ comment: e.target.value })} placeholder="Optional" />
        </label>
        <ToggleRow label="Is nullable" hint="Can hold NULL" checked={col.nullable && !col.primaryKey} disabled={col.primaryKey} onChange={(v) => onChange({ nullable: v })} />
        <ToggleRow label="Is unique" hint="No two rows hold the same value" checked={col.unique || col.primaryKey} disabled={col.primaryKey} onChange={(v) => onChange({ unique: v })} />
        <ToggleRow label="Define as array" hint="Holds a list of the type" checked={col.isArray} disabled={existing && col.identity} onChange={(v) => onChange({ isArray: v, identity: v ? false : col.identity })} />
        <label className="flex flex-col gap-1 text-[12px] text-fg-light">
          Check constraint
          <Input value={col.check} onChange={(e) => onChange({ check: e.target.value })} placeholder="length(name) < 50" className="font-mono text-[12px]" />
        </label>
      </PopoverContent>
    </Popover>
  );
}

export function PrimaryKeyBox({ col, onChange, disabled }: { col: ColumnDraft; onChange: (patch: Partial<ColumnDraft>) => void; disabled?: boolean }) {
  return (
    <Checkbox
      aria-label={`${col.name || "Column"} is the primary key`}
      checked={col.primaryKey}
      disabled={disabled}
      onCheckedChange={(v) => onChange({ primaryKey: v === true, nullable: v === true ? false : col.nullable })}
    />
  );
}
