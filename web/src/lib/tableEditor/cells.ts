import type { EditColumn, TableInfo } from "../../api/client";

// How the table editor shows and edits a cell, by the column's type
// (docs/ui-redesign.md, phase 2). Values travel as text, null for NULL,
// exactly as Postgres prints and parses them.

export type Val = string | null;

export type EditorKind = "boolean" | "enum" | "json" | "number" | "timestamp" | "date" | "time" | "uuid" | "array" | "text";

export function editorKind(col: Pick<EditColumn, "category" | "base_type" | "enum_values">): EditorKind {
  const t = col.base_type;
  if (col.category === "B") return "boolean";
  if (col.enum_values && col.enum_values.length > 0) return "enum";
  if (t === "json" || t === "jsonb") return "json";
  if (col.category === "A") return "array";
  if (col.category === "N") return "number";
  if (t === "uuid") return "uuid";
  if (t === "timestamp" || t === "timestamptz") return "timestamp";
  if (t === "date") return "date";
  if (t === "time" || t === "timetz") return "time";
  return "text";
}

/** Why a column can't be edited in the grid, or null when it can. */
export function readOnlyReason(col: Pick<EditColumn, "generated" | "identity">, info: Pick<TableInfo, "editable" | "read_only_reason">): string | null {
  if (!info.editable) return info.read_only_reason ?? "This table can't be edited here.";
  if (col.generated) return "A generated column: Postgres computes it.";
  if (col.identity === "always") return "An identity column: Postgres assigns it.";
  return null;
}

/** The value as the grid shows it. */
export function displayValue(col: Pick<EditColumn, "base_type"> | undefined, v: Val): string {
  if (v == null) return "NULL";
  if (col?.base_type === "bytea") return `\\x… (${Math.max(0, (v.length - 2) / 2)} bytes)`;
  if (col?.base_type === "bool") return v === "t" ? "true" : v === "f" ? "false" : v;
  return v;
}

/** A value the user typed, checked for its column: the text to send, or
 * why it won't do. Empty input means the empty string for text and NULL
 * for every other type, as in Studio. */
export function parseInput(col: Pick<EditColumn, "category" | "base_type" | "enum_values" | "nullable">, input: string): { value: Val } | { error: string } {
  const kind = editorKind(col);
  if (input === "" && kind !== "text") {
    return col.nullable ? { value: null } : { error: "This column can't be NULL." };
  }
  switch (kind) {
    case "number": {
      const n = input.trim();
      if (!/^[-+]?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?$/.test(n) && !/^[-+]?(Infinity|NaN)$/i.test(n)) return { error: "Not a number." };
      return { value: n };
    }
    case "json":
      try {
        JSON.parse(input);
        return { value: input };
      } catch {
        return { error: "Not valid JSON." };
      }
    case "boolean":
      if (["true", "t"].includes(input)) return { value: "t" };
      if (["false", "f"].includes(input)) return { value: "f" };
      return { error: "Pick true or false." };
    case "enum":
      return col.enum_values!.includes(input) ? { value: input } : { error: `One of ${col.enum_values!.join(", ")}.` };
    case "uuid":
      return /^[0-9a-f]{8}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{12}$/i.test(input.trim()) ? { value: input.trim() } : { error: "Not a UUID." };
    default:
      return { value: input };
  }
}

/** Pretty-prints JSON for the side editor; leaves anything else alone. */
export function prettyJSON(v: Val): string {
  if (v == null) return "";
  try {
    return JSON.stringify(JSON.parse(v), null, 2);
  } catch {
    return v;
  }
}

/** Default-value suggestions for a type, as Studio offers them. */
export function defaultSuggestions(type: string): { value: string; label: string }[] {
  const t = type.toLowerCase().replace(/\[\]$/, "");
  if (t.startsWith("timestamp")) return [{ value: "now()", label: "now()" }, { value: "(now() at time zone 'utc')", label: "now() in UTC" }];
  if (t === "date") return [{ value: "current_date", label: "current_date" }];
  if (t === "time" || t === "timetz") return [{ value: "current_time", label: "current_time" }];
  if (t === "uuid") return [{ value: "gen_random_uuid()", label: "gen_random_uuid()" }];
  if (t === "bool" || t === "boolean") return [{ value: "true", label: "true" }, { value: "false", label: "false" }];
  if (t === "text" || t.startsWith("varchar")) return [{ value: "''", label: "empty string" }];
  if (t === "jsonb" || t === "json") return [{ value: "'{}'::" + t, label: "empty object" }, { value: "'[]'::" + t, label: "empty array" }];
  return [];
}
