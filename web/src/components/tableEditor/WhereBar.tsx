import { History, ListFilter, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import type { TableInfo } from "../../api/client";
import { applySuggestion, suggest, type Suggestion } from "../../lib/tableEditor/whereBar";
import { cx, DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger } from "../ui";

/** Where a Postgres error sits in the typed condition (1-based). */
export type WhereError = { message: string; hint?: string; position?: number };

/**
 * The SQL filter bar (as DBeaver's): a condition typed after WHERE, with
 * suggestions for the table's columns, operators and functions. Enter
 * applies it; the server checks it and runs it read-only.
 */
export function WhereBar({
  info,
  applied,
  error,
  recent,
  onApply,
}: {
  info: TableInfo;
  /** The condition the grid is showing. */
  applied: string;
  error: WhereError | null;
  recent: string[];
  onApply: (expr: string) => void;
}) {
  const [draft, setDraft] = useState(applied);
  const [open, setOpen] = useState<Suggestion[]>([]);
  const [active, setActive] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  // What is applied changes under the draft, but not when the server has
  // just refused the draft: it stays, with the message, to be fixed.
  useEffect(() => {
    if (!error) setDraft(applied);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [applied]);

  const refresh = (text: string, force = false) => {
    const caret = input.current?.selectionStart ?? text.length;
    const s = suggest(text, caret, info.columns, force);
    setOpen(s);
    setActive(0);
  };
  const accept = (s: Suggestion) => {
    const el = input.current;
    const caret = el?.selectionStart ?? draft.length;
    const r = applySuggestion(draft, caret, s);
    setDraft(r.text);
    setOpen([]);
    requestAnimationFrame(() => {
      el?.focus();
      el?.setSelectionRange(r.caret, r.caret);
      refresh(r.text);
    });
  };
  const apply = (text = draft) => {
    setOpen([]);
    onApply(text.trim());
  };
  const sel = open[active];

  return (
    <div className="border-b border-line bg-surface px-3 py-1.5" data-testid="where-bar">
      <div className="relative flex items-center gap-2">
        <ListFilter className="h-3.5 w-3.5 shrink-0 text-muted" aria-hidden />
        <input
          ref={input}
          value={draft}
          spellCheck={false}
          autoComplete="off"
          aria-label="Filter rows with an SQL condition"
          aria-invalid={!!error}
          aria-expanded={open.length > 0}
          aria-controls="where-suggestions"
          role="combobox"
          placeholder="Filter rows with SQL, e.g. email = 'a@b.com' or phone like '081%'   (Ctrl+Space for suggestions)"
          onChange={(e) => {
            setDraft(e.target.value);
            refresh(e.target.value);
          }}
          onBlur={() => setTimeout(() => setOpen([]), 120)}
          onKeyDown={(e) => {
            if (e.key === " " && e.ctrlKey) {
              e.preventDefault();
              refresh(draft, true);
            } else if (open.length > 0 && e.key === "ArrowDown") {
              e.preventDefault();
              setActive((a) => (a + 1) % open.length);
            } else if (open.length > 0 && e.key === "ArrowUp") {
              e.preventDefault();
              setActive((a) => (a - 1 + open.length) % open.length);
            } else if (open.length > 0 && (e.key === "Tab" || e.key === "Enter")) {
              e.preventDefault();
              accept(open[active]);
            } else if (e.key === "Escape" && open.length > 0) {
              e.preventDefault();
              e.stopPropagation();
              setOpen([]);
            } else if (e.key === "Enter") {
              e.preventDefault();
              apply();
            }
          }}
          className={cx(
            "h-7 min-w-0 flex-1 rounded-md border bg-surface-2 px-2 font-mono text-[12px] text-fg placeholder:font-sans placeholder:text-muted",
            "focus:ring-2 focus:ring-accent-soft focus:outline-none",
            error ? "border-danger focus:border-danger" : "border-line-strong focus:border-accent",
          )}
          data-testid="where-input"
        />
        {(draft || applied) && (
          <button
            type="button"
            aria-label="Clear the filter"
            onClick={() => {
              setDraft("");
              setOpen([]);
              onApply("");
            }}
            className="flex h-7 w-7 items-center justify-center rounded-md text-muted hover:bg-surface-2 hover:text-fg"
            data-testid="where-clear"
          >
            <X className="h-3.5 w-3.5" />
          </button>
        )}
        <DropdownMenu>
          <DropdownMenuTrigger
            aria-label="Recent filters"
            disabled={recent.length === 0}
            className="flex h-7 w-7 items-center justify-center rounded-md text-muted hover:bg-surface-2 hover:text-fg disabled:opacity-40"
            data-testid="where-history"
          >
            <History className="h-3.5 w-3.5" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="max-w-[32rem]">
            <DropdownMenuLabel>Recent filters</DropdownMenuLabel>
            {recent.map((r) => (
              <DropdownMenuItem
                key={r}
                onSelect={() => {
                  setDraft(r);
                  apply(r);
                }}
              >
                <code className="truncate font-mono text-[12px]">{r}</code>
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>

        {open.length > 0 && (
          <div className="absolute top-8 left-6 z-30 flex max-w-[calc(100%-3rem)] rounded-md border border-line-strong bg-surface-2 shadow-lg" data-testid="where-suggestions">
            <ul id="where-suggestions" role="listbox" className="max-h-64 w-72 overflow-y-auto py-1">
              {open.map((s, i) => (
                <li
                  key={s.kind + s.label}
                  role="option"
                  aria-selected={i === active}
                  onMouseDown={(e) => {
                    e.preventDefault();
                    accept(s);
                  }}
                  onMouseEnter={() => setActive(i)}
                  className={cx("flex cursor-pointer items-center gap-2 px-2.5 py-1 text-[12px]", i === active && "bg-accent-soft")}
                >
                  <span className="w-12 shrink-0 text-[10px] tracking-wide text-muted uppercase">{s.kind === "column" ? "col" : s.kind.slice(0, 4)}</span>
                  <span className="truncate font-mono">{s.label}</span>
                </li>
              ))}
            </ul>
            {sel?.column && (
              <dl className="hidden w-56 shrink-0 gap-x-2 gap-y-0.5 border-l border-line px-3 py-2 text-[12px] sm:grid sm:grid-cols-[auto_1fr]" data-testid="where-column-info">
                <dt className="text-muted">Column</dt>
                <dd className="truncate font-mono">{sel.column.name}</dd>
                <dt className="text-muted">Position</dt>
                <dd>{sel.column.position}</dd>
                <dt className="text-muted">Type</dt>
                <dd className="truncate font-mono">{sel.column.type}</dd>
                <dt className="text-muted">Not null</dt>
                <dd>{sel.column.nullable ? "no" : "yes"}</dd>
              </dl>
            )}
          </div>
        )}
      </div>
      {error && (
        <p className="mt-1 ml-6 text-[12px] text-danger-text" role="alert" data-testid="where-error">
          {error.message}
          {error.position ? <span className="text-muted"> (at character {error.position})</span> : null}
          {error.hint ? <span className="text-muted"> — {error.hint}</span> : null}
        </p>
      )}
    </div>
  );
}
