import { useSyncExternalStore } from "react";

export type Theme = "light" | "dark" | "system";

const KEY = "pgdock.theme";

export function loadTheme(): Theme {
  try {
    const t = localStorage.getItem(KEY);
    if (t === "light" || t === "dark" || t === "system") return t;
  } catch {
    /* storage unavailable */
  }
  return "dark"; // the default (docs/ui-redesign.md)
}

export function applyTheme(t: Theme) {
  document.documentElement.setAttribute("data-theme", t);
  try {
    localStorage.setItem(KEY, t);
  } catch {
    /* storage unavailable */
  }
}

export function nextTheme(t: Theme): Theme {
  return t === "dark" ? "light" : t === "light" ? "system" : "dark";
}

const themeListeners = new Set<() => void>();

/** Sets the theme and tells every useTheme() about it. */
export function setTheme(t: Theme) {
  applyTheme(t);
  themeListeners.forEach((l) => l());
}

/** The current theme, kept in sync across components. */
export function useTheme(): [Theme, (t: Theme) => void] {
  const t = useSyncExternalStore(
    (l) => {
      themeListeners.add(l);
      return () => themeListeners.delete(l);
    },
    loadTheme,
    () => "dark" as Theme,
  );
  return [t, setTheme];
}
