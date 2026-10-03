import { QueryClient } from "@tanstack/react-query";
import { createRootRouteWithContext, createRoute, createRouter, Outlet, redirect } from "@tanstack/react-router";
import { AppLayout } from "./components/Layout";
import { ToastProvider } from "./components/Toasts";
import { sessionQuery } from "./lib/session";
import { AlertsPage } from "./pages/Alerts";
import { AccountPage } from "./pages/Account";
import { DevicePage } from "./pages/Device";
import { AdminUsersPage } from "./pages/AdminUsers";
import { AdminOrgPage, AdminOrgsPage, AdminPlansPage, DedicatedRequestsPage } from "./pages/AdminOrgs";
import { UsagePage } from "./pages/Usage";
import { AuditPage, OrgAuditPage } from "./pages/Audit";
import { OrgMembersPage, OrgSettingsPage } from "./pages/Org";
import { ProjectMembersPage } from "./pages/ProjectMembers";
import { InvitePage, ResetPasswordPage, SignupPage, VerifyEmailPage } from "./pages/Public";
import { LoginPage } from "./pages/Login";
import { NewProjectPage } from "./pages/NewProject";
import { OperationDetailPage, OperationsPage } from "./pages/Operations";
import { ImportProjectPage } from "./pages/ImportProject";
import { NodeDetailPage, NodesPage } from "./pages/Nodes";
import { ProjectBackupsPage } from "./pages/ProjectBackups";
import { ProjectBranchesPage } from "./pages/ProjectBranches";
import { ProjectJobsPage } from "./pages/ProjectJobs";
import { ProjectWebhooksPage } from "./pages/ProjectWebhooks";
import { ProjectConnectPage } from "./pages/ProjectConnect";
import { ProjectLayout, ProjectOverviewPage } from "./pages/ProjectOverview";
import { ProjectMetricsPage } from "./pages/ProjectMetrics";
import { ProjectSettingsPage } from "./pages/ProjectSettings";
import { ProjectSqlPage } from "./pages/ProjectSql";
import { ProjectTablesPage } from "./pages/ProjectTables";
import { ProjectsPage } from "./pages/Projects";
import { SettingsPage } from "./pages/Settings";
import { SetupPage } from "./pages/Setup";
import { NotFound } from "./pages/NotFound";

type Ctx = { queryClient: QueryClient };

const root = createRootRouteWithContext<Ctx>()({
  component: () => (
    <ToastProvider>
      <Outlet />
    </ToastProvider>
  ),
  notFoundComponent: NotFound,
});

// Every navigation checks the session: first-run setup, then sign-in.
const loadSession = (qc: QueryClient) => qc.ensureQueryData(sessionQuery);

const setup = createRoute({
  getParentRoute: () => root,
  path: "/setup",
  component: SetupPage,
});

const login = createRoute({
  getParentRoute: () => root,
  path: "/login",
  validateSearch: (s: Record<string, unknown>): { next?: string } => ({
    next: typeof s.next === "string" && s.next.startsWith("/") && !s.next.startsWith("//") ? s.next : undefined,
  }),
  beforeLoad: async ({ context }) => {
    const s = await loadSession(context.queryClient);
    if (s.setup_required) throw redirect({ to: "/setup" });
    if (s.authenticated) throw redirect({ to: "/projects" });
  },
  component: LoginPage,
});

const token = (s: Record<string, unknown>): { token?: string } => ({ token: typeof s.token === "string" ? s.token : undefined });

// Public pages: they work signed in or not.
const signup = createRoute({ getParentRoute: () => root, path: "/signup", component: SignupPage });
const verifyEmail = createRoute({ getParentRoute: () => root, path: "/verify-email", validateSearch: token, component: VerifyEmailPage });
const resetPassword = createRoute({ getParentRoute: () => root, path: "/reset-password", validateSearch: token, component: ResetPasswordPage });
const invite = createRoute({ getParentRoute: () => root, path: "/invite", validateSearch: token, component: InvitePage });

const app = createRoute({
  getParentRoute: () => root,
  id: "app",
  beforeLoad: async ({ context, location }) => {
    const s = await loadSession(context.queryClient);
    if (s.setup_required) throw redirect({ to: "/setup" });
    if (!s.authenticated) throw redirect({ to: "/login", search: { next: location.href } });
  },
  component: AppLayout,
});

const index = createRoute({
  getParentRoute: () => app,
  path: "/",
  beforeLoad: () => {
    throw redirect({ to: "/projects" });
  },
});

const projects = createRoute({ getParentRoute: () => app, path: "/projects", component: ProjectsPage });
const newProject = createRoute({ getParentRoute: () => app, path: "/projects/new", component: NewProjectPage });
const importProject = createRoute({ getParentRoute: () => app, path: "/projects/import", component: ImportProjectPage });
const project = createRoute({ getParentRoute: () => app, path: "/projects/$id", component: ProjectLayout });
const projectOverview = createRoute({ getParentRoute: () => project, path: "/", component: ProjectOverviewPage });
const projectConnect = createRoute({ getParentRoute: () => project, path: "/connect", component: ProjectConnectPage });
const projectSql = createRoute({ getParentRoute: () => project, path: "/sql", component: ProjectSqlPage });
const projectTables = createRoute({ getParentRoute: () => project, path: "/tables", component: ProjectTablesPage });
const projectMetrics = createRoute({ getParentRoute: () => project, path: "/metrics", component: ProjectMetricsPage });
const projectBackups = createRoute({ getParentRoute: () => project, path: "/backups", component: ProjectBackupsPage });
const projectBranches = createRoute({ getParentRoute: () => project, path: "/branches", component: ProjectBranchesPage });
const projectWebhooks = createRoute({ getParentRoute: () => project, path: "/webhooks", component: ProjectWebhooksPage });
const projectJobs = createRoute({ getParentRoute: () => project, path: "/jobs", component: ProjectJobsPage });
const projectSettings = createRoute({ getParentRoute: () => project, path: "/settings", component: ProjectSettingsPage });
const projectMembers = createRoute({ getParentRoute: () => project, path: "/members", component: ProjectMembersPage });
const account = createRoute({ getParentRoute: () => app, path: "/account", component: AccountPage });
const device = createRoute({
  getParentRoute: () => app,
  path: "/device",
  validateSearch: (s: Record<string, unknown>): { code?: string } => ({ code: typeof s.code === "string" && s.code ? s.code : undefined }),
  component: DevicePage,
});
const orgMembers = createRoute({ getParentRoute: () => app, path: "/org/members", component: OrgMembersPage });
const orgSettings = createRoute({ getParentRoute: () => app, path: "/org/settings", component: OrgSettingsPage });
const orgAudit = createRoute({ getParentRoute: () => app, path: "/org/audit", component: OrgAuditPage });
const adminUsers = createRoute({ getParentRoute: () => app, path: "/admin/users", component: AdminUsersPage });
const nodes = createRoute({ getParentRoute: () => app, path: "/nodes", component: NodesPage });
const nodeDetail = createRoute({ getParentRoute: () => app, path: "/nodes/$id", component: NodeDetailPage });
const operations = createRoute({ getParentRoute: () => app, path: "/operations", component: OperationsPage });
const operation = createRoute({ getParentRoute: () => app, path: "/operations/$id", component: OperationDetailPage });
const alertsRoute = createRoute({ getParentRoute: () => app, path: "/alerts", component: AlertsPage });
const audit = createRoute({ getParentRoute: () => app, path: "/audit", component: AuditPage });
const settings = createRoute({ getParentRoute: () => app, path: "/settings", component: SettingsPage });
const orgUsage = createRoute({ getParentRoute: () => app, path: "/org/usage", component: UsagePage });
const adminOrgs = createRoute({ getParentRoute: () => app, path: "/admin/orgs", component: AdminOrgsPage });
const adminOrg = createRoute({ getParentRoute: () => app, path: "/admin/orgs/$id", component: AdminOrgPage });
const adminPlans = createRoute({ getParentRoute: () => app, path: "/admin/plans", component: AdminPlansPage });
const adminRequests = createRoute({ getParentRoute: () => app, path: "/admin/dedicated-requests", component: DedicatedRequestsPage });

const routeTree = root.addChildren([
  setup,
  login,
  signup,
  verifyEmail,
  resetPassword,
  invite,
  app.addChildren([
    index,
    projects,
    newProject,
    importProject,
    project.addChildren([projectOverview, projectConnect, projectSql, projectTables, projectBackups,
    projectBranches, projectWebhooks, projectJobs, projectMetrics, projectMembers, projectSettings]),
    account,
    device,
    orgMembers,
    orgSettings,
    orgAudit,
    adminUsers,
    nodes,
    nodeDetail,
    operations,
    operation,
    alertsRoute,
    audit,
    settings,
    orgUsage,
    adminOrgs,
    adminOrg,
    adminPlans,
    adminRequests,
  ]),
]);

export function makeRouter(queryClient: QueryClient) {
  return createRouter({ routeTree, context: { queryClient }, defaultPreload: "intent", defaultPreloadStaleTime: 0 });
}

declare module "@tanstack/react-router" {
  interface Register {
    router: ReturnType<typeof makeRouter>;
  }
}
