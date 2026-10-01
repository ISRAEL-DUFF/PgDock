import { useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { api, errorMessage, type ProjectCredentials } from "../api/client";
import { ProvisionProgress } from "../components/ProvisionProgress";
import { Alert, Button, Card, Field, Input, PageHeader } from "../components/ui";

export function NewProjectPage() {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [creds, setCreds] = useState<ProjectCredentials | null>(null);

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

  return <ProvisionProgress creds={creds} />;
}
