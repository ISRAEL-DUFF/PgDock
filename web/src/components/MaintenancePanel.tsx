import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import {
  api,
  ApiRequestError,
  errorMessage,
  type MaintenanceWindow,
} from "../api/client";
import { formatDate } from "../lib/format";
import {
  Alert,
  Badge,
  Button,
  Field,
  Input,
  Panel,
  Select,
  Switch,
  Table,
} from "./ui";

const days = [
  "Sunday",
  "Monday",
  "Tuesday",
  "Wednesday",
  "Thursday",
  "Friday",
  "Saturday",
];

/**
 * The weekly maintenance window (V3 §2.4): instances whose image has a newer
 * Postgres minor release are restarted onto it one at a time, with the
 * poolers holding their clients.
 */
export function MaintenancePanel() {
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["maintenance"],
    queryFn: api.maintenance,
    refetchInterval: 30000,
  });
  const [w, setW] = useState<MaintenanceWindow | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    if (q.data && !w) setW(q.data.window);
  }, [q.data, w]);

  const save = async () => {
    if (!w) return;
    setBusy("window");
    setErr(null);
    try {
      setW(await api.putMaintenanceWindow(w));
      await qc.invalidateQueries({ queryKey: ["maintenance"] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(null);
    }
  };
  const upgradeNow = async (id: string) => {
    setBusy(id);
    setErr(null);
    try {
      const r = await api.minorUpgrade(id);
      if (r.error) setErr(r.error);
      await qc.invalidateQueries({ queryKey: ["maintenance"] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(null);
    }
  };

  // 503: no dedicated tier on this server, so no agent-run instances.
  if (q.error instanceof ApiRequestError && q.error.status === 503) return null;
  if (q.isError) return <Alert>{errorMessage(q.error)}</Alert>;
  if (!q.data || !w) return null;
  const st = q.data;
  const dirty = JSON.stringify(w) !== JSON.stringify(st.window);

  return (
    <Panel
      title="Maintenance window"
      actions={
        st.in_window ? (
          <Badge tone="accent">In the window now</Badge>
        ) : (
          <span className="text-xs text-muted">
            Next: {formatDate(st.next_window)}
          </span>
        )
      }
    >
      <div className="flex flex-col gap-4 text-sm" data-testid="maintenance">
        <p className="text-muted">
          Postgres minor releases are applied here: each instance whose image
          has a newer release is restarted onto it, one at a time. The poolers
          hold its clients for the restart (a second or two); session
          connections reconnect. Times are UTC.
        </p>
        <div className="flex flex-wrap items-end gap-3">
          <Field label="Automatic">
            {(id) => (
              <Switch
                id={id}
                checked={w.enabled}
                onCheckedChange={(v) => setW({ ...w, enabled: v })}
                aria-label="Automatic minor upgrades"
              />
            )}
          </Field>
          <Field label="Day">
            {(id) => (
              <Select
                id={id}
                value={w.weekday}
                onChange={(e) =>
                  setW({ ...w, weekday: Number(e.target.value) })
                }
              >
                {days.map((d, i) => (
                  <option key={d} value={i}>
                    {d}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <Field label="From (UTC hour)">
            {(id) => (
              <Input
                id={id}
                type="number"
                min={0}
                max={23}
                className="w-24"
                value={w.start_hour}
                onChange={(e) =>
                  setW({ ...w, start_hour: Number(e.target.value) })
                }
              />
            )}
          </Field>
          <Field label="Hours">
            {(id) => (
              <Input
                id={id}
                type="number"
                min={1}
                max={24}
                className="w-24"
                value={w.hours}
                onChange={(e) => setW({ ...w, hours: Number(e.target.value) })}
              />
            )}
          </Field>
          <Button onClick={save} busy={busy === "window"} disabled={!dirty}>
            Save
          </Button>
        </div>
        {st.behind.length === 0 ? (
          <p className="text-muted" data-testid="maintenance-current">
            Every instance runs the newest release its image has.
          </p>
        ) : (
          <Table head={["Instance", "Node", "Runs", "Available", ""]}>
            {st.behind.map((i) => (
              <tr key={i.id} data-testid="maintenance-behind">
                <td className="px-3 py-2">
                  <Badge tone={i.kind === "dedicated" ? "accent" : "muted"}>
                    {i.kind}
                  </Badge>
                </td>
                <td className="px-3 py-2">{i.node_name}</td>
                <td className="px-3 py-2 font-mono text-xs">{i.pg_release}</td>
                <td className="px-3 py-2 font-mono text-xs">
                  {i.pg_release_available}
                </td>
                <td className="px-3 py-2 text-right">
                  <Button
                    className="text-xs"
                    onClick={() => upgradeNow(i.id)}
                    busy={busy === i.id}
                  >
                    Upgrade now
                  </Button>
                </td>
              </tr>
            ))}
          </Table>
        )}
        {st.history.length > 0 && (
          <details>
            <summary className="cursor-pointer text-muted">
              Recent minor upgrades
            </summary>
            <Table
              head={["When", "Instance", "Release", "Clients held", "Result"]}
            >
              {st.history.map((h) => (
                <tr key={h.id}>
                  <td className="px-3 py-2 text-muted">
                    {formatDate(h.started_at)}
                  </td>
                  <td className="px-3 py-2">
                    {h.kind} on {h.node_name}
                  </td>
                  <td className="px-3 py-2 font-mono text-xs">
                    {h.from_release} → {h.to_release}
                  </td>
                  <td className="px-3 py-2">
                    {h.pause_ms != null
                      ? `${(h.pause_ms / 1000).toFixed(1)} s`
                      : "—"}
                  </td>
                  <td className="px-3 py-2">
                    {h.error ? (
                      <span className="text-danger-text" title={h.error}>
                        failed
                      </span>
                    ) : h.finished_at ? (
                      "done"
                    ) : (
                      "running"
                    )}
                  </td>
                </tr>
              ))}
            </Table>
          </details>
        )}
        {err && <Alert>{err}</Alert>}
      </div>
    </Panel>
  );
}
