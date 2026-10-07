import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { Bell, Box, Building2, Check, ChevronsUpDown, LogOut, Menu, Monitor, Moon, Plug, Plus, Search, ShieldCheck, Sun, User } from "lucide-react";
import { useMemo, useState, type FormEvent } from "react";
import { api, errorMessage, type Org, type Project } from "../../api/client";
import { clearCurrentOrg, setCurrentOrg, useCurrentOrg } from "../../lib/org";
import { sessionQuery } from "../../lib/session";
import { keyLabel } from "../../lib/shortcuts";
import { useTheme, type Theme } from "../../lib/theme";
import { ConnectPanel } from "../../pages/ProjectConnect";
import {
  Alert,
  Badge,
  Button,
  cx,
  Dialog,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
  Field,
  Input,
} from "../ui";
import { LogoMark } from "./Logo";
import type { ShellContext } from "./nav";

const crumb =
  "flex h-8 max-w-[24rem] min-w-0 shrink items-center gap-2 rounded-md px-2 text-[13px] text-fg hover:bg-surface-2 data-[state=open]:bg-surface-2 outline-none";

function Slash() {
  return (
    <span className="hidden text-line-strong select-none sm:inline" aria-hidden>
      /
    </span>
  );
}

/** The bar across the top: where you are (organisation, project, branch)
 * and what you can always reach (Connect, search, alerts, your account). */
export function TopBar({
  ctx,
  project,
  onSearch,
  onMenu,
}: {
  ctx: ShellContext;
  project?: Project;
  onSearch: () => void;
  /** Opens the navigation drawer on narrow screens. */
  onMenu: () => void;
}) {
  const { data: session } = useQuery(sessionQuery);
  const platformAdmin = session?.user?.platform_role === "platform_admin";
  const supportStaff = session?.user?.platform_role === "support";
  const [connect, setConnect] = useState(false);
  return (
    <header className="flex h-12 shrink-0 items-center gap-1 overflow-hidden border-b border-line bg-bg px-3" data-testid="top-bar">
      <button
        type="button"
        onClick={onMenu}
        aria-label="Open navigation"
        className="flex h-8 w-8 items-center justify-center rounded-md text-muted hover:bg-surface-2 hover:text-fg md:hidden"
        data-testid="mobile-menu"
      >
        <Menu className="h-4 w-4" strokeWidth={1.6} />
      </button>
      <Link to="/projects" className="mr-1 flex h-8 w-8 items-center justify-center rounded-md hover:bg-surface-2" aria-label="PGDock home">
        <LogoMark />
      </Link>
      <Slash />
      {ctx.kind === "platform" ? (
        <span className={cx(crumb, "hover:bg-transparent")} data-testid="platform-crumb">
          <ShieldCheck className="h-4 w-4 text-accent-text" strokeWidth={1.6} />
          Platform
          <Badge tone="accent">{supportStaff ? "SUPPORT" : "ADMIN"}</Badge>
        </span>
      ) : (
        <OrgSwitcher platformAdmin={platformAdmin} supportStaff={supportStaff} />
      )}
      {ctx.kind === "project" && project && (
        <>
          <Slash />
          <ProjectSwitcher project={project} />
          <Slash />
          <BranchSwitcher project={project} />
          <Button size="tiny" className="ml-2 shrink-0" icon={<Plug className="h-3.5 w-3.5" />} onClick={() => setConnect(true)} data-testid="connect-button" aria-label="Connect">
            <span className="hidden sm:inline">Connect</span>
          </Button>
          <Dialog open={connect} onOpenChange={setConnect} title={`Connect to ${project.name}`} className="w-[min(52rem,calc(100vw-2rem))]">
            <div className="max-h-[70vh] overflow-y-auto">
              <ConnectPanel p={project} />
            </div>
          </Dialog>
        </>
      )}
      <div className="ml-auto flex shrink-0 items-center gap-2">
        <button
          type="button"
          onClick={onSearch}
          className="hidden h-[30px] w-52 items-center gap-2 rounded-md border border-line-strong bg-surface px-2.5 text-[13px] text-muted hover:text-fg sm:flex"
          data-testid="search-button"
        >
          <Search className="h-3.5 w-3.5" />
          Search...
          <kbd className="ml-auto font-sans text-[11px]">{keyLabel("mod+k")}</kbd>
        </button>
        <InvitationsIndicator />
        {platformAdmin && <AlertsIndicator />}
        <UserMenu />
      </div>
    </header>
  );
}

function planBadge(o: Org) {
  return <Badge>{o.plan.toUpperCase()}</Badge>;
}

/** The organisation switcher (V2 §13), with "New organisation". */
function OrgSwitcher({ platformAdmin, supportStaff }: { platformAdmin: boolean; supportStaff: boolean }) {
  const { org, orgs } = useCurrentOrg();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [creating, setCreating] = useState(false);
  if (!org) return null;
  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger className={crumb} data-testid="org-switcher" aria-label={`Organisation: ${org.name}`}>
          <Building2 className="h-4 w-4 shrink-0 text-muted" strokeWidth={1.6} />
          <span className="hidden truncate sm:inline" data-testid="org-switcher-name">
            {org.name}
          </span>
          <span className="hidden shrink-0 sm:inline">{planBadge(org)}</span>
          <ChevronsUpDown className="h-3.5 w-3.5 shrink-0 text-muted" />
        </DropdownMenuTrigger>
        <DropdownMenuContent className="w-72">
          <DropdownMenuLabel>Organisations</DropdownMenuLabel>
          {orgs.map((o) => (
            <DropdownMenuItem
              key={o.id}
              data-testid={`org-option-${o.id}`}
              icon={o.id === org.id ? <Check className="h-3.5 w-3.5" /> : undefined}
              onSelect={() => {
                setCurrentOrg(o.id);
                void qc.invalidateQueries();
                void navigate({ to: "/projects" });
              }}
            >
              <span className="flex items-center gap-2">
                <span className="truncate">{o.name}</span>
                {o.personal && <span className="text-[11px] text-muted">personal</span>}
              </span>
            </DropdownMenuItem>
          ))}
          <DropdownMenuSeparator />
          <DropdownMenuItem icon={<Plus className="h-3.5 w-3.5" />} onSelect={() => setCreating(true)} data-testid="new-org">
            New organisation
          </DropdownMenuItem>
          {platformAdmin && (
            <DropdownMenuItem icon={<ShieldCheck className="h-3.5 w-3.5" />} onSelect={() => void navigate({ to: "/nodes" })} data-testid="platform-link">
              Platform admin
            </DropdownMenuItem>
          )}
          {supportStaff && (
            <DropdownMenuItem icon={<ShieldCheck className="h-3.5 w-3.5" />} onSelect={() => void navigate({ to: "/admin/support" })} data-testid="support-console-link">
              Support console
            </DropdownMenuItem>
          )}
        </DropdownMenuContent>
      </DropdownMenu>
      <NewOrgDialog open={creating} onOpenChange={setCreating} />
    </>
  );
}

function NewOrgDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (v: boolean) => void }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const create = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      const o = await api.createOrg(name.trim());
      await qc.invalidateQueries({ queryKey: ["orgs"] });
      setCurrentOrg(o.id);
      onOpenChange(false);
      setName("");
      await navigate({ to: "/projects" });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title="New organisation"
      description="An organisation holds projects and the people who work on them. You become its owner."
    >
      <form className="flex flex-col gap-4" onSubmit={create} data-testid="new-org-form">
        <Field label="Name">{(id) => <Input id={id} required maxLength={64} value={name} onChange={(e) => setName(e.target.value)} autoFocus />}</Field>
        {err && <Alert>{err}</Alert>}
        <div className="flex justify-end gap-2">
          <Button onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={!name.trim()}>
            Create organisation
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** The path after /projects/<id>, so switching keeps the same page. */
function subpath(): string {
  const m = /^\/projects\/[0-9a-f-]{36}(\/.*)?$/.exec(window.location.pathname);
  return m?.[1] ?? "";
}

/** The organisation's projects (not branches), searchable. */
function ProjectSwitcher({ project }: { project: Project }) {
  const navigate = useNavigate();
  const rootId = project.parent_project_id ?? project.id;
  const root = useQuery({
    queryKey: ["project", rootId],
    queryFn: () => api.project(rootId),
    enabled: !!project.parent_project_id,
  });
  const list = useQuery({
    queryKey: ["projects", project.org_id],
    queryFn: () => api.projects(project.org_id),
  });
  const [q, setQ] = useState("");
  const name = project.parent_project_id ? (root.data?.name ?? "…") : project.name;
  const items = useMemo(
    () => (list.data?.items ?? []).filter((x) => !x.parent_project_id && x.name.toLowerCase().includes(q.trim().toLowerCase())),
    [list.data, q],
  );
  return (
    <DropdownMenu onOpenChange={(o) => !o && setQ("")}>
      <DropdownMenuTrigger className={crumb} data-testid="project-switcher" aria-label={`Project: ${name}`}>
        <Box className="h-4 w-4 shrink-0 text-muted" strokeWidth={1.6} />
        <span className="truncate">{name}</span>
        <ChevronsUpDown className="h-3.5 w-3.5 shrink-0 text-muted" />
      </DropdownMenuTrigger>
      <DropdownMenuContent className="w-72 p-0">
        <div className="border-b border-line p-1.5">
          <Input
            autoFocus
            placeholder="Find project..."
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => e.stopPropagation()}
            className="h-7 border-transparent bg-transparent focus:ring-0"
          />
        </div>
        <div className="max-h-72 overflow-y-auto p-1">
          {items.map((x) => (
            <DropdownMenuItem
              key={x.id}
              icon={x.id === rootId ? <Check className="h-3.5 w-3.5" /> : undefined}
              onSelect={() => void navigate({ to: `/projects/${x.id}${subpath()}` })}
            >
              {x.name}
            </DropdownMenuItem>
          ))}
          {items.length === 0 && <p className="px-2 py-1.5 text-[13px] text-muted">No projects found</p>}
        </div>
        <DropdownMenuSeparator className="m-0" />
        <div className="p-1">
          <DropdownMenuItem onSelect={() => void navigate({ to: "/projects" })}>All projects</DropdownMenuItem>
          <DropdownMenuItem icon={<Plus className="h-3.5 w-3.5" />} onSelect={() => void navigate({ to: "/projects/new" })}>
            New project
          </DropdownMenuItem>
        </div>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/** Branches, as in Supabase: main (PRODUCTION) and its branches (PREVIEW). */
function BranchSwitcher({ project }: { project: Project }) {
  const navigate = useNavigate();
  const rootId = project.parent_project_id ?? project.id;
  const branches = useQuery({
    queryKey: ["branches", rootId],
    queryFn: () => api.branches(rootId),
  });
  const [q, setQ] = useState("");
  const isBranch = !!project.parent_project_id;
  const list = (branches.data?.items ?? []).filter((b) => b.name.toLowerCase().includes(q.trim().toLowerCase()));
  return (
    <DropdownMenu onOpenChange={(o) => !o && setQ("")}>
      <DropdownMenuTrigger className={crumb} data-testid="branch-switcher" aria-label={`Branch: ${isBranch ? project.name : "main"}`}>
        <span className="truncate">{isBranch ? project.name : "main"}</span>
        <span className="hidden shrink-0 sm:inline">{isBranch ? <Badge tone="accent">PREVIEW</Badge> : <Badge tone="warn">PRODUCTION</Badge>}</span>
        <ChevronsUpDown className="h-3.5 w-3.5 shrink-0 text-muted" />
      </DropdownMenuTrigger>
      <DropdownMenuContent className="w-72 p-0">
        <div className="border-b border-line p-1.5">
          <Input
            autoFocus
            placeholder="Find branch..."
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => e.stopPropagation()}
            className="h-7 border-transparent bg-transparent focus:ring-0"
          />
        </div>
        <div className="max-h-72 overflow-y-auto p-1">
          {"main".includes(q.trim().toLowerCase()) && (
            <DropdownMenuItem
              icon={!isBranch ? <Check className="h-3.5 w-3.5" /> : undefined}
              onSelect={() => void navigate({ to: `/projects/${rootId}${subpath()}` })}
            >
              <span className="flex items-center gap-2">
                main <Badge tone="warn">PRODUCTION</Badge>
              </span>
            </DropdownMenuItem>
          )}
          {list.map((b) => (
            <DropdownMenuItem
              key={b.id}
              icon={b.id === project.id ? <Check className="h-3.5 w-3.5" /> : undefined}
              onSelect={() => void navigate({ to: `/projects/${b.id}${subpath()}` })}
            >
              <span className="flex items-center gap-2">
                <span className="truncate">{b.name}</span>
                <Badge tone="accent">PREVIEW</Badge>
              </span>
            </DropdownMenuItem>
          ))}
        </div>
        <DropdownMenuSeparator className="m-0" />
        <div className="p-1">
          <DropdownMenuItem icon={<Plus className="h-3.5 w-3.5" />} onSelect={() => void navigate({ to: `/projects/${rootId}/branches` })}>
            Create branch
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => void navigate({ to: `/projects/${rootId}/branches` })}>Manage branches</DropdownMenuItem>
        </div>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/** Pending invitations (V2 §13). */
function InvitationsIndicator() {
  const q = useQuery({
    queryKey: ["me", "invitations"],
    queryFn: api.myInvitations,
    refetchInterval: 60_000,
    retry: false,
  });
  const n = q.data?.items.length ?? 0;
  if (n === 0) return null;
  return (
    <Link
      to="/account"
      hash="invitations"
      className="rounded-full bg-accent px-2 py-0.5 text-xs font-medium text-accent-fg"
      data-testid="invitations-indicator"
    >
      {n} invitation{n === 1 ? "" : "s"}
    </Link>
  );
}

/** Firing alerts, for the platform admin. */
function AlertsIndicator() {
  const firing = useQuery({
    queryKey: ["alerts", "count"],
    queryFn: () => api.alerts("firing"),
    refetchInterval: 30_000,
    retry: false,
  });
  const n = firing.data?.firing ?? 0;
  return (
    <Link
      to="/alerts"
      className="relative flex h-8 w-8 items-center justify-center rounded-full border border-line-strong hover:bg-surface-2"
      aria-label="Alerts"
    >
      <Bell className="h-4 w-4" strokeWidth={1.6} />
      {n > 0 && (
        <span
          data-testid="alerts-count"
          className={cx(
            "absolute -top-1 -right-1 rounded-full px-1 text-[10px] leading-4 font-semibold text-white",
            (firing.data?.critical ?? 0) > 0 ? "bg-danger" : "bg-warn",
          )}
        >
          {n}
        </span>
      )}
    </Link>
  );
}

const themes: { value: Theme; label: string; icon: typeof Sun }[] = [
  { value: "dark", label: "Dark", icon: Moon },
  { value: "light", label: "Light", icon: Sun },
  { value: "system", label: "System", icon: Monitor },
];

/** The avatar menu: account, theme, sign out. */
function UserMenu() {
  const { data: session } = useQuery(sessionQuery);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [theme, setTheme] = useTheme();
  const user = session?.user;
  const initials = (user?.name || user?.email || "?")
    .split(/[\s@.]+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((w) => w[0]!.toUpperCase())
    .join("");
  const logout = async () => {
    await api.logout().catch(() => {});
    qc.clear();
    clearCurrentOrg();
    await navigate({ to: "/login" });
  };
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        className="flex h-8 w-8 items-center justify-center rounded-full bg-accent-soft text-[11px] font-semibold text-accent-text outline-none hover:ring-2 hover:ring-line-strong"
        aria-label="Account menu"
        data-testid="user-menu"
      >
        {initials}
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-64">
        <div className="px-2 py-1.5">
          <p className="truncate text-[13px]">{user?.name || user?.email}</p>
          {user?.name && <p className="truncate text-xs text-muted">{user.email}</p>}
        </div>
        <DropdownMenuSeparator />
        <DropdownMenuItem icon={<User className="h-3.5 w-3.5" />} onSelect={() => void navigate({ to: "/account" })} data-testid="account-link">
          Account
        </DropdownMenuItem>
        <DropdownMenuSub>
          <DropdownMenuSubTrigger>
            <span className="flex h-4 w-4 items-center justify-center text-fg-light">
              <Moon className="h-3.5 w-3.5" />
            </span>
            Theme
          </DropdownMenuSubTrigger>
          <DropdownMenuSubContent>
            {themes.map((t) => (
              <DropdownMenuItem
                key={t.value}
                icon={theme === t.value ? <Check className="h-3.5 w-3.5" /> : <t.icon className="h-3.5 w-3.5" />}
                onSelect={() => setTheme(t.value)}
              >
                {t.label}
              </DropdownMenuItem>
            ))}
          </DropdownMenuSubContent>
        </DropdownMenuSub>
        <DropdownMenuSeparator />
        <DropdownMenuItem icon={<LogOut className="h-3.5 w-3.5" />} onSelect={() => void logout()} data-testid="sign-out">
          Sign out
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
