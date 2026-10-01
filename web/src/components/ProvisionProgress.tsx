import { Link } from "@tanstack/react-router";
import { useState } from "react";
import type { ProjectCredentials } from "../api/client";
import { useOperationStream } from "../lib/useOperationStream";
import { CredentialPanel } from "./Credentials";
import { OperationLog } from "./OperationLog";
import { Alert, Card, PageHeader, StatusBadge } from "./ui";

/**
 * A project being provisioned (create, restore into a new project, import):
 * the one-time credentials next to the live operation log.
 */
export function ProvisionProgress({ creds, progressTitle = "Provisioning" }: { creds: ProjectCredentials; progressTitle?: string }) {
  const [dismissed, setDismissed] = useState(false);
  const stream = useOperationStream(creds.operation.id);
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
        <Card title={progressTitle} actions={stream.status && <StatusBadge status={stream.status} />}>
          <OperationLog log={stream.log} live={!stream.done} />
          {stream.status === "failed" && (
            <div className="mt-3">
              <Alert title={`${progressTitle} failed`}>{stream.error}. Everything it created was rolled back.</Alert>
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
