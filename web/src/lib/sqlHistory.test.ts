import { describe, expect, it } from "vitest";
import { changesSchema } from "./sqlHistory";

describe("changesSchema", () => {
  it("is true for DDL", () => {
    expect(changesSchema("create table a (id int)")).toBe(true);
    expect(changesSchema("select 1; ALTER TABLE a ADD COLUMN b int")).toBe(true);
    expect(changesSchema("comment on table a is 'x'")).toBe(true);
  });
  it("is false for queries", () => {
    expect(changesSchema("select * from created_orders")).toBe(false);
    expect(changesSchema("insert into a values (1)")).toBe(false);
  });
});
