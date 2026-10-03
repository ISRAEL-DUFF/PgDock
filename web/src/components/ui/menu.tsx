import * as DM from "@radix-ui/react-dropdown-menu";
import { Check, ChevronRight } from "lucide-react";
import type { ComponentProps, ReactNode } from "react";
import { cx } from "./legacy";

/** Dropdown menus (Radix), styled like Studio's: compact, bordered, raised. */
export const DropdownMenu = DM.Root;
export const DropdownMenuTrigger = DM.Trigger;
export const DropdownMenuGroup = DM.Group;
export const DropdownMenuSub = DM.Sub;

export const menuSurface =
  "z-50 min-w-[12rem] overflow-hidden rounded-md border border-line-strong bg-surface-2 p-1 text-[13px] text-fg shadow-xl " +
  "data-[state=open]:animate-in data-[state=open]:fade-in-0";

export function DropdownMenuContent({
  className,
  sideOffset = 6,
  align = "start",
  ...rest
}: ComponentProps<typeof DM.Content>) {
  return (
    <DM.Portal>
      <DM.Content
        sideOffset={sideOffset}
        align={align}
        className={cx(menuSurface, className)}
        {...rest}
      />
    </DM.Portal>
  );
}

export const menuItem =
  "relative flex cursor-default items-center gap-2 rounded px-2 py-1.5 outline-none select-none " +
  "data-[highlighted]:bg-surface-3 data-[disabled]:pointer-events-none data-[disabled]:opacity-50";

export function DropdownMenuItem({
  className,
  icon,
  shortcut,
  danger,
  children,
  ...rest
}: ComponentProps<typeof DM.Item> & {
  icon?: ReactNode;
  shortcut?: string;
  danger?: boolean;
}) {
  return (
    <DM.Item
      className={cx(menuItem, danger && "text-danger", className)}
      {...rest}
    >
      {icon && (
        <span className="flex h-4 w-4 items-center justify-center text-fg-light">
          {icon}
        </span>
      )}
      <span className="flex-1">{children}</span>
      {shortcut && (
        <span className="ml-auto text-[11px] text-muted">{shortcut}</span>
      )}
    </DM.Item>
  );
}

export function DropdownMenuCheckboxItem({
  className,
  children,
  ...rest
}: ComponentProps<typeof DM.CheckboxItem>) {
  return (
    <DM.CheckboxItem className={cx(menuItem, "pl-7", className)} {...rest}>
      <DM.ItemIndicator className="absolute left-2 flex h-4 w-4 items-center justify-center">
        <Check className="h-3.5 w-3.5" />
      </DM.ItemIndicator>
      {children}
    </DM.CheckboxItem>
  );
}

export function DropdownMenuLabel({
  className,
  ...rest
}: ComponentProps<typeof DM.Label>) {
  return (
    <DM.Label
      className={cx(
        "px-2 py-1.5 text-[11px] tracking-wide text-muted uppercase",
        className,
      )}
      {...rest}
    />
  );
}

export function DropdownMenuSeparator({
  className,
  ...rest
}: ComponentProps<typeof DM.Separator>) {
  return (
    <DM.Separator
      className={cx("-mx-1 my-1 h-px bg-line", className)}
      {...rest}
    />
  );
}

export function DropdownMenuSubTrigger({
  className,
  children,
  ...rest
}: ComponentProps<typeof DM.SubTrigger>) {
  return (
    <DM.SubTrigger
      className={cx(menuItem, "data-[state=open]:bg-surface-3", className)}
      {...rest}
    >
      {children}
      <ChevronRight className="ml-auto h-3.5 w-3.5" />
    </DM.SubTrigger>
  );
}

export function DropdownMenuSubContent({
  className,
  ...rest
}: ComponentProps<typeof DM.SubContent>) {
  return (
    <DM.Portal>
      <DM.SubContent className={cx(menuSurface, className)} {...rest} />
    </DM.Portal>
  );
}
