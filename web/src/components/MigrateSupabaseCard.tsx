import { useState } from "react";
import { api, errorMessage } from "../api/client";
import { useOperationStream } from "../lib/useOperationStream";
import { OperationLog } from "./OperationLog";
import { Alert, Button, Field, Input, Panel, StatusBadge } from "./ui";

type Step = "policies" | "users" | "storage";

const steps: { step: Step; title: string; what: string }[] = [
  {
    step: "policies",
    title: "1. Policies",
    what: "Rewrites the imported policies and column defaults: anon, authenticated and service_role become this project's request roles, and auth.uid() becomes pgd_auth.uid().",
  },
  {
    step: "users",
    title: "2. Users",
    what: "Copies users and their sign-in identities with the same ids. Passwords keep working and are upgraded at each user's next sign-in. Sessions and MFA factors aren't copied.",
  },
  {
    step: "storage",
    title: "3. Files",
    what: "Copies buckets, files (paths, types, owners) and storage policies over Supabase's S3 connection.",
  },
];

/**
 * The Supabase migration helper (V4 §9): after importing a Supabase
 * database into this project, bring its policies, users and files across.
 * The source's credentials are used for the step and kept only in the
 * server's memory.
 */
export function MigrateSupabaseCard({ p }: { p: { id: string } }) {
  const [source, setSource] = useState("");
  const [endpoint, setEndpoint] = useState("");
  const [region, setRegion] = useState("");
  const [accessKey, setAccessKey] = useState("");
  const [secretKey, setSecretKey] = useState("");
  const [busy, setBusy] = useState<Step | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [opId, setOpId] = useState<string | undefined>();
  const stream = useOperationStream(opId);
  const running = !!opId && !stream.done;

  const run = async (step: Step) => {
    setBusy(step);
    setErr(null);
    try {
      const op = await api.migrateSupabase(p.id, {
        step,
        source_url: step === "policies" ? undefined : source,
        s3:
          step === "storage"
            ? {
                endpoint,
                region: region || undefined,
                access_key: accessKey,
                secret_key: secretKey,
              }
            : undefined,
      });
      setOpId(op.id);
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(null);
    }
  };
  const ready: Record<Step, boolean> = {
    policies: true,
    users: source.trim() !== "",
    storage:
      source.trim() !== "" &&
      endpoint.trim() !== "" &&
      accessKey.trim() !== "" &&
      secretKey !== "",
  };

  return (
    <Panel
      title="Migrate from Supabase"
      testId="migrate-supabase"
      description="Imported this project's database from Supabase? Bring its policies, users and files across. Each step reports what it couldn't carry over, and can run again."
      actions={
        opId && stream.status ? (
          <StatusBadge status={stream.status} />
        ) : undefined
      }
    >
      <div className="flex flex-col gap-4 text-sm">
        <div className="grid gap-3 sm:grid-cols-2">
          <Field
            label="Supabase database connection string"
            hint="Project Settings → Database → Connection string (session pooler or direct). Used by the users and files steps."
          >
            {(id) => (
              <Input
                id={id}
                type="password"
                autoComplete="off"
                placeholder="postgresql://postgres.<ref>:<password>@…:5432/postgres"
                value={source}
                onChange={(e) => setSource(e.target.value)}
              />
            )}
          </Field>
          <Field
            label="S3 endpoint"
            hint="Project Settings → Storage → S3 connection."
          >
            {(id) => (
              <Input
                id={id}
                placeholder="https://<ref>.supabase.co/storage/v1/s3"
                value={endpoint}
                onChange={(e) => setEndpoint(e.target.value)}
              />
            )}
          </Field>
          <Field label="S3 region">
            {(id) => (
              <Input
                id={id}
                placeholder="eu-west-2"
                value={region}
                onChange={(e) => setRegion(e.target.value)}
              />
            )}
          </Field>
          <Field label="S3 access key id">
            {(id) => (
              <Input
                id={id}
                autoComplete="off"
                value={accessKey}
                onChange={(e) => setAccessKey(e.target.value)}
              />
            )}
          </Field>
          <Field label="S3 secret access key">
            {(id) => (
              <Input
                id={id}
                type="password"
                autoComplete="off"
                value={secretKey}
                onChange={(e) => setSecretKey(e.target.value)}
              />
            )}
          </Field>
        </div>
        <ul className="flex flex-col gap-3">
          {steps.map((s) => (
            <li
              key={s.step}
              className="flex flex-wrap items-start justify-between gap-3"
            >
              <div className="max-w-xl">
                <div className="font-medium">{s.title}</div>
                <p className="text-muted">{s.what}</p>
              </div>
              <Button
                busy={busy === s.step}
                disabled={running || !ready[s.step]}
                onClick={() => run(s.step)}
                data-testid={`migrate-${s.step}`}
              >
                Run
              </Button>
            </li>
          ))}
        </ul>
        {opId && <OperationLog log={stream.log} live={!stream.done} />}
        {stream.status === "failed" && <Alert>{stream.error}</Alert>}
        {err && <Alert>{err}</Alert>}
      </div>
    </Panel>
  );
}
