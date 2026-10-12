import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, errorMessage, type Project } from "../api/client";
import type { components } from "../api/schema";
import { CostEstimate } from "./CostEstimate";
import { Alert, Button, Field, Panel, Select } from "./ui";

type Retention = components["schemas"]["BackupRetention"];

const retentions: { value: Retention; label: string }[] = [
  { value: "standard", label: "Standard: 7 daily, 4 weekly" },
  { value: "extended", label: "Extended: 30 daily, 12 weekly" },
  { value: "long", label: "Long: 30 daily, 52 weekly" },
];

const windows: { value: 7 | 14 | 30; label: string }[] = [
  { value: 7, label: "7 days" },
  { value: 14, label: "14 days" },
  { value: 30, label: "30 days" },
];

/** How long backups are kept (V4.1 §4): a shared project's retention, a
 * dedicated one's point-in-time recovery window. The longer choices are
 * billed add-ons on Pro and Team. */
export function BackupAddOnsCard({ p }: { p: Project }) {
  const qc = useQueryClient();
  const dedicated = p.tier === "dedicated";
  const [retention, setRetention] = useState<Retention>(
    p.settings.backup_retention ?? "standard",
  );
  const [days, setDays] = useState<7 | 14 | 30>(
    ((p.instance?.pitr_days ?? 7) as 7 | 14 | 30) || 7,
  );
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const current = dedicated
    ? (p.instance?.pitr_days ?? 7) === days
    : (p.settings.backup_retention ?? "standard") === retention;
  const shorter = dedicated && days < (p.instance?.pitr_days ?? 7);
  const save = async () => {
    setBusy(true);
    setErr(null);
    setSaved(false);
    try {
      if (dedicated) {
        await api.updateProjectInstance(p.id, { pitr_days: days });
      } else {
        await api.updateProject(p.id, {
          settings: { backup_retention: retention },
        });
      }
      await qc.invalidateQueries({ queryKey: ["projects"] });
      setSaved(true);
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Panel
      title={dedicated ? "Recovery window" : "Backup retention"}
      testId="backup-addons"
    >
      <div className="flex flex-col gap-3">
        {dedicated ? (
          <Field
            label="Point-in-time recovery"
            hint="14 and 30 days are add-ons on Pro and Team. A longer window grows day by day from now."
          >
            {(id) => (
              <Select
                id={id}
                value={String(days)}
                onChange={(e) => setDays(Number(e.target.value) as 7 | 14 | 30)}
              >
                {windows.map((w) => (
                  <option key={w.value} value={w.value}>
                    {w.label}
                  </option>
                ))}
              </Select>
            )}
          </Field>
        ) : (
          <Field
            label="Keep nightly backups"
            hint="Extended and long are add-ons on Pro and Team; the backups they keep also count as backup storage."
          >
            {(id) => (
              <Select
                id={id}
                value={retention}
                onChange={(e) => setRetention(e.target.value as Retention)}
              >
                {retentions.map((r) => (
                  <option key={r.value} value={r.value}>
                    {r.label}
                  </option>
                ))}
              </Select>
            )}
          </Field>
        )}
        {shorter && (
          <Alert tone="warn">
            Base backups older than {days} days are deleted at the next base
            backup; points before then can no longer be restored.
          </Alert>
        )}
        {!current && (
          <CostEstimate
            org={p.org_id}
            what="This choice"
            req={
              dedicated ? { pitr_days: days } : { backup_retention: retention }
            }
          />
        )}
        {err && <Alert>{err}</Alert>}
        <div className="flex items-center gap-3">
          <Button
            variant="primary"
            busy={busy}
            disabled={current}
            onClick={save}
          >
            Save
          </Button>
          {saved && current && (
            <span className="text-xs text-muted">Saved.</span>
          )}
        </div>
      </div>
    </Panel>
  );
}
