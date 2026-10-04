// Keyboard shortcuts across the app (docs/ui-redesign.md, phase 6): the
// key names, how they are shown, and the "g then a letter" sequences that
// jump between sections.

export type Shortcut = { keys: string; label: string };
export type ShortcutGroup = { title: string; items: Shortcut[] };

/** Whether to show ⌘ and ⌥ rather than Ctrl and Alt. */
export function isMac(platform = typeof navigator === "undefined" ? "" : navigator.platform): boolean {
  return /mac|iphone|ipad/i.test(platform);
}

const names: Record<string, [mac: string, other: string]> = {
  mod: ["⌘", "Ctrl"],
  ctrl: ["⌃", "Ctrl"],
  shift: ["⇧", "Shift"],
  alt: ["⌥", "Alt"],
  enter: ["↵", "Enter"],
  escape: ["Esc", "Esc"],
  arrows: ["↑↓←→", "↑↓←→"],
  space: ["Space", "Space"],
};

/** "mod+shift+f" → ["⌘", "⇧", "F"]; "g t" → ["G", "then", "T"]. */
export function keyParts(keys: string, mac = isMac()): string[] {
  return keys
    .split(" ")
    .flatMap((chord, i) => [...(i > 0 ? ["then"] : []), ...chord.split("+").map((k) => names[k]?.[mac ? 0 : 1] ?? (k.length === 1 ? k.toUpperCase() : k))]);
}

/** The keys as one short string for a button or menu ("⌘⇧F", "Ctrl+Shift+F"). */
export function keyLabel(keys: string, mac = isMac()): string {
  return keyParts(keys, mac)
    .filter((p) => p !== "then")
    .join(mac ? "" : "+");
}

/** The letter after "g" for each rail entry (nav.tsx keys). */
export const GO_KEYS: Record<string, string> = {
  // A project.
  overview: "o",
  tables: "t",
  sql: "s",
  database: "d",
  reports: "r",
  logs: "l",
  settings: ",",
  // The organisation.
  projects: "p",
  team: "m",
  operations: "n",
  usage: "u",
  "org-settings": ",",
  // The platform admin's area.
  nodes: "n",
  orgs: "o",
  users: "u",
  alerts: "a",
  "platform-settings": ",",
};

/** Does the event come from somewhere that takes text? Single-key
 * shortcuts stay out of the way there. */
export function isTyping(target: EventTarget | null): boolean {
  const el = target as HTMLElement | null;
  if (!el || typeof el.closest !== "function") return false;
  if (el.isContentEditable) return true;
  return !!el.closest("input, textarea, select, [contenteditable=''], [contenteditable='true'], .monaco-editor, [role='textbox'], [role='gridcell']");
}

/**
 * "g" then a letter within a second. feed() takes each key press and
 * returns the letter that completes a sequence, or null.
 */
export function goSequence(timeoutMs = 1000) {
  let armedAt = -Infinity;
  return {
    feed(key: string, now: number): string | null {
      if (now - armedAt <= timeoutMs) {
        armedAt = -Infinity;
        return key === "g" ? null : key;
      }
      if (key === "g") armedAt = now;
      return null;
    },
    get armed() {
      return armedAt !== -Infinity;
    },
  };
}

/** What the "?" sheet lists. Section shortcuts come from the rail. */
export function shortcutGroups(go: { label: string; key: string }[]): ShortcutGroup[] {
  return [
    {
      title: "General",
      items: [
        { keys: "mod+k", label: "Search projects and pages, run a command" },
        { keys: "?", label: "Show keyboard shortcuts" },
        { keys: "[", label: "Hide or show the section menu" },
        { keys: "escape", label: "Close a panel or dialog" },
      ],
    },
    ...(go.length ? [{ title: "Go to", items: go.map((g) => ({ keys: `g ${g.key}`, label: g.label })) }] : []),
    {
      title: "SQL editor",
      items: [
        { keys: "mod+enter", label: "Run the query (or the selection)" },
        { keys: "mod+shift+f", label: "Format the SQL" },
        { keys: "ctrl+space", label: "Suggest tables, columns and keywords" },
      ],
    },
    {
      title: "Table editor",
      items: [
        { keys: "arrows", label: "Move between cells" },
        { keys: "enter", label: "Edit the cell; save the edit" },
        { keys: "escape", label: "Cancel the edit" },
      ],
    },
  ];
}
