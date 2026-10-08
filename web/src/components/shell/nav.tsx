import {
  KeyRound,
  HardDrive,
  Activity,
  Bell,
  Building2,
  ChartNoAxesColumn,
  Database,
  FolderKanban,
  Gauge,
  House,
  Inbox,
  Coins,
  Globe,
  Layers,
  LifeBuoy,
  Scale,
  TrendingUp,
  Megaphone,
  ScrollText,
  Server,
  Settings,
  SquareTerminal,
  Table2,
  Users,
  type LucideIcon,
  Receipt,
  Tag,
} from "lucide-react";
import type { Org, Project } from "../../api/client";

/** A link in a section sidebar. */
export type SideLink = { label: string; to: string; testId?: string };

/** An entry of the icon rail. */
export type RailItem = {
  key: string;
  label: string;
  icon: LucideIcon;
  /** Where the rail icon goes. */
  to: string;
  /** Paths (prefixes) that make this entry active. */
  match: string[];
  /** Exact match only (an overview page). */
  exact?: boolean;
  /** The section's own sidebar, if it has one. */
  sidebar?: { title: string; links: SideLink[] };
  /** A rule before this entry. */
  divider?: boolean;
};

export type ShellContext =
  | { kind: "project"; projectId: string }
  | { kind: "platform" }
  | { kind: "org" };

const projectPath = /^\/projects\/([0-9a-f-]{36})(\/|$)/;
const platformPaths = ["/nodes", "/alerts", "/admin", "/audit"];

/** Which part of the app a path is in: a project, the platform admin's
 * area, or the organisation's. */
export function contextFor(pathname: string): ShellContext {
  const m = projectPath.exec(pathname);
  if (m) return { kind: "project", projectId: m[1] };
  if (
    pathname === "/settings" ||
    platformPaths.some((p) => pathname === p || pathname.startsWith(p + "/"))
  )
    return { kind: "platform" };
  return { kind: "org" };
}

/** A project's sections (docs/ui-redesign.md, the navigation map). */
export function projectRail(
  p: Pick<Project, "id" | "my_role" | "parent_project_id">,
): RailItem[] {
  const base = `/projects/${p.id}`;
  const admin = p.my_role === "admin";
  const dev = admin || p.my_role === "developer";
  const database: SideLink[] = [
    {
      label: p.parent_project_id ? "Branch" : "Branches",
      to: `${base}/branches`,
    },
    { label: "Backups", to: `${base}/backups` },
    ...(dev
      ? [
          { label: "Webhooks", to: `${base}/webhooks` },
          { label: "Scheduled jobs", to: `${base}/jobs` },
        ]
      : []),
    { label: "Extensions", to: `${base}/extensions` },
    ...(admin ? [{ label: "Migrations", to: `${base}/migrations` }] : []),
  ];
  const settings: SideLink[] = [
    ...(admin
      ? [
          { label: "General", to: `${base}/settings` },
          { label: "Database", to: `${base}/settings/database` },
          { label: "Compute and tier", to: `${base}/settings/compute` },
          { label: "Backup storage", to: `${base}/settings/storage` },
          { label: "API", to: `${base}/settings/api` },
        ]
      : []),
    { label: "Members", to: `${base}/members` },
    { label: "Connection", to: `${base}/connect` },
  ];
  return [
    {
      key: "overview",
      label: "Project Overview",
      icon: House,
      to: base,
      match: [base],
      exact: true,
    },
    {
      key: "tables",
      label: "Table Editor",
      icon: Table2,
      to: `${base}/tables`,
      match: [`${base}/tables`],
    },
    {
      key: "sql",
      label: "SQL Editor",
      icon: SquareTerminal,
      to: `${base}/sql`,
      match: [`${base}/sql`],
    },
    {
      key: "database",
      label: "Database",
      icon: Database,
      to: database[0].to,
      match: database.map((l) => l.to),
      sidebar: { title: "Database", links: database },
      divider: true,
    },
    ...(dev
      ? [
          {
            key: "auth",
            label: "Authentication",
            icon: KeyRound,
            to: `${base}/auth`,
            match: [`${base}/auth`],
          },
          {
            key: "files",
            label: "Storage",
            icon: HardDrive,
            to: `${base}/files`,
            match: [`${base}/files`],
          },
        ]
      : []),
    {
      key: "reports",
      label: "Reports",
      icon: ChartNoAxesColumn,
      to: `${base}/metrics`,
      match: [`${base}/metrics`],
    },
    {
      key: "insights",
      label: "Query insights",
      icon: Gauge,
      to: `${base}/insights`,
      match: [`${base}/insights`],
      divider: true,
    },
    {
      key: "logs",
      label: "Logs",
      icon: ScrollText,
      to: `${base}/logs`,
      match: [`${base}/logs`],
    },
    {
      key: "settings",
      label: "Project Settings",
      icon: Settings,
      to: settings[0].to,
      match: settings.map((l) => l.to),
      sidebar: { title: "Project Settings", links: settings },
      divider: true,
    },
  ];
}

/** The organisation's sections, outside a project. */
export function orgRail(org: Org | undefined): RailItem[] {
  const manager = org?.role === "owner" || org?.role === "admin";
  const items: RailItem[] = [
    {
      key: "projects",
      label: "Projects",
      icon: FolderKanban,
      to: "/projects",
      match: ["/projects"],
    },
    {
      key: "team",
      label: "Team",
      icon: Users,
      to: "/org/members",
      match: ["/org/members"],
    },
    {
      key: "operations",
      label: "Operations",
      icon: Activity,
      to: "/operations",
      match: ["/operations"],
    },
    {
      key: "support",
      label: "Support",
      icon: LifeBuoy,
      to: "/org/support",
      match: ["/org/support"],
    },
    {
      key: "legal",
      label: "Legal",
      icon: Scale,
      to: "/org/legal",
      match: ["/org/legal"],
    },
    {
      key: "pricing",
      label: "Pricing",
      icon: Tag,
      to: "/pricing",
      match: ["/pricing"],
    },
  ];
  // Owners and billing members see billing (V3 §3.2).
  if (org?.role === "owner" || org?.role === "billing") {
    items.push({
      key: "billing",
      label: "Billing",
      icon: Receipt,
      to: "/org/billing",
      match: ["/org/billing"],
    });
  }
  if (manager) {
    items.push(
      {
        key: "usage",
        label: "Usage & quotas",
        icon: ChartNoAxesColumn,
        to: "/org/usage",
        match: ["/org/usage"],
      },
      {
        key: "audit",
        label: "Audit log",
        icon: ScrollText,
        to: "/org/audit",
        match: ["/org/audit"],
      },
      {
        key: "org-settings",
        label: "Organisation settings",
        icon: Settings,
        to: "/org/settings",
        match: ["/org/settings"],
        divider: true,
      },
    );
  }
  return items;
}

const supportItem: RailItem = {
  key: "support-console",
  label: "Support",
  icon: LifeBuoy,
  to: "/admin/support",
  match: ["/admin/support"],
};

/** The platform's sections: everything for a platform admin, the support
 * console alone for support staff (V3 §7.1). */
export function platformRail(role: string = "platform_admin"): RailItem[] {
  if (role === "support") return [supportItem];
  return [
    {
      key: "nodes",
      label: "Nodes",
      icon: Server,
      to: "/nodes",
      match: ["/nodes"],
    },
    {
      key: "capacity",
      label: "Capacity",
      icon: Layers,
      to: "/admin/capacity",
      match: ["/admin/capacity"],
    },
    {
      key: "regions",
      label: "Regions",
      icon: Globe,
      to: "/admin/regions",
      match: ["/admin/regions"],
    },
    {
      key: "orgs",
      label: "Organisations",
      icon: Building2,
      to: "/admin/orgs",
      match: ["/admin/orgs"],
    },
    {
      key: "users",
      label: "Users",
      icon: Users,
      to: "/admin/users",
      match: ["/admin/users"],
    },
    {
      key: "plans",
      label: "Plans",
      icon: Gauge,
      to: "/admin/plans",
      match: ["/admin/plans"],
    },
    {
      key: "billing",
      label: "Billing",
      icon: Receipt,
      to: "/admin/billing",
      match: ["/admin/billing"],
    },
    {
      key: "revenue",
      label: "Revenue",
      icon: TrendingUp,
      to: "/admin/revenue",
      match: ["/admin/revenue"],
    },
    {
      key: "costs",
      label: "Costs & margins",
      icon: Coins,
      to: "/admin/costs",
      match: ["/admin/costs"],
    },
    {
      key: "legal",
      label: "Legal documents",
      icon: Scale,
      to: "/admin/legal",
      match: ["/admin/legal"],
    },
    supportItem,
    {
      key: "requests",
      label: "Dedicated requests",
      icon: Inbox,
      to: "/admin/dedicated-requests",
      match: ["/admin/dedicated-requests"],
    },
    {
      key: "alerts",
      label: "Alerts",
      icon: Bell,
      to: "/alerts",
      match: ["/alerts"],
      divider: true,
    },
    {
      key: "incidents",
      label: "Incidents",
      icon: Megaphone,
      to: "/admin/incidents",
      match: ["/admin/incidents"],
    },
    {
      key: "platform-audit",
      label: "Platform audit",
      icon: ScrollText,
      to: "/audit",
      match: ["/audit"],
    },
    {
      key: "platform-settings",
      label: "Platform settings",
      icon: Settings,
      to: "/settings",
      match: ["/settings"],
      divider: true,
    },
  ];
}

/** Whether a rail entry (or a sidebar link) is the current page. */
export function isActive(
  pathname: string,
  match: string[],
  exact?: boolean,
): boolean {
  const path = pathname.replace(/\/$/, "");
  return match.some((m) =>
    exact ? path === m : path === m || path.startsWith(m + "/"),
  );
}
