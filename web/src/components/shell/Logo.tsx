import { useId } from "react";
import { cx } from "../ui";

/** Xenibase's mark, the doorway: an arch with rows of data inside and a
 * small light above them (brand/mark.svg, docs/brand.md). */
export function LogoMark({ className }: { className?: string }) {
  const g = useId();
  return (
    <svg viewBox="0 0 64 64" className={cx("h-6 w-6", className)} aria-hidden>
      <defs>
        <linearGradient id={g} x1="10" y1="2" x2="54" y2="62" gradientUnits="userSpaceOnUse">
          <stop offset="0" stopColor="#2dd4bf" />
          <stop offset="1" stopColor="#0f766e" />
        </linearGradient>
      </defs>
      <path fill={`url(#${g})`} d="M12 30a20 20 0 0 1 40 0v23a5 5 0 0 1-5 5H17a5 5 0 0 1-5-5z" />
      <rect x="21" y="30" width="22" height="5.5" rx="2.75" fill="#fff" />
      <rect x="21" y="40" width="22" height="5.5" rx="2.75" fill="#fff" opacity="0.75" />
      <rect x="21" y="50" width="22" height="5.5" rx="2.75" fill="#fff" opacity="0.5" />
      <circle cx="32" cy="18" r="3.2" fill="#fef3c7" />
    </svg>
  );
}

/** The mark and the name (sign-in pages, the shell's collapsed menu). */
export function Logo({ className }: { className?: string }) {
  return (
    <span className={cx("flex items-center gap-2 text-[15px] font-semibold tracking-tight", className)}>
      <LogoMark />
      Xenibase
    </span>
  );
}
