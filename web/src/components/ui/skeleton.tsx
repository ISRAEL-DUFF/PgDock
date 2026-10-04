import { cx } from "./legacy";

// Loading placeholders in the shape of what's coming (docs/ui-redesign.md,
// phase 6), in place of a lone spinner.

/** A pulsing block. */
export function Skeleton({ className }: { className?: string }) {
  return <div aria-hidden className={cx("animate-pulse rounded bg-surface-3", className)} />;
}

function Loading({ children, className, label = "Loading" }: { children: React.ReactNode; className?: string; label?: string }) {
  return (
    <div role="status" aria-live="polite" className={className} data-testid="loading">
      <span className="sr-only">{label}…</span>
      {children}
    </div>
  );
}

/** A table that is loading: a header and a few rows. */
export function TableSkeleton({ rows = 4, cols = 4 }: { rows?: number; cols?: number }) {
  return (
    <Loading className="overflow-hidden rounded-md border border-line bg-surface">
      <div className="flex gap-6 border-b border-line bg-surface-2 px-3 py-2.5">
        {Array.from({ length: cols }, (_, i) => (
          <Skeleton key={i} className="h-3 flex-1" />
        ))}
      </div>
      {Array.from({ length: rows }, (_, r) => (
        <div key={r} className="flex gap-6 border-b border-line px-3 py-3 last:border-0">
          {Array.from({ length: cols }, (_, i) => (
            <Skeleton key={i} className={cx("h-3 flex-1", i === 0 && "max-w-[40%]", (r + i) % 3 === 1 && "max-w-[25%]")} />
          ))}
        </div>
      ))}
    </Loading>
  );
}

/** A grid of cards that is loading (the projects home). */
export function CardsSkeleton({ count = 3 }: { count?: number }) {
  return (
    <Loading className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
      {Array.from({ length: count }, (_, i) => (
        <div key={i} className="flex flex-col gap-4 rounded-md border border-line bg-surface p-4">
          <div className="flex justify-between">
            <div className="flex w-1/2 flex-col gap-2">
              <Skeleton className="h-3.5" />
              <Skeleton className="h-2.5 w-2/3" />
            </div>
            <Skeleton className="h-4 w-14 rounded-full" />
          </div>
          <div className="flex gap-1.5">
            <Skeleton className="h-4 w-14 rounded-full" />
            <Skeleton className="h-4 w-12 rounded-full" />
          </div>
          <Skeleton className="h-3 w-3/4" />
        </div>
      ))}
    </Loading>
  );
}

/** A panel of settings rows that is loading. */
export function PanelSkeleton({ rows = 3 }: { rows?: number }) {
  return (
    <Loading className="rounded-md border border-line bg-surface px-5 py-4">
      {Array.from({ length: rows }, (_, i) => (
        <div key={i} className="grid grid-cols-1 gap-3 border-b border-line py-4 first:pt-0 last:border-0 last:pb-0 md:grid-cols-[2fr_3fr] md:gap-6">
          <div className="flex flex-col gap-2">
            <Skeleton className="h-3 w-1/2" />
            <Skeleton className="h-2.5 w-3/4" />
          </div>
          <Skeleton className="h-[30px]" />
        </div>
      ))}
    </Loading>
  );
}

/** A whole page that is loading: its heading and two panels. */
export function PageSkeleton() {
  return (
    <Loading className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Skeleton className="h-6 w-56" />
        <Skeleton className="h-3 w-80 max-w-full" />
      </div>
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        {Array.from({ length: 4 }, (_, i) => (
          <div key={i} className="flex flex-col gap-2 rounded-md border border-line bg-surface px-4 py-3">
            <Skeleton className="h-2.5 w-1/2" />
            <Skeleton className="h-5 w-2/3" />
          </div>
        ))}
      </div>
      <div className="flex flex-col gap-3 rounded-md border border-line bg-surface px-5 py-4">
        <Skeleton className="h-3.5 w-40" />
        <Skeleton className="h-3 w-full" />
        <Skeleton className="h-3 w-5/6" />
        <Skeleton className="h-3 w-2/3" />
      </div>
    </Loading>
  );
}
