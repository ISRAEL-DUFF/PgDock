import { describe, expect, it } from "vitest";
import { guessOwnerColumn, ident, policyProblem, policySQL } from "./policies";

const roles = { anon: "db_k7_anon", user: "db_k7_user" };

describe("policies", () => {
  it("quotes identifiers that need it", () => {
    expect(ident("todos")).toBe("todos");
    expect(ident("My Table")).toBe('"My Table"');
    expect(ident('a"b')).toBe('"a""b"');
  });

  it("builds owner-only policies", () => {
    const sql = policySQL({
      schema: "public",
      table: "todos",
      template: "owner",
      roles,
      ownerColumn: "owner_id",
    });
    expect(sql).toContain(
      "ALTER TABLE public.todos ENABLE ROW LEVEL SECURITY;",
    );
    expect(sql).toContain(
      "CREATE POLICY todos_select ON public.todos FOR SELECT TO db_k7_user USING (owner_id = pgd_auth.uid());",
    );
    expect(sql).toContain(
      "FOR INSERT TO db_k7_user WITH CHECK (owner_id = pgd_auth.uid());",
    );
    expect(sql).not.toContain("db_k7_anon");
  });

  it("builds organisation-member policies", () => {
    const sql = policySQL({
      schema: "app",
      table: "projects",
      template: "org_members",
      roles,
      orgColumn: "org_id",
      membersTable: "members",
      membersOrgColumn: "org_id",
      membersUserColumn: "user_id",
    });
    expect(sql).toContain(
      "USING (org_id IN (SELECT org_id FROM app.members WHERE user_id = pgd_auth.uid()))",
    );
  });

  it("builds public-read policies that include anon", () => {
    const sql = policySQL({
      schema: "public",
      table: "posts",
      template: "public_read",
      roles,
      ownerColumn: "author_id",
    });
    expect(sql).toContain("FOR SELECT TO db_k7_anon, db_k7_user USING (true);");
    expect(sql).toContain(
      "FOR DELETE TO db_k7_user USING (author_id = pgd_auth.uid());",
    );
  });

  it("says what's missing", () => {
    expect(
      policyProblem({ schema: "public", table: "t", template: "owner", roles }),
    ).toMatch(/owner/);
    expect(
      policyProblem({
        schema: "public",
        table: "t",
        template: "org_members",
        roles,
        orgColumn: "o",
      }),
    ).toMatch(/membership/);
    expect(
      policyProblem({
        schema: "public",
        table: "t",
        template: "owner",
        roles,
        ownerColumn: "u",
      }),
    ).toBeNull();
  });

  it("guesses the owner column", () => {
    expect(
      guessOwnerColumn([
        { name: "id", base_type: "uuid" },
        { name: "user_id", base_type: "uuid" },
      ]),
    ).toBe("user_id");
    expect(
      guessOwnerColumn([
        { name: "id", base_type: "uuid" },
        { name: "writer", base_type: "uuid" },
      ]),
    ).toBe("writer");
    expect(
      guessOwnerColumn([{ name: "id", base_type: "int8" }]),
    ).toBeUndefined();
  });
});
