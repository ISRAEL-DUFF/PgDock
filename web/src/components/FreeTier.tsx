import { useQueryClient } from "@tanstack/react-query";
import { Moon } from "lucide-react";
import { useState } from "react";
import { api, errorMessage, type Project } from "../api/client";
import { formatDate } from "../lib/format";
import { Button } from "./ui";

/** A paused or archived Free project, with Resume (V3 §4). */
export function LifecycleBanner({ p }: { p: Project }) {
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [queued, setQueued] = useState(false);
  if (!p.lifecycle || p.lifecycle === "active") return null;
  const archived = p.lifecycle === "archived";
  const resume = async () => {
    setBusy(true);
    setErr(null);
    try {
      await api.resumeProject(p.id);
      setQueued(true);
      await qc.invalidateQueries({ queryKey: ["project", p.id] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div
      className="mb-4 flex flex-wrap items-center justify-between gap-3 rounded-md border border-warn/40 bg-warn/10 px-3 py-2 text-sm text-warn-text"
      role="status"
      data-testid="lifecycle-banner"
    >
      <span className="flex items-start gap-2">
        <Moon className="mt-0.5 h-4 w-4 shrink-0" aria-hidden />
        <span>
          {archived ? (
            <>
              <strong>Archived after long inactivity</strong> on{" "}
              {formatDate(p.archived_at)}. Its data is in a verified backup and
              its database is removed; the connection string still works.
              Resuming restores it from the archive, which takes minutes.
            </>
          ) : (
            <>
              <strong>Paused for inactivity</strong> on{" "}
              {formatDate(p.paused_at)}. Its data is kept. The next connection
              resumes it (the first is refused with a message; retry after about
              30 seconds), or resume it now.
            </>
          )}
          {queued && <> {archived ? "Restoring…" : "Resuming…"}</>}
          {err && <span className="block text-danger-text">{err}</span>}
        </span>
      </span>
      <Button
        variant="primary"
        busy={busy || queued}
        onClick={resume}
        data-testid="resume-project"
      >
        {archived ? "Restore" : "Resume"}
      </Button>
    </div>
  );
}
