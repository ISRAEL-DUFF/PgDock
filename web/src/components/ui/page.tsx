import type { ReactNode } from "react";
import { cx } from "./legacy";

// Page building blocks in Supabase Studio's style (docs/ui-redesign.md,
// phase 4): a page heading, panels, settings rows, stat tiles and
// definition lists. Project, organisation and admin pages use these
// instead of the first-generation Card.

/** A page: its title, a line of description, actions on the right. */
export function Page({
  title,
  description,
  actions,
  children,
  testId,
  className,
}: {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  testId?: string;
  className?: string;
}) {
  return (
    <div className={cx("flex flex-col gap-6", className)} data-testid={testId}>
      <header className="flex flex-wrap items-end justify-between gap-3">
        <div className="min-w-0">
          <h1 className="truncate text-[22px] leading-tight">{title}</h1>
          {description && <p className="mt-1 max-w-3xl text-[13px] text-muted">{description}</p>}
        </div>
        {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
      </header>
      {children}
    </div>
  );
}

/** A titled group of panels on a page ("Configuration", "Danger zone"). */
export function Section({ title, description, actions, children, testId }: { title?: ReactNode; description?: ReactNode; actions?: ReactNode; children: ReactNode; testId?: string }) {
  return (
    <section className="flex flex-col gap-3" data-testid={testId}>
      {(title || actions) && (
        <div className="flex flex-wrap items-end justify-between gap-2">
          <div>
            {title && <h2 className="text-[15px]">{title}</h2>}
            {description && <p className="mt-0.5 text-[13px] text-muted">{description}</p>}
          </div>
          {actions && <div className="flex items-center gap-2">{actions}</div>}
        </div>
      )}
      {children}
    </section>
  );
}

/** A bordered panel: an optional header, content, and a footer of actions
 * on the right, as Studio's settings forms. */
export function Panel({
  title,
  description,
  actions,
  footer,
  children,
  tone,
  className,
  bodyClassName,
  testId,
}: {
  title?: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  footer?: ReactNode;
  children?: ReactNode;
  tone?: "danger" | "warn";
  className?: string;
  bodyClassName?: string;
  testId?: string;
}) {
  return (
    <section
      className={cx(
        "overflow-hidden rounded-md border bg-surface",
        tone === "danger" ? "border-danger/40" : tone === "warn" ? "border-warn/40" : "border-line",
        className,
      )}
      data-testid={testId}
    >
      {(title || actions) && (
        <header className={cx("flex flex-wrap items-center justify-between gap-3 border-b px-5 py-3", tone === "danger" ? "border-danger/30 bg-danger/5" : "border-line")}>
          <div className="min-w-0">
            {title && <h3 className="text-[14px]">{title}</h3>}
            {description && <p className="mt-0.5 text-[12px] text-muted">{description}</p>}
          </div>
          {actions && <div className="flex items-center gap-2">{actions}</div>}
        </header>
      )}
      {children != null && <div className={cx("px-5 py-4", bodyClassName)}>{children}</div>}
      {footer && <footer className="flex flex-wrap items-center justify-end gap-2 border-t border-line bg-surface-2/40 px-5 py-3">{footer}</footer>}
    </section>
  );
}

/** A settings row: the label and its explanation on the left, the
 * control on the right. Rows stack inside a Panel with dividers. */
export function FormRow({ label, description, children, htmlFor }: { label: ReactNode; description?: ReactNode; children: ReactNode; htmlFor?: string }) {
  return (
    <div className="grid grid-cols-1 gap-2 border-b border-line py-4 first:pt-0 last:border-0 last:pb-0 md:grid-cols-[minmax(0,2fr)_minmax(0,3fr)] md:gap-6">
      <div>
        <label htmlFor={htmlFor} className="text-[13px] text-fg">
          {label}
        </label>
        {description && <p className="mt-0.5 text-[12px] text-muted">{description}</p>}
      </div>
      <div className="flex min-w-0 flex-col gap-1.5">{children}</div>
    </div>
  );
}

/** A number with its label, for overview tiles. */
export function Stat({ label, value, hint, testId }: { label: ReactNode; value: ReactNode; hint?: ReactNode; testId?: string }) {
  return (
    <div className="flex flex-col gap-1 rounded-md border border-line bg-surface px-4 py-3" data-testid={testId}>
      <span className="text-[12px] text-muted">{label}</span>
      <span className="text-[20px] leading-tight">{value}</span>
      {hint && <span className="text-[11px] text-muted">{hint}</span>}
    </div>
  );
}

/** Label / value pairs. */
export function KeyValues({ items, testId }: { items: [ReactNode, ReactNode][]; testId?: string }) {
  return (
    <dl className="grid grid-cols-[max-content_1fr] gap-x-8 gap-y-2.5 text-[13px]" data-testid={testId}>
      {items.map(([k, v], i) => (
        <div key={i} className="contents">
          <dt className="text-muted">{k}</dt>
          <dd className="min-w-0">{v}</dd>
        </div>
      ))}
    </dl>
  );
}
