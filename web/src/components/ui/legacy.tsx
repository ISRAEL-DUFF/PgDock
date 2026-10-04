import { twMerge } from "tailwind-merge";
import { useEffect, useId, useState, type ButtonHTMLAttributes, type InputHTMLAttributes, type ReactNode } from "react";

/** Joins class names; later Tailwind classes win over earlier ones, so a
 * caller's className overrides a component's defaults. */
export function cx(...parts: (string | false | null | undefined)[]): string {
  return twMerge(parts.filter(Boolean).join(" "));
}

type ButtonVariant = "primary" | "secondary" | "default" | "danger" | "warning" | "ghost";
type ButtonSize = "tiny" | "small" | "medium";

const buttonVariants: Record<ButtonVariant, string> = {
  primary: "bg-accent text-accent-fg border-accent-strong hover:bg-accent-strong",
  secondary: "bg-surface-2 text-fg border-line-strong hover:bg-surface-3",
  default: "bg-surface-2 text-fg border-line-strong hover:bg-surface-3",
  danger: "bg-danger text-white border-danger-strong hover:bg-danger-strong",
  warning: "bg-warn/15 text-warn-text border-warn/40 hover:bg-warn/25",
  ghost: "bg-transparent text-fg-light border-transparent hover:text-fg hover:bg-surface-2",
};

const buttonSizes: Record<ButtonSize, string> = {
  tiny: "h-[26px] px-2.5 text-xs gap-1.5",
  small: "h-[30px] px-3 text-[13px] gap-2",
  medium: "h-[34px] px-4 text-sm gap-2",
};

export function Button({
  variant = "secondary",
  size = "small",
  icon,
  shortcut,
  busy,
  className,
  children,
  disabled,
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: ButtonVariant;
  size?: ButtonSize;
  /** A leading icon (lucide). */
  icon?: ReactNode;
  /** A keyboard hint shown after the label, e.g. "⌘↵". */
  shortcut?: string;
  busy?: boolean;
}) {
  return (
    <button
      type="button"
      className={cx(
        "inline-flex shrink-0 items-center justify-center rounded-md border font-medium whitespace-nowrap",
        "transition-colors disabled:cursor-not-allowed disabled:opacity-50",
        buttonSizes[size],
        buttonVariants[variant],
        className,
      )}
      disabled={disabled || busy}
      {...rest}
    >
      {busy ? <Spinner /> : icon}
      {children}
      {shortcut && <kbd className="font-sans text-[11px] font-normal">{shortcut}</kbd>}
    </button>
  );
}

export function Spinner({ className }: { className?: string }) {
  return <span aria-hidden className={cx("inline-block h-3.5 w-3.5 animate-spin rounded-full border-2 border-current border-r-transparent", className)} />;
}

export function Field({ label, hint, error, children }: { label: string; hint?: ReactNode; error?: string | null; children: (id: string) => ReactNode }) {
  const id = useId();
  return (
    <div className="flex flex-col gap-1">
      <label htmlFor={id} className="text-[13px] text-fg-light">
        {label}
      </label>
      {children(id)}
      {hint && !error && <p className="text-xs text-muted">{hint}</p>}
      {error && <p className="text-xs text-danger-text">{error}</p>}
    </div>
  );
}

export function Input({ className, ...rest }: InputHTMLAttributes<HTMLInputElement>) {
  return (
    <input
      className={cx(
        "h-[30px] w-full rounded-md border border-line-strong bg-surface-2 px-2.5 text-[13px] text-fg placeholder:text-muted",
        "focus:border-accent focus:ring-2 focus:ring-accent-soft focus:outline-none",
        className,
      )}
      {...rest}
    />
  );
}

const tones = {
  ok: "border-ok/30 text-ok-text bg-ok/10",
  warn: "border-warn/30 text-warn-text bg-warn/10",
  danger: "border-danger/30 text-danger-text bg-danger/10",
  accent: "border-accent/30 text-accent-text bg-accent-soft",
  muted: "border-line-strong text-fg-light bg-surface-2",
} as const;

export type Tone = keyof typeof tones;

export function Badge({ tone = "muted", children }: { tone?: Tone; children: ReactNode }) {
  return <span className={cx("inline-flex h-5 items-center rounded-full border px-2 text-[11px] font-medium whitespace-nowrap", tones[tone])}>{children}</span>;
}

const statusTones: Record<string, Tone> = {
  active: "ok",
  healthy: "ok",
  succeeded: "ok",
  ok: "ok",
  success: "ok",
  provisioning: "accent",
  running: "accent",
  queued: "muted",
  pending: "accent",
  promoting: "accent",
  demoting: "accent",
  restoring: "accent",
  deleting: "warn",
  denied: "warn",
  error: "danger",
  failed: "danger",
  failure: "danger",
};

export function StatusBadge({ status }: { status: string }) {
  const tone = statusTones[status] ?? "muted";
  return (
    <Badge tone={tone}>
      {(status === "running" || status === "provisioning" || status === "deleting") && <Spinner className="mr-1 h-2.5 w-2.5" />}
      {status}
    </Badge>
  );
}

/** A long-lived resource's state (instances, nodes): no progress spinner. */
export function StateBadge({ state }: { state: string }) {
  const tone: Tone = state === "running" || state === "healthy" ? "ok" : state === "stopped" ? "warn" : (statusTones[state] ?? "muted");
  return <Badge tone={tone}>{state}</Badge>;
}

export function Alert({ tone = "danger", title, children }: { tone?: Tone; title?: string; children: ReactNode }) {
  return (
    <div role={tone === "danger" ? "alert" : "status"} className={cx("rounded-md border px-3 py-2 text-[13px]", tones[tone])}>
      {title && <p className="font-semibold">{title}</p>}
      <div className={title ? "mt-0.5" : undefined}>{children}</div>
    </div>
  );
}

export function CopyButton({ value, label = "Copy" }: { value: string; label?: string }) {
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const t = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(t);
  }, [copied]);
  return (
    <Button
      variant="default"
      size="tiny"
      onClick={async () => {
        await navigator.clipboard.writeText(value);
        setCopied(true);
      }}
      aria-label={`${label}: copied ${copied ? "yes" : "no"}`}
    >
      {copied ? "Copied" : label}
    </Button>
  );
}

/** A monospace value with a copy button; secret values are masked until revealed. */
export function CopyField({ label, value, secret, testId }: { label: string; value: string; secret?: boolean; testId?: string }) {
  const [shown, setShown] = useState(!secret);
  return (
    <div className="flex flex-col gap-1">
      <span className="text-xs font-medium text-muted">{label}</span>
      <div className="flex items-center gap-2">
        <code
          data-testid={testId}
          className="min-w-0 flex-1 truncate rounded-md border border-line-strong bg-surface-2 px-2.5 py-1.5 font-mono text-xs"
          title={shown ? value : undefined}
        >
          {shown ? value : "•".repeat(Math.min(value.length, 32))}
        </code>
        {secret && (
          <Button variant="ghost" size="tiny" onClick={() => setShown((s) => !s)}>
            {shown ? "Hide" : "Show"}
          </Button>
        )}
        <CopyButton value={value} />
      </div>
    </div>
  );
}

export function CodeBlock({ code }: { code: string }) {
  return (
    <div className="relative">
      <pre tabIndex={0} className="overflow-x-auto rounded-md border border-line bg-code p-3 pr-20 font-mono text-xs leading-relaxed">{code}</pre>
      <div className="absolute top-2 right-2">
        <CopyButton value={code} />
      </div>
    </div>
  );
}

/** Nothing here yet: what it is, why it's empty, and what to do. */
export function EmptyState({
  title,
  icon,
  action,
  children,
}: {
  title: string;
  /** A lucide icon, shown in a tile above the title. */
  icon?: ReactNode;
  /** The button that fills the list. */
  action?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <div className="flex flex-col items-center rounded-md border border-dashed border-line-strong px-6 py-10 text-center" data-testid="empty-state">
      {icon && (
        <div className="mb-3 flex h-10 w-10 items-center justify-center rounded-md border border-line bg-surface-2 text-muted [&_svg]:h-5 [&_svg]:w-5">
          {icon}
        </div>
      )}
      <p className="text-[14px] text-fg">{title}</p>
      {children && <div className="mt-1 max-w-md text-[13px] text-muted">{children}</div>}
      {action && <div className="mt-4">{action}</div>}
    </div>
  );
}

export function Table({ head, children }: { head: ReactNode[]; children: ReactNode }) {
  return (
    <div tabIndex={0} className="overflow-x-auto rounded-md border border-line bg-surface">
      <table className="w-full text-left text-[13px]">
        <thead className="border-b border-line bg-surface-2 text-xs text-fg-light">
          <tr>
            {head.map((h, i) => (
              <th key={i} className="px-3 py-2 font-medium">
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y divide-line [&>tr:hover]:bg-surface-2">{children}</tbody>
      </table>
    </div>
  );
}

export function Select({ className, ...rest }: React.SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select
      className={cx("h-[30px] rounded-md border border-line-strong bg-surface-2 px-2 text-[13px] text-fg focus:border-accent focus:outline-none", className)}
      {...rest}
    />
  );
}
