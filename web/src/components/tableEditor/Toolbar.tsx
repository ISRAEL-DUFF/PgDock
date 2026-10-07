import { ArrowDownUp, Columns3, Copy, Download, ListFilter, Plus, RefreshCw, Rows3, ShieldCheck, Trash2, X } from "lucide-react";
import { useState } from "react";
import type { GridFilter, TableInfo } from "../../api/client";
import { needsValue, opsFor, toGridFilters, type FilterRule, type SortRule } from "../../lib/tableEditor/filters";
import {
  Button,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
  Input,
  Popover,
  PopoverContent,
  PopoverTrigger,
  Select,
  cx,
} from "../ui";

let ruleSeq = 0;

/** Starts a download (the server sends it as an attachment). */
function download(url: string) {
  const a = document.createElement("a");
  a.href = url;
  a.click();
}

/** The Filter popover: conditions row by row, applied together. */
export function FilterPopover({ info, filters, onApply }: { info: TableInfo; filters: GridFilter[]; onApply: (f: GridFilter[]) => void }) {
  const [open, setOpen] = useState(false);
  const [rules, setRules] = useState<FilterRule[]>([]);
  const start = () =>
    setRules(filters.map((f) => ({ id: ++ruleSeq, column: f.column, op: f.op, value: f.values ? f.values.join(", ") : (f.value ?? "") })));
  const add = () => setRules((r) => [...r, { id: ++ruleSeq, column: info.columns[0]?.name ?? "", op: "eq", value: "" }]);
  const set = (id: number, patch: Partial<FilterRule>) => setRules((r) => r.map((x) => (x.id === id ? { ...x, ...patch } : x)));
  const apply = () => {
    onApply(toGridFilters(rules));
    setOpen(false);
  };
  return (
    <Popover
      open={open}
      onOpenChange={(o) => {
        if (o) start();
        setOpen(o);
      }}
    >
      <PopoverTrigger asChild>
        <Button variant={filters.length ? "secondary" : "ghost"} size="tiny" icon={<ListFilter className="h-3.5 w-3.5" />} data-testid="filter-button">
          {filters.length ? `Filtered by ${filters.length} rule${filters.length === 1 ? "" : "s"}` : "Filter"}
        </Button>
      </PopoverTrigger>
      <PopoverContent className="w-[34rem] p-0" data-testid="filter-popover">
        <div className="flex flex-col gap-2 p-3">
          {rules.length === 0 && <p className="text-[12px] text-muted">No filters applied to this view</p>}
          {rules.map((r) => {
            const col = info.columns.find((c) => c.name === r.column);
            const ops = opsFor(col);
            return (
              <div key={r.id} className="flex items-center gap-2" data-testid="filter-rule">
                <Select aria-label="Filter column" value={r.column} onChange={(e) => set(r.id, { column: e.target.value })} className="w-40">
                  {info.columns.map((c) => (
                    <option key={c.name}>{c.name}</option>
                  ))}
                </Select>
                <Select aria-label="Filter operator" value={r.op} onChange={(e) => set(r.id, { op: e.target.value as FilterRule["op"] })} className="w-28 font-mono">
                  {ops.map((o) => (
                    <option key={o.op} value={o.op} title={o.hint}>
                      {o.label}
                    </option>
                  ))}
                </Select>
                <Input
                  aria-label="Filter value"
                  value={r.value}
                  disabled={!needsValue(r.op)}
                  onChange={(e) => set(r.id, { value: e.target.value })}
                  onKeyDown={(e) => e.key === "Enter" && apply()}
                  placeholder={r.op === "in" ? "a, b, c" : needsValue(r.op) ? "Enter a value" : ""}
                  className="flex-1 font-mono text-[12px]"
                />
                <button type="button" aria-label="Remove filter" className="rounded p-1 text-muted hover:text-fg" onClick={() => setRules((x) => x.filter((y) => y.id !== r.id))}>
                  <X className="h-3.5 w-3.5" />
                </button>
              </div>
            );
          })}
        </div>
        <div className="flex items-center justify-between border-t border-line px-3 py-2">
          <Button size="tiny" variant="ghost" icon={<Plus className="h-3.5 w-3.5" />} onClick={add} data-testid="add-filter">
            Add filter
          </Button>
          <Button size="tiny" variant="primary" onClick={apply} data-testid="apply-filter">
            Apply filter
          </Button>
        </div>
      </PopoverContent>
    </Popover>
  );
}

/** The Sort popover: columns in priority order, each ascending or
 * descending. */
export function SortPopover({ info, sorts, onApply }: { info: TableInfo; sorts: SortRule[]; onApply: (s: SortRule[]) => void }) {
  const [open, setOpen] = useState(false);
  const [rules, setRules] = useState<SortRule[]>([]);
  const unused = info.columns.filter((c) => !rules.some((r) => r.column === c.name));
  return (
    <Popover
      open={open}
      onOpenChange={(o) => {
        if (o) setRules(sorts);
        setOpen(o);
      }}
    >
      <PopoverTrigger asChild>
        <Button variant={sorts.length ? "secondary" : "ghost"} size="tiny" icon={<ArrowDownUp className="h-3.5 w-3.5" />} data-testid="sort-button">
          {sorts.length ? `Sorted by ${sorts.length} rule${sorts.length === 1 ? "" : "s"}` : "Sort"}
        </Button>
      </PopoverTrigger>
      <PopoverContent className="w-96 p-0" data-testid="sort-popover">
        <div className="flex flex-col gap-2 p-3">
          {rules.length === 0 && <p className="text-[12px] text-muted">No sorts applied to this view</p>}
          {rules.map((r, i) => (
            <div key={r.column} className="flex items-center gap-2" data-testid="sort-rule">
              <span className="w-14 text-[11px] text-muted">{i === 0 ? "sort by" : "then by"}</span>
              <code className="flex-1 truncate font-mono text-[12px]">{r.column}</code>
              <Select aria-label={`Direction for ${r.column}`} value={r.desc ? "desc" : "asc"} onChange={(e) => setRules((x) => x.map((y) => (y.column === r.column ? { ...y, desc: e.target.value === "desc" } : y)))}>
                <option value="asc">ascending</option>
                <option value="desc">descending</option>
              </Select>
              <button type="button" aria-label={`Remove the sort on ${r.column}`} className="rounded p-1 text-muted hover:text-fg" onClick={() => setRules((x) => x.filter((y) => y.column !== r.column))}>
                <X className="h-3.5 w-3.5" />
              </button>
            </div>
          ))}
        </div>
        <div className="flex items-center justify-between gap-2 border-t border-line px-3 py-2">
          <Select aria-label="Pick a column to sort by" value="" onChange={(e) => e.target.value && setRules((x) => [...x, { column: e.target.value, desc: false }])} disabled={unused.length === 0} data-testid="add-sort">
            <option value="">Pick a column to sort by</option>
            {unused.map((c) => (
              <option key={c.name}>{c.name}</option>
            ))}
          </Select>
          <Button
            size="tiny"
            variant="primary"
            onClick={() => {
              onApply(rules);
              setOpen(false);
            }}
            data-testid="apply-sort"
          >
            Apply sorting
          </Button>
        </div>
      </PopoverContent>
    </Popover>
  );
}

export function Toolbar({
  info,
  filters,
  sorts,
  canEdit,
  selected,
  fetching,
  exportUrl,
  onFilters,
  onSorts,
  onInsertRow,
  onInsertColumn,
  onDeleteSelected,
  onCopySelected,
  onClearSelection,
  onRefresh,
  onPolicies,
}: {
  info: TableInfo;
  filters: GridFilter[];
  sorts: SortRule[];
  canEdit: boolean;
  selected: number;
  fetching: boolean;
  exportUrl: (format: "csv" | "json") => string;
  onFilters: (f: GridFilter[]) => void;
  onSorts: (s: SortRule[]) => void;
  onInsertRow: () => void;
  onInsertColumn: () => void;
  onDeleteSelected: () => void;
  onCopySelected: () => void;
  onClearSelection: () => void;
  onRefresh: () => void;
  onPolicies?: () => void;
}) {
  const editable = canEdit && info.editable;
  return (
    <div className="flex h-10 shrink-0 items-center gap-1.5 border-b border-line bg-surface px-2" data-testid="table-toolbar">
      {selected > 0 ? (
        <>
          <button type="button" aria-label="Clear the selection" className="rounded p-1 text-muted hover:text-fg" onClick={onClearSelection}>
            <X className="h-3.5 w-3.5" />
          </button>
          <span className="text-[12px] text-fg-light" data-testid="selection-count">
            {selected} row{selected === 1 ? "" : "s"} selected
          </span>
          <Button size="tiny" variant="ghost" icon={<Copy className="h-3.5 w-3.5" />} onClick={onCopySelected}>
            Copy
          </Button>
          {editable && (
            <Button size="tiny" variant="danger" icon={<Trash2 className="h-3.5 w-3.5" />} onClick={onDeleteSelected} data-testid="delete-rows">
              Delete {selected} row{selected === 1 ? "" : "s"}
            </Button>
          )}
        </>
      ) : (
        <>
          <Button size="tiny" variant="ghost" aria-label="Refresh" onClick={onRefresh} icon={<RefreshCw className={cx("h-3.5 w-3.5", fetching && "animate-spin")} />} data-testid="refresh" />
          <FilterPopover info={info} filters={filters} onApply={onFilters} />
          <SortPopover info={info} sorts={sorts} onApply={onSorts} />
          {editable && (
            <>
              <span className="mx-1 h-5 w-px bg-line" />
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button size="tiny" variant="primary" icon={<Plus className="h-3.5 w-3.5" />} data-testid="insert-menu">
                    Insert
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent className="w-64">
                  <DropdownMenuItem icon={<Rows3 className="h-4 w-4" />} onSelect={onInsertRow} data-testid="insert-row">
                    <span className="flex flex-col">
                      <span>Insert row</span>
                      <span className="text-[11px] text-muted">Insert a new row into {info.name}</span>
                    </span>
                  </DropdownMenuItem>
                  {info.kind === "table" && (
                    <DropdownMenuItem icon={<Columns3 className="h-4 w-4" />} onSelect={onInsertColumn} data-testid="insert-column">
                      <span className="flex flex-col">
                        <span>Insert column</span>
                        <span className="text-[11px] text-muted">Insert a new column into {info.name}</span>
                      </span>
                    </DropdownMenuItem>
                  )}
                </DropdownMenuContent>
              </DropdownMenu>
            </>
          )}
          {!editable && info.read_only_reason && (
            <span className="ml-2 truncate text-[12px] text-muted" data-testid="read-only-reason">
              {info.read_only_reason}
            </span>
          )}
          <span className="flex-1" />
          {editable && onPolicies && (info.kind === "table" || info.kind === "partitioned_table") && (
            <Button size="tiny" variant="ghost" icon={<ShieldCheck className="h-3.5 w-3.5" />} onClick={onPolicies} data-testid="policy-helper">
              Policies
            </Button>
          )}
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button size="tiny" variant="ghost" icon={<Download className="h-3.5 w-3.5" />} data-testid="export-menu">
                Export
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onSelect={() => download(exportUrl("csv"))} data-testid="export-csv">
                Export as CSV
              </DropdownMenuItem>
              <DropdownMenuItem onSelect={() => download(exportUrl("json"))} data-testid="export-json">
                Export as JSON
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </>
      )}
    </div>
  );
}
