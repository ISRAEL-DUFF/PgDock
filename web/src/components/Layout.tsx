import { Link } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Monitor, Moon, Sun } from "lucide-react";
import { useState } from "react";
import { api, errorMessage, type Terms } from "../api/client";
import { useCurrentOrg } from "../lib/org";
import { refreshSession } from "../lib/session";
import { nextTheme, useTheme } from "../lib/theme";
import { Logo, LogoMark } from "./shell/Logo";
import { Alert, Button, cx } from "./ui";

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

/**
 * The sign-in pages' layout, as Supabase's: the form on the left under the
 * logo, a quiet brand panel on the right on wide screens.
 */
export function AuthShell({ children, wide }: { children: React.ReactNode; wide?: boolean }) {
  const [theme, setTheme] = useTheme();
  const Icon = theme === "light" ? Sun : theme === "dark" ? Moon : Monitor;
  return (
    <div className="flex min-h-screen bg-bg">
      <div className="flex flex-1 flex-col border-line px-6 py-6 lg:max-w-[36rem] lg:border-r lg:bg-surface lg:px-10">
        <header className="flex items-center justify-between">
          <Logo />
          <button
            type="button"
            onClick={() => setTheme(nextTheme(theme))}
            aria-label={`Theme: ${theme}`}
            title={`Theme: ${theme}`}
            className="rounded-md p-1.5 text-muted hover:bg-surface-2 hover:text-fg"
          >
            <Icon className="h-4 w-4" strokeWidth={1.6} />
          </button>
        </header>
        <main className="flex flex-1 items-center justify-center py-10">
          <div className={cx("w-full", wide ? "max-w-xl" : "max-w-sm")}>{children}</div>
        </main>
      </div>
      <aside className="relative hidden flex-1 items-center justify-center overflow-hidden lg:flex" aria-hidden>
        <div className="absolute inset-0 bg-[radial-gradient(ellipse_at_30%_20%,var(--accent-soft),transparent_60%)]" />
        <div className="relative max-w-md px-10">
          <LogoMark className="mb-6 h-12 w-12" />
          <p className="text-2xl leading-snug font-medium tracking-tight">Postgres you run yourself, with the console you already know.</p>
          <p className="mt-3 text-sm text-muted">Projects, branches, backups and a table editor, on your own servers.</p>
        </div>
      </aside>
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
