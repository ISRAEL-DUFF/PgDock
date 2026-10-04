import { forwardRef, useEffect, useImperativeHandle, useRef } from "react";
import { formatDialect, postgresql } from "sql-formatter";
import type { DbSchema } from "../../api/client";
import { applyMonacoTheme, monaco } from "../../lib/monaco";
import { completionsAt, positionAt } from "../../lib/sqlEditor/complete";
import { useTheme } from "../../lib/theme";

export type CodeEditorHandle = {
  /** The selected text, or the whole document when nothing is selected. */
  runnable: () => string;
  focus: () => void;
  /** Formats the document as SQL (⌘⇧F). */
  format: () => void;
  /** Marks an error at Postgres's 1-based character position, or clears it. */
  markError: (pos: number | null, message?: string) => void;
};

// The completion source is the current project's schema; one provider
// serves every editor.
let schemaForCompletion: DbSchema = { schemas: [] };
let registered = false;

function prettySQL(sql: string): string {
  try {
    return formatDialect(sql, { dialect: postgresql, keywordCase: "preserve", tabWidth: 2 });
  } catch {
    return sql;
  }
}

function registerOnce() {
  if (registered) return;
  registered = true;
  const kinds = monaco.languages.CompletionItemKind;
  monaco.languages.registerCompletionItemProvider("pgsql", {
    triggerCharacters: ["."],
    provideCompletionItems(model, position) {
      const word = model.getWordUntilPosition(position);
      const range = new monaco.Range(position.lineNumber, word.startColumn, position.lineNumber, word.endColumn);
      const before = model.getValueInRange(new monaco.Range(1, 1, position.lineNumber, position.column));
      const items = completionsAt(before, model.getValue(), schemaForCompletion);
      return {
        suggestions: items.map((s, i) => ({
          label: s.label,
          kind: s.kind === "column" ? kinds.Field : s.kind === "table" ? kinds.Struct : s.kind === "view" ? kinds.Interface : s.kind === "schema" ? kinds.Module : kinds.Keyword,
          detail: s.detail,
          insertText: s.insertText,
          range,
          // Columns first, then tables, then keywords.
          sortText: `${{ column: 0, table: 1, view: 1, schema: 2, keyword: 3 }[s.kind]}${String(i).padStart(4, "0")}`,
        })),
      };
    },
  });
  monaco.languages.registerDocumentFormattingEditProvider("pgsql", {
    provideDocumentFormattingEdits(model) {
      return [{ range: model.getFullModelRange(), text: prettySQL(model.getValue()) }];
    },
  });
}

/**
 * Monaco, as Studio's SQL Editor uses it: PostgreSQL with completion from
 * the project's schema, ⌘↵ to run, ⌘⇧F to format. Also the read-only
 * Definition view and the JSON editor.
 */
export const CodeEditor = forwardRef<
  CodeEditorHandle,
  {
    value: string;
    onChange?: (v: string) => void;
    onRun?: () => void;
    language?: "pgsql" | "json";
    readOnly?: boolean;
    schema?: DbSchema;
    label: string;
    testId?: string;
    lineNumbers?: boolean;
  }
>(function CodeEditor({ value, onChange, onRun, language = "pgsql", readOnly, schema, label, testId, lineNumbers = true }, ref) {
  const host = useRef<HTMLDivElement>(null);
  const editor = useRef<monaco.editor.IStandaloneCodeEditor | null>(null);
  const onRunRef = useRef(onRun);
  const onChangeRef = useRef(onChange);
  onRunRef.current = onRun;
  onChangeRef.current = onChange;
  const [theme] = useTheme();

  if (schema) schemaForCompletion = schema;

  useEffect(() => {
    registerOnce();
    applyMonacoTheme();
    const e = monaco.editor.create(host.current!, {
      value,
      language,
      readOnly,
      automaticLayout: true,
      minimap: { enabled: false },
      fontSize: 13,
      fontFamily: 'ui-monospace, "SFMono-Regular", "JetBrains Mono", Menlo, monospace',
      lineNumbers: lineNumbers ? "on" : "off",
      lineNumbersMinChars: 3,
      scrollBeyondLastLine: false,
      renderLineHighlight: readOnly ? "none" : "line",
      tabSize: 2,
      wordWrap: "on",
      padding: { top: 12, bottom: 12 },
      overviewRulerLanes: 0,
      scrollbar: { verticalScrollbarSize: 8, horizontalScrollbarSize: 8 },
      fixedOverflowWidgets: true,
      acceptSuggestionOnEnter: "smart",
      ariaLabel: label,
    });
    editor.current = e;
    e.getDomNode()?.querySelector("textarea")?.setAttribute("data-testid", testId ?? "");
    e.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.Enter, () => onRunRef.current?.());
    e.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyMod.Shift | monaco.KeyCode.KeyF, () => void e.getAction("editor.action.formatDocument")?.run());
    const sub = e.onDidChangeModelContent(() => {
      monaco.editor.setModelMarkers(e.getModel()!, "pgdock", []);
      onChangeRef.current?.(e.getValue());
    });
    return () => {
      sub.dispose();
      e.getModel()?.dispose();
      e.dispose();
    };
    // The editor owns its document after mount; outside changes come
    // through the effect below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    const e = editor.current;
    if (e && e.getValue() !== value) e.setValue(value);
  }, [value]);

  useEffect(() => {
    applyMonacoTheme();
  }, [theme]);

  useImperativeHandle(ref, () => ({
    runnable: () => {
      const e = editor.current;
      if (!e) return value;
      const sel = e.getSelection();
      const text = sel && !sel.isEmpty() ? e.getModel()!.getValueInRange(sel) : "";
      return text.trim() ? text : e.getValue();
    },
    focus: () => editor.current?.focus(),
    format: () => void editor.current?.getAction("editor.action.formatDocument")?.run(),
    markError: (pos, message) => {
      const e = editor.current;
      const model = e?.getModel();
      if (!e || !model) return;
      if (pos == null) {
        monaco.editor.setModelMarkers(model, "pgdock", []);
        return;
      }
      const { line, column } = positionAt(model.getValue(), pos);
      const word = model.getWordAtPosition({ lineNumber: line, column });
      monaco.editor.setModelMarkers(model, "pgdock", [
        {
          severity: monaco.MarkerSeverity.Error,
          message: message ?? "Error",
          startLineNumber: line,
          startColumn: word?.startColumn ?? column,
          endLineNumber: line,
          endColumn: word?.endColumn ?? column + 1,
        },
      ]);
      e.revealLineInCenterIfOutsideViewport(line);
    },
  }));

  return <div ref={host} className="h-full min-h-0 w-full" data-testid={testId ? `${testId}-host` : undefined} aria-label={label} />;
});

export { prettySQL };
