import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api, errorMessage } from "../api/client";
import { Alert, Button, Card, PageHeader, Spinner, StatusBadge } from "../components/ui";
import { formatDate } from "../lib/format";
import { sessionQuery } from "../lib/session";
import { HostStep } from "./Setup";

export function SettingsPage() {
  const q = useQuery({ queryKey: ["settings", "general"], queryFn: api.generalSettings, refetchInterval: 10_000 });
  const { data: session } = useQuery(sessionQuery);
  const [editing, setEditing] = useState(false);
  if (q.isPending) return <Spinner />;
  if (q.isError) return <Alert>{errorMessage(q.error)}</Alert>;
  const s = q.data;
  const t = s.tls;
  return (
    <>
      <PageHeader title="Settings" />
      <div className="flex max-w-3xl flex-col gap-4">
        <Card title="Database hostname" actions={!editing && <Button className="text-xs" onClick={() => setEditing(true)}>Change</Button>}>
          {editing ? (
            <HostStep submitLabel="Save" onDone={() => setEditing(false)} />
          ) : (
            <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
              <dt className="text-muted">Hostname</dt>
              <dd className="font-mono" data-testid="db-host">{s.db_host}</dd>
              <dt className="text-muted">Ports</dt>
              <dd>
                {s.pooled_port} (pooled) · {s.session_port} (session)
              </dd>
              <dt className="text-muted">Client sslmode</dt>
              <dd>{s.sslmode}</dd>
            </dl>
          )}
        </Card>
        <Card title="Pooler TLS" actions={<StatusBadge status={t.state} />}>
          <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
            <dt className="text-muted">Mode</dt>
            <dd>{t.mode}</dd>
            <dt className="text-muted">Certificate for</dt>
            <dd className="font-mono">{t.host ?? "—"}</dd>
            <dt className="text-muted">Issuer</dt>
            <dd>{t.issuer ?? "—"}</dd>
            <dt className="text-muted">Expires</dt>
            <dd>{formatDate(t.not_after)}</dd>
          </dl>
          {t.error && (
            <div className="mt-3">
              <Alert tone="warn">{t.error}</Alert>
            </div>
          )}
        </Card>
        <Card title="Account">
          <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
            <dt className="text-muted">Email</dt>
            <dd>{session?.operator?.email}</dd>
            <dt className="text-muted">Role</dt>
            <dd>{session?.operator?.role}</dd>
            <dt className="text-muted">Idle timeout</dt>
            <dd>{session?.idle_timeout_seconds ? `${Math.round(session.idle_timeout_seconds / 3600)}h` : "—"}</dd>
          </dl>
        </Card>
      </div>
    </>
  );
}
