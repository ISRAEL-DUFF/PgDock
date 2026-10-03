import { useId } from "react";
import { cx } from "../ui";

/** PGDock's mark: a database cylinder in the accent's gradient, with two
 * cut rings. It stands alone in the top bar, as Supabase's bolt does. */
export function LogoMark({ className }: { className?: string }) {
  const g = useId();
  return (
    <svg viewBox="0 0 24 24" className={cx("h-6 w-6", className)} aria-hidden>
      <defs>
        <linearGradient
          id={g}
          x1="4"
          y1="2"
          x2="20"
          y2="22"
          gradientUnits="userSpaceOnUse"
        >
          <stop offset="0" stopColor="#c4b5fd" />
          <stop offset="0.55" stopColor="#8b5cf6" />
          <stop offset="1" stopColor="#6d28d9" />
        </linearGradient>
      </defs>
      <path
        fill={`url(#${g})`}
        d="M12 2.5c4.7 0 8 1.6 8 3.6v11.8c0 2-3.3 3.6-8 3.6s-8-1.6-8-3.6V6.1c0-2 3.3-3.6 8-3.6z"
      />
      <ellipse
        cx="12"
        cy="6.1"
        rx="5.6"
        ry="1.6"
        fill="var(--bg)"
        opacity="0.55"
      />
      <path
        d="M4 10.2c0 1.9 3.3 3.4 8 3.4s8-1.5 8-3.4"
        fill="none"
        stroke="var(--bg)"
        strokeWidth="1.3"
        opacity="0.7"
      />
      <path
        d="M4 14.4c0 1.9 3.3 3.4 8 3.4s8-1.5 8-3.4"
        fill="none"
        stroke="var(--bg)"
        strokeWidth="1.3"
        opacity="0.7"
      />
    </svg>
  );
}

/** The mark and the name (sign-in pages, the shell's collapsed menu). */
export function Logo({ className }: { className?: string }) {
  return (
    <span
      className={cx(
        "flex items-center gap-2 text-[15px] font-semibold tracking-tight",
        className,
      )}
    >
      <LogoMark />
      PGDock
    </span>
  );
}
