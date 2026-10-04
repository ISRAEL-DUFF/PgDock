import { useState } from "react";
import { CodeBlock, CopyField, Page, Panel, cx } from "../components/ui";
import type { Project } from "../api/client";
import { useProject } from "./ProjectOverview";

export function ProjectConnectPage() {
  const { data: p } = useProject();
  if (!p) return null;
  return (
    <Page title="Connect to your project" description="Connection details and snippets for your app, migrations and tools." testId="project-connect">
      <ConnectPanel p={p} />
    </Page>
  );
}

/** Connection strings and snippets: the Connect page, and the top bar's
 * Connect dialog. */
export function ConnectPanel({ p }: { p: Project }) {
  const [tab, setTab] = useState("psql");
  const c = p.connection;
  const pw = (url: string) =>
    url.replace(`${c.user}@`, `${c.user}:YOUR_PASSWORD@`);
  const pooled = pw(c.pooled_url);
  const session = pw(c.session_url);

  const snippets: Record<string, string> = {
    psql: `psql '${session}'`,
    ".env": `# App traffic through the transaction pooler
DATABASE_URL=${pooled}
# Migrations need session features
DIRECT_URL=${session}`,
    "node-postgres": `import pg from "pg";

const pool = new pg.Pool({ connectionString: process.env.DATABASE_URL });
const { rows } = await pool.query("select now()");`,
    Prisma: `// schema.prisma
datasource db {
  provider  = "postgresql"
  url       = env("DATABASE_URL")   // ${c.host}:${c.pooled_port} (pooled)
  directUrl = env("DIRECT_URL")     // ${c.host}:${c.session_port} (migrations)
}`,
    Django: `DATABASES = {
    "default": {
        "ENGINE": "django.db.backends.postgresql",
        "HOST": "${c.host}",
        "PORT": "${c.pooled_port}",
        "NAME": "${c.database}",
        "USER": "${c.user}",
        "PASSWORD": os.environ["DB_PASSWORD"],
        "OPTIONS": {"sslmode": "${c.sslmode}"},
        "DISABLE_SERVER_SIDE_CURSORS": True,  # transaction pooling
    }
}`,
    Go: `pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))`,
  };

  return (
    <div className="flex flex-col gap-4">
      <Panel title="Connection details">
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <CopyField label="Host" value={c.host} />
          <CopyField label="Database" value={c.database} />
          <CopyField label="User" value={c.user} />
          <CopyField label="SSL mode" value={c.sslmode} />
          <CopyField
            label="Pooled port (transaction mode)"
            value={String(c.pooled_port)}
          />
          <CopyField label="Session port" value={String(c.session_port)} />
        </div>
        <p className="mt-3 text-xs text-muted">
          Use the pooled port for application traffic and serverless functions;
          use the session port for migrations, <code>LISTEN</code>, and advisory
          locks. The password was shown once at creation; rotate it in Settings
          if you need a new one.
        </p>
      </Panel>
      <Panel title="Snippets">
        <div className="mb-3 flex flex-wrap gap-1">
          {Object.keys(snippets).map((k) => (
            <button
              key={k}
              type="button"
              onClick={() => setTab(k)}
              className={cx(
                "rounded-md border px-2.5 py-1 text-xs",
                tab === k
                  ? "border-line-strong bg-surface-3 text-fg"
                  : "border-transparent text-muted hover:text-fg",
              )}
            >
              {k}
            </button>
          ))}
        </div>
        <CodeBlock code={snippets[tab]} />
      </Panel>
    </div>
  );
}
