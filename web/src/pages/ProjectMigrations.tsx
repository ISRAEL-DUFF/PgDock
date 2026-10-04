import { useQuery } from "@tanstack/react-query";
import { Download } from "lucide-react";
import { api, errorMessage, type AuditEntry } from "../api/client";
import { Alert, Badge, Button, CodeBlock, CopyButton, EmptyState, Page, Panel, Spinner } from "../components/ui";
import { download } from "../lib/csv";
import { formatDate } from "../lib/format";
import { useProject } from "./ProjectOverview";

/** The SQL of a schema change, as the audit log kept it. */
function statements(e: AuditEntry): string[] {
  const sql = (e.detail as { sql?: unknown }).sql;
  return Array.isArray(sql) ? sql.filter((x): x is string => typeof x === "string") : [];
}

/** All the changes, oldest first, as one SQL file with a comment per
 * change. */
export function migrationFile(entries: AuditEntry[]): string {
  return [...entries]
    .reverse()
    .map((e) => {
      const kind = String((e.detail as { kind?: unknown }).kind ?? "change");
      const head = `-- ${formatDate(e.created_at)}: ${kind}${e.user_email ? ` by ${e.user_email}` : ""}`;
      return `${head}\n${statements(e)
        .map((s) => s.replace(/;?\s*$/, ";"))
        .join("\n")}\n`;
    })
    .join("\n");
}

/** Database → Migrations (docs/ui-redesign.md, phase 4): the schema
 * changes made in the table editor, from the audit log, to replay
 * elsewhere. */
export function ProjectMigrationsPage() {
  const { data: p } = useProject();
  const q = useQuery({
    queryKey: ["migrations", p?.id],
    queryFn: () => api.projectAudit(p!.id, { action: "project.schema.change", outcome: "success", limit: 200 }),
    enabled: !!p,
  });
  if (!p) return null;
  const items = (q.data?.items ?? []).filter((e) => statements(e).length > 0);
  return (
    <Page
      title="Migrations"
      description="Schema changes made from the table editor, newest first. Each was reviewed before it ran; the SQL is what ran."
      actions={
        items.length > 0 && (
          <Button icon={<Download className="h-3.5 w-3.5" />} onClick={() => download(`${p.db_name}-migrations.sql`, migrationFile(items))} data-testid="download-migrations">
            Download as SQL
          </Button>
        )
      }
      testId="project-migrations"
    >
      {q.isPending ? (
        <Spinner />
      ) : q.isError ? (
        <Alert>{errorMessage(q.error)}</Alert>
      ) : items.length === 0 ? (
        <EmptyState title="No schema changes yet">Changes you run from the table editor are listed here, with their SQL.</EmptyState>
      ) : (
        <div className="flex flex-col gap-3">
          {items.map((e) => {
            const sql = statements(e).join(";\n") + ";";
            return (
              <Panel
                key={e.id}
                title={
                  <span className="flex items-center gap-2">
                    <Badge tone="accent">{String((e.detail as { kind?: unknown }).kind ?? "change").replace(/_/g, " ")}</Badge>
                    <span className="text-[13px] text-muted">{formatDate(e.created_at)}</span>
                  </span>
                }
                actions={
                  <>
                    {e.user_email && <span className="text-[12px] text-muted">{e.user_email}</span>}
                    <CopyButton value={sql} label="Copy SQL" />
                  </>
                }
                testId="migration"
              >
                <CodeBlock code={sql} />
              </Panel>
            );
          })}
        </div>
      )}
    </Page>
  );
}
