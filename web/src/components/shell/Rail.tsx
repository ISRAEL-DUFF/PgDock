import { Link } from "@tanstack/react-router";
import { PanelLeftClose, PanelLeftOpen, Pin, PinOff } from "lucide-react";
import { useState } from "react";
import { cx } from "../ui";
import { isActive, type RailItem } from "./nav";

const PIN_KEY = "pgdock.rail.pinned";
const SIDEBAR_KEY = "pgdock.sidebar.collapsed";

function stored(key: string): boolean {
  try {
    return localStorage.getItem(key) === "1";
  } catch {
    return false;
  }
}

function store(key: string, on: boolean) {
  try {
    localStorage.setItem(key, on ? "1" : "0");
  } catch {
    /* storage unavailable */
  }
}

/**
 * The icon rail (Studio's left bar): icons only, expanding over the page
 * with labels on hover; the pin at the bottom keeps it expanded.
 */
export function Rail({
  items,
  pathname,
  label,
}: {
  items: RailItem[];
  pathname: string;
  label: string;
}) {
  const [pinned, setPinned] = useState(() => stored(PIN_KEY));
  return (
    <div
      className={cx(
        "group/rail relative hidden shrink-0 md:block",
        pinned ? "w-52" : "w-12",
      )}
      data-testid="rail"
    >
      <nav
        aria-label={label}
        className={cx(
          "absolute inset-y-0 left-0 z-30 flex flex-col overflow-hidden border-r border-line bg-bg py-2 transition-[width] duration-150",
          pinned
            ? "w-52"
            : "w-12 group-hover/rail:w-52 group-hover/rail:shadow-2xl",
        )}
      >
        <div className="flex flex-1 flex-col gap-0.5 px-2">
          {items.map((it) => {
            const active = isActive(pathname, it.match, it.exact);
            const Icon = it.icon;
            return (
              <div key={it.key}>
                {it.divider && <div className="mx-1 my-2 h-px bg-line" />}
                <Link
                  to={it.to}
                  aria-label={it.label}
                  aria-current={active ? "page" : undefined}
                  data-testid={`rail-${it.key}`}
                  className={cx(
                    "flex h-8 items-center gap-3 rounded-md px-[6px] text-[13px] text-muted hover:bg-surface-2 hover:text-fg",
                    active && "bg-surface-3 text-fg",
                  )}
                >
                  <Icon
                    className="h-[18px] w-[18px] shrink-0"
                    strokeWidth={1.6}
                    aria-hidden
                  />
                  <span
                    className={cx(
                      "whitespace-nowrap transition-opacity",
                      pinned
                        ? "opacity-100"
                        : "opacity-0 group-hover/rail:opacity-100",
                    )}
                  >
                    {it.label}
                  </span>
                </Link>
              </div>
            );
          })}
        </div>
        <div className="px-2">
          <button
            type="button"
            onClick={() => {
              store(PIN_KEY, !pinned);
              setPinned(!pinned);
            }}
            aria-label={pinned ? "Collapse the menu" : "Keep the menu expanded"}
            className="flex h-8 w-full items-center gap-3 rounded-md px-[6px] text-[13px] text-muted hover:bg-surface-2 hover:text-fg"
          >
            {pinned ? (
              <PinOff className="h-4 w-4 shrink-0" strokeWidth={1.6} />
            ) : (
              <Pin className="h-4 w-4 shrink-0" strokeWidth={1.6} />
            )}
            <span
              className={cx(
                "whitespace-nowrap",
                pinned
                  ? "opacity-100"
                  : "opacity-0 group-hover/rail:opacity-100",
              )}
            >
              {pinned ? "Collapse menu" : "Keep expanded"}
            </span>
          </button>
        </div>
      </nav>
    </div>
  );
}

/** Whether the section sidebar is collapsed (remembered in the browser). */
export function useSidebarCollapsed(): [boolean, (v: boolean) => void] {
  const [c, setC] = useState(() => stored(SIDEBAR_KEY));
  return [
    c,
    (v) => {
      store(SIDEBAR_KEY, v);
      setC(v);
    },
  ];
}

/** A section's own menu, beside the rail ("Database", "Project Settings"). */
export function SectionSidebar({
  item,
  pathname,
  onCollapse,
}: {
  item: RailItem;
  pathname: string;
  onCollapse: () => void;
}) {
  if (!item.sidebar) return null;
  return (
    <aside
      className="hidden w-60 shrink-0 flex-col border-r border-line bg-surface md:flex"
      data-testid="section-sidebar"
    >
      <div className="flex h-12 items-center justify-between border-b border-line pr-2 pl-5">
        <h2 className="text-[15px]">{item.sidebar.title}</h2>
        <button
          type="button"
          onClick={onCollapse}
          aria-label="Hide the section menu"
          className="rounded p-1 text-muted hover:bg-surface-2 hover:text-fg"
        >
          <PanelLeftClose className="h-4 w-4" strokeWidth={1.6} />
        </button>
      </div>
      <nav
        aria-label={item.sidebar.title}
        className="flex flex-col gap-0.5 p-3"
      >
        {item.sidebar.links.map((l) => {
          const active = isActive(pathname, [l.to], true);
          return (
            <Link
              key={l.to}
              to={l.to}
              data-testid={l.testId}
              aria-current={active ? "page" : undefined}
              className={cx(
                "rounded-md px-2.5 py-1.5 text-[13px] text-fg-light hover:bg-surface-2 hover:text-fg",
                active && "bg-surface-3 text-fg",
              )}
            >
              {l.label}
            </Link>
          );
        })}
      </nav>
    </aside>
  );
}

/** Shown in place of a hidden section menu. */
export function SidebarOpener({ onOpen }: { onOpen: () => void }) {
  return (
    <button
      type="button"
      onClick={onOpen}
      aria-label="Show the section menu"
      className="absolute top-3 left-3 z-10 hidden rounded p-1 text-muted hover:bg-surface-2 hover:text-fg md:block"
    >
      <PanelLeftOpen className="h-4 w-4" strokeWidth={1.6} />
    </button>
  );
}
