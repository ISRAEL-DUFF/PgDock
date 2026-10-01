import { QueryClient } from "@tanstack/react-query";
import { createRootRouteWithContext, createRoute, createRouter, Outlet, redirect } from "@tanstack/react-router";
import { AppLayout } from "./components/Layout";
import { ToastProvider } from "./components/Toasts";
import { sessionQuery } from "./lib/session";
import { AuditPage } from "./pages/Audit";
import { LoginPage } from "./pages/Login";
import { NewProjectPage } from "./pages/NewProject";
import { OperationDetailPage, OperationsPage } from "./pages/Operations";
import { ProjectConnectPage } from "./pages/ProjectConnect";
import { ProjectLayout, ProjectOverviewPage } from "./pages/ProjectOverview";
import { ProjectSettingsPage } from "./pages/ProjectSettings";
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
const project = createRoute({ getParentRoute: () => app, path: "/projects/$id", component: ProjectLayout });
const projectOverview = createRoute({ getParentRoute: () => project, path: "/", component: ProjectOverviewPage });
const projectConnect = createRoute({ getParentRoute: () => project, path: "/connect", component: ProjectConnectPage });
const projectSettings = createRoute({ getParentRoute: () => project, path: "/settings", component: ProjectSettingsPage });
const operations = createRoute({ getParentRoute: () => app, path: "/operations", component: OperationsPage });
const operation = createRoute({ getParentRoute: () => app, path: "/operations/$id", component: OperationDetailPage });
const audit = createRoute({ getParentRoute: () => app, path: "/audit", component: AuditPage });
const settings = createRoute({ getParentRoute: () => app, path: "/settings", component: SettingsPage });

const routeTree = root.addChildren([
  setup,
  login,
  app.addChildren([
    index,
    projects,
    newProject,
    project.addChildren([projectOverview, projectConnect, projectSettings]),
    operations,
    operation,
    audit,
    settings,
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
