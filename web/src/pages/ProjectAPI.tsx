import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type FormEvent } from "react";
import { api, errorMessage, type Project } from "../api/client";
import type { components } from "../api/schema";
import {
  Alert,
  Badge,
  Button,
  CopyButton,
  Dialog,
  Field,
  FormRow,
  Input,
  KeyValues,
  Page,
  Panel,
  Select,
  Spinner,
  Switch,
  Table,
} from "../components/ui";
import { formatDate, relativeTime } from "../lib/format";
import { useOperationStream } from "../lib/useOperationStream";
import { useProject } from "./ProjectOverview";

type Services = components["schemas"]["BackendServices"];
type CreatedKey = components["schemas"]["CreatedApiKey"];
type ExploreRequest = components["schemas"]["ExploreRequest"];
type ExploreResponse = components["schemas"]["ExploreResponse"];

/** Project Settings → API: backend services (V4 §2) - the project's API
 * URL, its publishable and secret keys, allowed origins and limits, and
 * the request log. */
export function ProjectAPIPage() {
  const { data: p } = useProject();
  if (!p) return null;
  return (
    <Page
      title="API"
      description="Your project's data API, auth, storage and realtime over HTTPS, with publishable and secret keys."
      testId="settings-api"
    >
      <ServicesPanels p={p} />
    </Page>
  );
}

function ServicesPanels({ p }: { p: Project }) {
  const q = useQuery({
    queryKey: ["services", p.id],
    queryFn: () => api.backendServices(p.id),
    refetchInterval: 10_000,
  });
  if (q.isPending) return <Spinner />;
  if (q.isError) return <Alert>{errorMessage(q.error)}</Alert>;
  const svc = q.data;
  return (
    <>
      <StatusPanel p={p} svc={svc} />
      {svc.enabled && (
        <>
          <KeysPanel p={p} svc={svc} />
          <SettingsPanel p={p} svc={svc} />
          <AdvisorPanel p={p} />
          <ExplorerPanel p={p} />
          <TypesPanel p={p} />
          <LogsPanel p={p} />
        </>
      )}
    </>
  );
}

function StatusPanel({ p, svc }: { p: Project; svc: Services }) {
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [opId, setOpId] = useState<string | undefined>();
  const [newKeys, setNewKeys] = useState<CreatedKey[]>([]);
  const stream = useOperationStream(opId);
  const run = async (f: () => Promise<{ id: string }>) => {
    setBusy(true);
    setErr(null);
    try {
      setOpId((await f()).id);
      await qc.invalidateQueries({ queryKey: ["services", p.id] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  const enable = () =>
    run(async () => {
      const r = await api.enableBackendServices(p.id);
      setNewKeys(r.keys);
      return r.operation;
    });
  const running = !!opId && !stream.done;
  useEffect(() => {
    if (stream.done)
      void qc.invalidateQueries({ queryKey: ["services", p.id] });
  }, [stream.done, qc, p.id]);
  return (
    <Panel
      title="Backend services"
      testId="services-status"
      description="Read and write data, sign users in, store files and push live updates straight from your apps. Off until you turn it on."
      actions={
        svc.enabled ? (
          <Button
            variant="ghost"
            busy={busy}
            onClick={() => run(() => api.disableBackendServices(p.id))}
          >
            Turn off
          </Button>
        ) : (
          <Button
            variant="primary"
            busy={busy || !!running}
            onClick={enable}
            disabled={p.status !== "active"}
          >
            Enable backend services
          </Button>
        )
      }
    >
      {err && (
        <div className="mb-3">
          <Alert>{err}</Alert>
        </div>
      )}
      {running && (
        <p className="mb-3 text-[13px] text-muted">
          {stream.log.at(-1)?.msg ?? "Working…"}
        </p>
      )}
      {stream.status === "failed" && (
        <Alert>{stream.error ?? "The operation failed."}</Alert>
      )}
      {svc.enabled ? (
        <KeyValues
          testId="services-url"
          items={[
            [
              "API URL",
              svc.url ? (
                <span className="inline-flex items-center gap-2 font-mono text-xs">
                  {svc.url}
                  <CopyButton value={svc.url} />
                </span>
              ) : (
                <span className="text-muted">
                  Set PGDOCK_API_DOMAIN to show the URL ({svc.ref})
                </span>
              ),
            ],
            [
              "Reference",
              <span key="r" className="font-mono text-xs">
                {svc.ref}
              </span>,
            ],
            ["Enabled", svc.enabled_at ? formatDate(svc.enabled_at) : "—"],
          ]}
        />
      ) : (
        <p className="text-[13px] text-muted">
          Enabling adds the roles your API requests run as and the{" "}
          <span className="font-mono">pgd_auth</span> schema to this database,
          and makes a publishable key (for apps) and a secret key (for servers).
          Tables are reachable with the publishable key only once they have
          row-level security.
        </p>
      )}
      {!svc.feed_configured && svc.enabled && (
        <div className="mt-3">
          <Alert tone="warn" title="No edge connected">
            pgdock-server has no PGDOCK_EDGE_SECRET, so no pgdock-edge can serve
            this project yet (docs/backend-services.md).
          </Alert>
        </div>
      )}
      {newKeys.length > 0 && (
        <NewKeys keys={newKeys} onClose={() => setNewKeys([])} />
      )}
    </Panel>
  );
}

/** The keys made just now; a secret key is shown this once. */
function NewKeys({
  keys,
  onClose,
}: {
  keys: CreatedKey[];
  onClose: () => void;
}) {
  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onClose()}
      title="Your API keys"
      description="Copy the secret key now: it isn't shown again. The publishable key stays visible."
      testId="new-api-keys"
      footer={
        <Button variant="primary" onClick={onClose}>
          I've copied them
        </Button>
      }
    >
      <div className="flex flex-col gap-3">
        {keys.map((k) => (
          <div key={k.key.id} className="flex flex-col gap-1">
            <span className="text-[13px] font-medium">
              {k.key.kind === "secret"
                ? "Secret key (servers only)"
                : "Publishable key (apps)"}{" "}
              · {k.key.name}
            </span>
            <span
              className="flex items-center gap-2 break-all rounded bg-surface-2 p-2 font-mono text-xs"
              data-testid={`key-${k.key.kind}`}
            >
              {k.value}
              <CopyButton value={k.value} />
            </span>
          </div>
        ))}
      </div>
    </Dialog>
  );
}

function KeysPanel({ p, svc }: { p: Project; svc: Services }) {
  const qc = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [kind, setKind] = useState<"publishable" | "secret">("publishable");
  const [name, setName] = useState("");
  const [made, setMade] = useState<CreatedKey | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const create = async (e: FormEvent) => {
    e.preventDefault();
    setErr(null);
    try {
      setMade(await api.createAPIKey(p.id, { kind, name }));
      setCreating(false);
      setName("");
      await qc.invalidateQueries({ queryKey: ["services", p.id] });
    } catch (e) {
      setErr(errorMessage(e));
    }
  };
  const revoke = async (id: string) => {
    setErr(null);
    try {
      await api.revokeAPIKey(p.id, id);
      await qc.invalidateQueries({ queryKey: ["services", p.id] });
    } catch (e) {
      setErr(errorMessage(e));
    }
  };
  return (
    <Panel
      title="API keys"
      testId="services-keys"
      description="The publishable key goes in browsers and mobile apps; requests run as anon, or as the signed-in user. The secret key bypasses row-level security: keep it on servers."
      actions={<Button onClick={() => setCreating(true)}>New key</Button>}
    >
      {err && (
        <div className="mb-3">
          <Alert>{err}</Alert>
        </div>
      )}
      <Table head={["Name", "Kind", "Key", "Last used", ""]}>
        {svc.keys.map((k) => (
          <tr key={k.id} data-testid="api-key-row">
            <td className="px-3 py-2 font-medium">{k.name}</td>
            <td className="px-3 py-2">
              <Badge tone={k.kind === "secret" ? "warn" : "accent"}>
                {k.kind}
              </Badge>
              {k.revoked_at && (
                <span className="ml-1">
                  <Badge tone="muted">revoked</Badge>
                </span>
              )}
            </td>
            <td className="px-3 py-2 font-mono text-xs">
              {k.key && !k.revoked_at ? (
                <span className="inline-flex items-center gap-2">
                  {k.key.slice(0, 18)}…
                  <CopyButton value={k.key} />
                </span>
              ) : (
                `${k.prefix}…`
              )}
            </td>
            <td className="px-3 py-2 text-xs text-muted">
              {k.last_used_at ? relativeTime(k.last_used_at) : "never"}
            </td>
            <td className="px-3 py-2 text-right">
              {!k.revoked_at && (
                <Button
                  variant="ghost"
                  className="text-xs"
                  onClick={() => void revoke(k.id)}
                >
                  Revoke
                </Button>
              )}
            </td>
          </tr>
        ))}
      </Table>
      <Dialog
        open={creating}
        onOpenChange={setCreating}
        title="New API key"
        description="Make a new key, move your apps to it, then revoke the old one."
        footer={
          <>
            <Button variant="ghost" onClick={() => setCreating(false)}>
              Cancel
            </Button>
            <Button type="submit" form="new-api-key" variant="primary">
              Create
            </Button>
          </>
        }
      >
        <form
          id="new-api-key"
          className="flex flex-col gap-3"
          onSubmit={create}
        >
          <Field label="Name">
            {(id) => (
              <Input
                id={id}
                required
                maxLength={64}
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            )}
          </Field>
          <Field label="Kind">
            {(id) => (
              <Select
                id={id}
                value={kind}
                onChange={(e) =>
                  setKind(e.target.value as "publishable" | "secret")
                }
              >
                <option value="publishable">Publishable (apps)</option>
                <option value="secret">Secret (servers)</option>
              </Select>
            )}
          </Field>
        </form>
      </Dialog>
      {made && <NewKeys keys={[made]} onClose={() => setMade(null)} />}
    </Panel>
  );
}

function SettingsPanel({ p, svc }: { p: Project; svc: Services }) {
  const qc = useQueryClient();
  const [origins, setOrigins] = useState(svc.cors_origins.join("\n"));
  const [timeout, setTimeoutMs] = useState(
    svc.settings.statement_timeout_ms ?? 0,
  );
  const [perIP, setPerIP] = useState(svc.settings.rate_per_ip ?? 0);
  const [exposed, setExposed] = useState(svc.exposed_schemas.join(", "));
  const [publicTables, setPublicTables] = useState(
    svc.public_tables.join("\n"),
  );
  const [secretInBrowser, setSecretInBrowser] = useState(
    svc.settings.allow_secret_in_browser ?? false,
  );
  const [replicaReads, setReplicaReads] = useState(
    svc.settings.replica_reads ?? false,
  );
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    setSaved(false);
    try {
      await api.updateBackendServices(p.id, {
        cors_origins: origins
          .split(/[\s,]+/)
          .map((o) => o.trim())
          .filter(Boolean),
        exposed_schemas: exposed
          .split(/[\s,]+/)
          .map((o) => o.trim())
          .filter(Boolean),
        public_tables: publicTables
          .split(/[\s,]+/)
          .map((o) => o.trim())
          .filter(Boolean),
        settings: {
          statement_timeout_ms: timeout,
          rate_per_ip: perIP,
          allow_secret_in_browser: secretInBrowser,
          replica_reads: replicaReads,
        },
      });
      await qc.invalidateQueries({ queryKey: ["services", p.id] });
      setSaved(true);
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Panel title="Access and limits" testId="services-settings">
      <form className="flex flex-col" onSubmit={save}>
        <FormRow
          label="Allowed origins"
          description="Pages that may call the API, one per line (https://app.example.com). Empty allows any."
        >
          <textarea
            aria-label="Allowed origins"
            className="min-h-20 w-full rounded-md border border-line-strong bg-surface-2 p-2.5 font-mono text-xs"
            value={origins}
            onChange={(e) => setOrigins(e.target.value)}
          />
        </FormRow>
        <FormRow
          label="Exposed schemas"
          description="The schemas the data API serves, comma separated (public by default)."
        >
          <Input
            aria-label="Exposed schemas"
            value={exposed}
            onChange={(e) => setExposed(e.target.value)}
          />
        </FormRow>
        <FormRow
          label="Public tables"
          description="Tables the publishable key may read without row-level security, one per line. Anyone with your app can read them."
        >
          <textarea
            aria-label="Public tables"
            className="min-h-16 w-full rounded-md border border-line-strong bg-surface-2 p-2.5 font-mono text-xs"
            value={publicTables}
            onChange={(e) => setPublicTables(e.target.value)}
          />
        </FormRow>
        <FormRow
          label="Statement timeout (ms)"
          description="Each request's limit; 0 is the default (8000), at most 15000."
        >
          <Input
            type="number"
            min={0}
            max={15000}
            aria-label="Statement timeout (ms)"
            value={timeout}
            onChange={(e) => setTimeoutMs(Number(e.target.value))}
          />
        </FormRow>
        <FormRow
          label="Requests per minute per IP"
          description="0 is the default (600)."
        >
          <Input
            type="number"
            min={0}
            value={perIP}
            onChange={(e) => setPerIP(Number(e.target.value))}
            aria-label="Requests per minute per IP"
          />
        </FormRow>
        <FormRow
          label="Allow the secret key from browsers"
          description="Off: requests with a secret key and an Origin header are refused."
        >
          <Switch
            aria-label="Allow the secret key from browsers"
            checked={secretInBrowser}
            onCheckedChange={setSecretInBrowser}
          />
        </FormRow>
        {p.tier === "dedicated" && (
          <FormRow
            label="Read from replicas by default"
            description="Publishable-key reads (GET) go to the project's read replicas, which can trail writes by a few seconds. Any request can ask with the header Read-Replica: allowed, or opt out with Read-Replica: primary."
          >
            <Switch
              aria-label="Read from replicas by default"
              checked={replicaReads}
              onCheckedChange={setReplicaReads}
            />
          </FormRow>
        )}
        <div className="flex items-center justify-end gap-3 pt-3">
          {saved && (
            <span className="text-xs text-muted">
              Saved; the edge applies it within seconds.
            </span>
          )}
          {err && <Alert>{err}</Alert>}
          <Button type="submit" variant="primary" busy={busy}>
            Save
          </Button>
        </div>
      </form>
    </Panel>
  );
}

function LogsPanel({ p }: { p: Project }) {
  const q = useQuery({
    queryKey: ["api-logs", p.id],
    queryFn: () => api.apiRequestLogs(p.id),
    refetchInterval: 15_000,
    retry: false,
  });
  return (
    <Panel
      title="Request log"
      description="The last requests to the API (kept 7 days)."
      testId="services-logs"
    >
      {q.isError ? (
        <Alert>{errorMessage(q.error)}</Alert>
      ) : !q.data || q.data.items.length === 0 ? (
        <p className="text-[13px] text-muted">No requests yet.</p>
      ) : (
        <Table head={["When", "Request", "Status", "Role", "Latency"]}>
          {q.data.items.map((l) => (
            <tr key={l.id} data-testid="api-log-row">
              <td
                className="px-3 py-2 text-xs text-muted"
                title={formatDate(l.at)}
              >
                {relativeTime(l.at)}
              </td>
              <td className="px-3 py-2 font-mono text-xs">
                {l.method} {l.path}
              </td>
              <td className="px-3 py-2">
                <Badge
                  tone={
                    l.status < 300 ? "ok" : l.status < 500 ? "warn" : "danger"
                  }
                >
                  {l.status}
                </Badge>
              </td>
              <td className="px-3 py-2 text-xs">{l.role ?? "—"}</td>
              <td className="px-3 py-2 text-xs text-muted">
                {l.latency_ms} ms
              </td>
            </tr>
          ))}
        </Table>
      )}
    </Panel>
  );
}

/** The security advisor (V4 §3.5): what an app's users could reach that
 * they perhaps shouldn't. */
function AdvisorPanel({ p }: { p: Project }) {
  const q = useQuery({
    queryKey: ["services-advisor", p.id],
    queryFn: () => api.securityAdvisor(p.id),
    retry: false,
  });
  return (
    <Panel
      title="Security advisor"
      testId="services-advisor"
      description="Checks the exposed schemas for tables without row-level security, policies that let everyone in, and functions that skip it."
      actions={
        <Button
          variant="ghost"
          busy={q.isFetching}
          onClick={() => void q.refetch()}
        >
          Check again
        </Button>
      }
    >
      {q.isPending ? (
        <Spinner />
      ) : q.isError ? (
        <Alert>{errorMessage(q.error)}</Alert>
      ) : q.data.items.length === 0 ? (
        <p className="text-[13px] text-muted" data-testid="advisor-clear">
          Nothing to flag.
        </p>
      ) : (
        <ul className="flex flex-col divide-y divide-line">
          {q.data.items.map((f) => (
            <li
              key={`${f.code} ${f.object}`}
              className="flex flex-col gap-1 py-2"
              data-testid="advisor-finding"
            >
              <span className="flex items-center gap-2">
                <Badge
                  tone={
                    f.level === "danger"
                      ? "danger"
                      : f.level === "warn"
                        ? "warn"
                        : "muted"
                  }
                >
                  {f.level}
                </Badge>
                <span className="font-mono text-xs">{f.object}</span>
              </span>
              <span className="text-[13px]">{f.message}</span>
              {f.fix && (
                <span className="flex items-start gap-2">
                  <code className="flex-1 rounded bg-surface-2 p-1.5 font-mono text-[11px] break-all">
                    {f.fix}
                  </code>
                  <CopyButton value={f.fix} />
                </span>
              )}
            </li>
          ))}
        </ul>
      )}
    </Panel>
  );
}

/** The request explorer (V4 §3.4): send a data API request as anon, a
 * user or the service role and see what that caller would get. */
function ExplorerPanel({ p }: { p: Project }) {
  const [method, setMethod] = useState<ExploreRequest["method"]>("GET");
  const [path, setPath] = useState("/data/v1/");
  const [role, setRole] = useState<ExploreRequest["role"]>("anon");
  const [userId, setUserId] = useState("");
  const [body, setBody] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [res, setRes] = useState<ExploreResponse | null>(null);
  const send = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      setRes(
        await api.exploreDataAPI(p.id, {
          method,
          path,
          role,
          user_id: role === "user" && userId ? userId : undefined,
          body: method === "POST" || method === "PATCH" ? body : undefined,
        }),
      );
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  let pretty = res?.body ?? "";
  if (res?.content_type?.includes("json")) {
    try {
      pretty = JSON.stringify(JSON.parse(res.body), null, 2);
    } catch {
      /* not JSON after all */
    }
  }
  return (
    <Panel
      title="Request explorer"
      testId="services-explorer"
      description="Try a data API request as a visitor (anon), a signed-in user or the service role. Writes are real."
    >
      <form className="flex flex-col gap-3" onSubmit={send}>
        <div className="flex flex-wrap gap-2">
          <Select
            aria-label="Method"
            value={method}
            onChange={(e) =>
              setMethod(e.target.value as ExploreRequest["method"])
            }
            className="w-28"
          >
            {["GET", "POST", "PATCH", "DELETE"].map((m) => (
              <option key={m}>{m}</option>
            ))}
          </Select>
          <Input
            aria-label="Path"
            className="min-w-60 flex-1 font-mono text-xs"
            value={path}
            onChange={(e) => setPath(e.target.value)}
            placeholder="/data/v1/todos?select=id,title"
          />
          <Select
            aria-label="Role"
            value={role}
            onChange={(e) => setRole(e.target.value as ExploreRequest["role"])}
            className="w-36"
          >
            <option value="anon">anon</option>
            <option value="user">a user</option>
            <option value="service">service</option>
          </Select>
          <Button type="submit" variant="primary" busy={busy}>
            Send
          </Button>
        </div>
        {role === "user" && (
          <Input
            aria-label="User id"
            className="font-mono text-xs"
            value={userId}
            onChange={(e) => setUserId(e.target.value)}
            placeholder="The user's id (the sub claim), a uuid"
          />
        )}
        {(method === "POST" || method === "PATCH") && (
          <textarea
            aria-label="Body"
            className="min-h-20 w-full rounded-md border border-line-strong bg-surface-2 p-2.5 font-mono text-xs"
            value={body}
            onChange={(e) => setBody(e.target.value)}
            placeholder='{"title": "Buy milk"}'
          />
        )}
      </form>
      {err && (
        <div className="mt-3">
          <Alert>{err}</Alert>
        </div>
      )}
      {res && (
        <div className="mt-3 flex flex-col gap-1" data-testid="explorer-result">
          <Badge
            tone={
              res.status < 300 ? "ok" : res.status < 500 ? "warn" : "danger"
            }
          >
            {res.status}
          </Badge>
          <pre className="max-h-80 overflow-auto rounded bg-surface-2 p-2.5 font-mono text-[11px] whitespace-pre-wrap">
            {pretty}
          </pre>
        </div>
      )}
    </Panel>
  );
}

const typeLangs = [
  { id: "ts", label: "TypeScript", file: "database.types.ts" },
  { id: "dart", label: "Dart", file: "database_types.dart" },
  { id: "go", label: "Go", file: "database_types.go" },
] as const;

/** Generated types (V4 §3.3) for the exposed schemas. */
function TypesPanel({ p }: { p: Project }) {
  const [lang, setLang] = useState<(typeof typeLangs)[number]["id"]>("ts");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const download = async () => {
    setBusy(true);
    setErr(null);
    try {
      const text = await api.serviceTypes(p.id, lang);
      const url = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
      const a = document.createElement("a");
      a.href = url;
      a.download = typeLangs.find((l) => l.id === lang)!.file;
      a.click();
      URL.revokeObjectURL(url);
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Panel
      title="Generate types"
      testId="services-types"
      description="Typed rows, inserts and updates for your tables, views and functions. Or run pgdock gen types --lang ts."
    >
      <div className="flex items-center gap-2">
        <Select
          aria-label="Language"
          value={lang}
          onChange={(e) =>
            setLang(e.target.value as (typeof typeLangs)[number]["id"])
          }
          className="w-40"
        >
          {typeLangs.map((l) => (
            <option key={l.id} value={l.id}>
              {l.label}
            </option>
          ))}
        </Select>
        <Button busy={busy} onClick={() => void download()}>
          Download
        </Button>
      </div>
      {err && (
        <div className="mt-3">
          <Alert>{err}</Alert>
        </div>
      )}
    </Panel>
  );
}
