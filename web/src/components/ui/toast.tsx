import { Toaster as Sonner } from "sonner";
import { useEffect, useState } from "react";

export { toast } from "sonner";

/** The toaster, themed to follow <html data-theme>. */
export function Toaster() {
  const [theme, setTheme] = useState<"light" | "dark" | "system">(
    () =>
      (document.documentElement.getAttribute("data-theme") as
        | "light"
        | "dark"
        | "system") ?? "dark",
  );
  useEffect(() => {
    const o = new MutationObserver(() =>
      setTheme(
        (document.documentElement.getAttribute("data-theme") as
          | "light"
          | "dark"
          | "system") ?? "dark",
      ),
    );
    o.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["data-theme"],
    });
    return () => o.disconnect();
  }, []);
  return (
    <Sonner
      theme={theme}
      position="bottom-right"
      toastOptions={{
        classNames: {
          toast: "!bg-surface-2 !border-line-strong !text-fg !text-[13px]",
          description: "!text-muted",
        },
      }}
    />
  );
}
