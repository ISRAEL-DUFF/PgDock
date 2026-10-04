import { AlertCircle, Download, Loader2, X } from "lucide-react";
import { useMemo, useState } from "react";
import { DataGrid, type Column } from "react-data-grid";
import "react-data-grid/lib/styles.css";
import type { SqlResult } from "../../api/client";
import { download, toCSV } from "../../lib/csv";
import { positionAt } from "../../lib/sqlEditor/complete";
import { Badge, Button, DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger, cx } from "../ui";

type Statement = SqlResult["results"][number];
type Row = { i: number; v: (string | null)[] };

/** One statement's rows, read-only, in the table editor's grid. */
function StatementGrid({ r }: { r: Statement }) {
  const columns = useMemo<Column<Row>[]>(
    () => [
      { key: "__n", name: "", width: 44, frozen: true, renderCell: ({ row }) => <span className="text-muted">{row.i + 1}</span> },
      ...r.columns.map((c, j) => ({
        key: String(j),
        name: c.name,
        resizable: true,
        // Wide enough for the header and the first rows' values.
        width: Math.min(Math.max(c.name.length * 8 + c.type.length * 7 + 48, ...r.rows.slice(0, 50).map((row) => (row[j]?.length ?? 4) * 7.5 + 24), 100), 360),
        renderHeaderCell: () => (
          <span className="flex items-center gap-1.5 px-2">
            <span className="truncate text-[12px] font-medium">{c.name}</span>
            <span className="truncate font-mono text-[11px] text-muted">{c.type}</span>
          </span>
        ),
        renderCell: ({ row }: { row: Row }) => {
          const v = row.v[j];
          return (
            <span className={cx(v == null && "text-muted")} title={v ?? "NULL"}>
              {v ?? "NULL"}
            </span>
          );
        },
      })),
    ],
    [r.columns, r.rows],
  );
  const rows = useMemo(() => r.rows.map((v, i) => ({ i, v: v.map((x) => x ?? null) })), [r.rows]);
  return (
    <div className="min-h-0 flex-1" data-testid="sql-grid">
      <DataGrid className="pgdock-grid" columns={columns} rows={rows} rowKeyGetter={(x) => x.i} rowHeight={30} headerRowHeight={32} aria-label="Results" />
    </div>
  );
}

/** The results of a run: each statement's rows or what it did, notices,
 * and the error with where it happened. */
export function ResultsPane({
  result,
  error,
  running,
  fileName,
  onCancel,
}: {
  result: { query: string; res: SqlResult } | null;
  error: string | null;
  running: boolean;
  fileName: string;
  onCancel: () => void;
}) {
  const [picked, setPicked] = useState<number | null>(null);
  const [tab, setTab] = useState<"results" | "notices">("results");
  const res = result?.res;
  const withRows = res ? res.results.map((r, i) => ({ r, i })).filter((x) => x.r.columns.length > 0) : [];
  // As Studio: the last statement that returned rows, unless one is picked.
  const idx = picked != null && res && picked < res.results.length ? picked : (withRows.at(-1)?.i ?? (res ? res.results.length - 1 : -1));
  const cur = res && idx >= 0 ? res.results[idx] : undefined;

  const exportAs = (fmt: "csv" | "json") => {
    if (!cur) return;
    const names = cur.columns.map((c) => c.name);
    if (fmt === "csv") download(`${fileName}.csv`, toCSV(names, cur.rows));
    else
      download(
        `${fileName}.json`,
        JSON.stringify(
          cur.rows.map((row) => Object.fromEntries(names.map((n, j) => [n, row[j] ?? null]))),
          null,
          2,
        ),
      );
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-bg" data-testid="sql-results">
      <div className="flex h-9 shrink-0 items-center gap-2 border-b border-line bg-surface px-3 text-[12px]">
        <div className="flex items-center gap-1" role="tablist">
          {(["results", "notices"] as const).map((t) => (
            <button
              key={t}
              type="button"
              role="tab"
              aria-selected={tab === t}
              onClick={() => setTab(t)}
              className={cx("rounded px-2 py-1 capitalize", tab === t ? "bg-surface-3 text-fg" : "text-muted hover:text-fg")}
            >
              {t}
              {t === "notices" && res && res.notices.length > 0 && <span className="ml-1 text-muted">({res.notices.length})</span>}
            </button>
          ))}
        </div>
        {res && res.results.length > 1 && tab === "results" && (
          <div className="flex items-center gap-1 overflow-x-auto" aria-label="Statements">
            {res.results.map((r, i) => (
              <button
                key={i}
                type="button"
                onClick={() => setPicked(i)}
                className={cx(
                  "rounded border px-1.5 py-0.5 font-mono text-[11px] whitespace-nowrap",
                  i === idx ? "border-accent text-fg" : "border-line text-muted hover:text-fg",
                )}
              >
                {i + 1}. {r.command}
              </button>
            ))}
          </div>
        )}
        <span className="flex-1" />
        {running ? (
          <>
            <Loader2 className="h-3.5 w-3.5 animate-spin text-muted" />
            <span className="text-muted">Running…</span>
            <Button size="tiny" variant="danger" icon={<X className="h-3.5 w-3.5" />} onClick={onCancel} data-testid="sql-cancel">
              Cancel
            </Button>
          </>
        ) : res ? (
          <>
            {res.read_only && <Badge tone="accent">read-only</Badge>}
            <span className="text-muted" data-testid="sql-summary">
              {res.results.length} statement{res.results.length === 1 ? "" : "s"} in {res.duration_ms} ms
            </span>
            {cur && cur.columns.length > 0 && (
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button size="tiny" variant="ghost" icon={<Download className="h-3.5 w-3.5" />}>
                    Export
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  <DropdownMenuItem onSelect={() => exportAs("csv")}>Download CSV</DropdownMenuItem>
                  <DropdownMenuItem onSelect={() => exportAs("json")}>Download JSON</DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            )}
          </>
        ) : null}
      </div>

      {tab === "notices" ? (
        <div className="min-h-0 flex-1 overflow-auto p-3 font-mono text-[12px]">
          {res && res.notices.length > 0 ? res.notices.map((n, i) => <p key={i}>{n}</p>) : <p className="text-muted">No notices.</p>}
        </div>
      ) : error ? (
        <div className="p-4" data-testid="sql-error">
          <ErrorBlock message={error} />
        </div>
      ) : !res ? (
        <div className="flex flex-1 items-center justify-center text-[13px] text-muted">{running ? "Running…" : "Click Run to execute your query."}</div>
      ) : (
        <div className="flex min-h-0 flex-1 flex-col">
          {res.error ? (
            <div className="overflow-auto p-4" data-testid="sql-error">
              <ErrorBlock
                message={`ERROR: ${res.error.code ? `${res.error.code}: ` : ""}${res.error.message}`}
                detail={res.error.detail}
                hint={res.error.hint}
                where={res.error.position && result ? positionAt(result.query, res.error.position) : undefined}
              />
              {res.results.length > 0 && (
                <p className="mt-3 text-[12px] text-muted">The statements before it ran; see their results above the error with the statement buttons.</p>
              )}
            </div>
          ) : cur && cur.columns.length > 0 ? (
            <>
              {cur.truncated && (
                <p className="shrink-0 border-b border-line bg-warn/10 px-3 py-1.5 text-[12px] text-warn-text">
                  Showing the first {cur.rows.length.toLocaleString()} of {cur.row_count.toLocaleString()} rows.
                </p>
              )}
              <StatementGrid key={idx} r={cur} />
              <div className="shrink-0 border-t border-line px-3 py-1.5 text-[12px] text-muted">
                {cur.row_count.toLocaleString()} row{cur.row_count === 1 ? "" : "s"}
              </div>
            </>
          ) : (
            <div className="p-4 text-[13px] text-fg-light">
              {cur && cur.row_count > 0
                ? `Success. ${cur.row_count.toLocaleString()} row${cur.row_count === 1 ? "" : "s"} affected.`
                : "Success. No rows returned."}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function ErrorBlock({ message, detail, hint, where }: { message: string; detail?: string; hint?: string; where?: { line: number; column: number } }) {
  return (
    <div className="flex gap-3 rounded-md border border-danger/40 bg-danger/10 p-3 font-mono text-[12px]">
      <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-danger-text" />
      <div className="flex flex-col gap-1">
        <p className="text-danger-text">{message}</p>
        {detail && <p>DETAIL: {detail}</p>}
        {hint && <p>HINT: {hint}</p>}
        {where && (
          <p className="text-muted">
            LINE {where.line}, COLUMN {where.column}
          </p>
        )}
      </div>
    </div>
  );
}
