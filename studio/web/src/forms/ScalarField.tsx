import { X } from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";
import { MODE_LABELS } from "../labels";
import {
  detectMode, emitEnv, emitLiteral, parseEnv, parseLiteral, type Mode, type ScalarKind,
} from "../lib/bcl";
import { useUi } from "../state/ui";
import { Problems, useDiagnostics } from "./ctx";

export interface ScalarProps {
  /** The field's name; used for accessible names and ids. */
  label: string;
  /** What to show as the label instead of `label` (a friendly name). */
  display?: string;
  kind: ScalarKind;
  /** Current raw BCL; undefined when the field is not set. */
  raw: string | undefined;
  /** Model path of the field (for diagnostics, focus and test ids). */
  path: string;
  /** Called with the new raw BCL, or undefined to remove the field. */
  onCommit(raw: string | undefined): void;
  doc?: string;
  /** Short help shown under the input. Falls back to the first sentence of `doc`. */
  help?: string;
  required?: boolean;
  placeholder?: string;
  options?: string[];
  /** Type-ahead suggestions for a free-text literal. */
  suggestions?: string[];
  /** List items and map values: no remove button, tighter layout. */
  compact?: boolean;
  onRemove?(): void;
  readOnly?: boolean;
}

const MODES: { id: Mode; label: string }[] = [
  { id: "literal", label: MODE_LABELS.literal.label },
  { id: "env", label: MODE_LABELS.env.label },
  { id: "expr", label: MODE_LABELS.expr.label },
];

/** Plain-language versions of the low-level input errors. */
export function friendlyInputError(msg: string): string {
  if (/whole number/.test(msg)) return "Use a whole number, like 3.";
  if (/duration/.test(msg)) return "Use a length of time, like 500ms, 30s, 5m or 1h30m.";
  if (/^expected a number/.test(msg)) return "Use a number, like 2.5.";
  if (/true or false/.test(msg)) return "Choose on or off.";
  if (/bare identifier/.test(msg)) return "Use letters, digits and _ . - only, with no spaces or quotes.";
  return msg;
}

/**
 * One scalar setting. Every scalar can be a fixed value, an environment lookup
 * or a formula; the mode is inferred from the current value and can be
 * switched by the user. Nothing is sent until the input is valid.
 */
export function ScalarField(p: ScalarProps) {
  const id = useId();
  const dev = useUi((s) => s.devView);
  const { here } = useDiagnostics(p.path);
  const inferred = detectMode(p.raw, p.kind);
  const [mode, setMode] = useState<Mode>(inferred);
  const lastEmitted = useRef<string | undefined>(p.raw);

  // Follow server/undo changes, but not our own echoes.
  useEffect(() => {
    if (p.raw !== lastEmitted.current) {
      lastEmitted.current = p.raw;
      setMode(detectMode(p.raw, p.kind));
    }
  }, [p.raw, p.kind]);

  const emit = (raw: string | undefined) => {
    lastEmitted.current = raw;
    p.onCommit(raw);
  };
  const shownLabel = p.display ?? p.label;
  const help = p.help ?? (p.doc && !p.compact ? firstSentence(p.doc) : undefined);

  return (
    <div className={`field${p.compact ? " compact" : ""}${here.length ? " has-problem" : ""}${mode !== "literal" ? " alt-mode" : ""}`} data-path={p.path} data-kind={p.kind}>
      <div className="field-head">
        {!p.compact && (
          <label htmlFor={id} className="field-label" title={p.label !== shownLabel ? `${p.label}${p.doc ? ` — ${firstSentence(p.doc)}` : ""}` : p.doc}>
            {shownLabel}
            {p.required && <span className="req" aria-hidden="true"> *</span>}
            {dev && p.label !== shownLabel && <code className="raw-name">{p.label}</code>}
          </label>
        )}
        <div className="mode-switch" role="radiogroup" aria-label={`${p.label} value mode`}>
          {MODES.map((m) => (
            <button
              key={m.id}
              type="button"
              role="radio"
              aria-checked={mode === m.id}
              className={mode === m.id ? "on" : ""}
              disabled={p.readOnly}
              title={MODE_LABELS[m.id].hint}
              onClick={() => setMode(m.id)}
            >
              {m.label}
            </button>
          ))}
        </div>
        {(p.onRemove || !p.compact) && (
          <button
            type="button"
            className="icon-btn sm field-remove"
            aria-label={`Remove ${p.label}`}
            data-tip="Remove"
            disabled={p.readOnly || (p.raw === undefined && !p.onRemove)}
            onClick={() => (p.onRemove ? p.onRemove() : emit(undefined))}
          >
            <X size={15} />
          </button>
        )}
      </div>
      <div className="field-body">
        {mode === "literal" && <LiteralInput id={id} {...p} emit={emit} />}
        {mode === "env" && <EnvInput id={id} raw={p.raw} label={p.label} readOnly={p.readOnly} emit={emit} />}
        {mode === "expr" && <ExprInput id={id} raw={p.raw} label={p.label} readOnly={p.readOnly} emit={emit} />}
      </div>
      {help && mode === "literal" && <p className="field-doc">{help}</p>}
      <Problems items={here} />
    </div>
  );
}

function firstSentence(s: string): string {
  const flat = s.replace(/\s+/g, " ").trim();
  return flat.length > 200 ? flat.slice(0, 197) + "…" : flat;
}

function LiteralInput(p: ScalarProps & { id: string; emit(raw: string | undefined): void }) {
  const current = p.raw === undefined ? "" : (parseLiteral(p.raw, p.kind) ?? "");
  const [text, setText] = useState(current);
  const [error, setError] = useState<string | null>(null);
  const last = useRef(p.raw);

  useEffect(() => {
    if (p.raw !== last.current) {
      last.current = p.raw;
      setText(p.raw === undefined ? "" : (parseLiteral(p.raw, p.kind) ?? ""));
      setError(null);
    }
  }, [p.raw, p.kind]);

  if (p.kind === "bool") {
    const on = p.raw?.trim() === "true";
    return (
      <label className={`switch-ctl${on ? " on" : ""}`}>
        <input
          id={p.id}
          type="checkbox"
          checked={on}
          disabled={p.readOnly}
          onChange={(e) => {
            const raw = e.target.checked ? "true" : "false";
            last.current = raw;
            p.emit(raw);
          }}
        />
        <span className="switch-track" aria-hidden="true"><span className="switch-thumb" /></span>
        <span className="switch-text">{p.raw === undefined ? "Not set" : on ? "On" : "Off"}</span>
      </label>
    );
  }

  const change = (v: string) => {
    setText(v);
    if (v === "" && p.kind !== "string") {
      setError(null);
      return; // an empty non-string input means "leave as is"; use × to remove
    }
    const r = emitLiteral(v, p.kind);
    if (r.error) {
      setError(friendlyInputError(r.error));
      return;
    }
    setError(null);
    last.current = r.raw;
    p.emit(r.raw);
  };

  if (p.options) {
    const known = p.options.includes(text) || text === "";
    return (
      <select id={p.id} value={text} disabled={p.readOnly} onChange={(e) => change(e.target.value)}>
        <option value="">Not set</option>
        {!known && <option value={text}>{text} (custom)</option>}
        {p.options.map((o) => (
          <option key={o} value={o}>
            {o}
          </option>
        ))}
      </select>
    );
  }

  const listId = p.suggestions ? `${p.id}-sug` : undefined;
  const multiline = p.kind === "string" && text.includes("\n");
  return (
    <>
      {multiline ? (
        <textarea id={p.id} value={text} rows={4} disabled={p.readOnly} onChange={(e) => change(e.target.value)} />
      ) : (
        <input
          id={p.id}
          type="text"
          list={listId}
          value={text}
          placeholder={p.placeholder ?? (p.kind === "duration" ? "e.g. 30s, 5m, 1h" : undefined)}
          inputMode={p.kind === "int" || p.kind === "number" ? "numeric" : undefined}
          spellCheck={false}
          disabled={p.readOnly}
          aria-invalid={error ? true : undefined}
          aria-describedby={error ? `${p.id}-err` : undefined}
          onChange={(e) => change(e.target.value)}
        />
      )}
      {listId && (
        <datalist id={listId}>
          {p.suggestions!.map((s) => (
            <option key={s} value={s} />
          ))}
        </datalist>
      )}
      {error && (
        <p id={`${p.id}-err`} className="problem error shake" role="alert">
          {error}
        </p>
      )}
    </>
  );
}

function EnvInput(p: { id: string; raw: string | undefined; label: string; readOnly?: boolean; emit(raw: string | undefined): void }) {
  const parsed = p.raw ? parseEnv(p.raw) : null;
  const [name, setName] = useState(parsed?.name ?? "");
  const [def, setDef] = useState(parsed?.def ?? "");
  const last = useRef(p.raw);

  useEffect(() => {
    if (p.raw !== last.current) {
      last.current = p.raw;
      const e = p.raw ? parseEnv(p.raw) : null;
      setName(e?.name ?? "");
      setDef(e?.def ?? "");
    }
  }, [p.raw]);

  const push = (n: string, d: string) => {
    if (!n.trim()) return; // env("") is meaningless; wait for a name
    const raw = emitEnv(n.trim(), d);
    last.current = raw;
    p.emit(raw);
  };

  return (
    <div className="env-row">
      <label className="env-cell">
        <span className="env-cap">Environment variable</span>
        <input
          id={p.id}
          type="text"
          aria-label={`${p.label} environment variable`}
          placeholder="e.g. APP_PORT"
          value={name}
          spellCheck={false}
          disabled={p.readOnly}
          onChange={(e) => {
            setName(e.target.value);
            push(e.target.value, def);
          }}
        />
      </label>
      <label className="env-cell">
        <span className="env-cap">Fallback if not set</span>
        <input
          type="text"
          aria-label={`${p.label} default value`}
          placeholder="optional"
          value={def}
          disabled={p.readOnly}
          onChange={(e) => {
            setDef(e.target.value);
            push(name, e.target.value);
          }}
        />
      </label>
    </div>
  );
}

function ExprInput(p: { id: string; raw: string | undefined; label: string; readOnly?: boolean; emit(raw: string | undefined): void }) {
  const [text, setText] = useState(p.raw ?? "");
  const last = useRef(p.raw);
  useEffect(() => {
    if (p.raw !== last.current) {
      last.current = p.raw;
      setText(p.raw ?? "");
    }
  }, [p.raw]);
  return (
    <input
      id={p.id}
      type="text"
      className="mono"
      aria-label={`${p.label} expression`}
      placeholder="Formula, e.g. upper(input.name)"
      value={text}
      spellCheck={false}
      disabled={p.readOnly}
      onChange={(e) => {
        setText(e.target.value);
        if (e.target.value.trim()) {
          last.current = e.target.value;
          p.emit(e.target.value);
        }
      }}
    />
  );
}
