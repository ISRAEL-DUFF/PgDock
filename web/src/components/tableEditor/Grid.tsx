import * as CM from "@radix-ui/react-context-menu";
import { ArrowDown, ArrowUp, ChevronDown, Copy, Edit3, ExternalLink, KeyRound, Link2, Plus, Trash2 } from "lucide-react";
import { useMemo, useRef, useState } from "react";
import { DataGrid, Row, SelectColumn, type Column, type RenderEditCellProps, type RenderHeaderCellProps } from "react-data-grid";
import "react-data-grid/lib/styles.css";
import type { EditColumn, TableInfo } from "../../api/client";
import { displayValue, editorKind, parseInput, readOnlyReason, type Val } from "../../lib/tableEditor/cells";
import type { SortRule } from "../../lib/tableEditor/filters";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger, cx, menuItem, menuSurface } from "../ui";

/** A row as the grid holds it: its key (the primary key, as JSON), its
 * xmin for conflict detection, and its values by column. */
export type GridRow = { key: string; xmin: string; v: Record<string, Val> };

export type GridActions = {
  /** Saves one cell; the grid shows the new value straight away. */
  onCellChange: (row: GridRow, column: string, value: Val) => void;
  onEditRow: (row: GridRow) => void;
  onDeleteRows: (rows: GridRow[]) => void;
  onEditColumn: (column: string) => void;
  onDeleteColumn: (column: string) => void;
  onAddColumn: () => void;
  onSort: (column: string, desc: boolean) => void;
  onOpenJSON: (row: GridRow, column: string) => void;
  onOpenForeign: (column: string, value: string) => void;
  onColumnResize: (column: string, width: number) => void;
  onColumnsReorder: (source: string, target: string) => void;
};

const copy = (s: string) => void navigator.clipboard?.writeText(s).catch(() => {});

/** The data grid (react-data-grid), styled and behaving as Studio's. */
export function Grid({
  info,
  columns: order,
  rows,
  widths,
  sorts,
  canEdit,
  selected,
  onSelectedChange,
  actions,
}: {
  info: TableInfo;
  /** The column names, in display order. */
  columns: string[];
  rows: GridRow[];
  widths: Record<string, number>;
  sorts: SortRule[];
  canEdit: boolean;
  selected: ReadonlySet<string>;
  onSelectedChange: (s: Set<string>) => void;
  actions: GridActions;
}) {
  const menuTarget = useRef<{ row: GridRow; column: string } | null>(null);
  const [menuFor, setMenuFor] = useState<{ row: GridRow; column: string } | null>(null);
  const byName = useMemo(() => new Map(info.columns.map((c) => [c.name, c])), [info.columns]);
  const fkFor = (name: string) => info.foreign_keys.find((f) => f.columns.length === 1 && f.columns[0] === name);

  const columns = useMemo(() => {
    const cols: Column<GridRow>[] = [];
    if (info.editable && canEdit) cols.push({ ...SelectColumn, frozen: true });
    for (const name of order) {
      const c = byName.get(name);
      if (!c) continue;
      const ro = readOnlyReason(c, info) ?? (canEdit ? null : "read-only");
      const kind = editorKind(c);
      cols.push({
        key: name,
        name,
        // Room for the name and the type, as Studio sizes its headers.
        width: widths[name] ?? Math.min(Math.max(name.length * 8 + c.type.length * 7 + 64, 120), 320),
        minWidth: 60,
        resizable: true,
        draggable: true,
        editable: !ro && kind !== "json",
        renderHeaderCell: (p) => (
          <HeaderCell p={p} col={c} info={info} sort={sorts.find((s) => s.column === name)} canEdit={canEdit && info.editable} actions={actions} />
        ),
        renderCell: ({ row }) => {
          const v = row.v[name];
          const fk = fkFor(name);
          return (
            <span className={cx("flex items-center gap-1.5", v == null && "text-muted")} data-testid={`cell-${name}`} title={v ?? "NULL"}>
              <span className="truncate">{displayValue(c, v)}</span>
              {fk && v != null && (
                <button
                  type="button"
                  aria-label={`Open the ${fk.ref_table} row`}
                  className="shrink-0 rounded p-0.5 text-muted hover:bg-surface-3 hover:text-fg"
                  onClick={(e) => {
                    e.stopPropagation();
                    actions.onOpenForeign(name, v);
                  }}
                  data-testid={`fk-${name}`}
                >
                  <ExternalLink className="h-3 w-3" />
                </button>
              )}
            </span>
          );
        },
        renderEditCell: (p) => <CellEditor p={p} col={c} />,
      });
    }
    if (info.editable && canEdit && info.kind === "table") {
      cols.push({
        key: "__add",
        name: "",
        width: 44,
        minWidth: 44,
        resizable: false,
        renderHeaderCell: () => (
          <button
            type="button"
            aria-label="Add column"
            className="flex h-full w-full items-center justify-center text-muted hover:bg-surface-2 hover:text-fg"
            onClick={actions.onAddColumn}
            data-testid="grid-add-column"
          >
            <Plus className="h-4 w-4" />
          </button>
        ),
        renderCell: () => null,
      });
    }
    return cols;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [order, byName, info, widths, sorts, canEdit, actions]);

  const rowWithMenu = menuFor;
  const menuCol = rowWithMenu ? byName.get(rowWithMenu.column) : undefined;
  const editableCell = rowWithMenu && menuCol && canEdit && !readOnlyReason(menuCol, info);
  return (
    <CM.Root onOpenChange={(o) => setMenuFor(o ? menuTarget.current : null)}>
      <CM.Trigger asChild>
        <div className="min-h-0 flex-1" data-testid="table-grid">
          <DataGrid<GridRow, unknown, string>
            className="pgdock-grid"
            columns={columns}
            rows={rows}
            rowKeyGetter={(r) => r.key}
            rowHeight={34}
            headerRowHeight={36}
            selectedRows={selected}
            onSelectedRowsChange={onSelectedChange}
            onRowsChange={(next, { indexes, column }) => {
              for (const i of indexes) {
                const before = rows[i];
                const after = next[i];
                if (before && after && before.v[column.key] !== after.v[column.key]) actions.onCellChange(before, column.key, after.v[column.key] ?? null);
              }
            }}
            onCellDoubleClick={({ row, column }, e) => {
              const c = byName.get(column.key);
              if (c && editorKind(c) === "json") {
                e.preventGridDefault();
                actions.onOpenJSON(row, column.key);
              }
            }}
            onCellContextMenu={({ row, column }) => {
              menuTarget.current = { row, column: column.key };
            }}
            onColumnResize={(column, width) => actions.onColumnResize(column.key, Math.round(width))}
            onColumnsReorder={(source, target) => actions.onColumnsReorder(source, target)}
            renderers={{
              renderRow: (key, props) => <Row key={key} {...props} {...{ "data-testid": "grid-row" }} />,
              noRowsFallback: (
                <div className="col-span-full flex items-center justify-center p-10 text-[13px] text-muted" style={{ gridColumn: "1/-1" }}>
                  {info.editable && canEdit ? "This table is empty. Insert a row to get started." : "No rows."}
                </div>
              ),
            }}
            aria-label={`${info.schema}.${info.name}`}
          />
        </div>
      </CM.Trigger>
      <CM.Portal>
        {rowWithMenu && (
          <CM.Content className={menuSurface}>
            <CM.Item className={menuItem} onSelect={() => copy(rowWithMenu.row.v[rowWithMenu.column] ?? "NULL")}>
              <Copy className="h-3.5 w-3.5" /> Copy cell content
            </CM.Item>
            {menuCol && editorKind(menuCol) === "json" && (
              <CM.Item className={menuItem} onSelect={() => actions.onOpenJSON(rowWithMenu.row, rowWithMenu.column)}>
                <Edit3 className="h-3.5 w-3.5" /> {editableCell ? "Edit JSON" : "View JSON"}
              </CM.Item>
            )}
            {editableCell && menuCol!.nullable && rowWithMenu.row.v[rowWithMenu.column] != null && (
              <CM.Item className={menuItem} onSelect={() => actions.onCellChange(rowWithMenu.row, rowWithMenu.column, null)}>
                Set to NULL
              </CM.Item>
            )}
            {info.editable && canEdit && (
              <>
                <CM.Separator className="my-1 h-px bg-line" />
                <CM.Item className={menuItem} onSelect={() => actions.onEditRow(rowWithMenu.row)}>
                  <Edit3 className="h-3.5 w-3.5" /> Edit row
                </CM.Item>
                <CM.Item className={cx(menuItem, "text-danger-text")} onSelect={() => actions.onDeleteRows([rowWithMenu.row])}>
                  <Trash2 className="h-3.5 w-3.5" /> Delete row
                </CM.Item>
              </>
            )}
          </CM.Content>
        )}
      </CM.Portal>
    </CM.Root>
  );
}

function HeaderCell({
  p,
  col,
  info,
  sort,
  canEdit,
  actions,
}: {
  p: RenderHeaderCellProps<GridRow>;
  col: EditColumn;
  info: TableInfo;
  sort: SortRule | undefined;
  canEdit: boolean;
  actions: GridActions;
}) {
  const pk = info.primary_key.includes(col.name);
  const fk = info.foreign_keys.some((f) => f.columns.includes(col.name));
  const isTable = info.kind === "table";
  return (
    <div className="group flex h-full items-center gap-1.5 px-2" data-testid={`column-${col.name}`}>
      {pk && <KeyRound className="h-3 w-3 shrink-0 text-warn-text" aria-label="Primary key" />}
      {fk && <Link2 className="h-3 w-3 shrink-0 text-muted" aria-label="Foreign key" />}
      <span className="truncate text-[12px] font-medium text-fg">{p.column.name}</span>
      <span className="truncate font-mono text-[11px] text-muted">{col.type}</span>
      {sort && (sort.desc ? <ArrowDown className="h-3 w-3 shrink-0 text-accent-text" /> : <ArrowUp className="h-3 w-3 shrink-0 text-accent-text" />)}
      <span className="flex-1" />
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            aria-label={`${col.name} column menu`}
            className="shrink-0 rounded p-0.5 text-muted opacity-60 group-hover:opacity-100 hover:bg-surface-3 hover:text-fg data-[state=open]:opacity-100"
            onPointerDown={(e) => e.stopPropagation()}
            onClick={(e) => e.stopPropagation()}
            data-testid={`column-menu-${col.name}`}
          >
            <ChevronDown className="h-3.5 w-3.5" />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {canEdit && isTable && (
            <>
              <DropdownMenuItem icon={<Edit3 className="h-3.5 w-3.5" />} onSelect={() => actions.onEditColumn(col.name)}>
                Edit column
              </DropdownMenuItem>
              <DropdownMenuSeparator />
            </>
          )}
          <DropdownMenuItem icon={<ArrowUp className="h-3.5 w-3.5" />} onSelect={() => actions.onSort(col.name, false)}>
            {sort && !sort.desc ? "Remove ascending sort" : "Sort ascending"}
          </DropdownMenuItem>
          <DropdownMenuItem icon={<ArrowDown className="h-3.5 w-3.5" />} onSelect={() => actions.onSort(col.name, true)}>
            {sort?.desc ? "Remove descending sort" : "Sort descending"}
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem icon={<Copy className="h-3.5 w-3.5" />} onSelect={() => copy(col.name)}>
            Copy name
          </DropdownMenuItem>
          {canEdit && isTable && (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem danger icon={<Trash2 className="h-3.5 w-3.5" />} onSelect={() => actions.onDeleteColumn(col.name)}>
                Delete column
              </DropdownMenuItem>
            </>
          )}
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}

/** Edits a cell in place: a select for booleans and enums, text for the
 * rest, checked against the type before it saves. */
function CellEditor({ p, col }: { p: RenderEditCellProps<GridRow>; col: EditColumn }) {
  const kind = editorKind(col);
  const v = p.row.v[col.name];
  const [text, setText] = useState(v ?? "");
  const [err, setErr] = useState<string | null>(null);
  const commit = (input: string, close = true) => {
    const r = parseInput(col, input);
    if ("error" in r) {
      setErr(r.error);
      return false;
    }
    p.onRowChange({ ...p.row, v: { ...p.row.v, [col.name]: r.value } }, close);
    return true;
  };
  if (kind === "boolean" || kind === "enum") {
    const options = kind === "boolean" ? ["true", "false"] : col.enum_values!;
    const cur = kind === "boolean" ? (v === "t" ? "true" : v === "f" ? "false" : "") : (v ?? "");
    return (
      <select
        autoFocus
        className="h-full w-full bg-surface-2 px-2 font-mono text-[12px] text-fg outline-none"
        value={cur}
        onChange={(e) => {
          const x = e.target.value;
          p.onRowChange({ ...p.row, v: { ...p.row.v, [col.name]: x === "" ? null : kind === "boolean" ? (x === "true" ? "t" : "f") : x } }, true);
        }}
        onKeyDown={(e) => e.key === "Escape" && p.onClose(false)}
        data-testid={`cell-input-${col.name}`}
      >
        {col.nullable && <option value="">NULL</option>}
        {options.map((o) => (
          <option key={o}>{o}</option>
        ))}
      </select>
    );
  }
  return (
    <div className="relative h-full w-full">
      <input
        autoFocus
        className={cx("h-full w-full bg-surface-2 px-2 font-mono text-[12px] text-fg outline-none", err && "ring-1 ring-danger ring-inset")}
        value={text}
        placeholder={v == null ? "NULL" : undefined}
        onChange={(e) => {
          setText(e.target.value);
          setErr(null);
        }}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            e.stopPropagation();
            commit(text);
          } else if (e.key === "Escape") {
            p.onClose(false);
          }
        }}
        onBlur={() => {
          if ((text || null) === v || (text === "" && v === "")) p.onClose(false);
          else if (!commit(text)) p.onClose(false);
        }}
        data-testid={`cell-input-${col.name}`}
        aria-invalid={!!err}
      />
      {err && (
        <span role="alert" className="absolute top-full left-0 z-10 mt-0.5 rounded bg-danger px-1.5 py-0.5 font-sans text-[11px] whitespace-nowrap text-white">
          {err}
        </span>
      )}
    </div>
  );
}
