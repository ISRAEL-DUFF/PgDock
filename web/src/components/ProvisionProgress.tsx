import { Link } from "@tanstack/react-router";
import { useState } from "react";
import type { ProjectCredentials } from "../api/client";
import { useOperationStream } from "../lib/useOperationStream";
import { CredentialPanel } from "./Credentials";
import { OperationLog } from "./OperationLog";
import { Alert, Page, Panel, StatusBadge } from "./ui";

/**
 * A project being provisioned (create, restore into a new project, import):
 * the one-time credentials next to the live operation log.
 */
export function ProvisionProgress({ creds, progressTitle = "Provisioning" }: { creds: ProjectCredentials; progressTitle?: string }) {
  const [dismissed, setDismissed] = useState(false);
  const stream = useOperationStream(creds.operation.id);
  const ready = stream.status === "succeeded";
  const failed = stream.status === "failed";
  const cleanedUp = !stream.log.some((l) => /rollback incomplete/i.test(l.msg));
  return (
    <Page title={creds.project.name} description={<span className="font-mono">{creds.project.db_name}</span>}>
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Panel title="Credentials">
          {failed ? (
            <div className="flex flex-col gap-3 text-sm">
              <p>Nothing was created, so there are no credentials to save.</p>
              <Link to="/projects" className="text-accent-text underline underline-offset-2 hover:no-underline">
                Back to projects
              </Link>
            </div>
          ) : !dismissed ? (
            <CredentialPanel creds={creds} ready={ready} onDismiss={() => setDismissed(true)} />
          ) : (
            <div className="flex flex-col gap-3 text-sm">
              <p>Credentials dismissed. You can rotate the password from the project settings at any time.</p>
              <Link to="/projects/$id" params={{ id: creds.project.id }} className="text-accent-text underline underline-offset-2 hover:no-underline">
                Open the project
              </Link>
            </div>
          )}
        </Panel>
        <Panel title={progressTitle} actions={stream.status && <StatusBadge status={stream.status} />}>
          <OperationLog log={stream.log} live={!stream.done} />
          {stream.status === "failed" && (
            <div className="mt-3">
              <Alert title={`${progressTitle} failed`}>{stream.error}.{" "}
                {cleanedUp ? "Everything it created was rolled back." : "It could not remove everything it created; ask an administrator to check for leftovers."}
              </Alert>
            </div>
          )}
          {ready && (
            <p className="mt-3 text-sm text-ok-text" data-testid="provision-ready">
              Ready — the connection strings work now.
            </p>
          )}
        </Panel>
      </div>
    </Page>
  );
}
