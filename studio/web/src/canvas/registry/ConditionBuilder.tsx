import { useEffect, useId, useRef, useState } from "react";
import { Problems, useDiagnostics } from "../../forms/ctx";
import { Icon } from "../icons";
import { friendlyCondition } from "../look";
import {
  OPS, blankClause, compileCondition, needsValue, parseCondition, withOp, withValue, type Clause, type Condition,
} from "./conditions";

interface Props {
  label: string;
  /** The formula, without quotes. Empty / undefined when there is none. */
  value: string | undefined;
  /** Model path of the field, for problems shown under it. */
  path: string;
  onChange(formula: string | undefined): void;
  /** Names to suggest on the left ("result.action", the facts a step reads). */
  suggestions?: readonly string[];
  readOnly?: boolean;
  help?: string;
  placeholder?: string;
  /** "Only follow this when ..." wording for the joiner. */
  lead?: string;
}

/**
 * Builds a condition from clauses ("total is over 1000, and role is admin") and
 * writes the formula. A formula the builder cannot represent stays a formula: it
 * is shown as text and never rewritten behind the user's back.
 */
export function ConditionBuilder({ label, value, path, onChange, suggestions = [], readOnly, help, placeholder, lead = "Follow this when" }: Props) {
  const id = useId();
  const listId = `${id}-facts`;
  const text = value ?? "";
  const parsed = parseCondition(text);
  const [formulaMode, setFormulaMode] = useState(false);
  const { here } = useDiagnostics(path);

  // The rows on screen. They follow the saved formula, but a row that is still being filled in (no formula
  // yet) lives only here, so adding a check does not make it vanish before you have typed anything.
  const [draft, setDraft] = useState<Condition>(parsed ?? { join: "and", clauses: [] });
  const lastFormula = useRef(compileCondition(draft));
  useEffect(() => {
    if (text !== lastFormula.current) {
      lastFormula.current = text;
      if (parsed) setDraft(parsed);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [text]);

  // A formula the builder can't read is edited as a formula.
  const asFormula = formulaMode || parsed === null;

  const commit = (c: Condition) => {
    setDraft(c);
    const expr = compileCondition(c);
    lastFormula.current = expr;
    if (expr === text) return; // nothing to write (e.g. a row still being filled in): no edit, and never a remove of what is not there
    onChange(expr === "" ? undefined : expr);
  };
  const cond: Condition = draft;
  const setClause = (i: number, next: Clause) => commit({ ...cond, clauses: cond.clauses.map((c, j) => (j === i ? next : c)) });
  const reads = text.trim() ? friendlyCondition(text) : null;

  return (
    <div className="cv-field cv-cond" data-path={path}>
      <label htmlFor={asFormula ? `${id}-formula` : undefined}>{label}</label>
      {asFormula ? (
        <FormulaBox id={`${id}-formula`} value={text} onCommit={(t) => onChange(t.trim() === "" ? undefined : t)} readOnly={readOnly} placeholder={placeholder} />
      ) : (
        <div className="cv-cond-body">
          {cond.clauses.length > 1 && (
            <div className="cv-cond-join">
              <span>{lead}</span>
              <select aria-label="How the checks combine" value={cond.join} disabled={readOnly} onChange={(e) => commit({ ...cond, join: e.target.value as "and" | "or" })}>
                <option value="and">all of these</option>
                <option value="or">any of these</option>
              </select>
            </div>
          )}
          {cond.clauses.length === 0 && <p className="cv-none">Always. Add a check to make it depend on something.</p>}
          {cond.clauses.map((c, i) => (
            <div className="cv-clause" key={i}>
              <input
                aria-label={`Check ${i + 1}: what to look at`}
                list={listId}
                value={c.left}
                placeholder="e.g. result.action"
                spellCheck={false}
                disabled={readOnly}
                onChange={(e) => setClause(i, { ...c, left: e.target.value })}
              />
              <select aria-label={`Check ${i + 1}: how to compare`} value={c.op} disabled={readOnly} onChange={(e) => setClause(i, withOp(c, e.target.value as Clause["op"]))}>
                {OPS.map((o) => <option key={o.op} value={o.op}>{o.label}</option>)}
              </select>
              {needsValue(c.op) ? (
                <ValueInput aria-label={`Check ${i + 1}: the value`} value={c.right} onCommit={(t) => setClause(i, withValue(c, t))} disabled={readOnly} />
              ) : (
                <span className="cv-clause-gap" />
              )}
              {!readOnly && (
                <button type="button" className="cv-x" aria-label={`Remove check ${i + 1}`} onClick={() => commit({ ...cond, clauses: cond.clauses.filter((_, j) => j !== i) })}>
                  <Icon name="x" size={12} />
                </button>
              )}
            </div>
          ))}
          <datalist id={listId}>{suggestions.map((s) => <option key={s} value={s} />)}</datalist>
          {!readOnly && (
            <button type="button" className="cv-addrow" onClick={() => commit({ ...cond, clauses: [...cond.clauses, blankClause("")] })}>
              <Icon name="plus" size={12} /> Add a check
            </button>
          )}
        </div>
      )}
      <div className="cv-cond-foot">
        {reads && !asFormula && <small className="cv-reads">Reads as: {reads}</small>}
        {parsed === null && <small>This is written as a formula, so it is shown as one.</small>}
        {parsed !== null && !readOnly && (
          <button type="button" className="link" onClick={() => setFormulaMode((m) => !m)}>{formulaMode ? "Use the builder" : "Edit as formula"}</button>
        )}
      </div>
      <Problems items={here} />
      {help && <small>{help}</small>}
    </div>
  );
}

function FormulaBox({ id, value, onCommit, readOnly, placeholder }: { id: string; value: string; onCommit(t: string): void; readOnly?: boolean; placeholder?: string }) {
  const [text, setText] = useState(value);
  const last = useRef(value);
  useEffect(() => {
    if (value !== last.current) {
      last.current = value;
      setText(value);
    }
  }, [value]);
  return (
    <textarea
      id={id}
      className="mono"
      rows={2}
      value={text}
      placeholder={placeholder ?? "e.g. result.action == 'approve'"}
      disabled={readOnly}
      spellCheck={false}
      onChange={(e) => setText(e.target.value)}
      onBlur={() => {
        if (text === last.current) return;
        last.current = text;
        onCommit(text);
      }}
    />
  );
}

/** A text input that commits when focus leaves it, so typing is not one edit per key. */
function ValueInput({ value, onCommit, disabled, ...rest }: { value: string; onCommit(t: string): void; disabled?: boolean; "aria-label": string }) {
  const [text, setText] = useState(value);
  const last = useRef(value);
  useEffect(() => {
    if (value !== last.current) {
      last.current = value;
      setText(value);
    }
  }, [value]);
  return (
    <input
      {...rest}
      value={text}
      disabled={disabled}
      spellCheck={false}
      placeholder="value"
      onChange={(e) => setText(e.target.value)}
      onBlur={() => {
        if (text === last.current) return;
        last.current = text;
        onCommit(text);
      }}
      onKeyDown={(e) => e.key === "Enter" && (e.target as HTMLInputElement).blur()}
    />
  );
}
