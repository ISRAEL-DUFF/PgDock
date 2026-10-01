import { useState } from "react";
import type { ProjectCredentials } from "../api/client";
import { Alert, Button, CopyField } from "./ui";

/** The one-time credential panel (spec §8.4): shown once, then gone. */
export function CredentialPanel({ creds, onDismiss, ready }: { creds: ProjectCredentials; onDismiss: () => void; ready: boolean }) {
  const [saved, setSaved] = useState(false);
  return (
    <div className="flex flex-col gap-4" data-testid="credential-panel">
      <Alert tone="warn" title="Save these now">
        This is the only time PGDock shows the password. It stores only a SCRAM verifier, so it cannot show it again;
        rotate the password if you lose it.
      </Alert>
      {!ready && <Alert tone="accent">The connection strings start working when provisioning finishes.</Alert>}
      <CopyField label="Password" value={creds.password} secret testId="credential-password" />
      <CopyField label="Pooled URL (transaction mode, for apps)" value={creds.connection.pooled_url} secret testId="credential-pooled-url" />
      <CopyField label="Session URL (for migrations and session features)" value={creds.connection.session_url} secret testId="credential-session-url" />
      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={saved} onChange={(e) => setSaved(e.target.checked)} />
        I've saved the password somewhere safe
      </label>
      <div>
        <Button variant="primary" disabled={!saved} onClick={onDismiss}>
          Done
        </Button>
      </div>
    </div>
  );
}
