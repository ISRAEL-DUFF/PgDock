import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { ApiRequestError, api, errorMessage, type EditColumn, type GridFilter, type GridOptions, type RowChange, type SaveRowsResult, type TableInfo } from "../api/client";
import { Alert, Badge, Button, Input, Modal, Select, Spinner, cx } from "./ui";

/** Values as the grid holds them: text, or null for NULL. */
type Val = string | null;
type Key = Record<string, string>;

const OPS: { op: GridFilter["op"]; label: string; noValue?: boolean }[] = [
  { op: "eq", label: "=" },
  { op: "neq", label: "≠" },
  { op: "contains", label: "contains" },
  { op: "gt", label: ">" },
  { op: "gte", label: "≥" },
  { op: "lt", label: "<" },
  { op: "lte", label: "≤" },
  { op: "in", label: "in list" },
  { op: "is_null", label: "is null", noValue: true },
  { op: "not_null", label: "is not null", noValue: true },
];

function keyOf(info: TableInfo, cols: string[], row: Val[]): Key {
  const k: Key = {};
  for (const c of info.primary_key) k[c] = row[cols.indexOf(c)] ?? "";
  return k;
}
const keyId = (k: Key) => JSON.stringify(k);

/** How a value shows in the grid: local time for timestamps, size for bytea. */
function display(col: EditColumn | undefined, v: Val): string {
  if (v == null) return "NULL";
  if (!col) return v;
  if (col.base_type === "bytea") return `${Math.max(0, (v.length - 2) / 2)} bytes`;
  if (col.base_type === "timestamptz") {
    const d = new Date(v.replace(" ", "T").replace(/([+-]\d\d)$/, "$1:00"));
    if (!Number.isNaN(d.getTime())) return d.toLocaleString();
  }
  return v;
}

function jsonValid(v: string) {
  try {
    JSON.parse(v);
    return true;
  } catch {
    return false;
  }
}

/** A type-aware input for one cell (V2 §4.2). */
function CellInput({ col, value, onDone, onCancel }: { col: EditColumn; value: Val; onDone: (v: Val) => void; onCancel: () => void }) {
  const [v, setV] = useState<string>(value ?? "");
  const [isNull, setNull] = useState(value == null);
  const isJSON = col.base_type === "json" || col.base_type === "jsonb";
  const bad = !isNull && isJSON && v !== "" && !jsonValid(v);
  const done = () => !bad && onDone(isNull ? null : v);
  const keys = (e: React.KeyboardEvent) => {
    if (e.key === "Escape") onCancel();
    if (e.key === "Enter" && !(isJSON && e.shiftKey === false && v.includes("\n"))) {
      e.preventDefault();
      done();
    }
  };
  let input;
  if (col.category === "B") {
    input = (
      <Select autoFocus value={isNull ? "" : v} onChange={(e) => (e.target.value === "" ? setNull(true) : (setNull(false), setV(e.target.value)))} onKeyDown={keys} className="text-xs">
        {col.nullable && <option value="">NULL</option>}
        <option value="t">true</option>
        <option value="f">false</option>
      </Select>
    );
  } else if (col.enum_values?.length) {
    input = (
      <Select autoFocus value={isNull ? "" : v} onChange={(e) => (e.target.value === "" ? setNull(true) : (setNull(false), setV(e.target.value)))} onKeyDown={keys} className="text-xs">
        {col.nullable && <option value="">NULL</option>}
        {col.enum_values.map((x) => (
          <option key={x} value={x}>
            {x}
          </option>
        ))}
      </Select>
    );
  } else if (isJSON) {
    input = (
      <textarea
        autoFocus
        rows={4}
        value={isNull ? "" : v}
        onChange={(e) => (setNull(false), setV(e.target.value))}
        onKeyDown={(e) => e.key === "Escape" && onCancel()}
        className={cx("w-64 rounded border bg-surface p-1 font-mono text-xs", bad ? "border-danger" : "border-line")}
        aria-invalid={bad}
      />
    );
  } else {
    const placeholder = col.category === "D" ? (col.base_type.includes("tz") ? "2026-10-03 12:00:00+00" : col.base_type === "date" ? "2026-10-03" : "") : isNull ? "NULL" : "";
    input = (
      <Input
        autoFocus
        value={isNull ? "" : v}
        placeholder={placeholder}
        inputMode={col.category === "N" ? "decimal" : undefined}
        onChange={(e) => (setNull(false), setV(e.target.value))}
        onKeyDown={keys}
        className="min-w-40 text-xs"
        data-testid={`cell-input-${col.name}`}
      />
    );
  }
  return (
    <div className="flex flex-col gap-1 rounded-md border border-accent bg-surface p-1.5 shadow-lg" onClick={(e) => e.stopPropagation()}>
      {input}
      {bad && <span className="text-[10px] text-danger">Not valid JSON</span>}
      <div className="flex items-center gap-1">
        {col.base_type === "uuid" && (
          <Button className="px-1.5 py-0.5 text-[10px]" onClick={() => (setNull(false), setV(crypto.randomUUID()))}>
            Generate
          </Button>
        )}
        {col.nullable && col.category !== "B" && !col.enum_values?.length && (
          <label className="flex items-center gap-1 text-[10px] text-muted">
            <input type="checkbox" checked={isNull} onChange={(e) => setNull(e.target.checked)} /> NULL
          </label>
        )}
        <span className="flex-1" />
        <Button className="px-1.5 py-0.5 text-[10px]" onClick={onCancel}>
          Cancel
        </Button>
        <Button variant="primary" className="px-1.5 py-0.5 text-[10px]" onClick={done} disabled={bad} data-testid="cell-ok">
          OK
        </Button>
      </div>
    </div>
  );
}

function FilterBar({ info, filters, onChange }: { info: TableInfo; filters: GridFilter[]; onChange: (f: GridFilter[]) => void }) {
  const [col, setCol] = useState(info.columns[0]?.name ?? "");
  const [op, setOp] = useState<GridFilter["op"]>("eq");
  const [value, setValue] = useState("");
  const noValue = OPS.find((o) => o.op === op)?.noValue;
  const add = () => {
    const f: GridFilter = { column: col, op };
    if (op === "in") f.values = value.split(",").map((x) => x.trim()).filter(Boolean);
    else if (!noValue) f.value = value;
    onChange([...filters, f]);
    setValue("");
  };
  return (
    <div className="mb-2 flex flex-wrap items-center gap-2" data-testid="filter-bar">
      {filters.map((f, i) => (
        <span key={i} className="flex items-center gap-1 rounded-full border border-line bg-surface-2 px-2 py-0.5 font-mono text-xs">
          {f.column} {OPS.find((o) => o.op === f.op)?.label} {f.values ? f.values.join(", ") : f.value}
          <button type="button" aria-label="Remove filter" className="text-muted hover:text-danger" onClick={() => onChange(filters.filter((_, j) => j !== i))}>
            ×
          </button>
        </span>
      ))}
      <form
        className="flex items-center gap-1"
        onSubmit={(e) => {
          e.preventDefault();
          add();
        }}
      >
        <Select aria-label="Filter column" value={col} onChange={(e) => setCol(e.target.value)} className="text-xs">
          {info.columns.map((c) => (
            <option key={c.name}>{c.name}</option>
          ))}
        </Select>
        <Select aria-label="Filter operator" value={op} onChange={(e) => setOp(e.target.value as GridFilter["op"])} className="text-xs">
          {OPS.map((o) => (
            <option key={o.op} value={o.op}>
              {o.label}
            </option>
          ))}
        </Select>
        {!noValue && <Input aria-label="Filter value" value={value} onChange={(e) => setValue(e.target.value)} placeholder={op === "in" ? "a, b, c" : "value"} className="w-32 text-xs" />}
        <Button type="submit" className="text-xs" data-testid="add-filter">
          Filter
        </Button>
      </form>
    </div>
  );
}

/** The referenced row of a foreign key, in a side panel. */
function ForeignRow({ projectId, refSchema, refTable, refColumn, value, onClose }: { projectId: string; refSchema: string; refTable: string; refColumn: string; value: string; onClose: () => void }) {
  const q = useQuery({
    queryKey: ["fk-row", projectId, refSchema, refTable, refColumn, value],
    queryFn: () => api.tableRows(projectId, refSchema, refTable, undefined, { filters: [{ column: refColumn, op: "eq", value }] }),
  });
  return (
    <Modal title={`${refSchema}.${refTable} where ${refColumn} = ${value}`} open onClose={onClose}>
      {q.isPending ? (
        <Spinner />
      ) : q.isError ? (
        <Alert>{errorMessage(q.error)}</Alert>
      ) : q.data.rows.length === 0 ? (
        <p className="text-sm text-muted">No such row.</p>
      ) : (
        <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-sm" data-testid="fk-panel">
          {q.data.columns.map((c, i) => (
            <div key={c.name} className="contents">
              <dt className="font-mono text-xs text-muted">{c.name}</dt>
              <dd className="font-mono text-xs break-all">{q.data.rows[0][i] ?? "NULL"}</dd>
            </div>
          ))}
        </dl>
      )}
    </Modal>
  );
}

type Edit = { key: Key; xmin: string; values: Record<string, Val> };
type Insert = { id: number; values: Record<string, Val> };

/** The data grid with row editing (V2 §4.1–4.2). */
export function RowEditor({ projectId, schema, table, info }: { projectId: string; schema: string; table: string; info: TableInfo }) {
  const qc = useQueryClient();
  const [grid, setGrid] = useState<GridOptions>({});
  const [cursors, setCursors] = useState<(string | undefined)[]>([undefined]);
  const after = cursors[cursors.length - 1];
  const rows = useQuery({
    queryKey: ["rows", projectId, schema, table, after, grid],
    queryFn: () => api.tableRows(projectId, schema, table, after, grid),
  });
  const [edits, setEdits] = useState<Record<string, Edit>>({});
  const [inserts, setInserts] = useState<Insert[]>([]);
  const [deletes, setDeletes] = useState<Record<string, { key: Key; xmin: string }>>({});
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [editing, setEditing] = useState<{ row: string; col: string } | null>(null);
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<SaveRowsResult | null>(null);
  // The row of the change that conflicted or failed.
  const [failedRef, setFailedRef] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [fk, setFk] = useState<{ refSchema: string; refTable: string; refColumn: string; value: string } | null>(null);
  const [nextId, setNextId] = useState(1);

  const editable = info.editable;
  const colInfo = (name: string) => info.columns.find((c) => c.name === name);
  const fkFor = (name: string) => info.foreign_keys.find((f) => f.columns.length === 1 && f.columns[0] === name);
  const nUpdates = Object.keys(edits).length;
  const nInserts = inserts.length;
  const nDeletes = Object.keys(deletes).length;
  const pending = nUpdates + nInserts + nDeletes;
  const plural = (n: number, w: string) => `${n} ${w}${n === 1 ? "" : "s"}`;
  const summary = [nUpdates && plural(nUpdates, "update"), nInserts && plural(nInserts, "insert"), nDeletes && plural(nDeletes, "delete")].filter(Boolean).join(", ");
  const setGridAnd = (g: GridOptions) => {
    setGrid(g);
    setCursors([undefined]);
  };
  const discard = () => {
    setEdits({});
    setInserts([]);
    setDeletes({});
    setSelected(new Set());
    setResult(null);
    setFailedRef(null);
    setErr(null);
  };

  // The changes in order: updates, inserts, deletes (as the summary reads).
  const changes = (): { change: RowChange; ref: string }[] => [
    ...Object.entries(edits).map(([id, e]) => ({ change: { op: "update" as const, key: e.key, xmin: e.xmin, values: e.values }, ref: id })),
    ...inserts.map((i) => ({ change: { op: "insert" as const, values: i.values }, ref: `new:${i.id}` })),
    ...Object.entries(deletes).map(([id, d]) => ({ change: { op: "delete" as const, key: d.key, xmin: d.xmin }, ref: id })),
  ];

  const save = async () => {
    setBusy(true);
    setErr(null);
    setResult(null);
    const list = changes();
    try {
      await api.saveRows(projectId, schema, table, list.map((c) => c.change));
      discard();
      setConfirm(false);
      await qc.invalidateQueries({ queryKey: ["rows", projectId, schema, table] });
    } catch (e) {
      setConfirm(false);
      if (e instanceof ApiRequestError && (e.status === 409 || e.status === 422)) {
        const r = e.body as unknown as SaveRowsResult;
        setResult(r);
        setFailedRef(list[r.conflict?.index ?? r.failed?.index ?? 0]?.ref ?? null);
      } else {
        setErr(errorMessage(e));
      }
    } finally {
      setBusy(false);
    }
  };

  if (rows.isPending) return <Spinner />;
  if (rows.isError) return <Alert>{errorMessage(rows.error)}</Alert>;
  const page = rows.data;
  const cols = page.columns.map((c) => c.name);

  const cellValue = (rowId: string, row: Val[], col: string): Val => {
    const e = edits[rowId];
    if (e && col in e.values) return e.values[col];
    return row[cols.indexOf(col)];
  };
  const setCell = (rowId: string, row: Val[], col: string, v: Val, xmin: string) => {
    if (rowId.startsWith("new:")) {
      const id = Number(rowId.slice(4));
      setInserts((ins) => ins.map((i) => (i.id === id ? { ...i, values: { ...i.values, [col]: v } } : i)));
      return;
    }
    const original = row[cols.indexOf(col)];
    setEdits((all) => {
      const cur = all[rowId] ?? { key: keyOf(info, cols, row), xmin, values: {} };
      const values = { ...cur.values };
      if (v === original) delete values[col];
      else values[col] = v;
      const next = { ...all };
      if (Object.keys(values).length === 0) delete next[rowId];
      else next[rowId] = { ...cur, values };
      return next;
    });
  };

  const renderCell = (rowId: string, row: Val[], col: string, xmin: string, isNew: boolean) => {
    const c = colInfo(col);
    const v = isNew ? (inserts.find((i) => `new:${i.id}` === rowId)?.values[col] ?? undefined) : cellValue(rowId, row, col);
    const changed = isNew ? v !== undefined : edits[rowId] && col in edits[rowId].values;
    const readOnly = !editable || !c || c.generated || !!c.identity || rowId in deletes;
    const f = fkFor(col);
    const isEditing = editing?.row === rowId && editing.col === col;
    return (
      <td
        key={col}
        title={v == null ? (isNew ? (c?.default ? `default: ${c.default}` : "NULL") : "NULL") : String(v)}
        onDoubleClick={() => !readOnly && setEditing({ row: rowId, col })}
        className={cx(
          "relative max-w-[24rem] truncate border-l border-line px-2 py-1 font-mono whitespace-pre",
          v == null && "text-muted italic",
          changed && "bg-warn/20",
          !readOnly && "cursor-text",
        )}
        data-testid={`cell-${col}`}
      >
        {isEditing && c ? (
          <div className="absolute top-0 left-0 z-20">
            <CellInput
              col={c}
              value={v ?? null}
              onCancel={() => setEditing(null)}
              onDone={(nv) => {
                setCell(rowId, row, col, nv, xmin);
                setEditing(null);
              }}
            />
          </div>
        ) : null}
        {isNew && v === undefined ? <span className="text-muted">{c?.default ? "default" : c?.identity || c?.generated ? "auto" : "NULL"}</span> : display(c, v ?? null)}
        {f && v != null && !isNew && (
          <button
            type="button"
            className="ml-1 text-accent"
            title={`Open the ${f.ref_table} row`}
            onClick={() => setFk({ refSchema: f.ref_schema, refTable: f.ref_table, refColumn: f.ref_columns[0], value: String(v) })}
            data-testid={`fk-${col}`}
          >
            ↗
          </button>
        )}
      </td>
    );
  };

  const sortBy = (col: string) => {
    if (grid.sort !== col) setGridAnd({ ...grid, sort: col, desc: false });
    else if (!grid.desc) setGridAnd({ ...grid, desc: true });
    else setGridAnd({ ...grid, sort: undefined, desc: false });
  };

  const conflictRow = result?.conflict;
  return (
    <div className="flex flex-col gap-2">
      <FilterBar info={info} filters={grid.filters ?? []} onChange={(filters) => setGridAnd({ ...grid, filters })} />
      <div className="flex flex-wrap items-center gap-2">
        {editable ? (
          <>
            <Button
              className="text-xs"
              onClick={() => {
                setInserts((ins) => [{ id: nextId, values: {} }, ...ins]);
                setNextId((n) => n + 1);
              }}
              data-testid="add-row"
            >
              Add row
            </Button>
            <Button
              className="text-xs"
              disabled={selected.size === 0}
              onClick={() => {
                const next = { ...deletes };
                for (const id of selected) {
                  const i = page.rows.findIndex((r) => keyId(keyOf(info, cols, r)) === id);
                  if (i >= 0) next[id] = { key: keyOf(info, cols, page.rows[i]), xmin: page.xmin![i] };
                }
                setDeletes(next);
                setSelected(new Set());
              }}
              data-testid="delete-rows"
            >
              Delete selected
            </Button>
          </>
        ) : (
          <span className="text-xs text-muted" data-testid="read-only-reason">
            {info.read_only_reason}
          </span>
        )}
        <span className="flex-1" />
        <a className="text-xs underline" href={api.exportUrl(projectId, schema, table, "csv", grid)} data-testid="export-csv">
          Export CSV
        </a>
        <a className="text-xs underline" href={api.exportUrl(projectId, schema, table, "json", grid)}>
          JSON
        </a>
        <span className="text-xs text-muted" data-testid="page-number">
          Page {cursors.length}
        </span>
        <Button className="text-xs" disabled={cursors.length === 1} onClick={() => setCursors((c) => c.slice(0, -1))}>
          Previous
        </Button>
        <Button className="text-xs" disabled={!page.next} onClick={() => page.next && setCursors((c) => [...c, page.next])}>
          Next
        </Button>
      </div>
      {pending > 0 && (
        <div className="flex items-center gap-2 rounded-md border border-warn/40 bg-warn/10 px-3 py-2 text-sm" data-testid="pending-changes">
          <span className="flex-1">Unsaved: {summary}</span>
          <Button className="text-xs" onClick={discard}>
            Discard
          </Button>
          <Button variant="primary" className="text-xs" onClick={() => setConfirm(true)} data-testid="save-rows">
            Save
          </Button>
        </div>
      )}
      {conflictRow && (
        <Alert title={conflictRow.deleted ? "Someone deleted this row since you loaded it" : "Someone changed this row since you loaded it"}>
          <div className="flex flex-col gap-2" data-testid="row-conflict">
            <span>Nothing was saved. {conflictRow.deleted ? "Your edit to it can't be applied." : "It now holds:"}</span>
            {!conflictRow.deleted && conflictRow.current && (
              <code className="block overflow-x-auto font-mono text-xs">
                {cols.map((c, i) => `${c} = ${conflictRow.current![i] ?? "NULL"}`).join(", ")}
              </code>
            )}
            <div className="flex gap-2">
              {!conflictRow.deleted && failedRef && edits[failedRef] && (
                <Button
                  className="text-xs"
                  onClick={() => {
                    // Keep the edit, on top of the row as it is now.
                    setEdits((all) => ({ ...all, [failedRef]: { ...all[failedRef], xmin: conflictRow.xmin! } }));
                    setResult(null);
                  }}
                  data-testid="conflict-reapply"
                >
                  Re-apply my edit
                </Button>
              )}
              <Button
                className="text-xs"
                onClick={() => {
                  if (failedRef) {
                    setEdits(({ [failedRef]: _e, ...rest }) => rest);
                    setDeletes(({ [failedRef]: _d, ...rest }) => rest);
                  }
                  setResult(null);
                  void rows.refetch();
                }}
                data-testid="conflict-discard"
              >
                Discard my edit
              </Button>
            </div>
          </div>
        </Alert>
      )}
      {result?.failed && (
        <Alert title="Postgres refused a change; nothing was saved">
          <span data-testid="row-error">
            {result.failed.error.message}
            {result.failed.error.detail ? ` — ${result.failed.error.detail}` : ""}
          </span>
        </Alert>
      )}
      {err && <Alert>{err}</Alert>}
      <div className="max-h-[36rem] overflow-auto rounded-md border border-line" data-testid="table-grid" onClick={() => setEditing(null)}>
        <table className="min-w-full border-collapse text-xs">
          <thead className="sticky top-0 z-10 bg-surface-2">
            <tr>
              {editable && <th className="w-6 border-b border-line" />}
              {page.columns.map((c) => (
                <th key={c.name} className="border-b border-l border-line px-2 py-1.5 text-left font-semibold whitespace-nowrap">
                  <button type="button" className="hover:underline" onClick={() => sortBy(c.name)} data-testid={`sort-${c.name}`}>
                    {c.name}
                    {grid.sort === c.name && (grid.desc ? " ↓" : " ↑")}
                  </button>
                  <span className="ml-1.5 font-normal text-muted">{c.type}</span>
                  {info.primary_key.includes(c.name) && <Badge tone="accent">pk</Badge>}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {inserts.map((ins) => {
              const id = `new:${ins.id}`;
              return (
                <tr key={id} className={cx("bg-ok/10", failedRef === id && "outline-2 outline-danger")} data-testid="new-row">
                  <td className="px-1 text-center">
                    <button type="button" aria-label="Remove new row" className="text-muted hover:text-danger" onClick={() => setInserts((all) => all.filter((x) => x.id !== ins.id))}>
                      ×
                    </button>
                  </td>
                  {cols.map((col) => renderCell(id, [], col, "", true))}
                </tr>
              );
            })}
            {page.rows.map((r, i) => {
              const id = editable ? keyId(keyOf(info, cols, r)) : String(i);
              const deleted = id in deletes;
              return (
                <tr
                  key={id}
                  className={cx("odd:bg-surface even:bg-surface-2/40", deleted && "bg-danger/10 line-through", failedRef === id && "outline-2 outline-danger")}
                  data-testid="grid-row"
                >
                  {editable && (
                    <td className="px-1 text-center">
                      <input
                        type="checkbox"
                        aria-label="Select row"
                        checked={selected.has(id) || deleted}
                        disabled={deleted}
                        onChange={(e) => {
                          const s = new Set(selected);
                          if (e.target.checked) s.add(id);
                          else s.delete(id);
                          setSelected(s);
                        }}
                      />
                    </td>
                  )}
                  {cols.map((col) => renderCell(id, r, col, page.xmin?.[i] ?? "", false))}
                </tr>
              );
            })}
          </tbody>
        </table>
        {page.rows.length === 0 && inserts.length === 0 && <p className="px-3 py-2 text-xs text-muted">No rows.</p>}
      </div>
      {page.order !== "primary_key" && (
        <p className="text-xs text-muted">
          {page.order === "offset" && grid.sort ? "Sorted by a column that isn't the primary key: pages by offset." : `No primary key: pages follow ${page.order === "ctid" ? "physical row order" : "the view's own order"}.`}
          {page.large_offset && " This deep, offset paging is slow: filter, or sort by the primary key."}
        </p>
      )}
      {editable && <p className="text-xs text-muted">Double-click a cell to edit it. Nothing is written until you save.</p>}
      <Modal title="Save changes" open={confirm} onClose={() => setConfirm(false)}>
        <div className="flex flex-col gap-3">
          <p className="text-sm">
            Save <strong data-testid="save-summary">{summary}</strong> to {schema}.{table}, in one transaction?
          </p>
          <div className="flex justify-end gap-2">
            <Button onClick={() => setConfirm(false)}>Cancel</Button>
            <Button variant="primary" busy={busy} onClick={() => void save()} data-testid="confirm-save">
              Save
            </Button>
          </div>
        </div>
      </Modal>
      {fk && <ForeignRow projectId={projectId} {...fk} onClose={() => setFk(null)} />}
    </div>
  );
}
