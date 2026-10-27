import { useEffect, useState } from "react";
import type { FieldRef } from "./model";
import { OP_LABEL, buildCondition, isBlankComparison, opsFor, parseCondition, type Clause, type Condition, type RuleOp } from "./rules";

interface Props { label: string; value: string; fields: FieldRef[]; disabled?: boolean; hint?: string; onCommit(expr: string): void }

/** Edits a condition as rows ("Claim status is Hold") joined by and/or, with the expression text one click away. */
/** A sensible first row for a field: a choice starts at its first option, a yes/no at yes, anything else at "is filled in". */
function startClause(f: FieldRef): Clause {
  if (f.kind === "roles") return { field: f.ref, op: "has", value: f.options[0] ?? "", kind: "string" };
  if (f.kind === "boolean") return { field: f.ref, op: "==", value: "true", kind: "bool" };
  if (f.options.length > 0) return { field: f.ref, op: "==", value: f.options[0]!, kind: "string" };
  return { field: f.ref, op: "filled", value: "", kind: ["number", "integer"].includes(f.kind) ? "number" : "string" };
}

export function RuleEditor({ label, value, fields, disabled, hint, onCommit }: Props) {
  const parsed = parseCondition(value);
  const [text, setText] = useState(value);
  const [asText, setAsText] = useState(false);
  useEffect(() => setText(value), [value]);
  const showText = asText || parsed === null;
  const cond: Condition = parsed ?? { join: "&&", clauses: [] };

  const change = (next: Condition) => onCommit(buildCondition(next));
  const setClause = (i: number, patch: Partial<Clause>) => change({ ...cond, clauses: cond.clauses.map((c, k) => (k === i ? { ...c, ...patch } : c)) });
  const refOf = (ref: string) => fields.find((f) => f.ref === ref);

  const pickField = (i: number, ref: string) => {
    const f = refOf(ref);
    if (f) { const c = startClause(f); setClause(i, { field: c.field, kind: c.kind, op: c.op, value: c.value, values: undefined }); }
  };

  return (
    <div className="dz-rule">
      <div className="dz-rulehead"><b>{label}</b>
        {parsed !== null && <button type="button" className="dz-link" disabled={disabled} onClick={() => setAsText(!asText)}>{showText ? "Use the builder" : "Edit as text"}</button>}
      </div>
      {showText ? (
        <>
          <textarea rows={2} disabled={disabled} value={text} aria-label={label} onChange={(e) => setText(e.target.value)} onBlur={() => text !== value && onCommit(text.trim())} placeholder="e.g. billing.claim_status == 'Hold'" />
          {parsed === null && <small className="dz-hint">This condition is more than the builder can show, so it is kept as text.</small>}
        </>
      ) : (
        <>
          {cond.clauses.length === 0 && <small className="dz-hint">{hint ?? "No condition."}</small>}
          {cond.clauses.map((c, i) => {
            const f = refOf(c.field);
            const ops = opsFor(f?.kind ?? (c.kind === "bool" ? "boolean" : "text"));
            if (!ops.includes(c.op)) ops.push(c.op);
            const needsValue = c.op !== "empty" && c.op !== "filled";
            return (
              <div key={i} className="dz-rulerow">
                {i > 0 && <button type="button" className="dz-join" disabled={disabled || i > 1} title={i > 1 ? "All conditions use the same join" : "Switch between and / or"} onClick={() => change({ ...cond, join: cond.join === "&&" ? "||" : "&&" })}>{cond.join === "&&" ? "and" : "or"}</button>}
                <select aria-label="Field" disabled={disabled} value={c.field} onChange={(e) => pickField(i, e.target.value)}>
                  {!f && <option value={c.field}>{c.field || "choose a field…"}</option>}
                  {fields.map((x) => <option key={x.ref} value={x.ref}>{x.label}</option>)}
                </select>
                <select aria-label="Operator" disabled={disabled} value={c.op} onChange={(e) => { const op = e.target.value as RuleOp; setClause(i, { op, values: op === "in" ? (c.values?.length ? c.values : f?.options.slice(0, 1) ?? []) : undefined }); }}>
                  {ops.map((o) => <option key={o} value={o}>{OP_LABEL[o]}</option>)}
                </select>
                {needsValue && (c.op === "in"
                  ? (
                    <select aria-label="Values" multiple disabled={disabled} className="dz-multi" value={c.values ?? []}
                      onChange={(e) => setClause(i, { values: [...e.target.selectedOptions].map((o) => o.value) })}>
                      {(f?.options ?? []).map((o) => <option key={o} value={o}>{f?.labels?.[o] ?? o}</option>)}
                    </select>
                  )
                  : c.kind === "bool"
                    ? <select aria-label="Value" disabled={disabled} value={c.value} onChange={(e) => setClause(i, { value: e.target.value })}><option value="true">yes</option><option value="false">no</option></select>
                    : f && f.options.length > 0
                      ? <select aria-label="Value" disabled={disabled} value={c.value} onChange={(e) => setClause(i, { value: e.target.value })}>{!f.options.includes(c.value) && <option value={c.value}>{c.value}</option>}{f.options.map((o) => <option key={o} value={o}>{f.labels?.[o] ?? o}</option>)}</select>
                      : <RuleValue disabled={disabled} value={c.value} onCommit={(v) => setClause(i, { value: v })} />)}
                {isBlankComparison(c) && (
                  <small className="dz-hint err dz-blankwarn">A field nobody has filled in yet is not equal to a blank value, so this can misfire.
                    <button type="button" className="dz-link" disabled={disabled} onClick={() => setClause(i, { op: c.op === "==" ? "empty" : "filled" })}>Use "{c.op === "==" ? OP_LABEL.empty : OP_LABEL.filled}" instead</button></small>
                )}
                <button type="button" aria-label="Remove condition" className="dz-link danger" disabled={disabled} onClick={() => change({ ...cond, clauses: cond.clauses.filter((_, k) => k !== i) })}>×</button>
              </div>
            );
          })}
          <button type="button" className="dz-link" disabled={disabled || fields.length === 0} onClick={() => change({ ...cond, clauses: [...cond.clauses, startClause(fields[0]!)] })}>+ Add a condition</button>
        </>
      )}
    </div>
  );
}

function RuleValue({ value, disabled, onCommit }: { value: string; disabled?: boolean; onCommit(v: string): void }) {
  const [v, setV] = useState(value);
  useEffect(() => setV(value), [value]);
  return <input aria-label="Value" disabled={disabled} value={v} onChange={(e) => setV(e.target.value)} onBlur={() => v !== value && onCommit(v)} />;
}
