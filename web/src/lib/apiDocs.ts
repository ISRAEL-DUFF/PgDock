// Request examples for the API page's quick start and per-table docs
// (V4.1 §9.1, §9.2), built from the services catalog. Only the publishable
// key ever goes in an example.
import type { components } from "../api/schema";

type CatalogTable = components["schemas"]["CatalogTable"];
type CatalogColumn = components["schemas"]["CatalogColumn"];
type CatalogFunction = components["schemas"]["CatalogFunction"];

export type Lang = "curl" | "ts" | "dart" | "go";
export type ClientLang = Exclude<Lang, "curl">;

export const LANG_LABELS: Record<Lang, string> = {
  curl: "curl",
  ts: "TypeScript",
  dart: "Dart",
  go: "Go",
};

/** What "Try it" puts in the request explorer. */
export interface ExplorePrefill {
  method: "GET" | "POST" | "PATCH" | "DELETE";
  path: string;
  body?: string;
}

export interface Example {
  id: "read" | "insert" | "update" | "delete" | "upsert" | "rpc";
  title: string;
  code: Record<Lang, string>;
  explore: ExplorePrefill;
}

const installs: Record<ClientLang, string> = {
  ts: "npm install @pgdock/client",
  dart: "flutter pub add pgdock",
  go: "go get github.com/israel-duff/pgdock/sdk/go",
};

/** The quick start in one language: its install line and a first program
 * (createClient, a read, sign-in with a phone code). */
export function quickStart(
  lang: ClientLang,
  url: string,
  key: string,
  table = "todos",
): { install: string; code: string } {
  const code = {
    ts: `import { createClient } from "@pgdock/client";

const pgd = createClient("${url}", "${key}");

// Read: the rows row-level security lets this caller see.
const { data, error } = await pgd.data.from("${table}").select().limit(20);

// Sign in with a code by SMS (or channel: "whatsapp").
await pgd.auth.signInWithOtp({ phone: "+2348031234567", channel: "sms" });
const { data: session } = await pgd.auth.verifyOtp({ type: "sms", phone: "+2348031234567", token: "123456" });`,
    dart: `import 'package:pgdock/pgdock.dart';

final pgd = PgdockClient('${url}', '${key}');

// Read: the rows row-level security lets this caller see.
final page = await pgd.data.from('${table}').select().limit(20).get();

// Sign in with a code by SMS (or channel: 'whatsapp').
await pgd.auth.signInWithOtp(phone: '+2348031234567', channel: 'sms');
final session = await pgd.auth.verifyOtp(type: 'sms', phone: '+2348031234567', token: '123456');`,
    go: `import pgdock "github.com/israel-duff/pgdock/sdk/go"

c, err := pgdock.New("${url}", "${key}")

// Read: the rows row-level security lets this caller see.
rows, _, err := pgdock.List[map[string]any](ctx, c.Data.From("${table}").Select("*").Limit(20))

// Sign in with a code by SMS (or Channel: "whatsapp").
err = c.Auth.SignInWithOTP(ctx, pgdock.OTP{Phone: "+2348031234567", Channel: "sms"})
session, err := c.Auth.VerifyOTP(ctx, pgdock.Verify{Type: "sms", Phone: "+2348031234567", Token: "123456"})`,
  }[lang];
  return { install: installs[lang], code };
}

/** The name the data API knows a relation by. */
export function relName(schema: string, name: string): string {
  return schema === "public" ? name : `${schema}.${name}`;
}

/** A plausible value for a column, for example bodies. */
export function sampleValue(c: CatalogColumn): unknown {
  if (c.enum && c.enum.length > 0) return c.enum[0];
  const t = c.type.toLowerCase();
  if (t.endsWith("[]")) return [];
  if (t === "boolean" || t === "bool") return true;
  if (/^(small|big)?int|^integer|serial/.test(t)) return 1;
  if (/numeric|decimal|real|double|float/.test(t)) return 1.5;
  if (t === "uuid") return "00000000-0000-0000-0000-000000000000";
  if (t.startsWith("timestamp")) return "2026-01-01T09:00:00Z";
  if (t === "date") return "2026-01-01";
  if (t === "json" || t === "jsonb") return {};
  return c.name === "email" ? "ada@example.com" : `a ${c.name}`;
}

const writable = (c: CatalogColumn) => !c.identity && !c.generated;

/** The columns an insert example sends: those that need a value, or the
 * first writable one when none does. */
function insertColumns(t: CatalogTable): CatalogColumn[] {
  const w = t.columns.filter(writable);
  const required = w.filter((c) => !c.nullable && c.default == null);
  return required.length > 0 ? required : w.slice(0, 1);
}

function row(cols: CatalogColumn[]): Record<string, unknown> {
  return Object.fromEntries(cols.map((c) => [c.name, sampleValue(c)]));
}

// ---- Rendering one call in each language ------------------------------------

const json = (v: unknown) => JSON.stringify(v);
const dartLit = (v: unknown): string => {
  if (typeof v === "string") return `'${v.replace(/'/g, "\\'")}'`;
  if (Array.isArray(v)) return `[${v.map(dartLit).join(", ")}]`;
  if (v && typeof v === "object")
    return `{${Object.entries(v)
      .map(([k, x]) => `'${k}': ${dartLit(x)}`)
      .join(", ")}}`;
  return String(v);
};
const goLit = (v: unknown): string => {
  if (typeof v === "string") return json(v);
  if (Array.isArray(v)) return `[]any{${v.map(goLit).join(", ")}}`;
  if (v && typeof v === "object")
    return `map[string]any{${Object.entries(v)
      .map(([k, x]) => `${json(k)}: ${goLit(x)}`)
      .join(", ")}}`;
  return String(v);
};

interface Filter {
  column: string;
  value: unknown;
}

/** The read example's filter: a boolean or enum column if there is one. */
function readFilter(t: CatalogTable): Filter | null {
  const b = t.columns.find((c) => c.type === "boolean");
  if (b) return { column: b.name, value: false };
  const e = t.columns.find((c) => c.enum && c.enum.length > 0);
  if (e) return { column: e.name, value: e.enum![0] };
  return null;
}

/** The read example's select: up to four columns and each relation its
 * foreign keys reach (both ways). */
export function readSelect(t: CatalogTable): string {
  const cols = t.columns.slice(0, 4).map((c) => c.name);
  const embeds = [...t.foreign_keys, ...t.referenced_by].map(
    (fk) => `${fk.embed}(*)`,
  );
  return [...cols, ...embeds.slice(0, 2)].join(",");
}

/** The examples for a table or view, with the project's URL and key. */
export function tableExamples(
  t: CatalogTable,
  url: string,
  key: string,
): Example[] {
  const name = relName(t.schema, t.name);
  const base = `${url}/data/v1/${name}`;
  const hdr = `-H "apikey: ${key}" -H "Authorization: Bearer $ACCESS_TOKEN"`;
  const select = readSelect(t);
  const f = readFilter(t);
  const pk = t.primary_key;
  const order = pk[0] ?? t.columns[0]?.name;
  const out: Example[] = [];

  // Read, with a filter and the relations it can embed.
  const where = f ? `${f.column}:eq:${String(f.value)}` : null;
  const qs = [
    `select=${select}`,
    ...(where ? [`where=${where}`] : []),
    ...(order ? [`order=${order}:desc`] : []),
    "limit=20",
  ];
  out.push({
    id: "read",
    title: "Read",
    code: {
      curl: `curl -G "${base}" ${hdr} \\\n${qs.map((q) => `  --data-urlencode "${q}"`).join(" \\\n")}`,
      ts: `const { data, error } = await pgd.data\n  .from("${name}")\n  .select("${select}")${f ? `\n  .eq("${f.column}", ${json(f.value)})` : ""}${order ? `\n  .order("${order}", "desc")` : ""}\n  .limit(20);`,
      dart: `final page = await pgd.data\n    .from('${name}')\n    .select('${select}')${f ? `\n    .eq('${f.column}', ${dartLit(f.value)})` : ""}${order ? `\n    .order('${order}', descending: true)` : ""}\n    .limit(20)\n    .get();`,
      go: `rows, page, err := pgdock.List[map[string]any](ctx, c.Data.From("${name}").\n\tSelect("${select}").${f ? `\n\tEq("${f.column}", ${goLit(f.value)}).` : ""}${order ? `\n\tOrder("${order}", true).` : ""}\n\tLimit(20))`,
    },
    explore: {
      method: "GET",
      path: `/data/v1/${name}?${qs.map((q) => q.replace(/ /g, "%20")).join("&")}`,
    },
  });
  if (t.kind !== "table") return out;

  // Insert.
  const ins = row(insertColumns(t));
  out.push({
    id: "insert",
    title: "Insert",
    code: {
      curl: `curl -X POST "${base}" ${hdr} \\\n  -d '${json(ins)}'`,
      ts: `const { data, error } = await pgd.data.from("${name}").insert(${json(ins)});`,
      dart: `final w = await pgd.data.from('${name}').insert(${dartLit(ins)}).run();`,
      go: `n, err := c.Data.From("${name}").Insert(${goLit(ins)}).Exec(ctx, &created)`,
    },
    explore: { method: "POST", path: `/data/v1/${name}`, body: json(ins) },
  });

  // Update and delete by key (single-column keys), else by the filter.
  const set = t.columns.find((c) => writable(c) && !pk.includes(c.name));
  const byKey = pk.length === 1;
  const keyPath = byKey
    ? `/data/v1/${name}/1`
    : `/data/v1/${name}?where=${where ?? `${order}:eq:1`}`;
  if (set) {
    const upd = { [set.name]: sampleValue(set) };
    out.push({
      id: "update",
      title: "Update",
      code: {
        curl: `curl -X PATCH "${url}${keyPath}" ${hdr} \\\n  -d '${json(upd)}'`,
        ts: `await pgd.data.from("${name}").update(${json(upd)})${byKey ? ".byKey(1)" : `.eq("${f?.column ?? order}", ${json(f?.value ?? 1)})`};`,
        dart: `await pgd.data.from('${name}').update(${dartLit(upd)})${byKey ? ".byKey(1)" : `.eq('${f?.column ?? order}', ${dartLit(f?.value ?? 1)})`}.run();`,
        go: `n, err := c.Data.From("${name}").Update(${goLit(upd)}).${byKey ? "Key(1)" : `Eq("${f?.column ?? order}", ${goLit(f?.value ?? 1)})`}.Exec(ctx, nil)`,
      },
      explore: { method: "PATCH", path: keyPath, body: json(upd) },
    });
  }
  out.push({
    id: "delete",
    title: "Delete",
    code: {
      curl: `curl -X DELETE "${url}${keyPath}" ${hdr}`,
      ts: `await pgd.data.from("${name}").delete()${byKey ? ".byKey(1)" : `.eq("${f?.column ?? order}", ${json(f?.value ?? 1)})`};`,
      dart: `await pgd.data.from('${name}').delete()${byKey ? ".byKey(1)" : `.eq('${f?.column ?? order}', ${dartLit(f?.value ?? 1)})`}.run();`,
      go: `n, err := c.Data.From("${name}").Delete().${byKey ? "Key(1)" : `Eq("${f?.column ?? order}", ${goLit(f?.value ?? 1)})`}.Exec(ctx, nil)`,
    },
    explore: { method: "DELETE", path: keyPath },
  });

  // Upsert on the primary key.
  if (pk.length > 0) {
    const up = {
      ...Object.fromEntries(pk.map((k) => [k, 1])),
      ...ins,
    };
    out.push({
      id: "upsert",
      title: "Upsert",
      code: {
        curl: `curl -X POST "${base}?on_conflict=${pk.join(",")}" ${hdr} \\\n  -d '${json(up)}'`,
        ts: `await pgd.data.from("${name}").upsert(${json(up)}, { onConflict: ${json(pk)} });`,
        dart: `await pgd.data.from('${name}').upsert(${dartLit(up)}, onConflict: ${dartLit(pk)}).run();`,
        go: `n, err := c.Data.From("${name}").Upsert(${goLit(up)}, ${pk.map((k) => json(k)).join(", ")}).Exec(ctx, nil)`,
      },
      explore: {
        method: "POST",
        path: `/data/v1/${name}?on_conflict=${pk.join(",")}`,
        body: json(up),
      },
    });
  }
  return out;
}

/** The example for calling a function. */
export function functionExample(
  fn: CatalogFunction,
  url: string,
  key: string,
): Example {
  const name = relName(fn.schema, fn.name);
  const args = Object.fromEntries(
    fn.args
      .filter((a) => !a.optional)
      .map((a) => [
        a.name,
        sampleValue({
          name: a.name,
          type: a.type,
          nullable: false,
          identity: false,
          generated: false,
        }),
      ]),
  );
  const hdr = `-H "apikey: ${key}" -H "Authorization: Bearer $ACCESS_TOKEN"`;
  return {
    id: "rpc",
    title: "Call",
    code: {
      curl: `curl -X POST "${url}/data/v1/rpc/${name}" ${hdr} \\\n  -d '${json(args)}'`,
      ts: `const { data, error } = await pgd.data.rpc("${name}", ${json(args)});`,
      dart: `final result = await pgd.data.rpc('${name}', ${dartLit(args)});`,
      go: `err := c.Data.RPC(ctx, "${name}", ${goLit(args)}, &result, false)`,
    },
    explore: { method: "POST", path: `/data/v1/rpc/${name}`, body: json(args) },
  };
}
