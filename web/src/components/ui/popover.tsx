import * as P from "@radix-ui/react-popover";
import * as T from "@radix-ui/react-tooltip";
import type { ComponentProps, ReactNode } from "react";
import { cx } from "./legacy";

export const Popover = P.Root;
export const PopoverTrigger = P.Trigger;
export const PopoverAnchor = P.Anchor;
export const PopoverClose = P.Close;

export function PopoverContent({
  className,
  sideOffset = 6,
  align = "start",
  ...rest
}: ComponentProps<typeof P.Content>) {
  return (
    <P.Portal>
      <P.Content
        sideOffset={sideOffset}
        align={align}
        className={cx(
          "z-50 rounded-md border border-line-strong bg-surface-2 p-3 text-[13px] text-fg shadow-xl outline-none",
          className,
        )}
        {...rest}
      />
    </P.Portal>
  );
}

export const TooltipProvider = T.Provider;

/** A hover label. Wrap the app in TooltipProvider (the shell does). */
export function Tooltip({
  content,
  side = "top",
  children,
}: {
  content: ReactNode;
  side?: "top" | "right" | "bottom" | "left";
  children: ReactNode;
}) {
  if (!content) return <>{children}</>;
  return (
    <T.Root>
      <T.Trigger asChild>{children}</T.Trigger>
      <T.Portal>
        <T.Content
          side={side}
          sideOffset={6}
          className="z-50 rounded-md border border-line-strong bg-surface-2 px-2 py-1 text-xs text-fg shadow-lg"
        >
          {content}
        </T.Content>
      </T.Portal>
    </T.Root>
  );
}
