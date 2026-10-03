// What the table editor remembers in the browser, per project: the open
// tabs, and each table's column widths and order (docs/ui-redesign.md,
// phase 2).

export type TableRef = { schema: string; table: string };

export const refKey = (r: TableRef) => `${r.schema}.${r.table}`;

function read<T>(key: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(key);
    return raw ? (JSON.parse(raw) as T) : fallback;
  } catch {
    return fallback;
  }
}

function write(key: string, v: unknown) {
  try {
    localStorage.setItem(key, JSON.stringify(v));
  } catch {
    /* storage unavailable */
  }
}

const tabsKey = (project: string) => `pgdock.editor.tabs.${project}`;

export function loadTabs(project: string): TableRef[] {
  const v = read<unknown>(tabsKey(project), []);
  return Array.isArray(v) ? v.filter((t): t is TableRef => typeof t?.schema === "string" && typeof t?.table === "string") : [];
}

export function saveTabs(project: string, tabs: TableRef[]) {
  write(tabsKey(project), tabs);
}

/** Opens a tab (at the end, unless it is already open). */
export function openTab(tabs: TableRef[], t: TableRef): TableRef[] {
  return tabs.some((x) => refKey(x) === refKey(t)) ? tabs : [...tabs, t];
}

/** Closes a tab, and says which to show next: its neighbour. */
export function closeTab(tabs: TableRef[], t: TableRef, current: TableRef | null): { tabs: TableRef[]; next: TableRef | null } {
  const i = tabs.findIndex((x) => refKey(x) === refKey(t));
  if (i < 0) return { tabs, next: current };
  const rest = tabs.filter((_, j) => j !== i);
  if (!current || refKey(current) !== refKey(t)) return { tabs: rest, next: current };
  return { tabs: rest, next: rest[Math.min(i, rest.length - 1)] ?? null };
}

/** Renames a tab after its table was renamed. */
export function renameTab(tabs: TableRef[], from: TableRef, to: TableRef): TableRef[] {
  return tabs.map((x) => (refKey(x) === refKey(from) ? to : x));
}

export type ColumnLayout = { widths: Record<string, number>; order: string[] };

const layoutKey = (project: string, t: TableRef) => `pgdock.editor.layout.${project}.${refKey(t)}`;

export function loadLayout(project: string, t: TableRef): ColumnLayout {
  const v = read<Partial<ColumnLayout>>(layoutKey(project, t), {});
  return { widths: v.widths && typeof v.widths === "object" ? v.widths : {}, order: Array.isArray(v.order) ? v.order : [] };
}

export function saveLayout(project: string, t: TableRef, l: ColumnLayout) {
  write(layoutKey(project, t), l);
}

/** The table's columns in the remembered order: known ones first as
 * arranged, new ones after in table order; dropped ones forgotten. */
export function orderColumns(columns: string[], order: string[]): string[] {
  const known = order.filter((c) => columns.includes(c));
  return [...known, ...columns.filter((c) => !known.includes(c))];
}

/** Moves a column before the one it was dropped on. */
export function moveColumn(order: string[], source: string, target: string): string[] {
  const from = order.indexOf(source);
  const to = order.indexOf(target);
  if (from < 0 || to < 0 || from === to) return order;
  const next = [...order];
  next.splice(from, 1);
  next.splice(to, 0, source);
  return next;
}
