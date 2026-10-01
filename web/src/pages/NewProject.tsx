import { Link } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { api, errorMessage, type ProjectCredentials } from "../api/client";
import { CredentialPanel } from "../components/Credentials";
import { OperationLog } from "../components/OperationLog";
import { Alert, Button, Card, Field, Input, PageHeader, StatusBadge } from "../components/ui";
import { useOperationStream } from "../lib/useOperationStream";

export function NewProjectPage() {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [creds, setCreds] = useState<ProjectCredentials | null>(null);
  const [dismissed, setDismissed] = useState(false);
  const stream = useOperationStream(creds?.operation.id);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      setCreds(await api.createProject({ name, description: description || undefined }));
      void qc.invalidateQueries({ queryKey: ["projects"] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  if (!creds) {
    return (
      <>
        <PageHeader title="New project" subtitle="A shared-tier database: its own database and role on the shared cluster." />
        <Card className="max-w-xl">
          <form className="flex flex-col gap-4" onSubmit={submit}>
            <Field label="Name" hint="The database name is derived from it, e.g. “My Blog” → my_blog_k2f9.">
              {(id) => <Input id={id} required maxLength={64} value={name} onChange={(e) => setName(e.target.value)} autoFocus />}
            </Field>
            <Field label="Description (optional)">
              {(id) => <Input id={id} maxLength={1000} value={description} onChange={(e) => setDescription(e.target.value)} />}
            </Field>
            {err && <Alert>{err}</Alert>}
            <div>
              <Button type="submit" variant="primary" busy={busy}>
                Create project
              </Button>
            </div>
          </form>
        </Card>
      </>
    );
  }

  const ready = stream.status === "succeeded";
  return (
    <>
      <PageHeader title={creds.project.name} subtitle={<span className="font-mono">{creds.project.db_name}</span>} />
      <div className="grid gap-4 lg:grid-cols-2">
        <Card title="Credentials">
          {!dismissed ? (
            <CredentialPanel creds={creds} ready={ready} onDismiss={() => setDismissed(true)} />
          ) : (
            <div className="flex flex-col gap-3 text-sm">
              <p>Credentials dismissed. You can rotate the password from the project settings at any time.</p>
              <Link to="/projects/$id" params={{ id: creds.project.id }} className="text-accent hover:underline">
                Open the project
              </Link>
            </div>
          )}
        </Card>
        <Card title="Provisioning" actions={stream.status && <StatusBadge status={stream.status} />}>
          <OperationLog log={stream.log} live={!stream.done} />
          {stream.status === "failed" && (
            <div className="mt-3">
              <Alert title="Provisioning failed">{stream.error}. Everything it created was rolled back.</Alert>
            </div>
          )}
          {ready && (
            <p className="mt-3 text-sm text-ok" data-testid="provision-ready">
              Ready — the connection strings work now.
            </p>
          )}
        </Card>
      </div>
    </>
  );
}
