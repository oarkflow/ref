// The code editor for templates and static files (CodeMirror 6). This module is
// only ever imported lazily, so CodeMirror stays out of the first-load bundle.
import { closeBrackets, closeBracketsKeymap } from "@codemirror/autocomplete";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import { css } from "@codemirror/lang-css";
import { html } from "@codemirror/lang-html";
import { javascript } from "@codemirror/lang-javascript";
import { json } from "@codemirror/lang-json";
import { bracketMatching, defaultHighlightStyle, indentOnInput, syntaxHighlighting } from "@codemirror/language";
import { lintGutter, setDiagnostics, type Diagnostic as CmDiagnostic } from "@codemirror/lint";
import { highlightSelectionMatches, searchKeymap } from "@codemirror/search";
import { Compartment, EditorState, type Extension } from "@codemirror/state";
import {
  Decoration, drawSelection, EditorView, highlightActiveLine, highlightActiveLineGutter, keymap, lineNumbers,
  MatchDecorator, ViewPlugin, type DecorationSet, type ViewUpdate,
} from "@codemirror/view";
import { forwardRef, useEffect, useImperativeHandle, useRef } from "react";
import type { Diagnostic } from "../api/types";
import { languageOf } from "./lib";

export interface CodeEditorHandle {
  /** Writes text at the cursor (replacing a selection) and focuses the editor. */
  insert(text: string): void;
  focus(): void;
  /** Moves the cursor to a line and scrolls it into view. */
  reveal(line: number): void;
}

export interface CodeEditorProps {
  path: string;
  value: string;
  readOnly?: boolean;
  diagnostics?: Diagnostic[];
  onChange?(value: string): void;
  onSave?(): void;
}

// SPL: `${expr}` and `@directive` get their own colours on top of HTML.
const splMatcher = new MatchDecorator({
  regexp: /\$\{[^}\n]*\}|@(?:extends|define|block|include|import|if|else|elseif|for|let|set|slot|fill|component|example|local|computed|signal)\b/g,
  decoration: (m) => Decoration.mark({ class: m[0].startsWith("$") ? "cm-spl-expr" : "cm-spl-directive" }),
});

const splHighlight = ViewPlugin.fromClass(
  class {
    decorations: DecorationSet;
    constructor(view: EditorView) {
      this.decorations = splMatcher.createDeco(view);
    }
    update(u: ViewUpdate) {
      this.decorations = splMatcher.updateDeco(u, this.decorations);
    }
  },
  { decorations: (v) => v.decorations },
);

// Colours come from the app's CSS variables, so light and dark follow the theme.
const theme = EditorView.theme({
  "&": { height: "100%", fontSize: "13px", backgroundColor: "var(--surface)", color: "var(--text)" },
  ".cm-scroller": { fontFamily: "var(--mono)", lineHeight: "1.6" },
  ".cm-content": { padding: "12px 0", caretColor: "var(--accent)" },
  ".cm-gutters": { backgroundColor: "var(--surface-2)", color: "var(--faint)", border: "none", borderRight: "1px solid var(--border)" },
  ".cm-activeLine": { backgroundColor: "color-mix(in srgb, var(--accent) 6%, transparent)" },
  ".cm-activeLineGutter": { backgroundColor: "color-mix(in srgb, var(--accent) 10%, transparent)", color: "var(--text)" },
  "&.cm-focused": { outline: "none" },
  "&.cm-focused .cm-selectionBackground, .cm-selectionBackground": { backgroundColor: "color-mix(in srgb, var(--accent) 22%, transparent)" },
  ".cm-lintRange-error": { backgroundImage: "none", textDecoration: "underline wavy var(--danger)", textUnderlineOffset: "3px" },
  ".cm-lintRange-warning": { backgroundImage: "none", textDecoration: "underline wavy var(--warn)", textUnderlineOffset: "3px" },
  ".cm-tooltip": { border: "1px solid var(--border)", borderRadius: "8px", backgroundColor: "var(--surface)", boxShadow: "var(--shadow-2)" },
  ".cm-spl-expr": { color: "var(--code-expr, #0f766e)", fontWeight: "600" },
  ".cm-spl-directive": { color: "var(--code-directive, #7c3aed)", fontWeight: "600" },
});

async function languageFor(path: string): Promise<Extension> {
  switch (languageOf(path)) {
    case "html": return [html(), splHighlight];
    case "css": return css();
    case "js": return javascript();
    case "json": return json();
    default: return [];
  }
}

/** Converts server diagnostics (1-based line/column) into CodeMirror ones. */
export function toCmDiagnostics(doc: EditorState["doc"], list: Diagnostic[]): CmDiagnostic[] {
  const out: CmDiagnostic[] = [];
  for (const d of list) {
    const lineNo = Math.min(Math.max(d.line ?? 1, 1), doc.lines);
    const line = doc.line(lineNo);
    const from = Math.min(line.from + Math.max((d.column ?? 1) - 1, 0), line.to);
    const to = from < line.to ? Math.min(from + Math.max(wordLength(line.text, from - line.from), 1), line.to) : line.to;
    out.push({ from, to: Math.max(to, from), severity: d.severity === "error" ? "error" : d.severity === "warning" ? "warning" : "info", message: d.message });
  }
  return out;
}

const wordLength = (text: string, at: number) => (/^[\w@$]+/.exec(text.slice(at))?.[0].length ?? 1);

export const CodeEditor = forwardRef<CodeEditorHandle, CodeEditorProps>(function CodeEditor(props, ref) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const latest = useRef(props);
  latest.current = props;
  const lang = useRef(new Compartment());
  const editable = useRef(new Compartment());

  useEffect(() => {
    const el = host.current;
    if (!el) return;
    const state = EditorState.create({
      doc: props.value,
      extensions: [
        lineNumbers(),
        highlightActiveLineGutter(),
        history(),
        drawSelection(),
        indentOnInput(),
        bracketMatching(),
        closeBrackets(),
        highlightActiveLine(),
        highlightSelectionMatches(),
        lintGutter(),
        syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
        EditorView.lineWrapping,
        keymap.of([
          { key: "Mod-s", preventDefault: true, run: () => (latest.current.onSave?.(), true) },
          ...closeBracketsKeymap, ...defaultKeymap, ...searchKeymap, ...historyKeymap, indentWithTab,
        ]),
        lang.current.of([]),
        editable.current.of([EditorView.editable.of(!props.readOnly), EditorState.readOnly.of(!!props.readOnly)]),
        theme,
        EditorView.updateListener.of((u) => {
          if (u.docChanged) latest.current.onChange?.(u.state.doc.toString());
        }),
      ],
    });
    const v = new EditorView({ state, parent: el });
    view.current = v;
    return () => {
      v.destroy();
      view.current = null;
    };
    // The editor is created once per file; content and flags are pushed in below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [props.path]);

  // Language follows the file type.
  useEffect(() => {
    let alive = true;
    void languageFor(props.path).then((ext) => {
      if (alive) view.current?.dispatch({ effects: lang.current.reconfigure(ext) });
    });
    return () => { alive = false; };
  }, [props.path]);

  // External content changes (undo elsewhere, another editor) replace the text.
  useEffect(() => {
    const v = view.current;
    if (!v) return;
    const cur = v.state.doc.toString();
    if (cur !== props.value) v.dispatch({ changes: { from: 0, to: cur.length, insert: props.value } });
  }, [props.value]);

  useEffect(() => {
    view.current?.dispatch({ effects: editable.current.reconfigure([EditorView.editable.of(!props.readOnly), EditorState.readOnly.of(!!props.readOnly)]) });
  }, [props.readOnly]);

  useEffect(() => {
    const v = view.current;
    if (!v) return;
    v.dispatch(setDiagnostics(v.state, toCmDiagnostics(v.state.doc, props.diagnostics ?? [])));
  }, [props.diagnostics, props.value, props.path]);

  useImperativeHandle(ref, () => ({
    insert(text) {
      const v = view.current;
      if (!v) return;
      const r = v.state.selection.main;
      v.dispatch({ changes: { from: r.from, to: r.to, insert: text }, selection: { anchor: r.from + text.length }, scrollIntoView: true });
      v.focus();
    },
    focus: () => view.current?.focus(),
    reveal(line) {
      const v = view.current;
      if (!v) return;
      const l = v.state.doc.line(Math.min(Math.max(line, 1), v.state.doc.lines));
      v.dispatch({ selection: { anchor: l.from }, effects: EditorView.scrollIntoView(l.from, { y: "center" }) });
      v.focus();
    },
  }));

  return <div className="code-editor" ref={host} data-testid="code-editor" />;
});

export default CodeEditor;
