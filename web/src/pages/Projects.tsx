import { Link, useNavigate } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { Database, GitBranch, LayoutGrid, List, Plus, Search, Upload } from "lucide-react";
import { useMemo, useState } from "react";
import { api, errorMessage, type Project } from "../api/client";
import { Alert, Badge, Button, EmptyState, Page, Select, StatusBadge, Table, cx, CardsSkeleton } from "../components/ui";
import { formatDate, relativeTime, timeUntil } from "../lib/format";
import { canManageOrg, useCurrentOrg } from "../lib/org";
import { backupIsStale } from "./ProjectBackups";
import { QuotasCard } from "./Usage";

const VIEW_KEY = "pgdock.projects.view";

function storedView(): "grid" | "list" {
  try {
    return localStorage.getItem(VIEW_KEY) === "list" ? "list" : "grid";
  } catch {
    return "grid";
  }
}

function LastBackup({ p }: { p: Project }) {
  return (
    <span data-testid="last-backup-cell">
      {p.last_backup_at ? (
        <span className={backupIsStale(p.last_backup_at) ? "text-warn-text" : "text-muted"} title={formatDate(p.last_backup_at)}>
          {backupIsStale(p.last_backup_at) && "⚠ "}
          {relativeTime(p.last_backup_at)}
        </span>
      ) : (
        <span className="text-warn-text">⚠ never</span>
      )}
    </span>
  );
}

function BranchExpiry({ b }: { b: Project }) {
  if (!b.branch) return null;
  return (
    <span className="text-xs text-muted" data-testid="branch-ttl-cell" title={formatDate(b.branch.expires_at)}>
      {b.branch.expires_at ? `expires ${timeUntil(b.branch.expires_at)}` : "kept"}
    </span>
  );
}

/** One project as a card on the organisation's home, as Studio's. */
function ProjectCard({ p, branches }: { p: Project; branches: Project[] }) {
  const navigate = useNavigate();
  return (
    <div
      role="group"
      aria-label={p.name}
      onClick={() => void navigate({ to: "/projects/$id", params: { id: p.id } })}
      className="group flex cursor-pointer flex-col gap-4 rounded-md border border-line bg-surface p-4 transition-colors hover:border-line-strong hover:bg-surface-2"
      data-testid="project-card"
    >
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <Link to="/projects/$id" params={{ id: p.id }} className="block truncate text-[14px] font-medium" onClick={(e) => e.stopPropagation()}>
            {p.name}
          </Link>
          <p className="truncate font-mono text-[11px] text-muted">{p.db_name}</p>
        </div>
        <StatusBadge status={p.status} />
      </div>
      <div className="flex flex-wrap items-center gap-1.5">
        <Badge tone={p.tier === "dedicated" ? "accent" : "muted"}>{p.tier === "dedicated" ? "Dedicated" : "Shared"}</Badge>
        {p.instance?.node_name && <Badge>{p.instance.node_name}</Badge>}
        {p.sensitive_data && <Badge tone="warn">sensitive</Badge>}
      </div>
      <div className="flex items-center justify-between text-[12px] text-muted">
        <span className="flex items-center gap-1">
          <Database className="h-3 w-3" /> Last backup <LastBackup p={p} />
        </span>
        <span>{relativeTime(p.created_at)}</span>
      </div>
      {branches.length > 0 && (
        <ul className="flex flex-col gap-1 border-t border-line pt-3" onClick={(e) => e.stopPropagation()}>
          {branches.map((b) => (
            <li key={b.id} className="flex items-center gap-2 text-[12px]" data-testid="branch-list-row">
              <GitBranch className="h-3 w-3 shrink-0 text-muted" />
              <Link to="/projects/$id" params={{ id: b.id }} className="truncate hover:underline">
                {b.name}
              </Link>
              <span className="flex-1" />
              <BranchExpiry b={b} />
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/** The organisation's home: its projects (Studio's project grid). */
export function ProjectsPage() {
  const { org } = useCurrentOrg();
  const q = useQuery({ queryKey: ["projects", org?.id], queryFn: () => api.projects(org?.id), refetchInterval: 15_000, enabled: !!org });
  const canCreate = canManageOrg(org) || !!org?.members_can_create_projects;
  const quotas = useQuery({ queryKey: ["org", org?.id, "quotas"], queryFn: () => api.orgQuotas(org!.id), enabled: !!org });
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState("");
  const [tier, setTier] = useState("");
  const [view, setViewState] = useState<"grid" | "list">(storedView);
  const setView = (v: "grid" | "list") => {
    setViewState(v);
    try {
      localStorage.setItem(VIEW_KEY, v);
    } catch {
      /* storage unavailable */
    }
  };
  const { parents, branchesOf, flat } = useMemo(() => {
    const s = search.trim().toLowerCase();
    const shown = (q.data?.items ?? []).filter(
      (p) => (!status || p.status === status) && (!tier || p.tier === tier) && (!s || p.name.toLowerCase().includes(s) || p.db_name.includes(s)),
    );
    // Branches go with their parent (V2 §13); one whose parent isn't shown
    // stands on its own.
    const ids = new Set(shown.map((p) => p.id));
    const kids = new Map<string, Project[]>();
    for (const p of shown)
      if (p.parent_project_id && ids.has(p.parent_project_id)) kids.set(p.parent_project_id, [...(kids.get(p.parent_project_id) ?? []), p]);
    const top = shown.filter((p) => !p.parent_project_id || !ids.has(p.parent_project_id));
    return { parents: top, branchesOf: (id: string) => kids.get(id) ?? [], flat: top.flatMap((p) => [p, ...(kids.get(p.id) ?? [])]) };
  }, [q.data, search, status, tier]);

  return (
    <Page
      title="Projects"
      description={org ? `In ${org.name}. Each project is one PostgreSQL database with its own role and connection strings.` : undefined}
      actions={
        canCreate && (
          <>
            <Link to="/projects/import">
              <Button icon={<Upload className="h-3.5 w-3.5" />}>Import</Button>
            </Link>
            <Link to="/projects/new">
              <Button variant="primary" icon={<Plus className="h-3.5 w-3.5" />}>
                New project
              </Button>
            </Link>
          </>
        )
      }
      testId="projects"
    >
      {quotas.data && <QuotasCard quotas={quotas.data} compact />}
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative">
          <Search className="pointer-events-none absolute top-1/2 left-2 h-3.5 w-3.5 -translate-y-1/2 text-muted" />
          <input
            type="search"
            placeholder="Search for a project"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            aria-label="Search projects"
            className="h-[30px] w-64 rounded-md border border-line-strong bg-surface-2 pr-2 pl-7 text-[13px] text-fg placeholder:text-muted focus:border-accent focus:outline-none"
          />
        </div>
        <Select value={tier} onChange={(e) => setTier(e.target.value)} aria-label="Filter by tier">
          <option value="">All tiers</option>
          <option value="shared">Shared</option>
          <option value="dedicated">Dedicated</option>
        </Select>
        <Select value={status} onChange={(e) => setStatus(e.target.value)} aria-label="Filter by status">
          <option value="">All statuses</option>
          {["active", "provisioning", "restoring", "deleting", "error"].map((s) => (
            <option key={s}>{s}</option>
          ))}
        </Select>
        <span className="flex-1" />
        <div className="inline-flex rounded-md border border-line-strong bg-surface p-0.5" role="radiogroup" aria-label="View">
          {(
            [
              ["grid", LayoutGrid],
              ["list", List],
            ] as const
          ).map(([v, Icon]) => (
            <button
              key={v}
              type="button"
              role="radio"
              aria-checked={view === v}
              aria-label={v === "grid" ? "Grid" : "List"}
              onClick={() => setView(v)}
              className={cx("rounded p-1", view === v ? "bg-surface-3 text-fg" : "text-muted hover:text-fg")}
            >
              <Icon className="h-3.5 w-3.5" />
            </button>
          ))}
        </div>
      </div>
      {q.isPending && <CardsSkeleton />}
      {q.isError && <Alert>{errorMessage(q.error)}</Alert>}
      {q.data && q.data.items.length === 0 && (
        <EmptyState
          title="No projects yet"
          icon={<Database />}
          action={
            canCreate && (
              <Link to="/projects/new">
                <Button variant="primary" icon={<Plus className="h-3.5 w-3.5" />}>
                  New project
                </Button>
              </Link>
            )
          }
        >
          Each project is a PostgreSQL database with its own connection strings. It takes a few seconds to create.
        </EmptyState>
      )}
      {q.data && q.data.items.length > 0 && parents.length === 0 && <p className="text-[13px] text-muted">No projects match.</p>}
      {q.data && parents.length > 0 && view === "grid" && (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
          {parents.map((p) => (
            <ProjectCard key={p.id} p={p} branches={branchesOf(p.id)} />
          ))}
        </div>
      )}
      {q.data && parents.length > 0 && view === "list" && (
        <Table head={["Name", "Database", "Tier", "Status", "Last backup", "Created"]}>
          {flat.map((p) => (
            <tr key={p.id} className="hover:bg-surface-2" data-testid={p.parent_project_id ? "branch-list-row" : "project-row"}>
              <td className={p.parent_project_id ? "py-2 pr-3 pl-8" : "px-3 py-2"}>
                {p.parent_project_id && <span className="mr-1 text-muted">↳</span>}
                <Link to="/projects/$id" params={{ id: p.id }} className="font-medium hover:underline">
                  {p.name}
                </Link>
                {p.sensitive_data && (
                  <span className="ml-2">
                    <Badge tone="warn">sensitive</Badge>
                  </span>
                )}
                {p.parent_project_id && (
                  <span className="ml-2">
                    <BranchExpiry b={p} />
                  </span>
                )}
              </td>
              <td className="px-3 py-2 font-mono text-xs text-muted">{p.db_name}</td>
              <td className="px-3 py-2">
                <Badge tone={p.tier === "dedicated" ? "accent" : "muted"}>{p.tier === "dedicated" ? "Dedicated" : "Shared"}</Badge>
              </td>
              <td className="px-3 py-2">
                <StatusBadge status={p.status} />
              </td>
              <td className="px-3 py-2">
                <LastBackup p={p} />
              </td>
              <td className="px-3 py-2 text-muted">{formatDate(p.created_at)}</td>
            </tr>
          ))}
        </Table>
      )}
    </Page>
  );
}
