import { beforeEach, describe, expect, it } from "vitest";
import type { TableInfo } from "../../api/client";
import { bareTextDefault, defaultSuggestions, editorKind, parseInput, readOnlyReason } from "./cells";
import { asChange, blankColumn, checkExpression, createTableChange, draftFromTable, editColumnChanges, editTableChanges, newTableColumns, validateTable } from "./columnForm";
import { describeFilter, opsFor, toGridFilters, toggleSort } from "./filters";
import { closeTab, loadTabs, moveColumn, openTab, orderColumns, saveTabs } from "./prefs";

const col = (patch: Partial<TableInfo["columns"][number]> = {}): TableInfo["columns"][number] => ({
  name: "c",
  type: "text",
  nullable: true,
  default: null,
  category: "S",
  base_type: "text",
  generated: false,
  comment: null,
  ...patch,
});

const orders: TableInfo = {
  schema: "public",
  name: "orders",
  kind: "table",
  primary_key: ["id"],
  columns: [
    col({ name: "id", type: "bigint", nullable: false, category: "N", base_type: "int8", identity: "always" }),
    col({ name: "email", comment: "who" }),
    col({ name: "total", type: "numeric(10,2)", category: "N", base_type: "numeric" }),
    col({ name: "customer_id", type: "bigint", category: "N", base_type: "int8" }),
    col({ name: "legacy" }),
  ],
  foreign_keys: [{ name: "orders_customer_id_fkey", columns: ["customer_id"], ref_schema: "public", ref_table: "customers", ref_columns: ["id"], on_delete: "NO ACTION", on_update: "NO ACTION" }],
  constraints: [
    { name: "orders_pkey", kind: "primary_key", definition: "PRIMARY KEY (id)", columns: ["id"] },
    { name: "orders_email_key", kind: "unique", definition: "UNIQUE (email)", columns: ["email"] },
    { name: "orders_total_check", kind: "check", definition: "CHECK ((total > (0)::numeric))", columns: ["total"] },
    { name: "orders_customer_id_fkey", kind: "foreign_key", definition: "FOREIGN KEY (customer_id) REFERENCES customers(id)", columns: ["customer_id"] },
  ],
  indexes: [],
  size_bytes: 0,
  row_estimate: 0,
  comment: null,
  editable: true,
};

describe("filters", () => {
  it("keeps only complete rules and splits in-lists", () => {
    expect(
      toGridFilters([
        { id: 1, column: "status", op: "eq", value: "paid" },
        { id: 2, column: "status", op: "eq", value: "" },
        { id: 3, column: "", op: "eq", value: "x" },
        { id: 4, column: "id", op: "in", value: " 1, 2 ,,3" },
        { id: 5, column: "note", op: "is_null", value: "ignored" },
      ]),
    ).toEqual([
      { column: "status", op: "eq", value: "paid" },
      { column: "id", op: "in", values: ["1", "2", "3"] },
      { column: "note", op: "is_null" },
    ]);
  });
  it("offers operators that suit the column", () => {
    expect(opsFor({ category: "B", base_type: "bool" }).map((o) => o.op)).toEqual(["eq", "neq", "is_null", "not_null"]);
    expect(opsFor({ category: "U", base_type: "jsonb" }).map((o) => o.op)).toEqual(["contains", "is_null", "not_null"]);
    expect(opsFor({ category: "N", base_type: "int4" })).toHaveLength(10);
  });
  it("describes a filter", () => {
    expect(describeFilter({ column: "id", op: "in", values: ["1", "2"] })).toBe("id in (1, 2)");
    expect(describeFilter({ column: "x", op: "not_null" })).toBe("x is not null");
    expect(describeFilter({ column: "x", op: "gte", value: "3" })).toBe("x >= 3");
  });
  it("adds, flips and removes a sort from the column menu", () => {
    let s = toggleSort([], "a", false);
    s = toggleSort(s, "b", true);
    expect(s).toEqual([
      { column: "a", desc: false },
      { column: "b", desc: true },
    ]);
    expect(toggleSort(s, "a", true)[0]).toEqual({ column: "a", desc: true });
    expect(toggleSort(s, "b", true)).toEqual([{ column: "a", desc: false }]);
  });
});

describe("cells", () => {
  it("picks an editor by type", () => {
    expect(editorKind({ category: "B", base_type: "bool" })).toBe("boolean");
    expect(editorKind({ category: "E", base_type: "mood", enum_values: ["sad", "ok"] })).toBe("enum");
    expect(editorKind({ category: "U", base_type: "jsonb" })).toBe("json");
    expect(editorKind({ category: "A", base_type: "_text" })).toBe("array");
    expect(editorKind({ category: "D", base_type: "timestamptz" })).toBe("timestamp");
    expect(editorKind({ category: "U", base_type: "uuid" })).toBe("uuid");
    expect(editorKind({ category: "S", base_type: "text" })).toBe("text");
  });
  it("checks input against the type", () => {
    const num = { category: "N", base_type: "numeric", nullable: true };
    expect(parseInput(num, "1.5e3")).toEqual({ value: "1.5e3" });
    expect(parseInput(num, "12abc")).toEqual({ error: "Not a number." });
    expect(parseInput(num, "")).toEqual({ value: null });
    expect(parseInput({ ...num, nullable: false }, "")).toEqual({ error: "This column can't be NULL." });
    expect(parseInput({ category: "S", base_type: "text", nullable: false }, "")).toEqual({ value: "" });
    expect(parseInput({ category: "U", base_type: "jsonb", nullable: true }, "{bad")).toEqual({ error: "Not valid JSON." });
    expect(parseInput({ category: "U", base_type: "jsonb", nullable: true }, '{"a":1}')).toEqual({ value: '{"a":1}' });
    expect(parseInput({ category: "B", base_type: "bool", nullable: true }, "true")).toEqual({ value: "t" });
    expect(parseInput({ category: "U", base_type: "uuid", nullable: true }, "nope")).toEqual({ error: "Not a UUID." });
  });
  it("explains read-only cells", () => {
    expect(readOnlyReason(col({ generated: true }), orders)).toMatch(/generated/);
    expect(readOnlyReason(col({ identity: "always" }), orders)).toMatch(/identity/);
    expect(readOnlyReason(col({ identity: "by_default" }), orders)).toBeNull();
    expect(readOnlyReason(col(), { editable: false, read_only_reason: "Views can't be edited" })).toBe("Views can't be edited");
  });
  it("suggests defaults by type", () => {
    expect(defaultSuggestions("timestamptz")[0].value).toBe("now()");
    expect(defaultSuggestions("uuid")[0].value).toBe("gen_random_uuid()");
    expect(defaultSuggestions("int4")).toEqual([]);
  });
});

describe("column form", () => {
  it("creates a table with Studio's starting columns and a foreign key", () => {
    const cols = [...newTableColumns(), blankColumn({ name: "customer_id", type: "int8", ref: { schema: "", table: "customers", column: "id", onDelete: "CASCADE", onUpdate: "NO ACTION" } })];
    const c = createTableChange({ schema: "public", name: " orders ", comment: "", columns: cols });
    expect(c).toEqual({
      kind: "create_table",
      schema: "public",
      table: "orders",
      comment: undefined,
      columns: [
        { name: "id", type: "int8 generated by default as identity", nullable: false, primary_key: true },
        { name: "created_at", type: "timestamptz", nullable: false, default: "now()" },
        { name: "customer_id", type: "int8", nullable: true, references: { schema: undefined, table: "customers", column: "id", on_delete: "CASCADE", on_update: "NO ACTION" } },
      ],
    });
  });
  it("validates names and foreign keys", () => {
    expect(validateTable({ schema: "public", name: "", comment: "", columns: [blankColumn({ name: "a" }), blankColumn({ name: "a" })] }, true)).toEqual([
      "Give the table a name.",
      "Column a appears twice.",
    ]);
    expect(validateTable({ schema: "public", name: "t", comment: "", columns: [blankColumn({ name: "a", ref: { schema: "", table: "x", column: "", onDelete: "NO ACTION", onUpdate: "NO ACTION" } })] }, true)).toEqual([
      "Finish the foreign key on a, or remove it.",
    ]);
  });
  it("reads a table back into a draft", () => {
    const d = draftFromTable(orders);
    const email = d.columns.find((c) => c.name === "email")!;
    expect(email.unique).toBe(true);
    expect(email.comment).toBe("who");
    expect(d.columns.find((c) => c.name === "total")!.check).toBe("total > (0)::numeric");
    expect(d.columns.find((c) => c.name === "customer_id")!.ref).toEqual({ schema: "public", table: "customers", column: "id", onDelete: "NO ACTION", onUpdate: "NO ACTION" });
    expect(d.columns.find((c) => c.name === "id")!.primaryKey).toBe(true);
  });
  it("leaves an unchanged table alone", () => {
    expect(editTableChanges(orders, draftFromTable(orders))).toEqual([]);
    expect(asChange(orders, [])).toBeNull();
  });
  it("turns an edited table into changes in a safe order", () => {
    const d = draftFromTable(orders);
    d.name = "purchases";
    d.comment = "Paid orders";
    d.columns = d.columns.filter((c) => c.name !== "legacy");
    const email = d.columns.find((c) => c.name === "email")!;
    email.name = "contact";
    email.unique = false;
    email.nullable = false;
    const total = d.columns.find((c) => c.name === "total")!;
    total.check = "";
    total.defaultValue = "0";
    d.columns.find((c) => c.name === "customer_id")!.ref!.onDelete = "CASCADE";
    d.columns.push(blankColumn({ name: "note", type: "text" }));
    expect(editTableChanges(orders, d)).toEqual([
      { kind: "drop_column", column_name: "legacy" },
      { kind: "alter_column", column_name: "email", nullable: false },
      { kind: "drop_constraint", name: "orders_email_key" },
      { kind: "alter_column", column_name: "total", default: "0" },
      { kind: "drop_constraint", name: "orders_total_check" },
      { kind: "drop_constraint", name: "orders_customer_id_fkey" },
      { kind: "rename_column", column_name: "email", new_name: "contact" },
      { kind: "add_column", column: { name: "note", type: "text", nullable: true } },
      { kind: "add_foreign_key", key_columns: ["customer_id"], ref_schema: "public", ref_table: "customers", ref_columns: ["id"], on_delete: "CASCADE", on_update: "NO ACTION" },
      { kind: "set_comment", comment: "Paid orders" },
      { kind: "rename_table", new_name: "purchases" },
    ]);
    const b = asChange(orders, editTableChanges(orders, d))!;
    expect(b.kind).toBe("batch");
    expect(b.changes!.every((c) => c.table === "orders" && c.schema === "public")).toBe(true);
  });
  it("edits one column: alter by the old name, comment by the new", () => {
    const d = draftFromTable(orders).columns.find((c) => c.name === "email")!;
    d.name = "mail";
    d.type = "varchar(320)";
    d.comment = "";
    expect(editColumnChanges(orders, d)).toEqual([
      { kind: "alter_column", column_name: "email", type: "varchar(320)" },
      { kind: "rename_column", column_name: "email", new_name: "mail" },
      { kind: "set_comment", column_name: "mail", comment: "" },
    ]);
  });
  it("strips a check down to its expression", () => {
    expect(checkExpression("CHECK ((price > 0))")).toBe("price > 0");
    expect(checkExpression("CHECK ((a > 0) AND (b > 0))")).toBe("(a > 0) AND (b > 0)");
    expect(checkExpression("CHECK (x) NOT VALID")).toBe("x");
  });
});

describe("tabs and layout", () => {
  beforeEach(() => {
    // Tests run in Node: a minimal in-memory localStorage.
    const m = new Map<string, string>();
    globalThis.localStorage = {
      getItem: (k: string) => m.get(k) ?? null,
      setItem: (k: string, v: string) => void m.set(k, v),
      removeItem: (k: string) => void m.delete(k),
      clear: () => m.clear(),
      key: (i: number) => [...m.keys()][i] ?? null,
      get length() {
        return m.size;
      },
    };
  });
  it("remembers open tabs per project", () => {
    const a = { schema: "public", table: "a" };
    const b = { schema: "public", table: "b" };
    const c = { schema: "public", table: "c" };
    let tabs = openTab(openTab(openTab([], a), b), a);
    tabs = openTab(tabs, c);
    expect(tabs).toEqual([a, b, c]);
    saveTabs("p1", tabs);
    expect(loadTabs("p1")).toEqual([a, b, c]);
    expect(loadTabs("p2")).toEqual([]);
    expect(closeTab(tabs, b, b)).toEqual({ tabs: [a, c], next: c });
    expect(closeTab(tabs, c, c)).toEqual({ tabs: [a, b], next: b });
    expect(closeTab(tabs, a, c)).toEqual({ tabs: [b, c], next: c });
    expect(closeTab([a], a, a)).toEqual({ tabs: [], next: null });
  });
  it("survives junk in storage", () => {
    localStorage.setItem("pgdock.editor.tabs.p1", "{not json");
    expect(loadTabs("p1")).toEqual([]);
    localStorage.setItem("pgdock.editor.tabs.p1", JSON.stringify([{ schema: "public" }, { schema: "public", table: "t" }]));
    expect(loadTabs("p1")).toEqual([{ schema: "public", table: "t" }]);
  });
  it("orders and moves columns", () => {
    expect(orderColumns(["id", "a", "b", "new"], ["b", "gone", "id", "a"])).toEqual(["b", "id", "a", "new"]);
    expect(moveColumn(["a", "b", "c"], "c", "a")).toEqual(["c", "a", "b"]);
    expect(moveColumn(["a", "b", "c"], "a", "c")).toEqual(["b", "c", "a"]);
  });
});

describe("bareTextDefault", () => {
  it("flags unquoted words on text columns", () => {
    expect(bareTextDefault("text", "free")).toBe("'free'");
    expect(bareTextDefault("varchar(20)", "new user")).toBe("'new user'");
  });
  it("leaves expressions, literals and other types alone", () => {
    expect(bareTextDefault("text", "'free'")).toBeNull();
    expect(bareTextDefault("text", "now()")).toBeNull();
    expect(bareTextDefault("text", "null")).toBeNull();
    expect(bareTextDefault("text", "")).toBeNull();
    expect(bareTextDefault("integer", "free")).toBeNull();
  });
});
