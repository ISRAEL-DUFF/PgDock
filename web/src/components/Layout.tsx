import { Link, Outlet, useNavigate } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, errorMessage, type Terms } from "../api/client";
import { canManageOrg, clearCurrentOrg, setCurrentOrg, useCurrentOrg } from "../lib/org";
import { refreshSession, sessionQuery } from "../lib/session";
import { applyTheme, loadTheme, nextTheme, type Theme } from "../lib/theme";
import { Alert, Button, cx, Field, Input, Modal, Select } from "./ui";

type NavItem = { to: string; label: string };

/** The organisation's pages, then the platform admin's (V2 §13). */
function navFor(platformAdmin: boolean, manager: boolean): { title?: string; items: NavItem[] }[] {
  const org: NavItem[] = [
    { to: "/projects", label: "Projects" },
    { to: "/operations", label: "Operations" },
    { to: "/org/members", label: "Members" },
  ];
  if (manager)
    org.push({ to: "/org/usage", label: "Usage & quotas" }, { to: "/org/settings", label: "Organisation" }, { to: "/org/audit", label: "Audit log" });
  const out: { title?: string; items: NavItem[] }[] = [{ items: org }];
  if (platformAdmin) {
    out.push({
      title: "Platform",
      items: [
        { to: "/nodes", label: "Nodes" },
        { to: "/alerts", label: "Alerts" },
        { to: "/admin/orgs", label: "Organisations" },
        { to: "/admin/users", label: "Users" },
        { to: "/admin/plans", label: "Plans" },
        { to: "/admin/dedicated-requests", label: "Dedicated requests" },
        { to: "/settings", label: "Platform settings" },
        { to: "/audit", label: "Platform audit" },
      ],
    });
  }
  return out;
}

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
  const platformAdmin = session?.user?.platform_role === "platform_admin";
  const { org, orgs } = useCurrentOrg();
  const firing = useQuery({
    queryKey: ["alerts", "count"],
    queryFn: () => api.alerts("firing"),
    refetchInterval: 30_000,
    retry: false,
    enabled: platformAdmin,
  });

  const logout = async () => {
    await api.logout().catch(() => {});
    qc.clear();
    clearCurrentOrg();
    await navigate({ to: "/login" });
  };

  if (session?.terms_required) return <TermsGate version={session.terms_required} />;

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
        <div className={cx("px-3 pb-2 md:block", menu ? "block" : "hidden")}>
          <OrgSwitcher />
        </div>
        <nav className={cx("flex-col gap-0.5 px-2 pb-3 md:flex", menu ? "flex" : "hidden")}>
          {navFor(platformAdmin, canManageOrg(org)).map((group, gi) => (
            <div key={gi} className="flex flex-col gap-0.5">
              {group.title && <div className="mt-3 px-3 pb-1 text-xs font-semibold uppercase tracking-wide text-muted">{group.title}</div>}
              {group.items.map((n) => (
                <Link
                  key={n.to}
                  to={n.to}
                  onClick={() => setMenu(false)}
                  className="rounded-md px-3 py-1.5 text-sm text-muted hover:bg-surface-2 hover:text-fg"
                  activeProps={{ className: "bg-surface-2 !text-fg font-medium" }}
                >
                  <span className="flex items-center justify-between">
                    {n.label}
                    {n.to === "/alerts" && (firing.data?.firing ?? 0) > 0 && (
                      <span
                        data-testid="alerts-count"
                        className={cx(
                          "rounded-full px-1.5 text-xs font-semibold",
                          (firing.data?.critical ?? 0) > 0 ? "bg-danger text-white" : "bg-warn text-white",
                        )}
                      >
                        {firing.data?.firing}
                      </span>
                    )}
                  </span>
                </Link>
              ))}
            </div>
          ))}
        </nav>
      </aside>
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex items-center justify-end gap-2 border-b border-line px-4 py-2">
          <InvitationsIndicator />
          <ThemeToggle />
          <Link to="/account" className="hidden text-xs text-muted hover:text-fg sm:inline" data-testid="account-link">
            {session?.user?.name || session?.user?.email}
          </Link>
          <Button variant="ghost" className="text-xs" onClick={logout}>
            Sign out
          </Button>
        </header>
        <main className="mx-auto w-full max-w-6xl flex-1 px-4 py-6">
          {platformAdmin && <BackupBanner />}
          <OrgBanners />
          {orgs.length > 0 && org === undefined ? null : <Outlet />}
        </main>
      </div>
    </div>
  );
}

/** Switches the organisation the pages work in; personal first (V2 §13). */
function OrgSwitcher() {
  const { org, orgs } = useCurrentOrg();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  if (!org) return null;
  const create = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      const o = await api.createOrg(name.trim());
      await qc.invalidateQueries({ queryKey: ["orgs"] });
      setCurrentOrg(o.id);
      setCreating(false);
      setName("");
      await navigate({ to: "/projects" });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <Select
        aria-label="Organisation"
        data-testid="org-switcher"
        className="w-full"
        value={org.id}
        onChange={(e) => {
          if (e.target.value === "__new") {
            setCreating(true);
            return;
          }
          setCurrentOrg(e.target.value);
          void qc.invalidateQueries();
          void navigate({ to: "/projects" });
        }}
      >
        {orgs.map((o) => (
          <option key={o.id} value={o.id}>
            {o.name}
            {o.personal ? " (personal)" : ""}
          </option>
        ))}
        <option value="__new">+ New organisation…</option>
      </Select>
      <Modal title="New organisation" open={creating} onClose={() => setCreating(false)}>
        <form className="flex flex-col gap-4" onSubmit={create}>
          <p className="text-sm text-muted">An organisation holds projects and the people who work on them. You become its owner.</p>
          <Field label="Name">
            {(id) => <Input id={id} required maxLength={64} value={name} onChange={(e) => setName(e.target.value)} autoFocus />}
          </Field>
          {err && <Alert>{err}</Alert>}
          <div className="flex justify-end gap-2">
            <Button onClick={() => setCreating(false)}>Cancel</Button>
            <Button type="submit" variant="primary" busy={busy} disabled={!name.trim()}>
              Create
            </Button>
          </div>
        </form>
      </Modal>
    </>
  );
}

/** Pending invitations, shown in the header (V2 §13). */
function InvitationsIndicator() {
  const q = useQuery({ queryKey: ["me", "invitations"], queryFn: api.myInvitations, refetchInterval: 60_000, retry: false });
  const n = q.data?.items.length ?? 0;
  if (n === 0) return null;
  return (
    <Link to="/account" hash="invitations" className="rounded-full bg-accent px-2 py-0.5 text-xs font-medium text-accent-fg" data-testid="invitations-indicator">
      {n} invitation{n === 1 ? "" : "s"}
    </Link>
  );
}

/** The current terms, which the user accepts before anything else. */
export function TermsGate({ version }: { version: number }) {
  const q = useQuery({ queryKey: ["terms"], queryFn: api.terms });
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const accept = async () => {
    setBusy(true);
    setErr(null);
    try {
      await api.acceptTerms(version);
      await refreshSession(qc);
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <AuthShell wide>
      <div className="flex flex-col gap-4">
        <h1 className="text-lg font-semibold">The terms of use have changed</h1>
        <p className="text-sm text-muted">Read and accept version {version} to keep using PGDock.</p>
        {q.data && <TermsText terms={q.data} />}
        {err && <Alert>{err}</Alert>}
        <Button variant="primary" busy={busy} onClick={accept} data-testid="accept-terms">
          I accept
        </Button>
      </div>
    </AuthShell>
  );
}

/** The terms of use and privacy notice, as published. */
export function TermsText({ terms }: { terms: Terms }) {
  return (
    <div className="max-h-80 overflow-y-auto rounded-md border border-line bg-surface-2 p-3 text-xs whitespace-pre-wrap" data-testid="terms-text">
      {terms.terms_md}
      {"\n\n"}
      {terms.privacy_md}
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
export function BackupBanner() {
  const q = useQuery({ queryKey: ["backups", "overview"], queryFn: () => api.backupOverview(), refetchInterval: 60_000, retry: false });
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

/**
 * The organisation's state, shown to everyone in it (V2 §13 banners): a
 * suspension and its reason, a pending deletion (owners can cancel), and
 * open break-glass sessions (owners can end them).
 */
export function OrgBanners() {
  const { org } = useCurrentOrg();
  const qc = useQueryClient();
  const [err, setErr] = useState<string | null>(null);
  if (!org) return null;
  const owner = org.role === "owner";
  const act = async (f: () => Promise<unknown>) => {
    setErr(null);
    try {
      await f();
      await qc.invalidateQueries();
    } catch (e) {
      setErr(errorMessage(e));
    }
  };
  const banner = "mb-4 flex flex-wrap items-center justify-between gap-2 rounded-md border px-3 py-2 text-sm";
  return (
    <>
      {err && (
        <div className="mb-4">
          <Alert>{err}</Alert>
        </div>
      )}
      {org.status === "suspended" && (
        <div className={cx(banner, "border-danger/40 bg-danger/10 text-danger")} role="alert" data-testid="suspended-banner">
          <span>
            <strong>{org.name} is suspended</strong>
            {org.suspended_reason ? `: ${org.suspended_reason}` : ""}. Its databases are offline and its data is kept; you can look around but not change anything.
          </span>
        </div>
      )}
      {org.status === "deleting" && (
        <div className={cx(banner, "border-danger/40 bg-danger/10 text-danger")} role="alert" data-testid="deleting-banner">
          <span>
            <strong>{org.name} will be deleted</strong>
            {org.delete_after ? ` on ${new Date(org.delete_after).toLocaleString()}` : ""}. Its databases are offline.
          </span>
          {owner && (
            <Button className="text-xs" onClick={() => act(() => api.cancelOrgDeletion(org.id))} data-testid="cancel-deletion">
              Cancel the deletion
            </Button>
          )}
        </div>
      )}
      {(org.break_glass ?? []).map((b) => (
        <div key={b.id} className={cx(banner, "border-warn/40 bg-warn/10 text-warn")} role="status" data-testid="break-glass-banner">
          <span>
            <strong>Break-glass access:</strong> the platform admin ({b.admin_email}) can act as an admin here until {new Date(b.expires_at).toLocaleString()}. Reason:{" "}
            {b.reason}
          </span>
          {owner && (
            <Button className="text-xs" onClick={() => act(() => api.endBreakGlass(org.id, b.id))} data-testid="end-break-glass">
              End the session
            </Button>
          )}
        </div>
      ))}
    </>
  );
}
