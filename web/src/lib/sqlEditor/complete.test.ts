import { describe, expect, it } from "vitest";
import type { DbSchema } from "../../api/client";
import { completionsAt, ident, positionAt, referencedTables } from "./complete";

const tbl = (name: string, cols: string[], kind: "table" | "view" = "table") => ({
  name,
  kind,
  size_bytes: 0,
  primary_key: [],
  indexes: [],
  columns: cols.map((c) => ({ name: c, type: "text", nullable: true })),
});

const tree: DbSchema = {
  schemas: [
    { name: "public", enums: [], tables: [tbl("orders", ["id", "total", "customer_id"]), tbl("customers", ["id", "name"]), tbl("big orders", ["x"])] },
    { name: "audit", enums: [], tables: [tbl("events", ["at", "what"]), tbl("recent", ["at"], "view")] },
  ],
};

const labels = (s: { label: string }[]) => s.map((x) => x.label);

describe("SQL completion", () => {
  it("finds tables by name and alias", () => {
    const refs = referencedTables('select * from orders o join public.customers as c on c.id = o.customer_id join audit.events e on true join "big orders" b on true', tree);
    expect(refs.get("o")).toEqual({ schema: "public", table: "orders" });
    expect(refs.get("c")).toEqual({ schema: "public", table: "customers" });
    expect(refs.get("e")).toEqual({ schema: "audit", table: "events" });
    expect(refs.get("big orders")).toEqual({ schema: "public", table: "big orders" });
    expect(refs.has("on")).toBe(false);
  });
  it("offers a schema's tables after its name and a dot", () => {
    expect(labels(completionsAt("select * from audit.", "select * from audit.", tree))).toEqual(["events", "recent"]);
    expect(completionsAt("select * from audit.", "", tree)[1].kind).toBe("view");
  });
  it("offers a table's columns after its alias", () => {
    const sql = "select o. from orders o";
    expect(labels(completionsAt("select o.", sql, tree))).toEqual(["id", "total", "customer_id"]);
    expect(labels(completionsAt("select orders.to", "select orders.to from orders", tree))).toEqual(["id", "total", "customer_id"]);
    expect(completionsAt("select nope.", "select nope.", tree)).toEqual([]);
  });
  it("offers the query's columns, then tables, schemas and keywords", () => {
    const s = completionsAt("select ", "select  from customers", tree);
    expect(labels(s).slice(0, 2)).toEqual(["id", "name"]);
    expect(labels(s)).toContain("orders");
    expect(labels(s)).toContain("audit.events");
    expect(labels(s)).toContain("audit");
    expect(labels(s)).toContain("select");
    expect(s.find((x) => x.label === "big orders")!.insertText).toBe('"big orders"');
  });
  it("quotes identifiers that need it", () => {
    expect(ident("orders")).toBe("orders");
    expect(ident("Orders")).toBe('"Orders"');
    expect(ident("select")).toBe('"select"');
    expect(ident('a"b')).toBe('"a""b"');
  });
  it("maps an error position to a line and column", () => {
    expect(positionAt("select 1;\nselect nope", 18)).toEqual({ line: 2, column: 8 });
    expect(positionAt("selec 1", 1)).toEqual({ line: 1, column: 1 });
  });
});
