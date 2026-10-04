import type { DbSchema } from "../../api/client";

// Completion for the SQL Editor (docs/ui-redesign.md, phase 3): schemas,
// tables and columns from the project's schema, and SQL keywords. Pure, so
// it is tested without Monaco.

export type Suggestion = { label: string; kind: "schema" | "table" | "view" | "column" | "keyword"; detail?: string; insertText: string };

export const KEYWORDS = [
  "select", "from", "where", "and", "or", "not", "in", "is", "null", "as", "join", "left join", "right join", "inner join",
  "full join", "on", "group by", "order by", "having", "limit", "offset", "insert into", "values", "update", "set",
  "delete from", "returning", "create table", "alter table", "drop table", "create index", "primary key", "references",
  "distinct", "case", "when", "then", "else", "end", "exists", "between", "like", "ilike", "union", "union all", "with",
  "count(*)", "now()", "begin", "commit", "rollback", "explain analyze", "asc", "desc", "true", "false", "default",
];

/** Quotes an identifier when it needs it. */
export function ident(s: string): string {
  return /^[a-z_][a-z0-9_$]*$/.test(s) && !KEYWORDS.includes(s) ? s : `"${s.replace(/"/g, '""')}"`;
}

const unquote = (s: string) => (s.startsWith('"') ? s.slice(1, -1).replace(/""/g, '"') : s.toLowerCase());

type Rel = { schema: string; table: string };

/** The tables a query reads, by the name or alias it uses for them. */
export function referencedTables(sql: string, tree: DbSchema): Map<string, Rel> {
  const out = new Map<string, Rel>();
  const name = String.raw`("(?:[^"]|"")+"|[A-Za-z_][\w$]*)`;
  const re = new RegExp(String.raw`\b(?:from|join|update|into)\s+(?:${name}\s*\.\s*)?${name}(?:\s+(?:as\s+)?${name})?`, "gi");
  for (const m of sql.matchAll(re)) {
    const schema = m[1] ? unquote(m[1]) : null;
    const table = unquote(m[2]);
    const found = tree.schemas.find((s) => (schema ? s.name === schema : s.name === "public") && s.tables.some((t) => t.name === table))
      ?? (schema ? undefined : tree.schemas.find((s) => s.tables.some((t) => t.name === table)));
    if (!found) continue;
    const rel = { schema: found.name, table };
    out.set(table, rel);
    const alias = m[3] ? unquote(m[3]) : null;
    if (alias && !/^(where|on|join|left|right|inner|full|group|order|limit|set|values|natural|cross|using)$/i.test(alias)) out.set(alias, rel);
  }
  return out;
}

function columnsOf(tree: DbSchema, rel: Rel): Suggestion[] {
  const t = tree.schemas.find((s) => s.name === rel.schema)?.tables.find((x) => x.name === rel.table);
  return (t?.columns ?? []).map((c) => ({ label: c.name, kind: "column", detail: `${c.type} · ${rel.table}`, insertText: ident(c.name) }));
}

/** What to offer at the cursor, given the text before it and the whole
 * query. */
export function completionsAt(before: string, sql: string, tree: DbSchema): Suggestion[] {
  const dotted = /("(?:[^"]|"")+"|[A-Za-z_][\w$]*)\s*\.\s*("?[\w$]*)$/.exec(before);
  if (dotted) {
    const q = unquote(dotted[1]);
    const schema = tree.schemas.find((s) => s.name === q);
    if (schema) {
      return schema.tables.map((t) => ({
        label: t.name,
        kind: t.kind === "table" || t.kind === "partitioned_table" ? "table" : "view",
        detail: `${t.kind.replace("_", " ")} · ${schema.name}`,
        insertText: ident(t.name),
      }));
    }
    const rel = referencedTables(sql, tree).get(q) ?? (tree.schemas.find((s) => s.name === "public")?.tables.some((t) => t.name === q) ? { schema: "public", table: q } : undefined);
    return rel ? columnsOf(tree, rel) : [];
  }
  const out: Suggestion[] = [];
  const seen = new Set<string>();
  for (const rel of referencedTables(sql, tree).values()) {
    for (const c of columnsOf(tree, rel)) {
      if (!seen.has(c.label)) {
        seen.add(c.label);
        out.push(c);
      }
    }
  }
  for (const s of tree.schemas) {
    for (const t of s.tables) {
      out.push({
        label: s.name === "public" ? t.name : `${s.name}.${t.name}`,
        kind: t.kind === "table" || t.kind === "partitioned_table" ? "table" : "view",
        detail: t.kind.replace("_", " "),
        insertText: s.name === "public" ? ident(t.name) : `${ident(s.name)}.${ident(t.name)}`,
      });
    }
    if (s.name !== "public") out.push({ label: s.name, kind: "schema", detail: "schema", insertText: ident(s.name) });
  }
  for (const k of KEYWORDS) out.push({ label: k, kind: "keyword", insertText: k });
  return out;
}

/** The line and column (1-based) of Postgres's 1-based character
 * position in the query, to mark an error in the editor. */
export function positionAt(sql: string, pos: number): { line: number; column: number } {
  const before = [...sql].slice(0, Math.max(0, pos - 1)).join("");
  const lines = before.split("\n");
  return { line: lines.length, column: [...lines[lines.length - 1]].length + 1 };
}
