import { useEffect, useRef } from "react";
import type { OperationLogEntry } from "../api/client";
import { formatTime } from "../lib/format";
import { cx } from "./ui";

const levelColor: Record<string, string> = { info: "text-fg", warn: "text-warn-text", error: "text-danger-text" };

export function OperationLog({ log, live }: { log: OperationLogEntry[]; live?: boolean }) {
  const end = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (live) end.current?.scrollIntoView({ block: "nearest" });
  }, [log.length, live]);
  if (log.length === 0) {
    return <p className="font-mono text-xs text-muted">{live ? "Waiting for a worker…" : "No log entries."}</p>;
  }
  return (
    <div tabIndex={0} aria-label="Operation log" role="log" className="max-h-96 overflow-y-auto rounded-md border border-line bg-code p-3 font-mono text-xs leading-relaxed" data-testid="operation-log">
      {log.map((l, i) => (
        <div key={i} className="flex gap-3">
          <span className="shrink-0 text-muted">{formatTime(l.ts)}</span>
          <span className="w-20 shrink-0 truncate text-muted">{l.step}</span>
          <span className={cx("min-w-0 break-words", levelColor[l.level])}>{l.msg}</span>
        </div>
      ))}
      <div ref={end} />
    </div>
  );
}
