import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { api, errorMessage, type ProjectCredentials } from "../api/client";
import { ProvisionProgress } from "../components/ProvisionProgress";
import { Alert, Button, Card, Field, Input, PageHeader, Select, cx } from "../components/ui";
import { useCurrentOrg } from "../lib/org";
import { sessionQuery } from "../lib/session";

type Tier = "shared" | "dedicated";

export function NewProjectPage() {
  const qc = useQueryClient();
  const { org } = useCurrentOrg();
  const { data: session } = useQuery(sessionQuery);
  // Dedicated instances are placed on nodes only the platform admin manages;
  // others create them within their organisation's allowance, or ask
  // through a promotion (V2 §10.6).
  const platformAdmin = session?.user?.platform_role === "platform_admin";
  const quotas = useQuery({ queryKey: ["org", org?.id, "quotas"], queryFn: () => api.orgQuotas(org!.id), enabled: !!org });
  const allowance = quotas.data?.dedicated_allowance;
  const dedicatedAllowed =
    !!allowance && (!!allowance.unlimited || (quotas.data?.dedicated_use.instances ?? 0) < allowance.instances);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [tier, setTier] = useState<Tier>("shared");
  const [nodeId, setNodeId] = useState("");
  const [profile, setProfile] = useState("");
  const [volume, setVolume] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [creds, setCreds] = useState<ProjectCredentials | null>(null);
  const profiles = useQuery({ queryKey: ["profiles"], queryFn: api.profiles, enabled: tier === "dedicated" });
  const nodes = useQuery({ queryKey: ["nodes"], queryFn: api.nodes, enabled: tier === "dedicated" && platformAdmin });
  const dedicatedNodes = (nodes.data?.items ?? []).filter((n) => (n.role === "dedicated" || n.role === "both") && n.agent.registered);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      setCreds(
        await api.createProject({
          org_id: org?.id,
          name,
          description: description || undefined,
          tier,
          ...(tier === "dedicated"
            ? { node_id: nodeId || undefined, profile: profile || undefined, volume_gb: volume ? Number(volume) : undefined }
            : {}),
        }),
      );
      void qc.invalidateQueries({ queryKey: ["projects"] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  if (creds) return <ProvisionProgress creds={creds} />;

  const tierOption = (t: Tier, title: string, body: string) => (
    <label
      className={cx(
        "flex flex-1 cursor-pointer flex-col gap-1 rounded-md border px-3 py-2 text-sm",
        tier === t ? "border-accent bg-accent/5" : "border-line hover:bg-surface-2",
      )}
    >
      <span className="flex items-center gap-2 font-medium">
        <input type="radio" name="tier" checked={tier === t} onChange={() => setTier(t)} />
        {title}
      </span>
      <span className="text-xs text-muted">{body}</span>
    </label>
  );

  return (
    <>
      <PageHeader title="New project" subtitle="A PostgreSQL 18 database with its own role and connection strings." />
      <Card className="max-w-xl">
        <form className="flex flex-col gap-4" onSubmit={submit}>
          <Field label="Name" hint="The database name is derived from it, e.g. “My Blog” → my_blog_k2f9.">
            {(id) => <Input id={id} required maxLength={64} value={name} onChange={(e) => setName(e.target.value)} autoFocus />}
          </Field>
          <Field label="Description (optional)">
            {(id) => <Input id={id} maxLength={1000} value={description} onChange={(e) => setDescription(e.target.value)} />}
          </Field>
          <fieldset className="flex flex-col gap-2">
            <legend className="mb-1 text-sm font-medium">Tier</legend>
            <div className="flex flex-wrap gap-2">
              {tierOption("shared", "Shared", "A database on the shared cluster. Ready in a second; nightly logical backups.")}
              {dedicatedAllowed &&
                tierOption("dedicated", "Dedicated", "Its own Postgres container with CPU and memory limits; continuous backups and point-in-time restore.")}
            </div>
          </fieldset>
          {tier === "dedicated" && (
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
              <Field label="Node">
                {(id) => (
                  <Select id={id} value={nodeId} onChange={(e) => setNodeId(e.target.value)}>
                    <option value="">Least loaded</option>
                    {dedicatedNodes.map((n) => (
                      <option key={n.id} value={n.id} disabled={n.status !== "healthy"}>
                        {n.status === "healthy" ? n.name : `${n.name} (${n.status})`}
                      </option>
                    ))}
                  </Select>
                )}
              </Field>
              <Field label="Size">
                {(id) => (
                  <Select id={id} value={profile} onChange={(e) => setProfile(e.target.value)}>
                    {(profiles.data?.items ?? []).map((p) => (
                      <option key={p.name} value={p.name === profiles.data?.default_profile ? "" : p.name}>
                        {p.name}: {p.cpus} CPU, {p.memory_mb / 1024} GB
                      </option>
                    ))}
                  </Select>
                )}
              </Field>
              <Field label="Volume (GB)">
                {(id) => (
                  <Input
                    id={id}
                    type="number"
                    min={1}
                    max={16384}
                    value={volume}
                    placeholder={String(profiles.data?.default_volume_gb ?? 20)}
                    onChange={(e) => setVolume(e.target.value)}
                  />
                )}
              </Field>
            </div>
          )}
          {tier === "dedicated" && nodes.data && dedicatedNodes.length === 0 && (
            <Alert tone="warn">No node with an agent accepts dedicated instances yet. On the Nodes page, give a node the role "dedicated" or "both", or add one.</Alert>
          )}
          {tier === "dedicated" && nodes.data && dedicatedNodes.length > 0 && !dedicatedNodes.some((n) => n.status === "healthy") && (
            <Alert tone="warn">Every node that takes dedicated instances is unreachable right now, so a new one cannot be placed. Check the agents on the Nodes page.</Alert>
          )}
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
