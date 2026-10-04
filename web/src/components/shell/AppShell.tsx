import { useQuery } from "@tanstack/react-query";
import { Outlet, useRouterState } from "@tanstack/react-router";
import { useState } from "react";
import { api } from "../../api/client";
import { useCurrentOrg } from "../../lib/org";
import { sessionQuery } from "../../lib/session";
import { BackupBanner, OrgBanners, TermsGate } from "../Layout";
import { cx, Toaster, TooltipProvider } from "../ui";
import { CommandMenu } from "./CommandMenu";
import {
  contextFor,
  isActive,
  orgRail,
  platformRail,
  projectRail,
} from "./nav";
import {
  Rail,
  SectionSidebar,
  SidebarOpener,
  useSidebarCollapsed,
} from "./Rail";
import { TopBar } from "./TopBar";

/**
 * The signed-in app (docs/ui-redesign.md): a top bar, the icon rail, the
 * current section's sidebar, and the page, edge to edge.
 */
export function AppShell() {
  const { data: session } = useQuery(sessionQuery);
  const platformAdmin = session?.user?.platform_role === "platform_admin";
  const { org, orgs } = useCurrentOrg();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const ctx = contextFor(pathname);
  const project = useQuery({
    queryKey: ["project", ctx.kind === "project" ? ctx.projectId : ""],
    queryFn: () => api.project((ctx as { projectId: string }).projectId),
    enabled: ctx.kind === "project",
    retry: false,
  });
  const [collapsed, setCollapsed] = useSidebarCollapsed();
  const [search, setSearch] = useState(false);

  if (session?.terms_required)
    return <TermsGate version={session.terms_required} />;

  const rail =
    ctx.kind === "project"
      ? project.data
        ? projectRail(project.data)
        : []
      : ctx.kind === "platform"
        ? platformRail()
        : orgRail(org);
  const active = rail.find((r) => isActive(pathname, r.match, r.exact));
  const sidebar = active?.sidebar && !collapsed ? active : undefined;
  // The editors use the whole width; other pages read best narrower.
  const wide = ctx.kind === "project" && /\/(tables|sql)\/?$/.test(pathname);

  return (
    <TooltipProvider delayDuration={300}>
      <div className="flex h-screen flex-col bg-bg text-fg">
        <TopBar
          ctx={ctx}
          project={project.data}
          onSearch={() => setSearch(true)}
        />
        <div className="flex min-h-0 flex-1">
          <Rail
            items={rail}
            pathname={pathname}
            label={
              ctx.kind === "project"
                ? "Project"
                : ctx.kind === "platform"
                  ? "Platform"
                  : "Organisation"
            }
          />
          {sidebar && (
            <SectionSidebar
              item={sidebar}
              pathname={pathname}
              onCollapse={() => setCollapsed(true)}
            />
          )}
          <main
            className="relative min-w-0 flex-1 overflow-y-auto"
            data-testid="main"
          >
            {active?.sidebar && collapsed && (
              <SidebarOpener onOpen={() => setCollapsed(false)} />
            )}
            <div
              className={cx(
                // The table and SQL editors fill the page edge to edge,
                // as Studio's do.
                wide
                  ? "flex h-full flex-col"
                  : "mx-auto w-full max-w-6xl px-6 py-8 lg:px-10",
              )}
            >
              {platformAdmin && <BackupBanner />}
              {ctx.kind !== "platform" && <OrgBanners />}
              {orgs.length > 0 && org === undefined ? null : <Outlet />}
            </div>
          </main>
        </div>
      </div>
      <CommandMenu open={search} onOpenChange={setSearch} pages={rail} />
      <Toaster />
    </TooltipProvider>
  );
}
