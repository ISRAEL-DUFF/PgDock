import { useEffect, useState } from "react";
import type { Operation, OperationLogEntry } from "../api/client";

export type StreamState = {
  log: OperationLogEntry[];
  status: Operation["status"] | null;
  error: string | null;
  attempts: number;
  done: boolean;
};

/** Follows an operation over Server-Sent Events until it finishes. */
export function useOperationStream(id: string | null | undefined): StreamState {
  const [state, setState] = useState<StreamState>({ log: [], status: null, error: null, attempts: 0, done: false });

  useEffect(() => {
    if (!id) return;
    setState({ log: [], status: null, error: null, attempts: 0, done: false });
    const es = new EventSource(`/api/v1/operations/${id}/stream`);
    es.addEventListener("log", (e) => {
      const entry = JSON.parse((e as MessageEvent).data) as OperationLogEntry;
      setState((s) => ({ ...s, log: [...s.log, entry] }));
    });
    const onStatus = (e: Event, done: boolean) => {
      const d = JSON.parse((e as MessageEvent).data) as { status: Operation["status"]; attempts: number; error: string | null };
      setState((s) => ({ ...s, status: d.status, attempts: d.attempts, error: d.error, done: s.done || done }));
      if (done) es.close();
    };
    es.addEventListener("status", (e) => onStatus(e, false));
    es.addEventListener("done", (e) => onStatus(e, true));
    return () => es.close();
  }, [id]);

  return state;
}
