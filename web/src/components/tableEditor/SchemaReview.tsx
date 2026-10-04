import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { api, errorMessage, type MigrationFormat, type SchemaChange, type SchemaPlan } from "../../api/client";
import { Alert, Badge, Button, CodeBlock, CopyButton, Dialog, Input, Select, Spinner, cx, toast } from "../ui";

const riskTone = { info: "muted", warning: "warn", danger: "danger" } as const;

const planSQL = (p: SchemaPlan) => p.statements.map((s) => s.sql.replace(/;?$/, ";")).join("\n");

/**
 * Every schema change from the table editor stops here: the SQL it will
 * run, the risk notes, and Run / Copy SQL / Save as migration (V2 §4.3).
 * Studio applies changes straight away; this review is PGDock's safety
 * step and stays.
 */
export function SchemaReview({
  projectId,
  change,
  title = "Review the change",
  success,
  onClose,
  onApplied,
}: {
  projectId: string;
  change: SchemaChange;
  title?: string;
  /** The toast after it runs. */
  success?: string;
  onClose: () => void;
  onApplied: () => void;
}) {
  const plan = useQuery({ queryKey: ["schema-plan", projectId, change], queryFn: () => api.previewSchema(projectId, change), retry: false });
  const prefs = useQuery({ queryKey: ["editor-prefs", projectId], queryFn: () => api.editorPreferences(projectId) });
  const [format, setFormat] = useState<MigrationFormat | null>(null);
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<ReactNode>(null);
  const qc = useQueryClient();
  const fmt = format ?? prefs.data?.migration_format ?? "sql";

  const run = async (p: SchemaPlan) => {
    setBusy(true);
    setErr(null);
    try {
      await api.applySchema(projectId, change, p.hash, p.confirm ? confirm : undefined);
      await Promise.all([
        qc.invalidateQueries({ queryKey: ["schema", projectId] }),
        qc.invalidateQueries({ queryKey: ["table-info", projectId] }),
        qc.invalidateQueries({ queryKey: ["rows", projectId] }),
        qc.invalidateQueries({ queryKey: ["count", projectId] }),
        qc.invalidateQueries({ queryKey: ["definition", projectId] }),
      ]);
      if (success) toast.success(success);
      onApplied();
    } catch (e) {
      const body = (e as { body?: { sql_error?: { message: string; hint?: string }; statement?: string } }).body;
      setErr(
        body?.sql_error ? (
          <>
            {body.sql_error.message}
            {body.sql_error.hint && <span className="block text-xs">{body.sql_error.hint}</span>}
            {body.statement && <code className="mt-1 block font-mono text-xs">{body.statement}</code>}
          </>
        ) : (
          errorMessage(e)
        ),
      );
    } finally {
      setBusy(false);
    }
  };
  const download = async () => {
    setErr(null);
    try {
      const m = await api.schemaMigration(projectId, change, fmt);
      await qc.invalidateQueries({ queryKey: ["editor-prefs", projectId] });
      const url = URL.createObjectURL(new Blob([m.content], { type: "text/plain" }));
      const a = document.createElement("a");
      a.href = url;
      a.download = m.filename;
      a.click();
      URL.revokeObjectURL(url);
    } catch (e) {
      setErr(errorMessage(e));
    }
  };

  const p = plan.data;
  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onClose()}
      title={title}
      description="This is the SQL that will run. Nothing changes until you run it."
      className="w-[min(44rem,calc(100vw-2rem))]"
      testId="schema-preview"
      footer={
        p && (
          <div className="flex w-full flex-wrap items-center gap-2">
            <CopyButton value={planSQL(p)} label="Copy SQL" />
            <Select aria-label="Migration format" value={fmt} onChange={(e) => setFormat(e.target.value as MigrationFormat)} data-testid="migration-format">
              <option value="sql">Plain SQL</option>
              <option value="goose">goose</option>
              <option value="dbmate">dbmate</option>
            </Select>
            <Button onClick={() => void download()} data-testid="save-migration">
              Save as migration
            </Button>
            <span className="flex-1" />
            <Button onClick={onClose}>Cancel</Button>
            <Button variant={p.confirm ? "danger" : "primary"} busy={busy} disabled={!!p.confirm && confirm !== p.confirm} onClick={() => void run(p)} data-testid="schema-run">
              Run
            </Button>
          </div>
        )
      }
    >
      {plan.isPending ? (
        <Spinner />
      ) : plan.isError ? (
        <Alert>{errorMessage(plan.error)}</Alert>
      ) : (
        <div tabIndex={0} className="flex max-h-[60vh] flex-col gap-3 overflow-y-auto">
          <CodeBlock code={planSQL(plan.data)} wrap />
          {plan.data.risks.length > 0 && (
            <ul className="flex flex-col gap-1.5" data-testid="schema-risks">
              {plan.data.risks.map((r, i) => (
                <li
                  key={i}
                  className={cx(
                    "flex items-start gap-2 rounded-md border px-3 py-2 text-[13px]",
                    r.level === "danger" ? "border-danger/40 bg-danger/10" : r.level === "warning" ? "border-warn/40 bg-warn/10" : "border-line bg-surface-2",
                  )}
                  data-testid={`risk-${r.level}`}
                >
                  <Badge tone={riskTone[r.level]}>{r.level}</Badge>
                  <span>{r.message}</span>
                </li>
              ))}
            </ul>
          )}
          {plan.data.confirm && (
            <label className="flex flex-col gap-1 text-[13px] text-fg-light">
              <span>
                Type <code className="font-mono text-fg">{plan.data.confirm}</code> to confirm
              </span>
              <Input value={confirm} onChange={(e) => setConfirm(e.target.value)} className="font-mono" data-testid="schema-confirm" autoFocus />
            </label>
          )}
          {err && <Alert>{err}</Alert>}
        </div>
      )}
    </Dialog>
  );
}
