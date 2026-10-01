import { Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { api, errorMessage } from "../api/client";
import { Alert, Badge, EmptyState, Input, PageHeader, Select, Spinner, StatusBadge, Table } from "../components/ui";
import { formatDate } from "../lib/format";

export function ProjectsPage() {
  const q = useQuery({ queryKey: ["projects"], queryFn: () => api.projects(), refetchInterval: 15_000 });
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState("");
  const items = useMemo(() => {
    const s = search.trim().toLowerCase();
    return (q.data?.items ?? []).filter(
      (p) => (!status || p.status === status) && (!s || p.name.toLowerCase().includes(s) || p.db_name.includes(s)),
    );
  }, [q.data, search, status]);

  return (
    <>
      <PageHeader
        title="Projects"
        subtitle="Each project is one PostgreSQL database with its own role and connection strings."
        actions={
          <Link to="/projects/new" className="rounded-md bg-accent px-3 py-1.5 text-sm font-medium text-accent-fg hover:opacity-90">
            New project
          </Link>
        }
      />
      <div className="mb-3 flex flex-wrap gap-2">
        <Input placeholder="Search name or database" value={search} onChange={(e) => setSearch(e.target.value)} className="max-w-xs" aria-label="Search projects" />
        <Select value={status} onChange={(e) => setStatus(e.target.value)} aria-label="Filter by status">
          <option value="">All statuses</option>
          {["active", "provisioning", "deleting", "error"].map((s) => (
            <option key={s}>{s}</option>
          ))}
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
        <Table head={["Name", "Database", "Tier", "Status", "Created"]}>
          {items.map((p) => (
            <tr key={p.id} className="hover:bg-surface-2">
              <td className="px-3 py-2">
                <Link to="/projects/$id" params={{ id: p.id }} className="font-medium hover:underline">
                  {p.name}
                </Link>
              </td>
              <td className="px-3 py-2 font-mono text-xs text-muted">{p.db_name}</td>
              <td className="px-3 py-2">
                <Badge tone={p.tier === "dedicated" ? "accent" : "muted"}>{p.tier === "dedicated" ? "Dedicated" : "Shared"}</Badge>
              </td>
              <td className="px-3 py-2">
                <StatusBadge status={p.status} />
              </td>
              <td className="px-3 py-2 text-muted">{formatDate(p.created_at)}</td>
            </tr>
          ))}
          {items.length === 0 && (
            <tr>
              <td colSpan={5} className="px-3 py-6 text-center text-muted">
                No projects match.
              </td>
            </tr>
          )}
        </Table>
      )}
    </>
  );
}
