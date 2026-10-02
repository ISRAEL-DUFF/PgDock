import { Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { api, errorMessage } from "../api/client";
import {
  Alert,
  Badge,
  EmptyState,
  Input,
  PageHeader,
  Select,
  Spinner,
  StatusBadge,
  Table,
} from "../components/ui";
import { formatDate, relativeTime } from "../lib/format";
import { canManageOrg, useCurrentOrg } from "../lib/org";
import { backupIsStale } from "./ProjectBackups";

export function ProjectsPage() {
  const { org } = useCurrentOrg();
  const q = useQuery({
    queryKey: ["projects", org?.id],
    queryFn: () => api.projects(org?.id),
    refetchInterval: 15_000,
    enabled: !!org,
  });
  const canCreate = canManageOrg(org) || !!org?.members_can_create_projects;
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState("");
  const [tier, setTier] = useState("");
  const items = useMemo(() => {
    const s = search.trim().toLowerCase();
    return (q.data?.items ?? []).filter(
      (p) =>
        (!status || p.status === status) &&
        (!tier || p.tier === tier) &&
        (!s || p.name.toLowerCase().includes(s) || p.db_name.includes(s)),
    );
  }, [q.data, search, status, tier]);

  return (
    <>
      <PageHeader
        title="Projects"
        subtitle={
          org
            ? `In ${org.name}. Each project is one PostgreSQL database with its own role and connection strings.`
            : undefined
        }
        actions={
          canCreate && (
            <>
              <Link
                to="/projects/import"
                className="rounded-md border border-line bg-surface px-3 py-1.5 text-sm font-medium hover:bg-surface-2"
              >
                Import
              </Link>
              <Link
                to="/projects/new"
                className="rounded-md bg-accent px-3 py-1.5 text-sm font-medium text-accent-fg hover:opacity-90"
              >
                New project
              </Link>
            </>
          )
        }
      />
      <div className="mb-3 flex flex-wrap gap-2">
        <Input
          placeholder="Search name or database"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className="max-w-xs"
          aria-label="Search projects"
        />
        <Select
          value={tier}
          onChange={(e) => setTier(e.target.value)}
          aria-label="Filter by tier"
        >
          <option value="">All tiers</option>
          <option value="shared">Shared</option>
          <option value="dedicated">Dedicated</option>
        </Select>
        <Select
          value={status}
          onChange={(e) => setStatus(e.target.value)}
          aria-label="Filter by status"
        >
          <option value="">All statuses</option>
          {["active", "provisioning", "restoring", "deleting", "error"].map(
            (s) => (
              <option key={s}>{s}</option>
            ),
          )}
        </Select>
      </div>
      {q.isPending && <Spinner />}
      {q.isError && <Alert>{errorMessage(q.error)}</Alert>}
      {q.data && q.data.items.length === 0 && (
        <EmptyState title="No projects yet">
          <Link to="/projects/new" className="text-accent hover:underline">
            Create your first database
          </Link>{" "}
          — it takes a few seconds.
        </EmptyState>
      )}
      {q.data && q.data.items.length > 0 && (
        <Table
          head={[
            "Name",
            "Database",
            "Tier",
            "Status",
            "Last backup",
            "Created",
          ]}
        >
          {items.map((p) => (
            <tr key={p.id} className="hover:bg-surface-2">
              <td className="px-3 py-2">
                <Link
                  to="/projects/$id"
                  params={{ id: p.id }}
                  className="font-medium hover:underline"
                >
                  {p.name}
                </Link>
              </td>
              <td className="px-3 py-2 font-mono text-xs text-muted">
                {p.db_name}
              </td>
              <td className="px-3 py-2">
                <Badge tone={p.tier === "dedicated" ? "accent" : "muted"}>
                  {p.tier === "dedicated" ? "Dedicated" : "Shared"}
                </Badge>
              </td>
              <td className="px-3 py-2">
                <StatusBadge status={p.status} />
              </td>
              <td className="px-3 py-2" data-testid="last-backup-cell">
                {p.last_backup_at ? (
                  <span
                    className={
                      backupIsStale(p.last_backup_at)
                        ? "text-warn"
                        : "text-muted"
                    }
                    title={formatDate(p.last_backup_at)}
                  >
                    {backupIsStale(p.last_backup_at) && "⚠ "}
                    {relativeTime(p.last_backup_at)}
                  </span>
                ) : (
                  <span className="text-warn">⚠ never</span>
                )}
              </td>
              <td className="px-3 py-2 text-muted">
                {formatDate(p.created_at)}
              </td>
            </tr>
          ))}
          {items.length === 0 && (
            <tr>
              <td colSpan={6} className="px-3 py-6 text-center text-muted">
                No projects match.
              </td>
            </tr>
          )}
        </Table>
      )}
    </>
  );
}
