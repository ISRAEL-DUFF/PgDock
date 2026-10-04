import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Command } from "cmdk";
import { Box, Keyboard, LogOut, Moon, Plus, Search } from "lucide-react";
import { useEffect, type ReactNode } from "react";
import { api } from "../../api/client";
import { clearCurrentOrg, useCurrentOrg } from "../../lib/org";
import { nextTheme, useTheme } from "../../lib/theme";
import type { RailItem } from "./nav";

const item =
  "flex cursor-default items-center gap-2.5 rounded-md px-2.5 py-2 text-[13px] text-fg-light outline-none data-[selected=true]:bg-surface-3 data-[selected=true]:text-fg";

/** ⌘K: jump to a project or a page, or run an action. */
export function CommandMenu({
  open,
  onOpenChange,
  pages,
  onShortcuts,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  pages: RailItem[];
  onShortcuts: () => void;
}) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { org } = useCurrentOrg();
  const [theme, setTheme] = useTheme();
  const projects = useQuery({
    queryKey: ["projects", org?.id],
    queryFn: () => api.projects(org!.id),
    enabled: open && !!org,
  });

  useEffect(() => {
    const down = (e: KeyboardEvent) => {
      if (e.key.toLowerCase() === "k" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        onOpenChange(!open);
      }
    };
    document.addEventListener("keydown", down);
    return () => document.removeEventListener("keydown", down);
  }, [open, onOpenChange]);

  const go = (to: string) => {
    onOpenChange(false);
    void navigate({ to });
  };
  const run = (f: () => void) => {
    onOpenChange(false);
    f();
  };

  return (
    <Command.Dialog
      open={open}
      onOpenChange={onOpenChange}
      label="Search"
      overlayClassName="fixed inset-0 z-40 bg-black/60"
      contentClassName="fixed top-[15vh] left-1/2 z-50 w-[min(40rem,calc(100vw-2rem))] -translate-x-1/2 overflow-hidden rounded-lg border border-line-strong bg-surface text-fg shadow-2xl"
    >
      <div className="flex items-center gap-2 border-b border-line px-3">
        <Search className="h-4 w-4 text-muted" />
        <Command.Input
          placeholder="Search projects and pages, or run a command..."
          className="h-11 flex-1 bg-transparent text-[14px] outline-none placeholder:text-muted"
        />
      </div>
      <Command.List className="max-h-[50vh] overflow-y-auto p-2">
        <Command.Empty className="px-2 py-6 text-center text-[13px] text-muted">No results.</Command.Empty>
        {pages.length > 0 && (
          <Group heading="Pages">
            {pages.map((p) => (
              <Command.Item key={p.key} value={`page ${p.label}`} onSelect={() => go(p.to)} className={item}>
                <p.icon className="h-4 w-4" strokeWidth={1.6} />
                {p.label}
              </Command.Item>
            ))}
          </Group>
        )}
        {(projects.data?.items.length ?? 0) > 0 && (
          <Group heading="Projects">
            {projects.data!.items.map((p) => (
              <Command.Item key={p.id} value={`project ${p.name} ${p.id}`} onSelect={() => go(`/projects/${p.id}`)} className={item}>
                <Box className="h-4 w-4" strokeWidth={1.6} />
                {p.name}
                {p.parent_project_id && <span className="text-xs text-muted">branch</span>}
              </Command.Item>
            ))}
          </Group>
        )}
        <Group heading="Actions">
          <Command.Item value="new project" onSelect={() => go("/projects/new")} className={item}>
            <Plus className="h-4 w-4" strokeWidth={1.6} />
            New project
          </Command.Item>
          <Command.Item value="toggle theme" onSelect={() => run(() => setTheme(nextTheme(theme)))} className={item}>
            <Moon className="h-4 w-4" strokeWidth={1.6} />
            Switch theme (now {theme})
          </Command.Item>
          <Command.Item value="keyboard shortcuts" onSelect={() => run(onShortcuts)} className={item}>
            <Keyboard className="h-4 w-4" strokeWidth={1.6} />
            Keyboard shortcuts
            <kbd className="ml-auto font-sans text-[11px] text-muted">?</kbd>
          </Command.Item>
          <Command.Item
            value="sign out"
            onSelect={() =>
              run(() => {
                void api
                  .logout()
                  .catch(() => {})
                  .then(() => {
                    qc.clear();
                    clearCurrentOrg();
                    void navigate({ to: "/login" });
                  });
              })
            }
            className={item}
          >
            <LogOut className="h-4 w-4" strokeWidth={1.6} />
            Sign out
          </Command.Item>
        </Group>
      </Command.List>
    </Command.Dialog>
  );
}

function Group({ heading, children }: { heading: string; children: ReactNode }) {
  return (
    <Command.Group
      heading={heading}
      className="mb-1 [&_[cmdk-group-heading]]:px-2.5 [&_[cmdk-group-heading]]:py-1.5 [&_[cmdk-group-heading]]:text-[11px] [&_[cmdk-group-heading]]:tracking-wider [&_[cmdk-group-heading]]:text-muted [&_[cmdk-group-heading]]:uppercase"
    >
      {children}
    </Command.Group>
  );
}
