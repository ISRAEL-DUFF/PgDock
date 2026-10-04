// Monaco, as the SQL Editor and the table editor use it (docs/ui-redesign.md,
// phase 3): the editor core with only the features we use, PostgreSQL and
// JSON, bundled with the app (no CDN), and themes matching PGDock's
// tokens. Importing this module sets it all up once.

import * as monaco from "monaco-editor/editor/editor.api.js";
import "monaco-editor/editor/browser/coreCommands.js";
import "monaco-editor/editor/contrib/bracketMatching/browser/bracketMatching.js";
import "monaco-editor/editor/contrib/clipboard/browser/clipboard.js";
import "monaco-editor/editor/contrib/comment/browser/comment.js";
import "monaco-editor/editor/contrib/contextmenu/browser/contextmenu.js";
import "monaco-editor/editor/contrib/cursorUndo/browser/cursorUndo.js";
import "monaco-editor/editor/contrib/find/browser/findController.js";
import "monaco-editor/editor/contrib/folding/browser/folding.js";
import "monaco-editor/editor/contrib/format/browser/formatActions.js";
import "monaco-editor/editor/contrib/hover/browser/hoverContribution.js";
import "monaco-editor/editor/contrib/linesOperations/browser/linesOperations.js";
import "monaco-editor/editor/contrib/multicursor/browser/multicursor.js";
import "monaco-editor/editor/contrib/placeholderText/browser/placeholderText.contribution.js";
import "monaco-editor/editor/contrib/snippet/browser/snippetController2.js";
import "monaco-editor/editor/contrib/suggest/browser/suggestController.js";
import "monaco-editor/editor/contrib/wordHighlighter/browser/wordHighlighter.js";
import "monaco-editor/editor/contrib/wordOperations/browser/wordOperations.js";
import "monaco-editor/editor/common/standaloneStrings.js";
import "monaco-editor/languages/definitions/pgsql/register.js";
import "monaco-editor/languages/features/json/register.js";
import EditorWorker from "monaco-editor/editor/editor.worker.js?worker";
import JsonWorker from "monaco-editor/language/json/json.worker.js?worker";

declare global {
  interface Window {
    MonacoEnvironment?: { getWorker: (id: string, label: string) => Worker };
  }
}

self.MonacoEnvironment = {
  getWorker: (_id: string, label: string) => (label === "json" ? new JsonWorker() : new EditorWorker()),
};

function cssVar(name: string, fallback: string): string {
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return v || fallback;
}

/** Monaco wants #rrggbb; our tokens are hex already. */
const hex = (v: string) => v.replace("#", "");

/** (Re)defines PGDock's themes from the current CSS tokens and picks the
 * one for the page's theme. */
export function applyMonacoTheme() {
  const dark = getComputedStyle(document.documentElement).colorScheme.includes("dark");
  const name = dark ? "pgdock-dark" : "pgdock-light";
  const accent = cssVar("--accent", dark ? "#8b5cf6" : "#7c3aed");
  monaco.editor.defineTheme(name, {
    base: dark ? "vs-dark" : "vs",
    inherit: true,
    rules: [
      { token: "keyword", foreground: hex(accent), fontStyle: "bold" },
      { token: "operator", foreground: hex(cssVar("--fg-light", "#b4b4b4")) },
      { token: "string", foreground: hex(cssVar("--ok", "#3ecf8e")) },
      { token: "number", foreground: hex(cssVar("--warn", "#f5a524")) },
      { token: "comment", foreground: hex(cssVar("--muted", "#8f8f8f")), fontStyle: "italic" },
      { token: "predefined", foreground: hex(cssVar("--warn", "#f5a524")) },
    ],
    colors: {
      "editor.background": cssVar("--bg", dark ? "#121212" : "#fcfcfc"),
      "editor.foreground": cssVar("--fg", dark ? "#ededed" : "#171717"),
      "editorLineNumber.foreground": cssVar("--muted", "#8f8f8f"),
      "editorLineNumber.activeForeground": cssVar("--fg", "#ededed"),
      "editor.lineHighlightBackground": cssVar("--surface", "#171717"),
      "editor.selectionBackground": accent + "55",
      "editorCursor.foreground": cssVar("--fg", "#ededed"),
      "editorWidget.background": cssVar("--surface-2", "#1f1f1f"),
      "editorWidget.border": cssVar("--border-strong", "#3e3e3e"),
      "editorSuggestWidget.background": cssVar("--surface-2", "#1f1f1f"),
      "editorSuggestWidget.selectedBackground": cssVar("--surface-3", "#262626"),
      "editorGutter.background": cssVar("--bg", dark ? "#121212" : "#fcfcfc"),
    },
  });
  monaco.editor.setTheme(name);
}

export { monaco };
