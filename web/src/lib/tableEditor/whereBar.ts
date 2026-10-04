import type { TableInfo } from "../../api/client";

/** One line in the filter bar's suggestion list. */
export type Suggestion = {
  label: string;
  /** What replaces the word being typed. */
  insert: string;
  kind: "column" | "operator" | "function" | "keyword";
  /** For a column: the facts DBeaver shows beside the list. */
  column?: { name: string; type: string; nullable: boolean; position: number };
};

const operators = ["and", "or", "not", "in", "like", "ilike", "between", "is null", "is not null", "is distinct from", "any", "all", "exists"];
const keywords = ["true", "false", "null", "case", "when", "then", "else", "end"];
const functions = [
  "lower(", "upper(", "length(", "trim(", "coalesce(", "abs(", "round(", "now()", "current_date", "date_trunc(", "extract(", "cast(",
  "left(", "right(", "substring(", "position(", "starts_with(", "to_char(", "age(", "jsonb_typeof(",
];

/** A name as it must be written in SQL: quoted unless plain lower case. */
export function sqlIdent(name: string): string {
  return /^[a-z_][a-z0-9_]*$/.test(name) ? name : `"${name.replace(/"/g, '""')}"`;
}

/** The word ending at the caret: where it starts, and what is typed of it. */
export function wordAt(text: string, caret: number): { start: number; prefix: string } {
  let start = caret;
  while (start > 0 && /[A-Za-z0-9_$"]/.test(text[start - 1])) start--;
  return { start, prefix: text.slice(start, caret) };
}

/** Whether the caret is inside a '…' string, where nothing is suggested. */
export function inString(text: string, caret: number): boolean {
  let open = false;
  for (let i = 0; i < caret; i++) if (text[i] === "'") open = !open;
  return open;
}

const rank = (label: string, prefix: string): number => {
  const l = label.toLowerCase().replace(/^"|"$/g, "");
  const p = prefix.toLowerCase().replace(/^"/, "");
  if (p === "") return 1;
  if (l === p) return -1;
  if (l.startsWith(p)) return 0;
  return l.includes(p) ? 2 : -1;
};

/**
 * Suggestions at the caret: the table's columns, then operators, keywords
 * and functions. With nothing typed it offers only when asked (force).
 */
export function suggest(text: string, caret: number, columns: TableInfo["columns"], force = false): Suggestion[] {
  if (inString(text, caret)) return [];
  const { prefix } = wordAt(text, caret);
  if (prefix === "" && !force) return [];
  const pool: Suggestion[] = [
    ...columns.map((c, i) => ({
      label: c.name,
      insert: sqlIdent(c.name),
      kind: "column" as const,
      column: { name: c.name, type: c.type, nullable: c.nullable, position: i + 1 },
    })),
    ...operators.map((o) => ({ label: o, insert: o, kind: "operator" as const })),
    ...keywords.map((k) => ({ label: k, insert: k, kind: "keyword" as const })),
    ...functions.map((f) => ({ label: f, insert: f, kind: "function" as const })),
  ];
  const kindOrder = { column: 0, operator: 1, keyword: 2, function: 3 };
  return pool
    .map((s) => ({ s, r: rank(s.label, prefix) }))
    .filter((x) => x.r >= 0 && x.s.label.toLowerCase() !== prefix.toLowerCase())
    .sort((a, b) => a.r - b.r || kindOrder[a.s.kind] - kindOrder[b.s.kind] || a.s.label.localeCompare(b.s.label))
    .slice(0, 12)
    .map((x) => x.s);
}

/** Puts a suggestion in place of the word at the caret, with a space after names and operators. */
export function applySuggestion(text: string, caret: number, s: Suggestion): { text: string; caret: number } {
  const { start } = wordAt(text, caret);
  const tail = text.slice(caret);
  const needsSpace = !s.insert.endsWith("(") && !s.insert.endsWith(")") && !tail.startsWith(" ") && s.kind !== "function";
  const insert = s.insert + (needsSpace ? " " : "");
  const next = text.slice(0, start) + insert + tail;
  const at = start + insert.length - (s.insert.endsWith("()") ? 0 : 0);
  return { text: next, caret: s.insert.endsWith("(") ? start + s.insert.length : at };
}

/** Recent filters for a table, newest first. */
export function pushRecent(list: string[], expr: string, max = 10): string[] {
  const e = expr.trim();
  if (!e) return list;
  return [e, ...list.filter((x) => x !== e)].slice(0, max);
}

const key = (project: string, schema: string, table: string) => `pgdock.where.${project}.${schema}.${table}`;

export function loadRecent(project: string, schema: string, table: string): string[] {
  try {
    const v = JSON.parse(localStorage.getItem(key(project, schema, table)) ?? "[]");
    return Array.isArray(v) ? v.filter((x) => typeof x === "string").slice(0, 10) : [];
  } catch {
    return [];
  }
}

export function saveRecent(project: string, schema: string, table: string, list: string[]) {
  try {
    localStorage.setItem(key(project, schema, table), JSON.stringify(list));
  } catch {
    /* storage unavailable */
  }
}
