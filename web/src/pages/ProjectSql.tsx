import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Copy, FileCode2, Play, Plus, Star, Wand2, X } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { api, errorMessage, type Project, type SavedQuery, type SqlResult } from "../api/client";
import { CodeEditor, type CodeEditorHandle } from "../components/sqlEditor/CodeEditor";
import { QuerySidebar } from "../components/sqlEditor/QuerySidebar";
import { ResultsPane } from "../components/sqlEditor/ResultsPane";
import { Alert, Badge, Button, Dialog, Input, Select, Spinner, Tooltip, cx, toast } from "../components/ui";
import { clearHistory, loadHistory, newQueryId, pushHistory } from "../lib/sqlHistory";
import type { Template } from "../lib/sqlEditor/templates";
import { useProject } from "./ProjectOverview";

const SPLIT_KEY = "pgdock.sql.split";
const tabsKey = (p: string) => `pgdock.sql.tabs.${p}`;

function readJSON<T>(key: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(key);
    return raw ? (JSON.parse(raw) as T) : fallback;
  } catch {
    return fallback;
  }
}

function writeJSON(key: string, v: unknown) {
  try {
    localStorage.setItem(key, JSON.stringify(v));
  } catch {
    /* storage unavailable */
  }
}

/** A name not yet taken: "Untitled query", "Untitled query 2"… */
function freshName(base: string, taken: string[]): string {
  if (!taken.includes(base)) return base;
  for (let i = 2; ; i++) if (!taken.includes(`${base} ${i}`)) return `${base} ${i}`;
}

/** The SQL Editor (docs/ui-redesign.md, phase 3), as Studio's: saved
 * queries in a sidebar, open ones as tabs, Monaco, and the results below. */
export function ProjectSqlPage() {
  const { data: p } = useProject();
  if (!p) return null;
  return <SqlEditorPage p={p} />;
}

type Run = { query: string; res: SqlResult };

function SqlEditorPage({ p }: { p: Project }) {
  const qc = useQueryClient();
  const list = useQuery({ queryKey: ["queries", p.id], queryFn: () => api.savedQueries(p.id) });
  const tree = useQuery({ queryKey: ["schema", p.id], queryFn: () => api.schema(p.id), enabled: p.status === "active" });
  const editor = useRef<CodeEditorHandle>(null);

  const [tabs, setTabsState] = useState<{ ids: string[]; active: string | null }>(() => readJSON(tabsKey(p.id), { ids: [], active: null }));
  const setTabs = (t: { ids: string[]; active: string | null }) => {
    setTabsState(t);
    writeJSON(tabsKey(p.id), t);
  };
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const [saving, setSaving] = useState<Record<string, "saving" | "saved" | "error">>({});
  const [runs, setRuns] = useState<Record<string, Run | undefined>>({});
  const [errors, setErrors] = useState<Record<string, string | null>>({});
  const [running, setRunning] = useState<{ tab: string; id: string } | null>(null);
  const [timeout, setTimeoutSec] = useState("30");
  const [readOnlyRun, setReadOnlyRun] = useState(false);
  const [history, setHistory] = useState<string[]>(() => loadHistory(p.id));
  const [split, setSplit] = useState<number>(() => readJSON(SPLIT_KEY, 50));
  const [dialog, setDialog] = useState<null | { kind: "rename"; q: SavedQuery } | { kind: "delete"; q: SavedQuery }>(null);

  const queries = list.data?.items ?? [];
  const byId = new Map(queries.map((q) => [q.id, q]));
  const openIds = tabs.ids.filter((id) => byId.has(id));
  const active = tabs.active && byId.has(tabs.active) ? byId.get(tabs.active)! : null;
  const projectReadOnly = p.settings.console_read_only || p.my_role === "read_only";
  const canAdminShared = p.my_role === "admin";
  const inactive = p.status !== "active";

  const setQueryInCache = useCallback(
    (q: SavedQuery) => qc.setQueryData<{ items: SavedQuery[] }>(["queries", p.id], (old) => old && { items: old.items.map((x) => (x.id === q.id ? q : x)) }),
    [qc, p.id],
  );
  const open = (q: SavedQuery) => setTabs({ ids: tabs.ids.includes(q.id) ? tabs.ids : [...tabs.ids, q.id], active: q.id });
  const close = (id: string) => {
    const i = openIds.indexOf(id);
    const ids = tabs.ids.filter((x) => x !== id);
    const rest = openIds.filter((x) => x !== id);
    setTabs({ ids, active: tabs.active === id ? (rest[Math.min(i, rest.length - 1)] ?? null) : tabs.active });
  };
  const create = async (name: string, sql: string) => {
    try {
      const q = await api.createSavedQuery(p.id, { name: freshName(name, queries.map((x) => x.name)), sql, visibility: "private" });
      qc.setQueryData<{ items: SavedQuery[] }>(["queries", p.id], (old) => ({ items: [q, ...(old?.items ?? [])] }));
      setTabs({ ids: [...tabs.ids, q.id], active: q.id });
      setTimeout(() => editor.current?.focus(), 50);
    } catch (e) {
      toast.error(errorMessage(e));
    }
  };

  // Each query's text saves a moment after typing stops, whichever tab is
  // showing by then; leaving the page saves what is pending at once.
  const pending = useRef(new Map<string, { timer: ReturnType<typeof setTimeout>; sql: string }>());
  const saveNow = useCallback(
    async (id: string, sql: string) => {
      pending.current.delete(id);
      try {
        setQueryInCache(await api.updateSavedQuery(p.id, id, { sql }));
        setSaving((s) => ({ ...s, [id]: "saved" }));
      } catch {
        setSaving((s) => ({ ...s, [id]: "error" }));
      }
    },
    [p.id, setQueryInCache],
  );
  const edit = (q: SavedQuery, sql: string) => {
    setDrafts((d) => ({ ...d, [q.id]: sql }));
    if (!q.mine) return;
    const prev = pending.current.get(q.id);
    if (prev) clearTimeout(prev.timer);
    setSaving((s) => ({ ...s, [q.id]: "saving" }));
    pending.current.set(q.id, { sql, timer: setTimeout(() => void saveNow(q.id, sql), 800) });
  };
  useEffect(() => {
    const all = pending.current;
    return () => {
      for (const [id, { timer, sql }] of all) {
        clearTimeout(timer);
        void saveNow(id, sql);
      }
    };
  }, [saveNow]);

  const run = async () => {
    if (!active || running || inactive) return;
    const query = editor.current?.runnable() ?? drafts[active.id] ?? active.sql;
    if (!query.trim()) return;
    const tab = active.id;
    const id = newQueryId();
    setRunning({ tab, id });
    setErrors((e) => ({ ...e, [tab]: null }));
    editor.current?.markError(null);
    setHistory(pushHistory(p.id, query));
    try {
      const res = await api.sql(p.id, { query, query_id: id, timeout_seconds: Number(timeout), read_only: readOnlyRun || projectReadOnly || undefined });
      setRuns((r) => ({ ...r, [tab]: { query, res } }));
      // Mark the error in the editor when the whole document ran.
      if (res.error?.position && query === editor.current?.runnable()) editor.current?.markError(res.error.position, res.error.message);
    } catch (e) {
      setRuns((r) => ({ ...r, [tab]: undefined }));
      setErrors((x) => ({ ...x, [tab]: errorMessage(e) }));
    } finally {
      setRunning(null);
    }
  };
  const cancel = async () => {
    if (!running) return;
    try {
      await api.cancelSql(p.id, running.id);
    } catch (e) {
      toast.error(errorMessage(e));
    }
  };

  // The split between editor and results, dragged.
  const area = useRef<HTMLDivElement>(null);
  const drag = (e: React.PointerEvent) => {
    const box = area.current?.getBoundingClientRect();
    if (!box) return;
    const move = (ev: PointerEvent) => setSplit(Math.min(85, Math.max(15, ((ev.clientY - box.top) / box.height) * 100)));
    const up = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
      setSplit((s) => {
        writeJSON(SPLIT_KEY, Math.round(s));
        return s;
      });
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
    e.preventDefault();
  };

  const update = async (q: SavedQuery, patch: { name?: string; visibility?: "private" | "shared" }) => {
    try {
      setQueryInCache(await api.updateSavedQuery(p.id, q.id, patch));
    } catch (e) {
      toast.error(errorMessage(e));
    }
  };
  const favorite = async (q: SavedQuery, fav: boolean) => {
    try {
      setQueryInCache(await api.favoriteSavedQuery(p.id, q.id, fav));
    } catch (e) {
      toast.error(errorMessage(e));
    }
  };

  if (list.isPending) return <Spinner />;
  if (list.isError) return <Alert>{errorMessage(list.error)}</Alert>;

  const status = active ? saving[active.id] : undefined;
  return (
    <div className="flex min-h-0 flex-1" data-testid="sql-editor-page">
      <QuerySidebar
        queries={queries}
        activeId={active?.id ?? null}
        canAdminShared={canAdminShared}
        history={history}
        actions={{
          onOpen: open,
          onNew: () => void create("Untitled query", ""),
          onTemplate: (t: Template) => void create(t.title, t.sql),
          onHistory: (sql) => void create("Untitled query", sql),
          onRename: (q) => setDialog({ kind: "rename", q }),
          onShare: (q, shared) => void update(q, { visibility: shared ? "shared" : "private" }).then(() => toast.success(shared ? `Shared ${q.name} with the project` : `${q.name} is private`)),
          onFavorite: (q, fav) => void favorite(q, fav),
          onDuplicate: (q) => void create(`${q.name} (copy)`, drafts[q.id] ?? q.sql),
          onDelete: (q) => setDialog({ kind: "delete", q }),
          onClearHistory: () => {
            clearHistory(p.id);
            setHistory([]);
          },
        }}
      />
      <div className="flex min-w-0 flex-1 flex-col">
        <div className="flex h-10 shrink-0 items-end gap-px overflow-x-auto border-b border-line bg-surface px-1" role="tablist" aria-label="Open queries">
          {openIds.map((id) => {
            const q = byId.get(id)!;
            const on = id === active?.id;
            return (
              <div key={id} className={cx("group flex h-9 max-w-[14rem] items-center gap-1.5 rounded-t-md border border-b-0 px-3 text-[12px]", on ? "border-line bg-bg text-fg" : "border-transparent text-muted hover:text-fg")}>
                <button type="button" role="tab" aria-selected={on} className="flex min-w-0 items-center gap-1.5" onClick={() => setTabs({ ...tabs, active: id })}>
                  <FileCode2 className="h-3.5 w-3.5 shrink-0" strokeWidth={1.6} />
                  <span className="truncate">{q.name}</span>
                </button>
                <button type="button" aria-label={`Close ${q.name}`} className="rounded p-0.5 opacity-0 group-hover:opacity-100 hover:bg-surface-3" onClick={() => close(id)}>
                  <X className="h-3 w-3" />
                </button>
              </div>
            );
          })}
          <button type="button" aria-label="New query" className="mb-1 ml-1 rounded p-1 text-muted hover:bg-surface-2 hover:text-fg" onClick={() => void create("Untitled query", "")}>
            <Plus className="h-4 w-4" />
          </button>
        </div>

        {!active ? (
          <div className="flex flex-1 items-center justify-center p-6">
            <div className="flex max-w-sm flex-col items-center gap-3 text-center">
              <FileCode2 className="h-8 w-8 text-muted" strokeWidth={1.4} />
              <p className="text-[14px]">Write SQL against your database, and save it for later.</p>
              <p className="text-[13px] text-muted">Queries run against the live database as {p.owner_role}.</p>
              <Button variant="primary" icon={<Plus className="h-3.5 w-3.5" />} onClick={() => void create("Untitled query", "")} data-testid="create-query">
                New query
              </Button>
            </div>
          </div>
        ) : (
          <>
            <div className="flex h-11 shrink-0 items-center gap-2 border-b border-line bg-bg px-3">
              <button type="button" className="truncate text-[13px] text-fg hover:underline disabled:no-underline" onClick={() => active.mine && setDialog({ kind: "rename", q: active })} disabled={!active.mine} data-testid="query-name">
                {active.name}
              </button>
              {active.visibility === "shared" && <Badge tone="accent">shared</Badge>}
              <span className="text-[11px] text-muted" data-testid="save-status">
                {!active.mine ? "Read-only: someone else's query" : status === "saving" ? "Saving…" : status === "error" ? "Not saved" : "Saved"}
              </span>
              <Tooltip content={active.favorite ? "Remove from favorites" : "Add to favorites"}>
                <button type="button" aria-label={active.favorite ? "Remove from favorites" : "Add to favorites"} onClick={() => void favorite(active, !active.favorite)} className="rounded p-1 text-muted hover:text-fg" data-testid="favorite-toggle">
                  <Star className={cx("h-3.5 w-3.5", active.favorite && "fill-warn text-warn")} />
                </button>
              </Tooltip>
              {!active.mine && (
                <Button size="tiny" icon={<Copy className="h-3.5 w-3.5" />} onClick={() => void create(`${active.name} (copy)`, active.sql)}>
                  Duplicate to edit
                </Button>
              )}
              <span className="flex-1" />
              <Button size="tiny" variant="ghost" icon={<Wand2 className="h-3.5 w-3.5" />} onClick={() => editor.current?.format()} disabled={!active.mine} shortcut="⌘⇧F">
                Format
              </Button>
              {projectReadOnly ? (
                <Tooltip content={p.my_role === "read_only" ? "Your role on this project is read-only." : "This project's console is read-only (change in Settings)."}>
                  <span>
                    <Badge tone="accent">read-only</Badge>
                  </span>
                </Tooltip>
              ) : (
                <Select aria-label="Role" value={readOnlyRun ? "ro" : "rw"} onChange={(e) => setReadOnlyRun(e.target.value === "ro")} className="h-7 text-[12px]" data-testid="run-role">
                  <option value="rw">{p.owner_role} (read/write)</option>
                  <option value="ro">read-only</option>
                </Select>
              )}
              <Select aria-label="Timeout" value={timeout} onChange={(e) => setTimeoutSec(e.target.value)} className="h-7 text-[12px]">
                <option value="30">30 s</option>
                <option value="60">1 min</option>
                <option value="300">5 min</option>
              </Select>
              <Button variant="primary" size="tiny" icon={<Play className="h-3.5 w-3.5" />} shortcut="⌘↵" busy={running?.tab === active.id} disabled={inactive || !!running} onClick={() => void run()} data-testid="run-query">
                Run
              </Button>
            </div>
            {inactive && (
              <div className="px-3 pt-2">
                <Alert>The project is {p.status}; queries run once it is active.</Alert>
              </div>
            )}
            {projectReadOnly && p.settings.console_read_only && (
              <p className="shrink-0 border-b border-line bg-accent-soft px-3 py-1.5 text-[12px] text-fg-light">
                This project's console is read-only: one statement at a time, inside a read-only transaction (
                <Link to="/projects/$id/settings" params={{ id: p.id }} className="underline">
                  change in Settings
                </Link>
                ).
              </p>
            )}
            <div ref={area} className="flex min-h-0 flex-1 flex-col">
              <div style={{ height: `${split}%` }} className="min-h-0">
                <CodeEditor
                  key={active.id}
                  ref={editor}
                  value={drafts[active.id] ?? active.sql}
                  onChange={(v) => edit(active, v)}
                  onRun={() => void run()}
                  readOnly={!active.mine}
                  schema={tree.data}
                  label="SQL"
                  testId="sql-editor"
                />
              </div>
              <div role="separator" aria-orientation="horizontal" aria-label="Resize the results" onPointerDown={drag} className="h-1.5 shrink-0 cursor-row-resize border-y border-line bg-surface hover:bg-accent/40" />
              <ResultsPane
                key={active.id}
                result={runs[active.id] ?? null}
                error={errors[active.id] ?? null}
                running={running?.tab === active.id}
                fileName={`${p.db_name}-${active.name.replace(/[^\w-]+/g, "_")}`}
                onCancel={() => void cancel()}
              />
            </div>
          </>
        )}
      </div>

      {dialog?.kind === "rename" && <RenameDialog q={dialog.q} onClose={() => setDialog(null)} onSave={(name) => void update(dialog.q, { name }).then(() => setDialog(null))} />}
      {dialog?.kind === "delete" && (
        <Dialog
          open
          onOpenChange={(o) => !o && setDialog(null)}
          title={`Delete ${dialog.q.name}`}
          description={dialog.q.visibility === "shared" ? "It is shared: it disappears for everyone in the project." : "This can't be undone."}
          footer={
            <>
              <Button onClick={() => setDialog(null)}>Cancel</Button>
              <Button
                variant="danger"
                data-testid="confirm-delete-query"
                onClick={async () => {
                  const q = dialog.q;
                  try {
                    await api.deleteSavedQuery(p.id, q.id);
                    qc.setQueryData<{ items: SavedQuery[] }>(["queries", p.id], (old) => old && { items: old.items.filter((x) => x.id !== q.id) });
                    close(q.id);
                    setDialog(null);
                    toast.success(`Deleted ${q.name}`);
                  } catch (e) {
                    toast.error(errorMessage(e));
                  }
                }}
              >
                Delete
              </Button>
            </>
          }
        />
      )}
    </div>
  );
}

function RenameDialog({ q, onClose, onSave }: { q: SavedQuery; onClose: () => void; onSave: (name: string) => void }) {
  const [name, setName] = useState(q.name);
  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onClose()}
      title="Rename query"
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" disabled={!name.trim()} onClick={() => onSave(name.trim())} data-testid="rename-save">
            Rename
          </Button>
        </>
      }
    >
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (name.trim()) onSave(name.trim());
        }}
      >
        <Input value={name} onChange={(e) => setName(e.target.value)} autoFocus aria-label="Name" data-testid="rename-input" />
      </form>
    </Dialog>
  );
}
