import { describe, expect, it } from "vitest";
import type { components } from "../api/schema";
import {
  functionExample,
  quickStart,
  readSelect,
  relName,
  tableExamples,
} from "./apiDocs";

type CatalogTable = components["schemas"]["CatalogTable"];

const todos: CatalogTable = {
  schema: "public",
  name: "todos",
  kind: "table",
  rls: true,
  public: false,
  primary_key: ["id"],
  columns: [
    {
      name: "id",
      type: "bigint",
      nullable: false,
      identity: true,
      generated: false,
    },
    {
      name: "list_id",
      type: "bigint",
      nullable: true,
      identity: false,
      generated: false,
    },
    {
      name: "body",
      type: "text",
      nullable: false,
      identity: false,
      generated: false,
    },
    {
      name: "done",
      type: "boolean",
      nullable: false,
      default: "false",
      identity: false,
      generated: false,
    },
  ],
  foreign_keys: [
    {
      name: "todos_list_id_fkey",
      columns: ["list_id"],
      table: "public.lists",
      ref_columns: ["id"],
      embed: "lists",
      multiple: false,
    },
  ],
  referenced_by: [],
  policies: [],
  access: {},
};

const url = "https://k7f3.api.example";
const key = "pgd_pub_abc";

describe("quickStart", () => {
  it("fills in the URL and key in every language", () => {
    for (const lang of ["ts", "dart", "go"] as const) {
      const q = quickStart(lang, url, key);
      expect(q.code).toContain(url);
      expect(q.code).toContain(key);
      expect(q.install).not.toBe("");
    }
  });
});

describe("tableExamples", () => {
  const ex = tableExamples(todos, url, key);
  it("has read, insert, update, delete and upsert", () => {
    expect(ex.map((e) => e.id)).toEqual([
      "read",
      "insert",
      "update",
      "delete",
      "upsert",
    ]);
  });
  it("reads with a filter and the foreign key's embed", () => {
    expect(readSelect(todos)).toBe("id,list_id,body,done,lists(*)");
    const read = ex[0];
    expect(read.code.curl).toContain("where=done:eq:false");
    expect(read.code.ts).toContain('.eq("done", false)');
    expect(read.explore).toEqual({
      method: "GET",
      path: "/data/v1/todos?select=id,list_id,body,done,lists(*)&where=done:eq:false&order=id:desc&limit=20",
    });
  });
  it("inserts the columns that need a value, never the identity", () => {
    const ins = ex[1];
    expect(ins.explore.body).toBe('{"body":"a body"}');
    expect(ins.code.dart).toContain("{'body': 'a body'}");
    expect(ins.code.go).toContain('map[string]any{"body": "a body"}');
  });
  it("updates and deletes by key", () => {
    expect(ex[2].explore.path).toBe("/data/v1/todos/1");
    expect(ex[3].code.ts).toContain(".byKey(1)");
  });
  it("uses only the publishable key", () => {
    for (const e of ex) expect(e.code.curl).toContain(`apikey: ${key}`);
  });
  it("only reads a view", () => {
    expect(
      tableExamples({ ...todos, kind: "view" }, url, key).map((e) => e.id),
    ).toEqual(["read"]);
  });
});

describe("functionExample", () => {
  it("posts the required arguments", () => {
    const ex = functionExample(
      {
        schema: "api",
        name: "close_cart",
        args: [
          { name: "cart_id", type: "integer", optional: false },
          { name: "note", type: "text", optional: true },
        ],
        returns: "void",
        returns_set: false,
        volatility: "volatile",
        security_definer: false,
      },
      url,
      key,
    );
    expect(relName("api", "close_cart")).toBe("api.close_cart");
    expect(ex.explore).toEqual({
      method: "POST",
      path: "/data/v1/rpc/api.close_cart",
      body: '{"cart_id":1}',
    });
  });
});
