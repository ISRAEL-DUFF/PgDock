import * as C from "@radix-ui/react-checkbox";
import * as S from "@radix-ui/react-switch";
import * as Tb from "@radix-ui/react-tabs";
import { Check, Minus } from "lucide-react";
import type { ComponentProps, ReactNode } from "react";
import { cx } from "./legacy";

export function Checkbox({
  className,
  ...rest
}: ComponentProps<typeof C.Root>) {
  return (
    <C.Root
      className={cx(
        "flex h-4 w-4 shrink-0 items-center justify-center rounded border border-line-strong bg-surface-2",
        "data-[state=checked]:border-accent data-[state=checked]:bg-accent data-[state=indeterminate]:border-accent data-[state=indeterminate]:bg-accent",
        className,
      )}
      {...rest}
    >
      <C.Indicator className="text-accent-fg">
        {rest.checked === "indeterminate" ? (
          <Minus className="h-3 w-3" />
        ) : (
          <Check className="h-3 w-3" />
        )}
      </C.Indicator>
    </C.Root>
  );
}

export function Switch({ className, ...rest }: ComponentProps<typeof S.Root>) {
  return (
    <S.Root
      className={cx(
        "relative inline-flex h-[18px] w-8 shrink-0 items-center rounded-full border border-line-strong bg-surface-3 transition-colors",
        "data-[state=checked]:border-accent data-[state=checked]:bg-accent",
        className,
      )}
      {...rest}
    >
      <S.Thumb className="block h-3.5 w-3.5 translate-x-px rounded-full bg-white shadow transition-transform data-[state=checked]:translate-x-[14px]" />
    </S.Root>
  );
}

export const Tabs = Tb.Root;
export const TabsContent = Tb.Content;

/** Underline tabs ("Results", "Chart"). */
export function TabsList({
  className,
  ...rest
}: ComponentProps<typeof Tb.List>) {
  return (
    <Tb.List
      className={cx("flex items-center gap-4 border-b border-line", className)}
      {...rest}
    />
  );
}

export function TabsTrigger({
  className,
  ...rest
}: ComponentProps<typeof Tb.Trigger>) {
  return (
    <Tb.Trigger
      className={cx(
        "-mb-px border-b-2 border-transparent px-0.5 py-2 text-[13px] text-muted hover:text-fg",
        "data-[state=active]:border-fg data-[state=active]:text-fg",
        className,
      )}
      {...rest}
    />
  );
}

/** Segmented control ("Data | Definition"). */
export function Segmented<T extends string>({
  value,
  onChange,
  options,
  className,
}: {
  value: T;
  onChange: (v: T) => void;
  options: { value: T; label: ReactNode }[];
  className?: string;
}) {
  return (
    <div
      className={cx(
        "inline-flex rounded-md border border-line-strong bg-surface p-0.5",
        className,
      )}
      role="radiogroup"
    >
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          role="radio"
          aria-checked={value === o.value}
          onClick={() => onChange(o.value)}
          className={cx(
            "rounded px-3 py-0.5 text-[13px] text-muted",
            value === o.value && "bg-surface-3 text-fg",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

/** Uppercase section label ("PRIVATE (3)"). */
export function SectionLabel({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  return (
    <p
      className={cx(
        "px-2 text-[11px] font-medium tracking-wider text-muted uppercase",
        className,
      )}
    >
      {children}
    </p>
  );
}
