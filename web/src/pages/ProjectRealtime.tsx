import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { api, errorMessage, type Project } from "../api/client";
import {
  Alert,
  Badge,
  Button,
  Field,
  Input,
  Page,
  Panel,
  Spinner,
  Stat,
  Switch,
  Table,
} from "../components/ui";
import { useProject } from "./ProjectOverview";

/** Project → Realtime (V4 §6): which tables' changes reach subscribed
 * clients, the broadcast topics kept as history, and the limits and use. */
export function ProjectRealtimePage() {
  const { data: p } = useProject();
  if (!p) return null;
  return (
    <Page
      title="Realtime"
      description="Live updates over WebSockets: database changes filtered by each user's policies, broadcast and presence."
      testId="project-realtime"
    >
      <Realtime p={p} />
    </Page>
  );
}

function Realtime({ p }: { p: Project }) {
  const qc = useQueryClient();
  const svc = useQuery({
    queryKey: ["services", p.id],
    queryFn: () => api.backendServices(p.id),
  });
  const rt = useQuery({
    queryKey: ["realtime", p.id],
    queryFn: () => api.realtime(p.id),
    enabled: !!svc.data?.enabled,
  });
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [topics, setTopics] = useState<string | null>(null);
  const manage = p.my_role === "admin" || p.my_role === "developer";
  if (svc.isPending) return <Spinner />;
  if (svc.isError) return <Alert>{errorMessage(svc.error)}</Alert>;
  if (!svc.data.enabled)
    return (
      <Panel title="Backend services are off">
        <p className="text-[13px] text-muted">
          Realtime is part of backend services.{" "}
          <Link
            to="/projects/$id/settings/api"
            params={{ id: p.id }}
            className="text-accent underline"
          >
            Turn them on in Project Settings → API
          </Link>
          .
        </p>
      </Panel>
    );
  if (rt.isPending) return <Spinner />;
  if (rt.isError) return <Alert>{errorMessage(rt.error)}</Alert>;
  const o = rt.data;
  const toggle = async (schema: string, table: string, on: boolean) => {
    setBusy(`${schema}.${table}`);
    setErr(null);
    try {
      await api.setRealtimeTable(p.id, schema, table, on);
      await qc.invalidateQueries({ queryKey: ["realtime", p.id] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(null);
    }
  };
  const saveTopics = async () => {
    setBusy("topics");
    setErr(null);
    try {
      await api.setRealtimeTopics(
        p.id,
        (topics ?? "")
          .split(",")
          .map((t) => t.trim())
          .filter(Boolean),
      );
      await qc.invalidateQueries({ queryKey: ["realtime", p.id] });
      setTopics(null);
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(null);
    }
  };
  const url = svc.data.url ?? "https://<ref>.<domain>";
  return (
    <div className="flex flex-col gap-4">
      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat
          label="Messages this month"
          value={Math.round(o.messages_this_month).toLocaleString()}
          testId="realtime-stat-messages"
        />
        <Stat
          label="Connection-minutes this month"
          value={Math.round(o.connection_minutes_this_month).toLocaleString()}
        />
        <Stat
          label="Connections per edge"
          value={o.max_connections ?? "unlimited"}
          hint="your plan's limit"
        />
        <Stat
          label="Tables with realtime"
          value={o.tables.filter((t) => t.enabled).length}
        />
      </div>
      {o.messages_blocked && (
        <Alert tone="warn" title="Realtime is paused">
          This month's realtime messages are used up: new connections are
          refused until next month or a larger plan.
        </Alert>
      )}
      {err && <Alert>{err}</Alert>}
      <Panel
        title="Database changes"
        description="Turn realtime on for a table and subscribed clients get its inserts, updates and deletes, each only the rows their policies let them read. Anon and signed-in users can subscribe only to tables with row-level security."
        bodyClassName="p-0"
      >
        {o.tables.length === 0 ? (
          <p className="px-4 py-3 text-[13px] text-muted">
            No tables in the API's exposed schemas yet.
          </p>
        ) : (
          <Table head={["Table", "Row-level security", "Realtime"]}>
            {o.tables.map((t) => (
              <tr key={`${t.schema}.${t.table}`} data-testid="realtime-table">
                <td className="px-3 py-2 font-medium">
                  {t.schema}.{t.table}
                </td>
                <td className="px-3 py-2">
                  {t.rls ? (
                    <Badge tone="ok">on</Badge>
                  ) : (
                    <Badge tone="warn">off: users can't subscribe</Badge>
                  )}
                </td>
                <td className="px-3 py-2">
                  {t.has_primary_key ? (
                    <Switch
                      checked={t.enabled}
                      disabled={!manage || busy === `${t.schema}.${t.table}`}
                      onCheckedChange={(v) => void toggle(t.schema, t.table, v)}
                      aria-label={`Realtime for ${t.schema}.${t.table}`}
                    />
                  ) : (
                    <span className="text-xs text-muted">
                      needs a primary key
                    </span>
                  )}
                </td>
              </tr>
            ))}
          </Table>
        )}
      </Panel>
      <Panel
        title="Broadcast history"
        description="Broadcasts on these topics are kept 7 days, for clients that join late (GET /realtime/v1/history/<topic>)."
      >
        <div className="flex flex-col gap-3">
          <Field
            label="Topics"
            hint="Comma-separated, without the realtime: prefix, such as room-1, lobby."
          >
            {(fid) => (
              <Input
                id={fid}
                value={topics ?? o.persisted_topics.join(", ")}
                disabled={!manage}
                onChange={(e) => setTopics(e.target.value)}
              />
            )}
          </Field>
          {manage && (
            <div>
              <Button
                variant="primary"
                busy={busy === "topics"}
                disabled={topics == null}
                onClick={() => void saveTopics()}
              >
                Save
              </Button>
            </div>
          )}
        </div>
      </Panel>
      <Panel
        title="Connect"
        description="Supabase's realtime clients work unchanged."
      >
        <pre className="overflow-x-auto rounded-md bg-surface-2 p-3 text-xs">{`const client = createClient("${url}", "<publishable key>")
client
  .channel("todos")
  .on("postgres_changes", { event: "*", schema: "public", table: "todos" },
      (change) => console.log(change))
  .subscribe()`}</pre>
        <p className="mt-2 text-xs text-muted">
          Private channels check policies on pgd_realtime.channel_access: SELECT
          to join and receive, INSERT to send.
        </p>
      </Panel>
    </div>
  );
}
