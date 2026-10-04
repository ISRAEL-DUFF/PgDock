import { ChevronRight, Copy, Edit3, FileCode2, History, MoreVertical, Plus, Search, Share2, Star, StarOff, Trash2, Users } from "lucide-react";
import { useState, type ReactNode } from "react";
import type { SavedQuery } from "../../api/client";
import { TEMPLATES, type Template } from "../../lib/sqlEditor/templates";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger, cx } from "../ui";

export type QueryActions = {
  onOpen: (q: SavedQuery) => void;
  onNew: () => void;
  onTemplate: (t: Template) => void;
  onHistory: (sql: string) => void;
  onRename: (q: SavedQuery) => void;
  onShare: (q: SavedQuery, shared: boolean) => void;
  onFavorite: (q: SavedQuery, favorite: boolean) => void;
  onDuplicate: (q: SavedQuery) => void;
  onDelete: (q: SavedQuery) => void;
  onClearHistory: () => void;
};

function Group({ title, count, icon, children, defaultOpen = true, testId }: { title: string; count?: number; icon?: ReactNode; children: ReactNode; defaultOpen?: boolean; testId?: string }) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <section data-testid={testId}>
      <button
        type="button"
        onClick={() => setOpen(!open)}
        aria-expanded={open}
        className="flex w-full items-center gap-1.5 px-3 py-1.5 text-[11px] font-medium tracking-wider text-muted uppercase hover:text-fg"
      >
        <ChevronRight className={cx("h-3 w-3 transition-transform", open && "rotate-90")} />
        {icon}
        {title}
        {count != null && <span className="font-normal">({count})</span>}
      </button>
      {open && <div className="flex flex-col pb-2">{children}</div>}
    </section>
  );
}

/** The SQL Editor's sidebar, as Studio's: search, new query, and the
 * queries grouped as favourites, shared and private, then templates and
 * this browser's history. */
export function QuerySidebar({
  queries,
  activeId,
  canAdminShared,
  history,
  actions,
}: {
  queries: SavedQuery[];
  activeId: string | null;
  canAdminShared: boolean;
  history: string[];
  actions: QueryActions;
}) {
  const [q, setQ] = useState("");
  const f = q.trim().toLowerCase();
  const match = (x: SavedQuery) => !f || x.name.toLowerCase().includes(f) || x.sql.toLowerCase().includes(f);
  const shown = queries.filter(match);
  const favorites = shown.filter((x) => x.favorite);
  const shared = shown.filter((x) => x.visibility === "shared");
  const priv = shown.filter((x) => x.visibility === "private" && x.mine);

  const item = (x: SavedQuery) => {
    const active = x.id === activeId;
    return (
      <div key={x.id} className={cx("group mx-2 flex h-7 items-center rounded-md text-[13px] text-fg-light hover:bg-surface-2 hover:text-fg", active && "bg-surface-3 text-fg")}>
        <button type="button" onClick={() => actions.onOpen(x)} className="flex min-w-0 flex-1 items-center gap-2 px-2 text-left" aria-current={active || undefined} data-testid={`query-${x.name}`}>
          <FileCode2 className="h-3.5 w-3.5 shrink-0 text-muted" strokeWidth={1.6} />
          <span className="truncate">{x.name}</span>
        </button>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button type="button" aria-label={`${x.name} actions`} className="mr-1 rounded p-0.5 text-muted opacity-0 group-hover:opacity-100 hover:bg-surface-3 hover:text-fg focus:opacity-100 data-[state=open]:opacity-100">
              <MoreVertical className="h-3.5 w-3.5" />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" side="right">
            {x.mine && (
              <DropdownMenuItem icon={<Edit3 className="h-3.5 w-3.5" />} onSelect={() => actions.onRename(x)}>
                Rename query
              </DropdownMenuItem>
            )}
            {x.mine && (
              <DropdownMenuItem icon={<Share2 className="h-3.5 w-3.5" />} onSelect={() => actions.onShare(x, x.visibility !== "shared")} data-testid="share-query">
                {x.visibility === "shared" ? "Make private" : "Share with the project"}
              </DropdownMenuItem>
            )}
            <DropdownMenuItem icon={x.favorite ? <StarOff className="h-3.5 w-3.5" /> : <Star className="h-3.5 w-3.5" />} onSelect={() => actions.onFavorite(x, !x.favorite)}>
              {x.favorite ? "Remove from favorites" : "Add to favorites"}
            </DropdownMenuItem>
            <DropdownMenuItem icon={<Copy className="h-3.5 w-3.5" />} onSelect={() => actions.onDuplicate(x)}>
              Duplicate query
            </DropdownMenuItem>
            {(x.mine || (x.visibility === "shared" && canAdminShared)) && (
              <>
                <DropdownMenuSeparator />
                <DropdownMenuItem danger icon={<Trash2 className="h-3.5 w-3.5" />} onSelect={() => actions.onDelete(x)}>
                  Delete query
                </DropdownMenuItem>
              </>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    );
  };
  const empty = (text: string) => <p className="px-5 py-1 text-[12px] text-muted">{text}</p>;

  return (
    <aside className="flex w-64 shrink-0 flex-col border-r border-line bg-surface" data-testid="query-sidebar">
      <div className="flex items-center gap-1 border-b border-line p-3">
        <div className="relative flex-1">
          <Search className="pointer-events-none absolute top-1/2 left-2 h-3.5 w-3.5 -translate-y-1/2 text-muted" />
          <input
            type="search"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Search queries..."
            aria-label="Search queries"
            className="h-7 w-full rounded-md border border-line-strong bg-surface-2 pr-2 pl-7 text-[12px] text-fg placeholder:text-muted focus:border-accent focus:outline-none"
          />
        </div>
        <button type="button" aria-label="New query" onClick={actions.onNew} className="rounded-md border border-line-strong bg-surface-2 p-1.5 text-fg-light hover:text-fg" data-testid="new-query">
          <Plus className="h-3.5 w-3.5" />
        </button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto py-2">
        {favorites.length > 0 && (
          <Group title="Favorites" count={favorites.length} testId="favorite-queries">
            {favorites.map(item)}
          </Group>
        )}
        <Group title="Shared" count={shared.length} icon={<Users className="h-3 w-3" />} testId="shared-queries">
          {shared.length ? shared.map(item) : empty("Share a query to show it to everyone in the project.")}
        </Group>
        <Group title="Private" count={priv.length} testId="private-queries">
          {priv.length ? priv.map(item) : empty(f ? "No queries match your search." : "Queries you create are saved here.")}
        </Group>
        <Group title="Templates" defaultOpen={false} testId="templates">
          {TEMPLATES.map((t) => (
            <button
              key={t.title}
              type="button"
              onClick={() => actions.onTemplate(t)}
              title={t.description}
              className="mx-2 flex h-7 items-center gap-2 rounded-md px-2 text-left text-[13px] text-fg-light hover:bg-surface-2 hover:text-fg"
            >
              <FileCode2 className="h-3.5 w-3.5 shrink-0 text-muted" strokeWidth={1.6} />
              <span className="truncate">{t.title}</span>
            </button>
          ))}
        </Group>
        <Group title="History" defaultOpen={false} icon={<History className="h-3 w-3" />}>
          {history.length === 0
            ? empty("Queries you run are kept in this browser only.")
            : history.map((h, i) => (
                <button
                  key={i}
                  type="button"
                  title={h}
                  onClick={() => actions.onHistory(h)}
                  className="mx-2 truncate rounded-md px-2 py-1 text-left font-mono text-[11px] text-muted hover:bg-surface-2 hover:text-fg"
                >
                  {h.replace(/\s+/g, " ")}
                </button>
              ))}
          {history.length > 0 && (
            <button type="button" onClick={actions.onClearHistory} className="mx-4 mt-1 self-start text-[11px] text-muted hover:text-fg">
              Clear history
            </button>
          )}
        </Group>
      </div>
    </aside>
  );
}
