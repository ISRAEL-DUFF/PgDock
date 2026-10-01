import { Link, Outlet, useNavigate } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import { sessionQuery } from "../lib/session";
import { applyTheme, loadTheme, nextTheme, type Theme } from "../lib/theme";
import { Button, cx } from "./ui";

const nav = [
  { to: "/projects", label: "Projects" },
  { to: "/nodes", label: "Nodes" },
  { to: "/operations", label: "Operations" },
  { to: "/audit", label: "Audit log" },
  { to: "/settings", label: "Settings" },
] as const;

export function Logo() {
  return (
    <span className="flex items-center gap-2 font-semibold">
      <svg viewBox="0 0 32 32" className="h-6 w-6" aria-hidden>
        <rect width="32" height="32" rx="7" fill="var(--accent)" />
        <path d="M9 23V9h7a5 5 0 0 1 0 10h-4v4z" fill="var(--accent-fg)" />
      </svg>
      PGDock
    </span>
  );
}

export function ThemeToggle() {
  const [theme, setTheme] = useState<Theme>(loadTheme);
  const label = theme === "system" ? "Theme: system" : theme === "dark" ? "Theme: dark" : "Theme: light";
  return (
    <Button
      variant="ghost"
      className="px-2 text-xs"
      onClick={() => {
        const t = nextTheme(theme);
        applyTheme(t);
        setTheme(t);
      }}
    >
      {label}
    </Button>
  );
}

export function AppLayout() {
  const { data: session } = useQuery(sessionQuery);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [menu, setMenu] = useState(false);

  const logout = async () => {
    await api.logout().catch(() => {});
    qc.clear();
    await navigate({ to: "/login" });
  };

  return (
    <div className="flex min-h-screen flex-col md:flex-row">
      <aside className="border-b border-line bg-surface md:w-56 md:shrink-0 md:border-r md:border-b-0">
        <div className="flex items-center justify-between px-4 py-3">
          <Link to="/projects">
            <Logo />
          </Link>
          <Button variant="ghost" className="md:hidden" onClick={() => setMenu((m) => !m)} aria-expanded={menu}>
            Menu
          </Button>
        </div>
        <nav className={cx("flex-col gap-0.5 px-2 pb-3 md:flex", menu ? "flex" : "hidden")}>
          {nav.map((n) => (
            <Link
              key={n.to}
              to={n.to}
              onClick={() => setMenu(false)}
              className="rounded-md px-3 py-1.5 text-sm text-muted hover:bg-surface-2 hover:text-fg"
              activeProps={{ className: "bg-surface-2 !text-fg font-medium" }}
            >
              {n.label}
            </Link>
          ))}
        </nav>
      </aside>
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex items-center justify-end gap-2 border-b border-line px-4 py-2">
          <ThemeToggle />
          <span className="hidden text-xs text-muted sm:inline">{session?.operator?.email}</span>
          <Button variant="ghost" className="text-xs" onClick={logout}>
            Sign out
          </Button>
        </header>
        <main className="mx-auto w-full max-w-6xl flex-1 px-4 py-6">
          <BackupBanner />
          <Outlet />
        </main>
      </div>
    </div>
  );
}

/** Centered card layout for login and setup. */
export function AuthShell({ children, wide }: { children: React.ReactNode; wide?: boolean }) {
  return (
    <div className="flex min-h-screen flex-col items-center justify-center gap-6 px-4 py-10">
      <Logo />
      <div className={cx("w-full rounded-lg border border-line bg-surface p-6", wide ? "max-w-xl" : "max-w-sm")}>{children}</div>
      <ThemeToggle />
    </div>
  );
}

/**
 * Warns while backups cannot run, or the backup key was never confirmed as
 * stored offline (spec §15: "a warning banner if never re-confirmed").
 */
function BackupBanner() {
  const q = useQuery({ queryKey: ["backups", "overview"], queryFn: api.backupOverview, refetchInterval: 60_000, retry: false });
  const o = q.data;
  if (!o) return null;
  let msg: string | null = null;
  let to: "/settings" | "/nodes" = "/settings";
  if (!o.storage_configured) msg = "Backups are off: no S3 storage is configured.";
  else if (!o.key.exists) msg = "Backups are off: no backup key yet.";
  else if (!o.key.confirmed_at) msg = "The backup key was never confirmed as stored offline. Without it, backups cannot be restored.";
  else if (!o.agent_available) {
    msg = "No agent is registered, so backups cannot run.";
    to = "/nodes";
  }
  if (!msg) return null;
  return (
    <div className="mb-4 flex flex-wrap items-center justify-between gap-2 rounded-md border border-warn/40 bg-warn/10 px-3 py-2 text-sm text-warn" role="status" data-testid="backup-banner">
      <span>{msg}</span>
      <Link to={to} className="font-medium underline">
        Fix it
      </Link>
    </div>
  );
}
