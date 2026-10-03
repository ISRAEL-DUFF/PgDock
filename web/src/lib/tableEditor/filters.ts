import type { EditColumn, GridFilter, GridSort } from "../../api/client";

// The table editor's Filter and Sort popovers (docs/ui-redesign.md, phase
// 2): rules the user builds row by row, turned into the grid API's
// filters and sort order.

export type FilterOp = GridFilter["op"];

/** One row of the Filter popover. */
export type FilterRule = { id: number; column: string; op: FilterOp; value: string };

/** The operators offered, labelled as Supabase Studio labels them. */
export const FILTER_OPS: { op: FilterOp; label: string; hint: string; noValue?: boolean }[] = [
  { op: "eq", label: "=", hint: "equals" },
  { op: "neq", label: "<>", hint: "not equal" },
  { op: "gt", label: ">", hint: "greater than" },
  { op: "lt", label: "<", hint: "less than" },
  { op: "gte", label: ">=", hint: "greater than or equal" },
  { op: "lte", label: "<=", hint: "less than or equal" },
  { op: "contains", label: "~~*", hint: "contains, any case" },
  { op: "in", label: "in", hint: "one of a list" },
  { op: "is_null", label: "is null", hint: "has no value", noValue: true },
  { op: "not_null", label: "is not null", hint: "has a value", noValue: true },
];

export function needsValue(op: FilterOp): boolean {
  return !FILTER_OPS.find((o) => o.op === op)?.noValue;
}

/** Operators that make sense for a column: no ordering on booleans or
 * JSON, no "contains" where text matching is odd. */
export function opsFor(col: Pick<EditColumn, "category" | "base_type"> | undefined): typeof FILTER_OPS {
  if (!col) return FILTER_OPS;
  const json = col.base_type === "json" || col.base_type === "jsonb";
  return FILTER_OPS.filter((o) => {
    if (col.category === "B") return ["eq", "neq", "is_null", "not_null"].includes(o.op);
    if (json) return ["contains", "is_null", "not_null"].includes(o.op);
    return true;
  });
}

/** Splits an "in" list: commas, with surrounding spaces trimmed. */
export function splitList(v: string): string[] {
  return v
    .split(",")
    .map((x) => x.trim())
    .filter(Boolean);
}

/** The rules the API can run: complete ones only (a column, and a value
 * where the operator needs one). */
export function toGridFilters(rules: FilterRule[]): GridFilter[] {
  const out: GridFilter[] = [];
  for (const r of rules) {
    if (!r.column) continue;
    if (!needsValue(r.op)) {
      out.push({ column: r.column, op: r.op });
    } else if (r.op === "in") {
      const values = splitList(r.value);
      if (values.length > 0) out.push({ column: r.column, op: "in", values });
    } else if (r.value !== "") {
      out.push({ column: r.column, op: r.op, value: r.value });
    }
  }
  return out;
}

/** A filter in words, for the toolbar's button ("status = paid"). */
export function describeFilter(f: GridFilter): string {
  const label = FILTER_OPS.find((o) => o.op === f.op)?.label ?? f.op;
  if (f.op === "is_null" || f.op === "not_null") return `${f.column} ${label}`;
  if (f.op === "in") return `${f.column} in (${(f.values ?? []).join(", ")})`;
  return `${f.column} ${label} ${f.value ?? ""}`;
}

/** One row of the Sort popover. */
export type SortRule = GridSort;

/** Adds a column to the sort, or flips/removes it: the column header's
 * menu does this. */
export function toggleSort(sorts: SortRule[], column: string, desc: boolean): SortRule[] {
  const i = sorts.findIndex((s) => s.column === column);
  if (i < 0) return [...sorts, { column, desc }];
  if (sorts[i].desc === desc) return sorts.filter((_, j) => j !== i);
  return sorts.map((s, j) => (j === i ? { ...s, desc } : s));
}
