import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api, errorMessage, type DbTable } from "../api/client";
import { RowEditor } from "../components/RowEditor";
import { NewObjectButtons, StructureEditor } from "../components/SchemaEditor";
import { Alert, Badge, Button, Card, Spinner, cx } from "../components/ui";
import { formatBytes } from "../lib/format";
import { useProject } from "./ProjectOverview";

const kindLabel: Record<DbTable["kind"], string> = {
  table: "table",
  partitioned_table: "partitioned",
  view: "view",
  materialized_view: "mat. view",
  foreign_table: "foreign",
};

/** The table editor: browse, filter and edit rows, and change the schema
 * (spec §8.6, V2 §4). */
export function ProjectTablesPage() {
  const { data: p } = useProject();
  const schema = useQuery({ queryKey: ["schema", p?.id], queryFn: () => api.schema(p!.id), enabled: !!p && p.status === "active" });
  const [sel, setSel] = useState<{ schema: string; table: string } | null>(null);
  const [filter, setFilter] = useState("");
  if (!p) return null;
  if (p.status !== "active") return <Alert>The project is {p.status}; the browser is available once it is active.</Alert>;
  if (schema.isPending) return <Spinner />;
  if (schema.isError) return <Alert>{errorMessage(schema.error)}</Alert>;

  const schemas = schema.data.schemas;
  const current = sel ?? (schemas[0]?.tables[0] ? { schema: schemas[0].name, table: schemas[0].tables[0].name } : null);
  const table = current && schemas.find((s) => s.name === current.schema)?.tables.find((t) => t.name === current.table);
  const f = filter.trim().toLowerCase();
  const canEdit = (p.my_role === "admin" || p.my_role === "developer") && !p.settings.console_read_only;

  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-[15rem_1fr]">
      <Card title="Schema" className="h-fit" actions={<Button variant="ghost" className="text-xs" onClick={() => schema.refetch()}>Refresh</Button>}>
        {canEdit && (
          <div className="mb-2">
            <NewObjectButtons
              projectId={p.id}
              schemas={schemas.map((x) => x.name)}
              onCreated={(sc, table) => {
                void schema.refetch();
                if (table) setSel({ schema: sc, table });
              }}
            />
          </div>
        )}
        <input
          type="search"
          placeholder="Filter tables"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          className="mb-2 w-full rounded-md border border-line bg-surface px-2 py-1 text-xs"
        />
        <nav aria-label="Tables" className="flex max-h-[40rem] flex-col gap-2 overflow-auto" data-testid="schema-tree">
          {schemas.map((s) => {
            const tables = s.tables.filter((t) => !f || t.name.toLowerCase().includes(f));
            if (f && tables.length === 0) return null;
            return (
              <details key={s.name} open>
                <summary className="cursor-pointer text-xs font-semibold text-muted">
                  {s.name} <span className="font-normal">({s.tables.length})</span>
                </summary>
                <ul className="mt-1">
                  {tables.map((t) => {
                    const active = current?.schema === s.name && current.table === t.name;
                    return (
                      <li key={t.name}>
                        <button
                          type="button"
                          onClick={() => setSel({ schema: s.name, table: t.name })}
                          aria-current={active || undefined}
                          className={cx(
                            "flex w-full items-center justify-between gap-2 rounded px-1.5 py-0.5 text-left text-sm hover:bg-surface-2",
                            active && "bg-surface-2 font-medium",
                          )}
                        >
                          <span className="truncate font-mono text-xs">{t.name}</span>
                          {t.kind !== "table" && <span className="text-[10px] text-muted">{kindLabel[t.kind]}</span>}
                        </button>
                      </li>
                    );
                  })}
                  {s.tables.length === 0 && <li className="px-1.5 text-xs text-muted">empty</li>}
                </ul>
              </details>
            );
          })}
        </nav>
      </Card>
      <div className="flex min-w-0 flex-col gap-4">
        {table && current ? (
          <TableView
            key={`${current.schema}.${current.table}`}
            projectId={p.id}
            schema={current.schema}
            t={table}
            canEdit={canEdit}
            onSelect={(name) => {
              void schema.refetch();
              setSel(name ? { schema: current.schema, table: name } : null);
            }}
          />
        ) : (
          <Card>
            <p className="text-sm text-muted">No tables yet. Create one with New table, or in the SQL console.</p>
          </Card>
        )}
      </div>
    </div>
  );
}

function TableView({ projectId, schema, t, canEdit, onSelect }: { projectId: string; schema: string; t: DbTable; canEdit: boolean; onSelect: (table: string | null) => void }) {
  const [tab, setTab] = useState<"rows" | "structure">("rows");
  const info = useQuery({ queryKey: ["table-info", projectId, schema, t.name], queryFn: () => api.tableInfo(projectId, schema, t.name) });
  return (
    <>
      <Card
        title={
          <span className="flex items-center gap-2 font-mono">
            {schema}.{t.name} <Badge>{kindLabel[t.kind]}</Badge>
          </span>
        }
        actions={
          <div className="flex gap-1" role="tablist">
            {(["rows", "structure"] as const).map((x) => (
              <Button key={x} role="tab" aria-selected={tab === x} variant={tab === x ? "primary" : "ghost"} className="text-xs" onClick={() => setTab(x)} data-testid={`tab-${x}`}>
                {x === "rows" ? "Rows" : "Structure"}
              </Button>
            ))}
          </div>
        }
      >
        <dl className="mb-3 grid grid-cols-[max-content_1fr] gap-x-6 gap-y-1 text-sm" data-testid="table-info">
          <dt className="text-muted">Rows (estimate)</dt>
          <dd>{t.row_estimate == null ? "not analyzed yet" : t.row_estimate.toLocaleString()}</dd>
          <dt className="text-muted">Size</dt>
          <dd>{formatBytes(t.size_bytes)}</dd>
          <dt className="text-muted">Structure</dt>
          <dd>
            {t.columns.length} columns, {t.indexes.length} indexes
          </dd>
          {t.primary_key.length > 0 && (
            <>
              <dt className="text-muted">Primary key</dt>
              <dd className="font-mono text-xs">{t.primary_key.join(", ")}</dd>
            </>
          )}
          {t.comment && (
            <>
              <dt className="text-muted">Comment</dt>
              <dd>{t.comment}</dd>
            </>
          )}
        </dl>
        {info.isPending ? (
          <Spinner />
        ) : info.isError ? (
          <Alert>{errorMessage(info.error)}</Alert>
        ) : tab === "rows" ? (
          <RowEditor projectId={projectId} schema={schema} table={t.name} info={info.data} />
        ) : (
          <StructureEditor projectId={projectId} info={info.data} canEdit={canEdit} onRenamed={(n) => onSelect(n)} onDropped={() => onSelect(null)} />
        )}
      </Card>
    </>
  );
}
