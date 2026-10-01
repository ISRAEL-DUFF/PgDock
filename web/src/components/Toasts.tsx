import { Link } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react";
import { useOperationStream } from "../lib/useOperationStream";
import { Spinner, StatusBadge } from "./ui";

type Toast = { id: string; operationId: string; label: string };

const Ctx = createContext<(operationId: string, label: string) => void>(() => {});

/** Shows long operations as progress toasts linking to /operations/:id. */
export function useOperationToast() {
  return useContext(Ctx);
}

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const add = useCallback((operationId: string, label: string) => {
    setToasts((t) => [...t.filter((x) => x.operationId !== operationId), { id: crypto.randomUUID(), operationId, label }]);
  }, []);
  const remove = useCallback((id: string) => setToasts((t) => t.filter((x) => x.id !== id)), []);
  return (
    <Ctx.Provider value={add}>
      {children}
      <div className="pointer-events-none fixed right-4 bottom-4 z-50 flex w-80 flex-col gap-2" aria-live="polite">
        {toasts.map((t) => (
          <OperationToast key={t.id} toast={t} onDone={() => remove(t.id)} />
        ))}
      </div>
    </Ctx.Provider>
  );
}

function OperationToast({ toast, onDone }: { toast: Toast; onDone: () => void }) {
  const s = useOperationStream(toast.operationId);
  const qc = useQueryClient();
  const last = s.log[s.log.length - 1];
  useEffect(() => {
    if (!s.done) return;
    void qc.invalidateQueries();
    const t = setTimeout(onDone, s.status === "failed" ? 15000 : 5000);
    return () => clearTimeout(t);
  }, [s.done, s.status, qc, onDone]);
  return (
    <div className="pointer-events-auto rounded-lg border border-line bg-surface p-3 shadow-lg">
      <div className="flex items-center justify-between gap-2">
        <Link to="/operations/$id" params={{ id: toast.operationId }} className="truncate text-sm font-medium hover:underline">
          {toast.label}
        </Link>
        {s.status ? <StatusBadge status={s.status} /> : <Spinner />}
      </div>
      {last && <p className="mt-1 truncate font-mono text-xs text-muted">{last.msg}</p>}
      {s.status === "failed" && s.error && <p className="mt-1 text-xs text-danger">{s.error}</p>}
    </div>
  );
}
