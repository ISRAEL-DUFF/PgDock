import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import { PostgreSQL, sql } from "@codemirror/lang-sql";
import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import { EditorState } from "@codemirror/state";
import { EditorView, keymap, lineNumbers, placeholder } from "@codemirror/view";
import { tags } from "@lezer/highlight";
import { forwardRef, useEffect, useImperativeHandle, useRef } from "react";

// Colours come from the theme tokens, so light and dark both work.
const highlight = HighlightStyle.define([
  { tag: tags.keyword, color: "var(--accent)", fontWeight: "600" },
  { tag: [tags.string, tags.special(tags.string)], color: "var(--ok)" },
  { tag: [tags.number, tags.bool, tags.null], color: "var(--warn)" },
  { tag: [tags.lineComment, tags.blockComment], color: "var(--muted)", fontStyle: "italic" },
  { tag: tags.operator, color: "var(--fg)" },
]);

const theme = EditorView.theme({
  "&": { backgroundColor: "var(--code-bg)", color: "var(--fg)", fontSize: "13px", borderRadius: "6px" },
  "&.cm-focused": { outline: "1px solid var(--accent)" },
  ".cm-content": { fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace", caretColor: "var(--fg)", minHeight: "8rem" },
  ".cm-gutters": { backgroundColor: "var(--code-bg)", color: "var(--muted)", border: "none" },
  ".cm-activeLine": { backgroundColor: "transparent" },
  "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection": { backgroundColor: "color-mix(in srgb, var(--accent) 30%, transparent)" },
  ".cm-scroller": { maxHeight: "22rem", overflow: "auto" },
});

export type SqlEditorHandle = {
  /** The selected text, or the whole document when nothing is selected. */
  runnable: () => string;
  focus: () => void;
};

/** CodeMirror with PostgreSQL highlighting; Ctrl/Cmd+Enter runs (spec §8.5). */
export const SqlEditor = forwardRef<
  SqlEditorHandle,
  { value: string; onChange: (v: string) => void; onRun: () => void; label: string }
>(function SqlEditor({ value, onChange, onRun, label }, ref) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const onRunRef = useRef(onRun);
  const onChangeRef = useRef(onChange);
  onRunRef.current = onRun;
  onChangeRef.current = onChange;

  useEffect(() => {
    const v = new EditorView({
      parent: host.current!,
      state: EditorState.create({
        doc: value,
        extensions: [
          lineNumbers(),
          history(),
          sql({ dialect: PostgreSQL }),
          syntaxHighlighting(highlight),
          theme,
          placeholder("SELECT now();"),
          EditorView.lineWrapping,
          EditorView.contentAttributes.of({ "aria-label": label, "data-testid": "sql-editor" }),
          keymap.of([
            { key: "Mod-Enter", preventDefault: true, run: () => (onRunRef.current(), true) },
            indentWithTab,
            ...defaultKeymap,
            ...historyKeymap,
          ]),
          EditorView.updateListener.of((u) => {
            if (u.docChanged) onChangeRef.current(u.state.doc.toString());
          }),
        ],
      }),
    });
    view.current = v;
    return () => v.destroy();
    // The editor owns its document after mount; outside changes go
    // through the effect below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    const v = view.current;
    if (v && v.state.doc.toString() !== value) {
      v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value } });
    }
  }, [value]);

  useImperativeHandle(ref, () => ({
    runnable: () => {
      const v = view.current;
      if (!v) return value;
      const sel = v.state.selection.main;
      const text = sel.empty ? "" : v.state.sliceDoc(sel.from, sel.to);
      return text.trim() ? text : v.state.doc.toString();
    },
    focus: () => view.current?.focus(),
  }));

  return <div ref={host} className="rounded-md border border-line" />;
});

const viewerTheme = EditorView.theme({
  "&": { height: "100%", borderRadius: "0" },
  "&.cm-focused": { outline: "none" },
  ".cm-scroller": { maxHeight: "none", height: "100%" },
});

/** Read-only SQL with the editor's highlighting: the table editor's
 * Definition view. */
export function SqlViewer({ value, label }: { value: string; label: string }) {
  const host = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const v = new EditorView({
      parent: host.current!,
      state: EditorState.create({
        doc: value,
        extensions: [
          lineNumbers(),
          sql({ dialect: PostgreSQL }),
          syntaxHighlighting(highlight),
          theme,
          viewerTheme,
          EditorState.readOnly.of(true),
          EditorView.contentAttributes.of({ "aria-label": label, "data-testid": "table-definition" }),
        ],
      }),
    });
    return () => v.destroy();
  }, [value, label]);
  return <div ref={host} className="h-full min-h-0" />;
}
