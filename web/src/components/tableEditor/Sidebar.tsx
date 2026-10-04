import { Copy, Download, Edit3, Eye, Files, Globe, Layers, ListFilter, MoreVertical, Plus, Search, Table2, Trash2, ChevronsUpDown, Check, FolderPlus, ListPlus } from "lucide-react";
import { useState } from "react";
import type { DbSchema, DbTable } from "../../api/client";
import type { TableRef } from "../../lib/tableEditor/prefs";
import {
  Button,
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
  SectionLabel,
  cx,
} from "../ui";

type Kind = DbTable["kind"];

const kindIcon: Record<Kind, typeof Table2> = {
  table: Table2,
  partitioned_table: Layers,
  view: Eye,
  materialized_view: Files,
  foreign_table: Globe,
};

const kindLabel: Record<Kind, string> = {
  table: "Table",
  partitioned_table: "Partitioned table",
  view: "View",
  materialized_view: "Materialized view",
  foreign_table: "Foreign table",
};

export type SidebarActions = {
  onOpen: (t: TableRef) => void;
  onNewTable: () => void;
  onEditTable: (t: TableRef) => void;
  onDuplicate: (t: TableRef) => void;
  onDelete: (t: TableRef) => void;
  onExport: (t: TableRef) => void;
  onNewSchema: () => void;
  onNewEnum: () => void;
};

/** The table editor's left sidebar, as Studio's. */
export function Sidebar({
  tree,
  schema,
  onSchema,
  current,
  canEdit,
  actions,
}: {
  tree: DbSchema;
  schema: string;
  onSchema: (s: string) => void;
  current: TableRef | null;
  canEdit: boolean;
  actions: SidebarActions;
}) {
  const [q, setQ] = useState("");
  const [shown, setShown] = useState<Record<"view" | "materialized_view" | "foreign_table", boolean>>({ view: true, materialized_view: true, foreign_table: true });
  const tables = (tree.schemas.find((s) => s.name === schema)?.tables ?? []).filter((t) => {
    if (t.kind in shown && !shown[t.kind as keyof typeof shown]) return false;
    return !q.trim() || t.name.toLowerCase().includes(q.trim().toLowerCase());
  });
  return (
    <aside className="flex w-64 shrink-0 flex-col border-r border-line bg-surface" data-testid="table-sidebar">
      <div className="flex flex-col gap-2 border-b border-line p-3">
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button type="button" className="flex h-8 items-center gap-2 rounded-md border border-line-strong bg-surface-2 px-2.5 text-left text-[12px]" data-testid="schema-picker">
              <span className="text-muted">schema</span>
              <span className="flex-1 truncate font-mono text-fg">{schema}</span>
              <ChevronsUpDown className="h-3.5 w-3.5 text-muted" />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent className="w-56">
            <DropdownMenuLabel>Schemas</DropdownMenuLabel>
            {tree.schemas.map((s) => (
              <DropdownMenuItem key={s.name} onSelect={() => onSchema(s.name)} icon={s.name === schema ? <Check className="h-3.5 w-3.5" /> : undefined}>
                <span className="font-mono text-[12px]">{s.name}</span>
              </DropdownMenuItem>
            ))}
            {canEdit && (
              <>
                <DropdownMenuSeparator />
                <DropdownMenuItem icon={<FolderPlus className="h-3.5 w-3.5" />} onSelect={actions.onNewSchema} data-testid="new-schema">
                  Create a new schema
                </DropdownMenuItem>
                <DropdownMenuItem icon={<ListPlus className="h-3.5 w-3.5" />} onSelect={actions.onNewEnum} data-testid="new-enum">
                  Create an enumerated type
                </DropdownMenuItem>
              </>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
        {canEdit && (
          <Button className="justify-start" icon={<Plus className="h-3.5 w-3.5" />} onClick={actions.onNewTable} data-testid="new-table">
            New table
          </Button>
        )}
      </div>
      <div className="flex items-center gap-1 px-3 pt-3">
        <div className="relative flex-1">
          <Search className="pointer-events-none absolute top-1/2 left-2 h-3.5 w-3.5 -translate-y-1/2 text-muted" />
          <input
            type="search"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Search tables..."
            aria-label="Search tables"
            className="h-7 w-full rounded-md border border-line-strong bg-surface-2 pr-2 pl-7 text-[12px] text-fg placeholder:text-muted focus:border-accent focus:outline-none"
          />
        </div>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button type="button" aria-label="Show object types" className="rounded-md p-1.5 text-muted hover:bg-surface-2 hover:text-fg">
              <ListFilter className="h-3.5 w-3.5" />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuLabel>Show</DropdownMenuLabel>
            {(["view", "materialized_view", "foreign_table"] as const).map((k) => (
              <DropdownMenuCheckboxItem key={k} checked={shown[k]} onCheckedChange={(v) => setShown({ ...shown, [k]: v === true })}>
                {kindLabel[k]}s
              </DropdownMenuCheckboxItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      <SectionLabel className="px-4 pt-3 pb-1">Tables ({tables.length})</SectionLabel>
      <nav aria-label="Tables" className="min-h-0 flex-1 overflow-y-auto px-2 pb-3" data-testid="schema-tree">
        {tables.length === 0 && <p className="px-2 py-2 text-[12px] text-muted">{q ? "No tables match your search" : "No tables in this schema"}</p>}
        {tables.map((t) => {
          const ref = { schema, table: t.name };
          const active = current?.schema === schema && current.table === t.name;
          const Icon = kindIcon[t.kind];
          const isTable = t.kind === "table" || t.kind === "partitioned_table";
          return (
            <div
              key={t.name}
              className={cx("group flex h-8 items-center rounded-md text-[13px] text-fg-light hover:bg-surface-2 hover:text-fg", active && "bg-surface-3 text-fg")}
            >
              <button type="button" onClick={() => actions.onOpen(ref)} aria-current={active || undefined} className="flex min-w-0 flex-1 items-center gap-2 px-2 text-left" title={`${kindLabel[t.kind]} ${t.name}`}>
                <Icon className="h-3.5 w-3.5 shrink-0 text-muted" strokeWidth={1.6} />
                <span className="truncate">{t.name}</span>
              </button>
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <button type="button" aria-label={`${t.name} actions`} className="mr-1 rounded p-0.5 text-muted opacity-0 group-hover:opacity-100 hover:bg-surface-3 hover:text-fg focus:opacity-100 data-[state=open]:opacity-100" data-testid={`table-menu-${t.name}`}>
                    <MoreVertical className="h-3.5 w-3.5" />
                  </button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="start" side="right">
                  {canEdit && isTable && (
                    <DropdownMenuItem icon={<Edit3 className="h-3.5 w-3.5" />} onSelect={() => actions.onEditTable(ref)}>
                      Edit table
                    </DropdownMenuItem>
                  )}
                  {canEdit && t.kind === "table" && (
                    <DropdownMenuItem icon={<Files className="h-3.5 w-3.5" />} onSelect={() => actions.onDuplicate(ref)}>
                      Duplicate table
                    </DropdownMenuItem>
                  )}
                  <DropdownMenuItem icon={<Copy className="h-3.5 w-3.5" />} onSelect={() => void navigator.clipboard?.writeText(t.name).catch(() => {})}>
                    Copy name
                  </DropdownMenuItem>
                  <DropdownMenuItem icon={<Download className="h-3.5 w-3.5" />} onSelect={() => actions.onExport(ref)}>
                    Export data as CSV
                  </DropdownMenuItem>
                  {canEdit && isTable && (
                    <>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem danger icon={<Trash2 className="h-3.5 w-3.5" />} onSelect={() => actions.onDelete(ref)}>
                        Delete table
                      </DropdownMenuItem>
                    </>
                  )}
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          );
        })}
      </nav>
    </aside>
  );
}
