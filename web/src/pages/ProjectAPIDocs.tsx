// The API page's developer panels (V4.1 §9.1-9.3): the quick start, the
// per-table docs and this month's usage.
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api, errorMessage, type Project } from "../api/client";
import type { components } from "../api/schema";
import {
  Alert,
  Badge,
  Button,
  CodeBlock,
  CopyButton,
  KeyValues,
  Panel,
  Segmented,
  Select,
  Spinner,
  Table,
} from "../components/ui";
import {
  functionExample,
  LANG_LABELS,
  quickStart,
  relName,
  tableExamples,
  type ClientLang,
  type Example,
  type ExplorePrefill,
  type Lang,
} from "../lib/apiDocs";
import { naira } from "../lib/billing";
import { sessionQuery } from "../lib/session";
import { SERVICE_LABELS } from "./Billing";

type Services = components["schemas"]["BackendServices"];
type CatalogTable = components["schemas"]["CatalogTable"];

const KEY_PLACEHOLDER = "pgd_pub_…";

/** The project's publishable key, when its whole value is known. */
export function publishableKey(svc: Services): string {
  return (
    svc.keys?.find((k) => k.kind === "publishable" && !k.revoked_at && k.key)
      ?.key ?? KEY_PLACEHOLDER
  );
}

function Copyable({ value }: { value: string }) {
  return (
    <span className="inline-flex items-center gap-2 font-mono text-xs">
      {value}
      <CopyButton value={value} />
    </span>
  );
}

function readFlag(key: string): boolean | null {
  try {
    const v = localStorage.getItem(key);
    return v === null ? null : v === "1";
  } catch {
    return null;
  }
}

function writeFlag(key: string, v: boolean) {
  try {
    localStorage.setItem(key, v ? "1" : "0");
  } catch {
    /* storage unavailable: not remembered */
  }
}

/** Quick start (V4.1 §9.1): the URL and publishable key filled in, in each
 * client language. Collapsed or open as this user last left it. */
export function QuickStartPanel({ p, svc }: { p: Project; svc: Services }) {
  const { data: session } = useQuery(sessionQuery);
  const storeKey = `pgdock.quickstart.${session?.user?.id ?? "anon"}.${p.id}`;
  const [open, setOpenState] = useState<boolean>(
    () => readFlag(storeKey) ?? true,
  );
  const setOpen = (v: boolean) => {
    setOpenState(v);
    writeFlag(storeKey, v);
  };
  const [lang, setLang] = useState<ClientLang>("ts");
  const url = svc.url ?? "";
  const key = publishableKey(svc);
  const q = quickStart(lang, url, key);
  return (
    <Panel
      title="Quick start"
      testId="services-quickstart"
      description="Your first request from an app, with this project's URL and publishable key."
      actions={
        <Button
          size="small"
          variant="ghost"
          aria-expanded={open}
          onClick={() => setOpen(!open)}
        >
          {open ? "Hide" : "Show"}
        </Button>
      }
    >
      {open && (
        <div className="flex flex-col gap-3">
          <KeyValues
            testId="quickstart-values"
            items={[
              ["Project URL", <Copyable key="url" value={url} />],
              ["Publishable key", <Copyable key="key" value={key} />],
            ]}
          />
          {key === KEY_PLACEHOLDER && (
            <Alert tone="accent">
              Create a publishable key under API keys to fill it in here.
            </Alert>
          )}
          <Segmented
            value={lang}
            onChange={setLang}
            options={(["ts", "dart", "go"] as const).map((l) => ({
              value: l,
              label: LANG_LABELS[l],
            }))}
          />
          <CodeBlock code={q.install} />
          <CodeBlock code={q.code} />
          <p className="text-[13px] text-muted">
            Typed rows come from{" "}
            <button
              type="button"
              className="text-accent-text underline underline-offset-2 hover:no-underline"
              onClick={() =>
                document
                  .querySelector('[data-testid="services-types"]')
                  ?.scrollIntoView({ behavior: "smooth" })
              }
            >
              Generate types
            </button>{" "}
            below, or <code>pgdock gen types --lang {lang}</code>.
          </p>
        </div>
      )}
    </Panel>
  );
}

function AccessBadges({ t }: { t: CatalogTable }) {
  return (
    <div className="flex flex-wrap gap-2" data-testid="docs-access">
      {(["anon", "user"] as const).map((role) => {
        const a = t.access[role];
        const can = a
          ? (["select", "insert", "update", "delete"] as const).filter(
              (k) => a[k],
            )
          : [];
        return (
          <Badge key={role} tone={can.length ? "warn" : "muted"}>
            {role}: {can.length ? can.join(", ") : "no access"}
          </Badge>
        );
      })}
    </div>
  );
}

function Examples({
  examples,
  onTry,
}: {
  examples: Example[];
  onTry: (e: ExplorePrefill) => void;
}) {
  const [lang, setLang] = useState<Lang>("curl");
  return (
    <div className="flex flex-col gap-3">
      <Segmented
        value={lang}
        onChange={setLang}
        options={(["curl", "ts", "dart", "go"] as const).map((l) => ({
          value: l,
          label: LANG_LABELS[l],
        }))}
      />
      {examples.map((e) => (
        <div
          key={e.id}
          className="flex flex-col gap-1"
          data-testid={`docs-example-${e.id}`}
        >
          <div className="flex items-center justify-between">
            <h4 className="text-[13px] font-medium">{e.title}</h4>
            <Button size="small" onClick={() => onTry(e.explore)}>
              Try it
            </Button>
          </div>
          <CodeBlock code={e.code[lang]} />
        </div>
      ))}
    </div>
  );
}

/** The API docs (V4.1 §9.2): each exposed table, view and function with
 * its columns, row-level security and examples. */
export function DocsPanel({
  p,
  svc,
  onTry,
}: {
  p: Project;
  svc: Services;
  onTry: (e: ExplorePrefill) => void;
}) {
  const q = useQuery({
    queryKey: ["services-catalog", p.id],
    queryFn: () => api.servicesCatalog(p.id),
    retry: false,
  });
  const [sel, setSel] = useState("");
  const url = q.data?.api_url ?? svc.url ?? "";
  const key = publishableKey(svc);
  const items = q.data
    ? [
        ...q.data.tables.map((t) => ({
          id: `t:${relName(t.schema, t.name)}`,
          label: `${relName(t.schema, t.name)}${t.kind === "table" ? "" : ` (${t.kind.replace("_", " ")})`}`,
        })),
        ...q.data.functions.map((f) => ({
          id: `f:${relName(f.schema, f.name)}`,
          label: `${relName(f.schema, f.name)}()`,
        })),
      ]
    : [];
  const current = sel || items[0]?.id || "";
  const table = q.data?.tables.find(
    (t) => `t:${relName(t.schema, t.name)}` === current,
  );
  const fn = q.data?.functions.find(
    (f) => `f:${relName(f.schema, f.name)}` === current,
  );
  return (
    <Panel
      title="API docs"
      testId="services-docs"
      description="What each table, view and function accepts, who may use it, and how to call it."
    >
      {q.isPending ? (
        <Spinner />
      ) : q.isError ? (
        <Alert>{errorMessage(q.error)}</Alert>
      ) : items.length === 0 ? (
        <p className="text-[13px] text-muted">
          Nothing is exposed yet: create a table in {q.data.schemas.join(", ")}.
        </p>
      ) : (
        <div className="flex flex-col gap-4">
          <Select
            aria-label="Table or function"
            value={current}
            onChange={(e) => setSel(e.target.value)}
            className="w-72"
          >
            {items.map((i) => (
              <option key={i.id} value={i.id}>
                {i.label}
              </option>
            ))}
          </Select>
          {table && (
            <div className="flex flex-col gap-4" data-testid="docs-table">
              <Table head={["Column", "Type", "Nullable", "Default"]}>
                {table.columns.map((c) => (
                  <tr key={c.name}>
                    <td className="px-3 py-2 font-mono text-xs">
                      {c.name}
                      {table.primary_key.includes(c.name) && (
                        <span className="ml-1 text-muted">(key)</span>
                      )}
                    </td>
                    <td className="px-3 py-2 font-mono text-xs">
                      {c.type}
                      {c.enum && (
                        <span className="text-muted">
                          {" "}
                          ({c.enum.join(", ")})
                        </span>
                      )}
                    </td>
                    <td className="px-3 py-2 text-xs">
                      {c.nullable ? "yes" : "no"}
                    </td>
                    <td className="px-3 py-2 font-mono text-xs text-muted">
                      {c.identity
                        ? "identity"
                        : c.generated
                          ? "generated"
                          : (c.default ?? "—")}
                    </td>
                  </tr>
                ))}
              </Table>
              <div className="flex flex-col gap-2">
                <div className="flex items-center gap-2 text-[13px]">
                  Row-level security
                  <Badge tone={table.rls ? "ok" : "danger"}>
                    {table.rls ? "on" : "off"}
                  </Badge>
                  {table.public && <Badge tone="warn">public table</Badge>}
                </div>
                <AccessBadges t={table} />
                {table.policies.length > 0 && (
                  <Table head={["Policy", "For", "Roles", "Rule"]}>
                    {table.policies.map((pol) => (
                      <tr key={pol.name}>
                        <td className="px-3 py-2 text-xs">{pol.name}</td>
                        <td className="px-3 py-2 text-xs">{pol.command}</td>
                        <td className="px-3 py-2 text-xs">
                          {pol.roles.join(", ")}
                        </td>
                        <td className="px-3 py-2 font-mono text-xs">
                          {pol.using && <div>using {pol.using}</div>}
                          {pol.check && <div>check {pol.check}</div>}
                        </td>
                      </tr>
                    ))}
                  </Table>
                )}
              </div>
              <Examples
                examples={tableExamples(table, url, key)}
                onTry={onTry}
              />
            </div>
          )}
          {fn && (
            <div className="flex flex-col gap-3" data-testid="docs-function">
              <p className="font-mono text-xs">
                {relName(fn.schema, fn.name)}(
                {fn.args
                  .map((a) => `${a.name} ${a.type}${a.optional ? "?" : ""}`)
                  .join(", ")}
                ) → {fn.returns_set ? "setof " : ""}
                {fn.returns}
                <span className="text-muted"> · {fn.volatility}</span>
              </p>
              <Examples
                examples={[functionExample(fn, url, key)]}
                onTry={onTry}
              />
            </div>
          )}
        </div>
      )}
    </Panel>
  );
}

const METRIC_LABELS: Record<string, string> = {
  api_requests: "Requests",
  api_egress_gb: "Transfer out (GB)",
  auth_mau: "Monthly active users",
  messages_sms: "SMS codes",
  messages_whatsapp: "WhatsApp codes",
  storage_gb_hours: "File storage (GB-hours)",
  storage_egress_gb: "File downloads (GB)",
  image_transforms: "Image transforms",
  realtime_connection_minutes: "Realtime connection minutes",
  realtime_messages: "Realtime messages",
};

const num = (n: number | null | undefined) =>
  n == null ? "—" : n.toLocaleString("en-NG", { maximumFractionDigits: 2 });

/** Usage this month (V4.1 §9.3): the project's, the organisation's, and
 * the plan's allowance and limit, which the organisation's projects share. */
export function UsagePanel({ p }: { p: Project }) {
  const q = useQuery({
    queryKey: ["services-usage", p.id],
    queryFn: () => api.servicesUsage(p.id),
    refetchInterval: 60_000,
    retry: false,
  });
  return (
    <Panel
      title="Usage this month"
      testId="services-usage"
      description="Allowances and limits are the organisation's, shared by its projects."
    >
      {q.isPending ? (
        <Spinner />
      ) : q.isError ? (
        <Alert>{errorMessage(q.error)}</Alert>
      ) : (
        <div className="flex flex-col gap-4">
          <Table
            head={["", "This project", "Organisation", "Included", "Limit"]}
          >
            {q.data.metrics.map((m) => {
              const cap = m.limit ?? null;
              const over = cap != null && m.org_quantity >= cap;
              return (
                <tr key={m.metric} data-testid="usage-row">
                  <td className="px-3 py-2 text-xs">
                    {METRIC_LABELS[m.metric] ?? m.metric}
                  </td>
                  <td className="px-3 py-2 text-xs">{num(m.quantity)}</td>
                  <td className="px-3 py-2 text-xs">{num(m.org_quantity)}</td>
                  <td className="px-3 py-2 text-xs text-muted">
                    {num(m.included)}
                  </td>
                  <td className="px-3 py-2 text-xs">
                    {over ? <Badge tone="danger">{num(cap)}</Badge> : num(cap)}
                  </td>
                </tr>
              );
            })}
          </Table>
          {q.data.charges && (
            <div data-testid="usage-charges">
              <h4 className="mb-2 text-[13px] font-medium">
                Charges so far, before VAT
              </h4>
              {q.data.charges.length === 0 ? (
                <p className="text-[13px] text-muted">
                  Nothing beyond the plan this month.
                </p>
              ) : (
                <KeyValues
                  items={q.data.charges.map((c) => [
                    SERVICE_LABELS[c.service] ?? c.service,
                    naira(c.amount_minor),
                  ])}
                />
              )}
            </div>
          )}
        </div>
      )}
    </Panel>
  );
}
