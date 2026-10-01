import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api, errorMessage, type DbTable } from "../api/client";
import { ResultGrid } from "../components/ResultGrid";
import { Alert, Badge, Button, Card, Spinner, Table, cx } from "../components/ui";
import { formatBytes } from "../lib/format";
import { useProject } from "./ProjectOverview";

const kindLabel: Record<DbTable["kind"], string> = {
  table: "table",
  partitioned_table: "partitioned",
  view: "view",
  materialized_view: "mat. view",
  foreign_table: "foreign",
};

/** The read-only table browser (spec §8.6). */
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

  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-[15rem_1fr]">
      <Card title="Schema" className="h-fit" actions={<Button variant="ghost" className="text-xs" onClick={() => schema.refetch()}>Refresh</Button>}>
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
          <TableView key={`${current.schema}.${current.table}`} projectId={p.id} schema={current.schema} t={table} />
        ) : (
          <Card>
            <p className="text-sm text-muted">No tables yet. Create some in the SQL console.</p>
          </Card>
        )}
      </div>
    </div>
  );
}

function TableView({ projectId, schema, t }: { projectId: string; schema: string; t: DbTable }) {
  // Cursors of the pages so far, for Previous.
  const [cursors, setCursors] = useState<(string | undefined)[]>([undefined]);
  const after = cursors[cursors.length - 1];
  const rows = useQuery({
    queryKey: ["rows", projectId, schema, t.name, after],
    queryFn: () => api.tableRows(projectId, schema, t.name, after),
  });
  const page = cursors.length;
  return (
    <>
      <Card
        title={
          <span className="flex items-center gap-2 font-mono">
            {schema}.{t.name} <Badge>{kindLabel[t.kind]}</Badge>
          </span>
        }
      >
        <dl className="mb-3 grid grid-cols-[max-content_1fr] gap-x-6 gap-y-1 text-sm" data-testid="table-info">
          <dt className="text-muted">Rows (estimate)</dt>
          <dd>{t.row_estimate == null ? "not analyzed yet" : t.row_estimate.toLocaleString()}</dd>
          <dt className="text-muted">Size</dt>
          <dd>{formatBytes(t.size_bytes)}</dd>
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
        <details>
          <summary className="cursor-pointer text-sm font-medium">
            {t.columns.length} columns, {t.indexes.length} indexes
          </summary>
          <div className="mt-2 flex flex-col gap-3">
            <Table head={["Column", "Type", "Nullable", "Default"]}>
              {t.columns.map((c) => (
                <tr key={c.name}>
                  <td className="px-3 py-1 font-mono text-xs">{c.name}</td>
                  <td className="px-3 py-1 font-mono text-xs">{c.type}</td>
                  <td className="px-3 py-1 text-xs">{c.nullable ? "yes" : "no"}</td>
                  <td className="px-3 py-1 font-mono text-xs text-muted">{c.default ?? ""}</td>
                </tr>
              ))}
            </Table>
            {t.indexes.length > 0 && (
              <Table head={["Index", "Definition"]}>
                {t.indexes.map((i) => (
                  <tr key={i.name}>
                    <td className="px-3 py-1 font-mono text-xs">
                      {i.name} {i.primary ? <Badge tone="accent">primary</Badge> : i.unique ? <Badge>unique</Badge> : null}
                    </td>
                    <td className="px-3 py-1 font-mono text-xs text-muted">{i.definition}</td>
                  </tr>
                ))}
              </Table>
            )}
          </div>
        </details>
      </Card>
      <Card
        title="Data"
        actions={
          <div className="flex items-center gap-2">
            <span className="text-xs text-muted" data-testid="page-number">
              Page {page}
            </span>
            <Button className="text-xs" disabled={page === 1} onClick={() => setCursors((c) => c.slice(0, -1))}>
              Previous
            </Button>
            <Button
              className="text-xs"
              disabled={!rows.data?.next}
              onClick={() => rows.data?.next && setCursors((c) => [...c, rows.data.next])}
            >
              Next
            </Button>
          </div>
        }
      >
        {rows.isPending ? (
          <Spinner />
        ) : rows.isError ? (
          <Alert>{errorMessage(rows.error)}</Alert>
        ) : (
          <>
            <ResultGrid columns={rows.data.columns} rows={rows.data.rows} offset={(page - 1) * 50} testId="table-grid" />
            {rows.data.order !== "primary_key" && (
              <p className="mt-2 text-xs text-muted">
                No primary key: pages follow {rows.data.order === "ctid" ? "physical row order" : "the view's own order"}, which may shift while it changes.
              </p>
            )}
          </>
        )}
      </Card>
    </>
  );
}
