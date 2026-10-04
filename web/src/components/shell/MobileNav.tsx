import * as D from "@radix-ui/react-dialog";
import { Link } from "@tanstack/react-router";
import { X } from "lucide-react";
import { cx } from "../ui";
import { LogoMark } from "./Logo";
import { isActive, type RailItem } from "./nav";

/** On narrow screens the rail and the section menu become one drawer. */
export function MobileNav({
  open,
  onOpenChange,
  items,
  pathname,
  label,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  items: RailItem[];
  pathname: string;
  label: string;
}) {
  return (
    <D.Root open={open} onOpenChange={onOpenChange}>
      <D.Portal>
        <D.Overlay className="fixed inset-0 z-40 bg-black/60 md:hidden" />
        <D.Content
          className="fixed inset-y-0 left-0 z-50 flex w-[min(18rem,85vw)] flex-col border-r border-line-strong bg-surface text-fg shadow-2xl outline-none md:hidden"
          data-testid="mobile-nav"
        >
          <div className="flex h-12 items-center justify-between border-b border-line px-3">
            <D.Title className="flex items-center gap-2 text-[14px]">
              <LogoMark /> {label}
            </D.Title>
            <D.Description className="sr-only">Navigation</D.Description>
            <D.Close className="rounded p-1 text-muted hover:bg-surface-2 hover:text-fg" aria-label="Close navigation">
              <X className="h-4 w-4" />
            </D.Close>
          </div>
          <nav aria-label={label} className="flex-1 overflow-y-auto p-2">
            {items.map((it) => {
              const active = isActive(pathname, it.match, it.exact);
              const Icon = it.icon;
              return (
                <div key={it.key}>
                  {it.divider && <div className="mx-1 my-2 h-px bg-line" />}
                  <Link
                    to={it.to}
                    onClick={() => onOpenChange(false)}
                    aria-current={active && !it.sidebar ? "page" : undefined}
                    className={cx(
                      "flex h-9 items-center gap-3 rounded-md px-2 text-[14px] text-fg-light hover:bg-surface-2 hover:text-fg",
                      active && "bg-surface-3 text-fg",
                    )}
                  >
                    <Icon className="h-[18px] w-[18px] shrink-0" strokeWidth={1.6} aria-hidden />
                    {it.label}
                  </Link>
                  {active && it.sidebar && (
                    <div className="my-1 ml-5 flex flex-col gap-0.5 border-l border-line pl-3">
                      {it.sidebar.links.map((l) => {
                        const here = isActive(pathname, [l.to], true);
                        return (
                          <Link
                            key={l.to}
                            to={l.to}
                            onClick={() => onOpenChange(false)}
                            aria-current={here ? "page" : undefined}
                            className={cx("rounded-md px-2 py-1.5 text-[13px] text-fg-light hover:bg-surface-2 hover:text-fg", here && "bg-surface-3 text-fg")}
                          >
                            {l.label}
                          </Link>
                        );
                      })}
                    </div>
                  )}
                </div>
              );
            })}
          </nav>
        </D.Content>
      </D.Portal>
    </D.Root>
  );
}
