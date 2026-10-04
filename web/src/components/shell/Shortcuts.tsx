import { useNavigate } from "@tanstack/react-router";
import { useEffect, useMemo, useRef } from "react";
import { GO_KEYS, goSequence, isTyping, keyParts, shortcutGroups } from "../../lib/shortcuts";
import { Dialog } from "../ui";
import type { RailItem } from "./nav";

/** The rail entries that have a "g" shortcut, with their letter. */
export function goTargets(rail: RailItem[]): (RailItem & { goKey: string })[] {
  const seen = new Set<string>();
  return rail.flatMap((r) => {
    const k = GO_KEYS[r.key];
    if (!k || seen.has(k)) return [];
    seen.add(k);
    return [{ ...r, goKey: k }];
  });
}

/** App-wide keys: "?" for the sheet, "[" for the section menu, and "g"
 * then a letter to jump to a section. Ignored while typing. */
export function useGlobalShortcuts({ rail, onHelp, onToggleSidebar }: { rail: RailItem[]; onHelp: () => void; onToggleSidebar: () => void }) {
  const navigate = useNavigate();
  const seq = useRef(goSequence());
  const targets = useMemo(() => goTargets(rail), [rail]);
  const latest = useRef({ targets, onHelp, onToggleSidebar });
  latest.current = { targets, onHelp, onToggleSidebar };

  useEffect(() => {
    const down = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey || isTyping(e.target)) return;
      // A dialog or panel that's open owns the keyboard.
      if (document.querySelector("[role=dialog], [role=alertdialog], [role=menu]")) return;
      const { targets, onHelp, onToggleSidebar } = latest.current;
      if (e.key === "?") {
        e.preventDefault();
        onHelp();
        return;
      }
      if (e.key === "[") {
        e.preventDefault();
        onToggleSidebar();
        return;
      }
      const letter = seq.current.feed(e.key, performance.now());
      if (letter) {
        const t = targets.find((x) => x.goKey === letter);
        if (t) {
          e.preventDefault();
          void navigate({ to: t.to });
        }
      }
    };
    document.addEventListener("keydown", down);
    return () => document.removeEventListener("keydown", down);
  }, [navigate]);
}

export function Keys({ keys }: { keys: string }) {
  return (
    <span className="inline-flex items-center gap-1">
      {keyParts(keys).map((p, i) =>
        p === "then" ? (
          <span key={i} className="text-[11px] text-muted">
            then
          </span>
        ) : (
          <kbd key={i} className="min-w-[1.5rem] rounded border border-line-strong bg-surface-2 px-1.5 py-0.5 text-center font-sans text-[11px] text-fg-light">
            {p}
          </kbd>
        ),
      )}
    </span>
  );
}

/** The "?" sheet: every shortcut, grouped. */
export function ShortcutsSheet({ open, onOpenChange, rail }: { open: boolean; onOpenChange: (v: boolean) => void; rail: RailItem[] }) {
  const groups = shortcutGroups(goTargets(rail).map((t) => ({ key: t.goKey, label: t.label })));
  return (
    <Dialog title="Keyboard shortcuts" open={open} onOpenChange={onOpenChange} className="w-[min(44rem,calc(100vw-2rem))]" testId="shortcuts-sheet">
      <div className="grid max-h-[65vh] gap-x-8 gap-y-5 overflow-y-auto sm:grid-cols-2">
        {groups.map((g) => (
          <section key={g.title}>
            <h3 className="mb-2 text-[11px] tracking-wider text-muted uppercase">{g.title}</h3>
            <dl className="flex flex-col gap-2">
              {g.items.map((s) => (
                <div key={s.keys + s.label} className="flex items-center justify-between gap-3 text-[13px]">
                  <dt className="text-fg-light">{s.label}</dt>
                  <dd>
                    <Keys keys={s.keys} />
                  </dd>
                </div>
              ))}
            </dl>
          </section>
        ))}
      </div>
    </Dialog>
  );
}
