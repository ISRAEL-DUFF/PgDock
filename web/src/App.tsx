import { useEffect, useState } from "react";
import { getVersion, type Version } from "./api/client";

type State = { kind: "loading" } | { kind: "ok"; version: Version } | { kind: "error"; message: string };

export function App() {
  const [state, setState] = useState<State>({ kind: "loading" });

  useEffect(() => {
    getVersion()
      .then((version) => setState({ kind: "ok", version }))
      .catch((err: unknown) => setState({ kind: "error", message: String(err) }));
  }, []);

  return (
    <main className="shell">
      <h1>PGDock</h1>
      <p className="lede">Self-hosted managed PostgreSQL. The full UI arrives in M2.</p>
      <section className="card" aria-live="polite">
        <h2>Server</h2>
        {state.kind === "loading" && <p>Connecting…</p>}
        {state.kind === "error" && <p className="error">Could not reach the API: {state.message}</p>}
        {state.kind === "ok" && (
          <dl>
            <dt>Version</dt>
            <dd>{state.version.version}</dd>
            <dt>Commit</dt>
            <dd>
              <code>{state.version.commit}</code>
            </dd>
            <dt>Built</dt>
            <dd>{state.version.build_date}</dd>
            <dt>Go</dt>
            <dd>{state.version.go_version}</dd>
          </dl>
        )}
      </section>
    </main>
  );
}
