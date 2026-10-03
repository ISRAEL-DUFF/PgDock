import * as D from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import type { ReactNode } from "react";
import { cx } from "./legacy";

/** A panel that slides in from the right, full height, with a sticky
 * footer: Studio's way of creating and editing things. */
export function SidePanel({
  open,
  onOpenChange,
  title,
  description,
  footer,
  size = "medium",
  children,
  testId,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  description?: ReactNode;
  footer?: ReactNode;
  size?: "medium" | "large" | "xlarge";
  children: ReactNode;
  testId?: string;
}) {
  const width = {
    medium: "w-[min(36rem,100vw)]",
    large: "w-[min(48rem,100vw)]",
    xlarge: "w-[min(64rem,100vw)]",
  }[size];
  return (
    <D.Root open={open} onOpenChange={onOpenChange}>
      <D.Portal>
        <D.Overlay className="fixed inset-0 z-40 bg-black/50" />
        <D.Content
          data-testid={testId}
          className={cx(
            "fixed inset-y-0 right-0 z-50 flex flex-col border-l border-line-strong bg-surface text-fg shadow-2xl outline-none",
            width,
          )}
        >
          <header className="flex items-start justify-between gap-3 border-b border-line px-5 py-3">
            <div className="min-w-0">
              <D.Title className="text-[15px]">{title}</D.Title>
              {description ? (
                <D.Description className="mt-0.5 text-[13px] text-muted">
                  {description}
                </D.Description>
              ) : (
                <D.Description className="sr-only">
                  {typeof title === "string" ? title : "Panel"}
                </D.Description>
              )}
            </div>
            <D.Close
              className="rounded p-1 text-muted hover:bg-surface-2 hover:text-fg"
              aria-label="Close"
            >
              <X className="h-4 w-4" />
            </D.Close>
          </header>
          <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">
            {children}
          </div>
          {footer && (
            <footer className="flex justify-end gap-2 border-t border-line px-5 py-3">
              {footer}
            </footer>
          )}
        </D.Content>
      </D.Portal>
    </D.Root>
  );
}

/** A centred dialog (Radix): confirmations and small forms. */
export function Dialog({
  open,
  onOpenChange,
  title,
  description,
  footer,
  children,
  testId,
  className,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  description?: ReactNode;
  footer?: ReactNode;
  children?: ReactNode;
  testId?: string;
  className?: string;
}) {
  return (
    <D.Root open={open} onOpenChange={onOpenChange}>
      <D.Portal>
        <D.Overlay className="fixed inset-0 z-40 bg-black/60" />
        <D.Content
          data-testid={testId}
          className={cx(
            "fixed top-1/2 left-1/2 z-50 w-[min(32rem,calc(100vw-2rem))] -translate-x-1/2 -translate-y-1/2 rounded-lg border border-line-strong bg-surface text-fg shadow-2xl outline-none",
            className,
          )}
        >
          <div className="border-b border-line px-5 py-3">
            <D.Title className="text-[15px]">{title}</D.Title>
            {description ? (
              <D.Description className="mt-0.5 text-[13px] text-muted">
                {description}
              </D.Description>
            ) : (
              <D.Description className="sr-only">
                {typeof title === "string" ? title : "Dialog"}
              </D.Description>
            )}
          </div>
          {children && <div className="px-5 py-4">{children}</div>}
          {footer && (
            <div className="flex justify-end gap-2 border-t border-line px-5 py-3">
              {footer}
            </div>
          )}
        </D.Content>
      </D.Portal>
    </D.Root>
  );
}
