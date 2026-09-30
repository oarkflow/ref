import { useEffect, useRef, useState } from "react";
import { quote, unquote } from "../../lib/bcl";
import { emitObjectList, freshName, getPair, parseObjectList, withPair, type ObjItem } from "../objectList";
import { useIntentIds, type FieldIO } from "./kit";

export interface Col {
  key: string;
  label: string;
  kind: "text" | "expr" | "intent" | "number";
  placeholder?: string;
  hint?: string;
  /** shown first in the row header */
  title?: boolean;
}

function Cell({ col, raw, onCommit, disabled, intents }: { col: Col; raw: string | undefined; onCommit(raw: string | undefined): void; disabled: boolean; intents: string[] }) {
  const cur = raw === undefined ? "" : (unquote(raw) ?? raw);
  const [text, setText] = useState(cur);
  const last = useRef(cur);
  useEffect(() => {
    if (cur !== last.current) {
      last.current = cur;
      setText(cur);
    }
  }, [cur]);
  const commit = () => {
    if (text === last.current) return;
    last.current = text;
    if (text === "") return onCommit(undefined);
    onCommit(col.kind === "number" ? (/^-?\d+(\.\d+)?$/.test(text) ? text : quote(text)) : quote(text));
  };
  const id = `${col.key}-${Math.abs(hash(col.label + col.key))}`;
  if (col.kind === "intent") {
    const known = intents.includes(cur);
    return (
      <label className="cv-cell">
        <span>{col.label}</span>
        <select value={cur} disabled={disabled} onChange={(e) => onCommit(e.target.value ? quote(e.target.value) : undefined)}>
          <option value="">—</option>
          {!known && cur && <option value={cur}>{cur}</option>}
          {intents.map((i) => (
            <option key={i} value={i}>{i}</option>
          ))}
        </select>
      </label>
    );
  }
  return (
    <label className="cv-cell" htmlFor={id}>
      <span>{col.label}</span>
      {col.kind === "expr" ? (
        <textarea id={id} className="mono" rows={2} value={text} disabled={disabled} placeholder={col.placeholder} spellCheck={false} onChange={(e) => setText(e.target.value)} onBlur={commit} />
      ) : (
        <input id={id} value={text} disabled={disabled} placeholder={col.placeholder} spellCheck={false} inputMode={col.kind === "number" ? "decimal" : undefined} onChange={(e) => setText(e.target.value)} onBlur={commit} />
      )}
    </label>
  );
}

const hash = (s: string) => [...s].reduce((h, c) => (h * 31 + c.charCodeAt(0)) | 0, 7);

/**
 * A list of inline objects (cases, rules, branches) as rows of labelled fields.
 * Lists that cannot be read exactly (comments, odd syntax) stay in a text box:
 * nothing is rewritten that the editor did not understand.
 */
export function ObjectListEditor({ io, cols, rowLabel, addLabel, seed, readOnly, empty }: {
  io: FieldIO; cols: Col[]; rowLabel: string; addLabel: string; seed: string; readOnly: boolean; empty: string;
}) {
  const intents = useIntentIds();
  const items = io.raw === undefined ? [] : parseObjectList(io.raw);
  if (items === null) {
    return (
      <div className="cv-field" data-path={io.path}>
        <p className="hint">This list uses syntax the visual editor does not change (for example comments). Edit it as text.</p>
        <RawList io={io} readOnly={readOnly} />
      </div>
    );
  }
  const write = (next: ObjItem[]) => io.set(next.length ? emitObjectList(next) : "[]");
  const move = (i: number, d: -1 | 1) => {
    const j = i + d;
    if (j < 0 || j >= items.length) return;
    const next = [...items];
    [next[i], next[j]] = [next[j]!, next[i]!];
    write(next);
  };
  return (
    <div className="cv-objlist" data-path={io.path}>
      {items.length === 0 && <p className="cv-none">{empty}</p>}
      {items.map((it, i) => {
        const titleCol = cols.find((c) => c.title) ?? cols[0]!;
        const title = getPair(it, titleCol.key);
        return (
          <fieldset key={i} className="cv-objrow">
            <legend>
              <span>{rowLabel} {i + 1}</span>
              {title && <em>{unquote(title) ?? title}</em>}
            </legend>
            <div className="cv-objgrid">
              {cols.map((c) => (
                <Cell key={c.key} col={c} raw={getPair(it, c.key)} disabled={readOnly} intents={intents} onCommit={(raw) => write(items.map((x, k) => (k === i ? withPair(x, c.key, raw) : x)))} />
              ))}
            </div>
            {!readOnly && (
              <div className="cv-rowtools">
                <button type="button" onClick={() => move(i, -1)} disabled={i === 0} aria-label={`Move ${rowLabel} ${i + 1} up`}>↑</button>
                <button type="button" onClick={() => move(i, 1)} disabled={i === items.length - 1} aria-label={`Move ${rowLabel} ${i + 1} down`}>↓</button>
                <button type="button" className="danger" onClick={() => write(items.filter((_, k) => k !== i))} aria-label={`Remove ${rowLabel} ${i + 1}`}>Remove</button>
              </div>
            )}
          </fieldset>
        );
      })}
      {!readOnly && (
        <button type="button" className="cv-addrow" onClick={() => write([...items, { pairs: [{ key: seed, raw: quote(freshName(items, rowLabel.toLowerCase())) }] }])}>
          + {addLabel}
        </button>
      )}
    </div>
  );
}

function RawList({ io, readOnly }: { io: FieldIO; readOnly: boolean }) {
  const [text, setText] = useState(io.raw ?? "");
  useEffect(() => setText(io.raw ?? ""), [io.raw]);
  return <textarea className="mono" rows={6} value={text} disabled={readOnly} spellCheck={false} onChange={(e) => setText(e.target.value)} onBlur={() => text !== (io.raw ?? "") && io.set(text.trim() || undefined)} />;
}
