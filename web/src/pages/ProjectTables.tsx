import { keepPreviousData, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { Plus, Table2, X } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { ApiRequestError, api, errorMessage, type DbSchema, type GridFilter, type Project, type RowChange, type SaveRowsResult, type SchemaChange, type TableInfo, type TablePage } from "../api/client";
import { LazyCodeEditor as CodeEditor } from "../components/sqlEditor/LazyCodeEditor";
import { ChangeDialog, DeleteRowsDialog, ForeignRowPanel, JsonPanel } from "../components/tableEditor/Extras";
import { Footer, PAGE_SIZES, type Mode } from "../components/tableEditor/Footer";
import { Grid, type GridActions, type GridRow } from "../components/tableEditor/Grid";
import { ColumnPanel, RowPanel, TablePanel } from "../components/tableEditor/Panels";
import { SchemaReview } from "../components/tableEditor/SchemaReview";
import { Sidebar } from "../components/tableEditor/Sidebar";
import { Toolbar } from "../components/tableEditor/Toolbar";
import { Alert, Button, Spinner, cx, toast } from "../components/ui";
import type { Val } from "../lib/tableEditor/cells";
import { toggleSort, type SortRule } from "../lib/tableEditor/filters";
import { closeTab, loadLayout, loadTabs, moveColumn, openTab, orderColumns, refKey, renameTab, saveLayout, saveTabs, type ColumnLayout, type TableRef } from "../lib/tableEditor/prefs";
import { useProject } from "./ProjectOverview";

const PAGE_SIZE_KEY = "pgdock.editor.pageSize";

function storedPageSize(): number {
  try {
    const n = Number(localStorage.getItem(PAGE_SIZE_KEY));
    return (PAGE_SIZES as readonly number[]).includes(n) ? n : 100;
  } catch {
    return 100;
  }
}

type Review = { change: SchemaChange; title?: string; success?: string; after?: () => void };

/** The table editor (docs/ui-redesign.md, phase 2): Studio's layout and
 * behaviour on PGDock's grid, row and schema APIs. */
export function ProjectTablesPage() {
  const { data: p } = useProject();
  if (!p) return null;
  if (p.status !== "active") {
    return (
      <div className="p-6">
        <Alert>The project is {p.status}; the table editor is available once it is active.</Alert>
      </div>
    );
  }
  return <TableEditor p={p} />;
}

function TableEditor({ p }: { p: Project }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const search = useSearch({ from: "/app/projects/$id/tables" });
  const tree = useQuery({ queryKey: ["schema", p.id], queryFn: () => api.schema(p.id) });
  const canEdit = (p.my_role === "admin" || p.my_role === "developer") && !p.settings.console_read_only;
  const [tabs, setTabsState] = useState<TableRef[]>(() => loadTabs(p.id));
  const setTabs = (t: TableRef[]) => {
    setTabsState(t);
    saveTabs(p.id, t);
  };
  const current: TableRef | null = search.table ? { schema: search.schema ?? "public", table: search.table } : null;
  const [schema, setSchema] = useState(search.schema ?? current?.schema ?? "public");
  const [panel, setPanel] = useState<null | { kind: "new-table" } | { kind: "edit-table"; ref: TableRef } | { kind: "dialog"; dialog: "schema" | "enum" | "duplicate"; ref?: TableRef }>(null);
  const [review, setReview] = useState<Review | null>(null);

  const open = useCallback(
    (t: TableRef | null) => {
      if (t) setTabsState((x) => {
        const next = openTab(x, t);
        saveTabs(p.id, next);
        return next;
      });
      void navigate({ to: "/projects/$id/tables", params: { id: p.id }, search: t ? { schema: t.schema, table: t.table } : {} });
    },
    [navigate, p.id],
  );

  // Open the table in the address bar as a tab; with none, the last tab.
  useEffect(() => {
    if (current) {
      setTabsState((x) => {
        const next = openTab(x, current);
        if (next !== x) saveTabs(p.id, next);
        return next;
      });
      setSchema(current.schema);
    } else if (tabs.length > 0) {
      open(tabs[tabs.length - 1]);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [search.schema, search.table]);

  if (tree.isPending) return <Spinner />;
  if (tree.isError) return <Alert>{errorMessage(tree.error)}</Alert>;
  const schemas = tree.data;
  const exists = (t: TableRef) => !!schemas.schemas.find((s) => s.name === t.schema)?.tables.some((x) => x.name === t.table);
  const shownTabs = tabs.filter(exists);

  const reviewThen = (r: Review) => setReview(r);
  const exportCSV = (t: TableRef) => {
    const a = document.createElement("a");
    a.href = api.exportUrl(p.id, t.schema, t.table, "csv");
    a.click();
  };

  return (
    <div className="flex min-h-0 flex-1" data-testid="table-editor">
      <Sidebar
        tree={schemas}
        schema={schemas.schemas.some((s) => s.name === schema) ? schema : (schemas.schemas[0]?.name ?? "public")}
        onSchema={setSchema}
        current={current}
        canEdit={canEdit}
        actions={{
          onOpen: open,
          onNewTable: () => setPanel({ kind: "new-table" }),
          onEditTable: (ref) => setPanel({ kind: "edit-table", ref }),
          onDuplicate: (ref) => setPanel({ kind: "dialog", dialog: "duplicate", ref }),
          onDelete: (ref) =>
            reviewThen({
              change: { kind: "drop_table", schema: ref.schema, table: ref.table },
              title: `Delete table ${ref.table}`,
              success: `Deleted table ${ref.table}`,
              after: () => {
                const { tabs: rest, next } = closeTab(tabs, ref, current);
                setTabs(rest);
                open(next);
              },
            }),
          onExport: exportCSV,
          onNewSchema: () => setPanel({ kind: "dialog", dialog: "schema" }),
          onNewEnum: () => setPanel({ kind: "dialog", dialog: "enum" }),
        }}
      />
      <div className="flex min-w-0 flex-1 flex-col">
        <div className="flex h-10 shrink-0 items-end gap-px overflow-x-auto border-b border-line bg-surface px-1" role="tablist" aria-label="Open tables">
          {shownTabs.map((t) => {
            const active = current && refKey(current) === refKey(t);
            return (
              <div
                key={refKey(t)}
                className={cx(
                  "group flex h-9 max-w-[14rem] items-center gap-1.5 rounded-t-md border border-b-0 px-3 text-[12px]",
                  active ? "border-line bg-bg text-fg" : "border-transparent text-muted hover:text-fg",
                )}
              >
                <button type="button" role="tab" aria-selected={!!active} className="flex min-w-0 items-center gap-1.5" onClick={() => open(t)} data-testid={`tab-${t.table}`}>
                  <Table2 className="h-3.5 w-3.5 shrink-0" strokeWidth={1.6} />
                  <span className="truncate">{t.schema === "public" ? t.table : `${t.schema}.${t.table}`}</span>
                </button>
                <button
                  type="button"
                  aria-label={`Close ${t.table}`}
                  className="rounded p-0.5 opacity-0 group-hover:opacity-100 hover:bg-surface-3"
                  onClick={() => {
                    const { tabs: rest, next } = closeTab(shownTabs, t, current);
                    setTabs(rest);
                    if (active) open(next);
                  }}
                >
                  <X className="h-3 w-3" />
                </button>
              </div>
            );
          })}
          {canEdit && (
            <button type="button" aria-label="New table" className="mb-1 ml-1 rounded p-1 text-muted hover:bg-surface-2 hover:text-fg" onClick={() => setPanel({ kind: "new-table" })}>
              <Plus className="h-4 w-4" />
            </button>
          )}
        </div>
        {current && exists(current) ? (
          <TableView
            key={refKey(current)}
            p={p}
            tableRef={current}
            tree={schemas}
            canEdit={canEdit}
            onReview={reviewThen}
            onOpen={open}
          />
        ) : (
          <div className="flex flex-1 items-center justify-center p-6">
            <div className="flex max-w-sm flex-col items-center gap-3 text-center">
              <Table2 className="h-8 w-8 text-muted" strokeWidth={1.4} />
              <p className="text-[14px]">{current ? `There is no table ${current.table} in ${current.schema}.` : "Select a table from the navigation panel on the left to view its data"}</p>
              {canEdit && (
                <>
                  <p className="text-[13px] text-muted">or create a new one.</p>
                  <Button variant="primary" icon={<Plus className="h-3.5 w-3.5" />} onClick={() => setPanel({ kind: "new-table" })}>
                    Create a new table
                  </Button>
                </>
              )}
            </div>
          </div>
        )}
      </div>

      {panel?.kind === "new-table" && (
        <TablePanel
          schema={schemas.schemas.some((s) => s.name === schema) ? schema : "public"}
          tree={schemas}
          onClose={() => setPanel(null)}
          onReview={(change, { table }) =>
            reviewThen({
              change,
              title: `Create table ${table}`,
              success: `Created table ${table}`,
              after: () => {
                setPanel(null);
                open({ schema: change.schema ?? "public", table });
              },
            })
          }
        />
      )}
      {panel?.kind === "edit-table" && <EditTablePanel p={p} tableRef={panel.ref} tree={schemas} onClose={() => setPanel(null)} onReview={reviewThen} onDone={(to) => {
        setPanel(null);
        if (to.table !== panel.ref.table) {
          setTabs(renameTab(tabs, panel.ref, to));
          open(to);
        }
      }} />}
      {panel?.kind === "dialog" && (
        <ChangeDialog
          kind={panel.dialog}
          schema={panel.ref?.schema ?? schema}
          table={panel.ref?.table}
          onClose={() => setPanel(null)}
          onReview={(change, after) =>
            reviewThen({
              change,
              success: panel.dialog === "duplicate" ? `Duplicated ${panel.ref?.table}` : panel.dialog === "schema" ? `Created schema ${change.name}` : `Created type ${change.name}`,
              after: () => {
                setPanel(null);
                if (panel.dialog === "schema") setSchema(after.schema);
                if (after.table) open({ schema: after.schema, table: after.table });
              },
            })
          }
        />
      )}
      {review && (
        <SchemaReview
          projectId={p.id}
          change={review.change}
          title={review.title}
          success={review.success}
          onClose={() => setReview(null)}
          onApplied={() => {
            const after = review.after;
            setReview(null);
            after?.();
            void qc.invalidateQueries({ queryKey: ["schema", p.id] });
          }}
        />
      )}
    </div>
  );
}

/** Edit table needs the table's full info before the panel opens. */
function EditTablePanel({ p, tableRef, tree, onClose, onReview, onDone }: { p: Project; tableRef: TableRef; tree: DbSchema; onClose: () => void; onReview: (r: Review) => void; onDone: (to: TableRef) => void }) {
  const info = useQuery({ queryKey: ["table-info", p.id, tableRef.schema, tableRef.table], queryFn: () => api.tableInfo(p.id, tableRef.schema, tableRef.table) });
  if (!info.data) return null;
  return (
    <TablePanel
      schema={tableRef.schema}
      tree={tree}
      info={info.data}
      onClose={onClose}
      onReview={(change, { table }) =>
        onReview({ change, title: `Update table ${tableRef.table}`, success: `Updated table ${table}`, after: () => onDone({ schema: tableRef.schema, table }) })
      }
    />
  );
}

/** The grid key of a row: its primary key, as JSON. */
function keyOf(info: TableInfo, cols: string[], row: Val[], i: number): string {
  if (!info.editable || info.primary_key.length === 0) return String(i);
  return JSON.stringify(info.primary_key.map((k) => row[cols.indexOf(k)]));
}

function pkOf(info: TableInfo, row: GridRow): Record<string, string | null> {
  return Object.fromEntries(info.primary_key.map((k) => [k, row.v[k] ?? null]));
}

function TableView({
  p,
  tableRef,
  tree,
  canEdit,
  onReview,
  onOpen,
}: {
  p: Project;
  tableRef: TableRef;
  tree: DbSchema;
  canEdit: boolean;
  onReview: (r: Review) => void;
  onOpen: (t: TableRef) => void;
}) {
  const qc = useQueryClient();
  const { schema, table } = tableRef;
  const [filters, setFilters] = useState<GridFilter[]>([]);
  const [sorts, setSorts] = useState<SortRule[]>([]);
  const [page, setPage] = useState(0);
  const [pageSize, setPageSizeState] = useState(storedPageSize);
  const [mode, setMode] = useState<Mode>("data");
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const [layout, setLayoutState] = useState<ColumnLayout>(() => loadLayout(p.id, tableRef));
  const [panel, setPanel] = useState<
    | null
    | { kind: "row"; row?: GridRow }
    | { kind: "column"; column?: string }
    | { kind: "json"; row: GridRow; column: string }
    | { kind: "fk"; column: string; value: string }
    | { kind: "delete-rows"; rows: GridRow[] }
  >(null);

  const info = useQuery({ queryKey: ["table-info", p.id, schema, table], queryFn: () => api.tableInfo(p.id, schema, table) });
  const grid = { filters, order: sorts };
  const rowsKey = ["rows", p.id, schema, table, grid, page, pageSize] as const;
  const rows = useQuery({
    queryKey: rowsKey,
    queryFn: () => api.tablePage(p.id, schema, table, grid, page * pageSize, pageSize),
    placeholderData: keepPreviousData,
    // The grid holds each row's xmin for conflict checks: reload it when
    // asked (or after a save), not whenever the window regains focus.
    refetchOnWindowFocus: false,
  });
  const count = useQuery({ queryKey: ["count", p.id, schema, table, filters], queryFn: () => api.tableCount(p.id, schema, table, filters) });
  const definition = useQuery({ queryKey: ["definition", p.id, schema, table], queryFn: () => api.tableDefinition(p.id, schema, table), enabled: mode === "definition" });

  const setLayout = (l: ColumnLayout) => {
    setLayoutState(l);
    saveLayout(p.id, tableRef, l);
  };
  const resetPage = () => {
    setPage(0);
    setSelected(new Set());
  };

  const i = info.data;
  const pageData = rows.data;
  const cols = useMemo(() => pageData?.columns.map((c) => c.name) ?? [], [pageData]);
  const gridRows: GridRow[] = useMemo(() => {
    if (!pageData || !i) return [];
    return pageData.rows.map((r, n) => ({
      key: keyOf(i, cols, r, n),
      xmin: pageData.xmin?.[n] ?? "",
      v: Object.fromEntries(cols.map((c, j) => [c, r[j]])),
    }));
  }, [pageData, i, cols]);

  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["rows", p.id, schema, table] });
    void qc.invalidateQueries({ queryKey: ["count", p.id, schema, table] });
  };

  /** Runs row changes in one transaction; conflicts and refusals become
   * toasts, as in Studio. Returns an error message, or null. */
  const save = async (changes: RowChange[]): Promise<string | null> => {
    try {
      await api.saveRows(p.id, schema, table, changes);
      refresh();
      return null;
    } catch (e) {
      refresh();
      if (e instanceof ApiRequestError && (e.status === 409 || e.status === 422)) {
        const r = e.body as unknown as SaveRowsResult;
        if (r.conflict) {
          const msg = r.conflict.deleted ? "Someone deleted this row since you loaded it" : "Someone changed this row since you loaded it";
          toast.error(msg, { description: "Nothing was saved. The grid now shows the row as it is.", action: { label: "Reload rows", onClick: refresh } });
          return msg;
        }
        if (r.failed) {
          const msg = r.failed.error.message + (r.failed.error.detail ? ` — ${r.failed.error.detail}` : "");
          toast.error("Postgres refused the change", { description: msg });
          return msg;
        }
      }
      toast.error(errorMessage(e));
      return errorMessage(e);
    }
  };

  const actions: GridActions = {
    onCellChange: (row, column, value) => {
      if (!i) return;
      // Show the new value at once; the refetch after saving confirms it.
      qc.setQueryData<TablePage>(rowsKey, (old) =>
        old && { ...old, rows: old.rows.map((r, n) => (gridRows[n]?.key === row.key ? r.map((x, j) => (cols[j] === column ? value : x)) : r)) },
      );
      void save([{ op: "update", key: pkOf(i, row), xmin: row.xmin, values: { [column]: value } }]);
    },
    onEditRow: (row) => setPanel({ kind: "row", row }),
    onDeleteRows: (rs) => setPanel({ kind: "delete-rows", rows: rs }),
    onEditColumn: (column) => setPanel({ kind: "column", column }),
    onDeleteColumn: (column) =>
      onReview({ change: { kind: "drop_column", schema, table, column_name: column }, title: `Delete column ${column}`, success: `Deleted column ${column}` }),
    onAddColumn: () => setPanel({ kind: "column" }),
    onSort: (column, desc) => {
      setSorts(toggleSort(sorts, column, desc));
      resetPage();
    },
    onOpenJSON: (row, column) => setPanel({ kind: "json", row, column }),
    onOpenForeign: (column, value) => setPanel({ kind: "fk", column, value }),
    onColumnResize: (column, width) => setLayout({ ...layout, widths: { ...layout.widths, [column]: width } }),
    onColumnsReorder: (source, target) => setLayout({ ...layout, order: moveColumn(orderColumns(cols, layout.order), source, target) }),
  };

  if (info.isPending) return <Spinner />;
  if (info.isError) return <Alert>{errorMessage(info.error)}</Alert>;
  const t = info.data;
  const fk = panel?.kind === "fk" ? t.foreign_keys.find((f) => f.columns.length === 1 && f.columns[0] === panel.column) : undefined;
  const selectedRows = gridRows.filter((r) => selected.has(r.key));

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {mode === "data" && (
        <Toolbar
          info={t}
          filters={filters}
          sorts={sorts}
          canEdit={canEdit}
          selected={selected.size}
          fetching={rows.isFetching}
          exportUrl={(format) => api.exportUrl(p.id, schema, table, format, grid)}
          onFilters={(f) => {
            setFilters(f);
            resetPage();
          }}
          onSorts={(s) => {
            setSorts(s);
            resetPage();
          }}
          onInsertRow={() => setPanel({ kind: "row" })}
          onInsertColumn={() => setPanel({ kind: "column" })}
          onDeleteSelected={() => setPanel({ kind: "delete-rows", rows: selectedRows })}
          onCopySelected={() => {
            void navigator.clipboard?.writeText(JSON.stringify(selectedRows.map((r) => r.v), null, 2)).catch(() => {});
            toast.success(`Copied ${selectedRows.length} row${selectedRows.length === 1 ? "" : "s"}`);
          }}
          onClearSelection={() => setSelected(new Set())}
          onRefresh={refresh}
        />
      )}
      {mode === "definition" ? (
        <div className="min-h-0 flex-1 overflow-hidden bg-code">
          {definition.isPending ? <Spinner /> : definition.isError ? <Alert>{errorMessage(definition.error)}</Alert> : <CodeEditor value={definition.data.sql} readOnly label={`Definition of ${table}`} testId="table-definition" />}
        </div>
      ) : rows.isError ? (
        <div className="p-4">
          <Alert>{errorMessage(rows.error)}</Alert>
        </div>
      ) : !pageData ? (
        <Spinner />
      ) : (
        <Grid
          info={t}
          columns={orderColumns(cols, layout.order)}
          rows={gridRows}
          widths={layout.widths}
          sorts={sorts}
          canEdit={canEdit}
          selected={selected}
          onSelectedChange={setSelected}
          actions={actions}
        />
      )}
      <Footer
        page={page}
        pageSize={pageSize}
        count={count.data}
        hasNext={!!pageData?.next}
        mode={mode}
        onPage={(n) => {
          setPage(n);
          setSelected(new Set());
        }}
        onPageSize={(n) => {
          setPageSizeState(n);
          try {
            localStorage.setItem(PAGE_SIZE_KEY, String(n));
          } catch {
            /* storage unavailable */
          }
          resetPage();
        }}
        onMode={setMode}
      />

      {panel?.kind === "row" && (
        <RowPanel
          info={t}
          row={panel.row?.v}
          onClose={() => setPanel(null)}
          onSave={async (values) => {
            const err = await save(panel.row ? [{ op: "update", key: pkOf(t, panel.row), xmin: panel.row.xmin, values }] : [{ op: "insert", values }]);
            if (!err) {
              setPanel(null);
              toast.success(panel.row ? "Row updated" : "Row added");
            }
            return err;
          }}
        />
      )}
      {panel?.kind === "column" && (
        <ColumnPanel
          info={t}
          tree={tree}
          column={panel.column}
          onClose={() => setPanel(null)}
          onReview={(change) =>
            onReview({
              change,
              title: panel.column ? `Update column ${panel.column}` : `Add a column to ${table}`,
              success: panel.column ? `Updated column ${panel.column}` : "Added the column",
              after: () => setPanel(null),
            })
          }
        />
      )}
      {panel?.kind === "json" && (
        <JsonPanel
          col={t.columns.find((c) => c.name === panel.column)!}
          value={panel.row.v[panel.column] ?? null}
          readOnly={!canEdit || !t.editable || t.columns.find((c) => c.name === panel.column)!.generated}
          onClose={() => setPanel(null)}
          onSave={(v) => {
            actions.onCellChange(panel.row, panel.column, v);
            setPanel(null);
          }}
        />
      )}
      {panel?.kind === "fk" && fk && (
        <ForeignRowPanel
          projectId={p.id}
          refSchema={fk.ref_schema}
          refTable={fk.ref_table}
          refColumn={fk.ref_columns[0]}
          value={panel.value}
          onClose={() => setPanel(null)}
          onOpenTable={() => {
            setPanel(null);
            onOpen({ schema: fk.ref_schema, table: fk.ref_table });
          }}
        />
      )}
      {panel?.kind === "delete-rows" && (
        <DeleteRowsDialog
          count={panel.rows.length}
          table={table}
          onClose={() => setPanel(null)}
          onConfirm={async () => {
            const err = await save(panel.rows.map((r) => ({ op: "delete" as const, key: pkOf(t, r), xmin: r.xmin })));
            setPanel(null);
            if (!err) {
              setSelected(new Set());
              toast.success(`Deleted ${panel.rows.length} row${panel.rows.length === 1 ? "" : "s"}`);
            }
          }}
        />
      )}
    </div>
  );
}
