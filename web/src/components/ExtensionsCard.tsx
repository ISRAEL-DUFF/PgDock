import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, errorMessage, type Project } from "../api/client";
import { Alert, Badge, Button, Card, Spinner, Table } from "./ui";

/** The extension allow-list for a project (spec §7.4). */
export function ExtensionsCard({ p }: { p: Project }) {
  const qc = useQueryClient();
  const key = ["extensions", p.id];
  const q = useQuery({ queryKey: key, queryFn: () => api.extensions(p.id), enabled: p.status === "active" });
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  if (p.status !== "active") return null;

  const enable = async (name: string) => {
    setBusy(name);
    setErr(null);
    try {
      qc.setQueryData(key, await api.enableExtension(p.id, name));
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(null);
    }
  };

  return (
    <Card title="Extensions">
      <p className="mb-3 text-sm text-muted">
        Allow-listed extensions can be enabled here; they are created in the <span className="font-mono">public</span> schema.
        {p.tier === "shared" && " The dedicated tier allows a few more."}
      </p>
      {err && (
        <div className="mb-3">
          <Alert>{err}</Alert>
        </div>
      )}
      {q.isPending ? (
        <Spinner />
      ) : q.isError ? (
        <Alert>{errorMessage(q.error)}</Alert>
      ) : (
        <Table head={["Extension", "Status", ""]}>
          {q.data.items.map((x) => (
            <tr key={x.name} data-testid={`ext-${x.name}`}>
              <td className="px-3 py-1.5 font-mono text-xs">{x.name}</td>
              <td className="px-3 py-1.5 text-xs">
                {x.installed_version ? (
                  <Badge tone="ok">enabled {x.installed_version}</Badge>
                ) : !x.allowed ? (
                  <span className="text-muted">dedicated tier only</span>
                ) : !x.available ? (
                  <span className="text-muted">not installed on this server</span>
                ) : (
                  <span className="text-muted">available {x.default_version}</span>
                )}
              </td>
              <td className="px-3 py-1.5 text-right">
                {!x.installed_version && x.allowed && x.available && (
                  <Button className="text-xs" busy={busy === x.name} disabled={busy !== null} onClick={() => enable(x.name)}>
                    Enable
                  </Button>
                )}
              </td>
            </tr>
          ))}
        </Table>
      )}
    </Card>
  );
}
