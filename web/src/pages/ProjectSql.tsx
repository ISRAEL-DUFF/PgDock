import { Link } from "@tanstack/react-router";
import { useRef, useState } from "react";
import { api, errorMessage, type SqlResult } from "../api/client";
import { ResultGrid } from "../components/ResultGrid";
import { SqlEditor, type SqlEditorHandle } from "../components/SqlEditor";
import { Alert, Badge, Button, Card, Select } from "../components/ui";
import { download, toCSV } from "../lib/csv";
import { clearHistory, loadHistory, newQueryId, pushHistory } from "../lib/sqlHistory";
import { useProject } from "./ProjectOverview";

/** Line and column of a 1-based character position in text. */
function lineCol(text: string, pos: number): string {
  const before = [...text].slice(0, pos - 1).join("");
  const lines = before.split("\n");
  return `line ${lines.length}, column ${lines[lines.length - 1].length + 1}`;
}

/** The SQL console (spec §8.5). */
export function ProjectSqlPage() {
  const { data: p } = useProject();
  const editor = useRef<SqlEditorHandle>(null);
  const [text, setText] = useState("");
  const [running, setRunning] = useState<{ id: string; query: string } | null>(null);
  const [result, setResult] = useState<{ query: string; res: SqlResult } | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [timeout, setTimeoutSec] = useState("30");
  const [readOnlyRun, setReadOnlyRun] = useState(false);
  const [history, setHistory] = useState<string[]>(() => (p ? loadHistory(p.id) : []));
  if (!p) return null;

  // Read-only members always run read-only (V2 §2.3).
  const projectReadOnly = p.settings.console_read_only || p.my_role === "read_only";
  const inactive = p.status !== "active";

  const run = async () => {
    const query = editor.current?.runnable() ?? text;
    if (!query.trim() || running) return;
    const id = newQueryId();
    setRunning({ id, query });
    setErr(null);
    setHistory(pushHistory(p.id, query));
    try {
      const res = await api.sql(p.id, { query, query_id: id, timeout_seconds: Number(timeout), read_only: readOnlyRun || projectReadOnly || undefined });
      setResult({ query, res });
    } catch (e) {
      setResult(null);
      setErr(errorMessage(e));
    } finally {
      setRunning(null);
    }
  };

  const cancel = async () => {
    if (!running) return;
    try {
      await api.cancelSql(p.id, running.id);
    } catch (e) {
      setErr(errorMessage(e));
    }
  };

  const res = result?.res;
  return (
    <div className="grid grid-cols-1 gap-4 xl:grid-cols-[1fr_16rem]">
      <div className="flex min-w-0 flex-col gap-4">
        <Alert tone="warn" title="Queries run against the live database">
          As <span className="font-mono">{p.owner_role}</span>, with that role's permissions. Results show at most 1,000 rows per statement.
          {projectReadOnly ? (
            <>
              {" "}
              This project's console is <strong>read-only</strong>: one statement at a time, inside a read-only transaction (
              <Link to="/projects/$id/settings" params={{ id: p.id }} className="underline">
                change in Settings
              </Link>
              ).
            </>
          ) : null}
        </Alert>
        {inactive && <Alert>The project is {p.status}; the console is available once it is active.</Alert>}
        <Card
          title={
            <span className="flex items-center gap-2">
              SQL console {projectReadOnly ? <Badge tone="accent">read-only</Badge> : <Badge tone="warn">read/write</Badge>}
            </span>
          }
        >
          <div className="flex flex-col gap-3">
            <SqlEditor ref={editor} value={text} onChange={setText} onRun={run} label="SQL" />
            <div className="flex flex-wrap items-center gap-3">
              <Button variant="primary" onClick={run} busy={!!running} disabled={inactive}>
                Run
              </Button>
              {running && (
                <Button variant="danger" onClick={cancel} data-testid="sql-cancel">
                  Cancel
                </Button>
              )}
              <span className="text-xs text-muted">Ctrl/Cmd+Enter runs the selection, or everything.</span>
              <div className="ml-auto flex items-center gap-3 text-sm">
                {!projectReadOnly && (
                  <label className="flex items-center gap-1.5">
                    <input type="checkbox" checked={readOnlyRun} onChange={(e) => setReadOnlyRun(e.target.checked)} />
                    Read-only
                  </label>
                )}
                <label className="flex items-center gap-1.5">
                  Timeout
                  <Select value={timeout} onChange={(e) => setTimeoutSec(e.target.value)} className="w-auto" aria-label="Timeout">
                    <option value="30">30 s</option>
                    <option value="60">1 min</option>
                    <option value="300">5 min</option>
                  </Select>
                </label>
              </div>
            </div>
          </div>
        </Card>

        {err && <Alert>{err}</Alert>}
        {res && (
          <div className="flex flex-col gap-3" data-testid="sql-results">
            <p className="text-xs text-muted">
              {res.results.length} statement{res.results.length === 1 ? "" : "s"} in {res.duration_ms} ms{res.read_only ? ", read-only" : ""}
            </p>
            {res.results.map((r, i) => (
              <Card
                key={i}
                title={<span className="font-mono text-xs">{r.command}</span>}
                actions={
                  r.columns.length > 0 && (
                    <Button
                      variant="ghost"
                      className="text-xs"
                      onClick={() =>
                        download(
                          `${p.db_name}-result-${i + 1}.csv`,
                          toCSV(
                            r.columns.map((c) => c.name),
                            r.rows,
                          ),
                        )
                      }
                    >
                      Export CSV
                    </Button>
                  )
                }
              >
                {r.columns.length > 0 ? (
                  <>
                    {r.truncated && (
                      <p className="mb-2 text-xs text-warn">
                        Showing the first {r.rows.length.toLocaleString()} of {r.row_count.toLocaleString()} rows.
                      </p>
                    )}
                    <ResultGrid columns={r.columns} rows={r.rows} testId="sql-grid" />
                  </>
                ) : (
                  <p className="text-sm text-muted">
                    {r.row_count > 0 ? `${r.row_count.toLocaleString()} row${r.row_count === 1 ? "" : "s"} affected.` : "Done."}
                  </p>
                )}
              </Card>
            ))}
            {res.error && (
              <div data-testid="sql-error">
                <Alert title={res.error.code ? `Error ${res.error.code}` : "Error"}>
                  <p>{res.error.message}</p>
                  {res.error.detail && <p className="mt-1">Detail: {res.error.detail}</p>}
                  {res.error.hint && <p className="mt-1">Hint: {res.error.hint}</p>}
                  {res.error.position && result && <p className="mt-1 text-xs">At {lineCol(result.query, res.error.position)}.</p>}
                </Alert>
              </div>
            )}
            {res.notices.length > 0 && (
              <Card title="Notices">
                <ul className="font-mono text-xs">
                  {res.notices.map((n, i) => (
                    <li key={i}>{n}</li>
                  ))}
                </ul>
              </Card>
            )}
          </div>
        )}
      </div>

      <Card
        title="History"
        className="h-fit"
        actions={
          history.length > 0 && (
            <button
              type="button"
              className="text-xs text-muted hover:text-fg"
              onClick={() => {
                clearHistory(p.id);
                setHistory([]);
              }}
            >
              Clear
            </button>
          )
        }
      >
        {history.length === 0 ? (
          <p className="text-xs text-muted">Queries you run are kept in this browser only.</p>
        ) : (
          <ul className="flex max-h-[36rem] flex-col gap-1 overflow-auto">
            {history.map((h, i) => (
              <li key={i}>
                <button
                  type="button"
                  title={h}
                  onClick={() => {
                    setText(h);
                    editor.current?.focus();
                  }}
                  className="w-full truncate rounded px-1.5 py-1 text-left font-mono text-xs text-muted hover:bg-surface-2 hover:text-fg"
                >
                  {h.replace(/\s+/g, " ")}
                </button>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </div>
  );
}
