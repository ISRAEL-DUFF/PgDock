// Row-level security policy templates for the table editor's policy helper
// (V4 §3.6): each builds the SQL that enables RLS on a table and adds the
// policies, shown to the user before it runs.

export type PolicyTemplate = "owner" | "org_members" | "public_read";

export type Roles = { anon: string; user: string };

export type PolicyInput = {
  schema: string;
  table: string;
  template: PolicyTemplate;
  roles: Roles;
  /** The column holding the owner's user id (owner, public_read). */
  ownerColumn?: string;
  /** org_members: the column holding the row's organisation. */
  orgColumn?: string;
  /** org_members: the membership table, its organisation and user columns. */
  membersTable?: string;
  membersOrgColumn?: string;
  membersUserColumn?: string;
};

export const policyTemplates: {
  id: PolicyTemplate;
  label: string;
  hint: string;
}[] = [
  {
    id: "owner",
    label: "Owner only",
    hint: "Signed-in users read and change only their own rows.",
  },
  {
    id: "org_members",
    label: "Members of an organisation",
    hint: "Signed-in users read and change the rows of the organisations they belong to, from a membership table.",
  },
  {
    id: "public_read",
    label: "Public read, owner write",
    hint: "Anyone with your app reads every row; signed-in users change only their own.",
  },
];

/** Quotes an identifier the way Postgres would need it. */
export function ident(name: string): string {
  return /^[a-z_][a-z0-9_$]*$/.test(name)
    ? name
    : `"${name.replace(/"/g, '""')}"`;
}

/** schema.table, quoted; a dotted name is taken as schema.table. */
function qualified(schema: string, table: string): string {
  return `${ident(schema)}.${ident(table)}`;
}

function membersRef(input: PolicyInput): string {
  const t = input.membersTable ?? "";
  const dot = t.indexOf(".");
  return dot > 0
    ? qualified(t.slice(0, dot), t.slice(dot + 1))
    : qualified(input.schema, t);
}

/** What's missing before the template can be built, or null. */
export function policyProblem(input: PolicyInput): string | null {
  if (input.template === "org_members") {
    if (!input.orgColumn)
      return "Choose the column holding the row's organisation.";
    if (
      !input.membersTable ||
      !input.membersOrgColumn ||
      !input.membersUserColumn
    )
      return "Name the membership table and its organisation and user columns.";
    return null;
  }
  if (!input.ownerColumn)
    return "Choose the column holding the owner's user id.";
  return null;
}

/** The SQL for a template: enable RLS, then one policy per command. */
export function policySQL(input: PolicyInput): string {
  const t = qualified(input.schema, input.table);
  const user = ident(input.roles.user);
  const anon = ident(input.roles.anon);
  const name = (suffix: string) => ident(`${input.table}_${suffix}`);
  const lines = [`ALTER TABLE ${t} ENABLE ROW LEVEL SECURITY;`];
  const crud = (cond: string) => {
    lines.push(
      `CREATE POLICY ${name("select")} ON ${t} FOR SELECT TO ${user} USING (${cond});`,
    );
    lines.push(
      `CREATE POLICY ${name("insert")} ON ${t} FOR INSERT TO ${user} WITH CHECK (${cond});`,
    );
    lines.push(
      `CREATE POLICY ${name("update")} ON ${t} FOR UPDATE TO ${user} USING (${cond}) WITH CHECK (${cond});`,
    );
    lines.push(
      `CREATE POLICY ${name("delete")} ON ${t} FOR DELETE TO ${user} USING (${cond});`,
    );
  };
  switch (input.template) {
    case "owner":
      crud(`${ident(input.ownerColumn ?? "")} = pgd_auth.uid()`);
      break;
    case "org_members":
      crud(
        `${ident(input.orgColumn ?? "")} IN (SELECT ${ident(input.membersOrgColumn ?? "")} FROM ${membersRef(input)} WHERE ${ident(input.membersUserColumn ?? "")} = pgd_auth.uid())`,
      );
      break;
    case "public_read": {
      const own = `${ident(input.ownerColumn ?? "")} = pgd_auth.uid()`;
      lines.push(
        `CREATE POLICY ${name("read_all")} ON ${t} FOR SELECT TO ${anon}, ${user} USING (true);`,
      );
      lines.push(
        `CREATE POLICY ${name("insert")} ON ${t} FOR INSERT TO ${user} WITH CHECK (${own});`,
      );
      lines.push(
        `CREATE POLICY ${name("update")} ON ${t} FOR UPDATE TO ${user} USING (${own}) WITH CHECK (${own});`,
      );
      lines.push(
        `CREATE POLICY ${name("delete")} ON ${t} FOR DELETE TO ${user} USING (${own});`,
      );
      break;
    }
  }
  return lines.join("\n");
}

/** The column a user id most likely lives in: a uuid named like an owner. */
export function guessOwnerColumn(
  cols: { name: string; base_type: string }[],
): string | undefined {
  const uuids = cols.filter((c) => c.base_type === "uuid");
  for (const n of [
    "owner_id",
    "user_id",
    "created_by",
    "author_id",
    "profile_id",
  ]) {
    if (uuids.some((c) => c.name === n)) return n;
  }
  return uuids.find((c) => c.name !== "id")?.name;
}
