import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, errorMessage } from "../api/client";
import type { components } from "../api/schema";
import {
  Alert,
  Badge,
  Button,
  Field,
  Input,
  Page,
  PageSkeleton,
  Panel,
  Table,
} from "../components/ui";
import { formatDate } from "../lib/format";

type Version = components["schemas"]["PgVersionInfo"];

const TONE = {
  preview: "accent",
  supported: "ok",
  deprecated: "warn",
  retired: "danger",
} as const;

/** Platform → Postgres versions (V4.1 §6.1): promote a preview, deprecate
 * a major with at least 180 days' notice (its projects' owners are told,
 * and reminded at 90, 30 and 7 days), or retire one nothing runs on. */
export function AdminPgVersionsPage() {
  const list = useQuery({
    queryKey: ["admin", "pg-versions"],
    queryFn: api.adminPgVersions,
  });
  const [editing, setEditing] = useState<Version | null>(null);
  if (!list.data) return <PageSkeleton />;
  return (
    <Page
      title="Postgres versions"
      description="Where each Postgres major is in its life. A deprecation needs at least 180 days' notice; retired majors take no new projects, and their projects keep running, unsupported."
      testId="admin-pg-versions"
    >
      <Panel title="Majors">
        <Table head={["Major", "Status", "Projects", "Retires", "Notes", ""]}>
          {list.data.items.map((v) => (
            <tr key={v.major}>
              <td className="px-3 py-2 font-medium">Postgres {v.major}</td>
              <td className="px-3 py-2">
                <Badge tone={TONE[v.status]}>{v.status}</Badge>
                {!v.installed && (
                  <span className="ml-2 text-xs text-muted">no image</span>
                )}
              </td>
              <td className="px-3 py-2 tabular-nums">{v.projects ?? 0}</td>
              <td className="px-3 py-2">
                {v.retires_at ? formatDate(v.retires_at) : "—"}
              </td>
              <td className="px-3 py-2 text-muted">{v.notes || "—"}</td>
              <td className="px-3 py-2 text-right">
                <Button size="small" onClick={() => setEditing(v)}>
                  Change
                </Button>
              </td>
            </tr>
          ))}
        </Table>
      </Panel>
      {editing && <ChangeVersion v={editing} onDone={() => setEditing(null)} />}
    </Page>
  );
}

function ChangeVersion({ v, onDone }: { v: Version; onDone: () => void }) {
  const qc = useQueryClient();
  const minDate = new Date(Date.now() + 181 * 86400_000)
    .toISOString()
    .slice(0, 10);
  const [retires, setRetires] = useState(
    v.retires_at ? v.retires_at.slice(0, 10) : minDate,
  );
  const [notes, setNotes] = useState(v.notes);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const change = async (status: Version["status"]) => {
    setBusy(true);
    setErr(null);
    try {
      await api.updatePgVersion(v.major, {
        status,
        notes,
        retires_at:
          status === "deprecated"
            ? new Date(`${retires}T00:00:00Z`).toISOString()
            : undefined,
      });
      await qc.invalidateQueries({ queryKey: ["admin", "pg-versions"] });
      await qc.invalidateQueries({ queryKey: ["profiles"] });
      onDone();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Panel title={`Postgres ${v.major}`} testId="pg-version-change">
      <div className="flex flex-col gap-3">
        <Field label="Notes" hint="Shown to owners in the deprecation notices.">
          {(id) => (
            <Input
              id={id}
              value={notes}
              onChange={(e) => setNotes(e.target.value)}
            />
          )}
        </Field>
        {(v.status === "supported" || v.status === "deprecated") && (
          <Field
            label="Retires on"
            hint="At least 180 days ahead. Owners of its projects are emailed now and at 90, 30 and 7 days."
          >
            {(id) => (
              <Input
                id={id}
                type="date"
                min={minDate}
                value={retires}
                onChange={(e) => setRetires(e.target.value)}
              />
            )}
          </Field>
        )}
        {err && <Alert>{err}</Alert>}
        <div className="flex flex-wrap gap-2">
          {v.status === "preview" && (
            <Button
              variant="primary"
              busy={busy}
              onClick={() => change("supported")}
            >
              Promote to supported
            </Button>
          )}
          {(v.status === "supported" || v.status === "deprecated") && (
            <Button
              variant="primary"
              busy={busy}
              onClick={() => change("deprecated")}
            >
              {v.status === "deprecated" ? "Move the date" : "Deprecate"}
            </Button>
          )}
          {v.status === "deprecated" && (
            <Button busy={busy} onClick={() => change("supported")}>
              Withdraw the deprecation
            </Button>
          )}
          {v.status !== "retired" && (
            <Button
              variant="danger"
              busy={busy}
              onClick={() => change("retired")}
            >
              Retire
            </Button>
          )}
          <Button onClick={onDone}>Cancel</Button>
        </div>
      </div>
    </Panel>
  );
}
