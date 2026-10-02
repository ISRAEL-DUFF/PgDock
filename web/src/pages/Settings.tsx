import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api, errorMessage } from "../api/client";
import { Alert, Button, Card, PageHeader, Spinner, StatusBadge } from "../components/ui";
import { formatDate } from "../lib/format";
import { AlertSettingsCard, IsolationChecksCard } from "../components/AlertSettingsCard";
import { BackupKeyPanel, StorageForm } from "../components/BackupSetup";
import { MailCard, SignupCard, TermsCard } from "../components/PlatformCards";
import { TokenSettingsCard } from "../components/Tokens";
import { HostStep } from "./Setup";

export function SettingsPage() {
  const q = useQuery({ queryKey: ["settings", "general"], queryFn: api.generalSettings, refetchInterval: 10_000 });
  const [editing, setEditing] = useState(false);
  const [editStorage, setEditStorage] = useState(false);
  const storage = useQuery({ queryKey: ["settings", "storage"], queryFn: api.storage });
  const ov = useQuery({ queryKey: ["backups", "overview"], queryFn: () => api.backupOverview(), refetchInterval: 15_000 });
  if (q.isPending) return <Spinner />;
  if (q.isError) return <Alert>{errorMessage(q.error)}</Alert>;
  const s = q.data;
  const t = s.tls;
  return (
    <>
      <PageHeader title="Platform settings" />
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
        <Card
          title="Backup storage"
          actions={storage.data?.configured && !editStorage && <Button className="text-xs" onClick={() => setEditStorage(true)}>Change</Button>}
        >
          {storage.data && (!storage.data.configured || editStorage) ? (
            <StorageForm onSaved={() => setEditStorage(false)} />
          ) : (
            <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm" data-testid="storage-summary">
              <dt className="text-muted">Endpoint</dt>
              <dd className="font-mono text-xs">{storage.data?.endpoint}</dd>
              <dt className="text-muted">Bucket</dt>
              <dd className="font-mono text-xs">
                {storage.data?.bucket}/{storage.data?.prefix}
              </dd>
              <dt className="text-muted">Access key</dt>
              <dd className="font-mono text-xs">{storage.data?.access_key}</dd>
            </dl>
          )}
        </Card>
        <Card title="Backup key">
          <BackupKeyPanel />
        </Card>
        {ov.data && (
          <Card title="Backup checks">
            <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
              <dt className="text-muted">Weekly restore test</dt>
              <dd>
                {ov.data.last_restore_test ? (
                  <span className="flex items-center gap-2">
                    <StatusBadge status={ov.data.last_restore_test.status} /> {formatDate(ov.data.last_restore_test.created_at)}
                  </span>
                ) : (
                  "not run yet"
                )}
              </dd>
              <dt className="text-muted">Metadata self-backup</dt>
              <dd>{ov.data.last_metadata_backup ? formatDate(ov.data.last_metadata_backup.finished_at) : "not run yet"}</dd>
              <dt className="text-muted">Agent</dt>
              <dd>{ov.data.agent_available ? "registered" : "none registered (see Nodes)"}</dd>
            </dl>
          </Card>
        )}
        <AlertSettingsCard />
        <IsolationChecksCard />
        <MailCard />
        <SignupCard />
        <TokenSettingsCard />
        <TermsCard />
      </div>
    </>
  );
}
