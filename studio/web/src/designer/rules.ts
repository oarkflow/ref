// A small, safe builder for the conditions pipelines use (visible_if, required_if,
// disabled_if): "form.field == 'value'" clauses joined by && or ||. Anything
// else stays text, so nothing an author wrote by hand is ever rewritten.

export type RuleOp = "==" | "!=" | ">" | "<" | ">=" | "<=" | "empty" | "filled" | "has" | "in";
export type ValueKind = "string" | "bool" | "number";

export interface Clause { field: string; op: RuleOp; value: string; kind: ValueKind; /** For "in": the listed values. */ values?: string[] }
export interface Condition { join: "&&" | "||"; clauses: Clause[] }

export const OP_LABEL: Record<RuleOp, string> = {
  "==": "is", "!=": "is not", ">": "is greater than", "<": "is less than", ">=": "is at least", "<=": "is at most", empty: "is empty", filled: "is filled in", has: "includes", in: "is one of",
};

const REF = "[A-Za-z_][A-Za-z0-9_]*(?:\\.[A-Za-z_][A-Za-z0-9_]*)*";
const CLAUSE = new RegExp(`^\\s*(${REF})\\s*(==|!=|>=|<=|>|<)\\s*('(?:[^'\\\\]|\\\\.)*'|true|false|-?\\d+(?:\\.\\d+)?)\\s*$`);
// `field empty` / `field not empty` are true for a value that was never entered, a blank one and zero.
// `field == ''` is false (and `!= ''` true) for a value never entered, so they are not "empty" / "filled in".
const HAS = new RegExp(`^\\s*(${REF})\\s+has\\s+('(?:[^'\\\\]|\\\\.)*')\\s*$`);
const IN = new RegExp(`^\\s*(${REF})\\s+in\\s+\\[((?:\\s*'(?:[^'\\\\]|\\\\.)*'\\s*,?)*)\\]\\s*$`);
const EMPTY = new RegExp(`^\\s*(${REF})\\s+(not\\s+)?empty\\s*$`);

/** Splits on a top-level operator, ignoring text inside quotes; null when parentheses or `!` make it more than a flat list. */
function splitTop(expr: string, op: "&&" | "||"): string[] | null {
  const parts: string[] = [];
  let cur = "", quote = false;
  for (let i = 0; i < expr.length; i++) {
    const ch = expr[i]!;
    if (quote) { cur += ch; if (ch === "\\") cur += expr[++i] ?? ""; else if (ch === "'") quote = false; continue; }
    if (ch === "'") { quote = true; cur += ch; continue; }
    if (ch === "(" || ch === ")") return null;
    if (expr.startsWith(op, i)) { parts.push(cur); cur = ""; i += op.length - 1; continue; }
    cur += ch;
  }
  parts.push(cur);
  return parts;
}

const unq = (s: string) => s.slice(1, -1).replace(/\\(.)/g, "$1");
const esc = (s: string) => "'" + s.replace(/\\/g, "\\\\").replace(/'/g, "\\'") + "'";

/** The condition as rows, or null when the text is not a flat list of simple comparisons. */
export function parseCondition(expr: string): Condition | null {
  const text = expr.trim();
  if (!text) return { join: "&&", clauses: [] };
  const hasAnd = splitTop(text, "&&"), hasOr = splitTop(text, "||");
  if (!hasAnd || !hasOr) return null;
  if (hasAnd.length > 1 && hasOr.length > 1) return null; // mixed: precedence is for a human to read
  const join: "&&" | "||" = hasOr.length > 1 ? "||" : "&&";
  const clauses: Clause[] = [];
  for (const part of join === "||" ? hasOr : hasAnd) {
    const h = HAS.exec(part);
    if (h) { clauses.push({ field: h[1]!, op: "has", value: unq(h[2]!), kind: "string" }); continue; }
    const inn = IN.exec(part);
    if (inn) { clauses.push({ field: inn[1]!, op: "in", value: "", kind: "string", values: [...inn[2]!.matchAll(/'((?:[^'\\\\]|\\\\.)*)'/g)].map((x) => unq(`'${x[1]}'`)) }); continue; }
    const e = EMPTY.exec(part);
    if (e) { clauses.push({ field: e[1]!, op: e[2] ? "filled" : "empty", value: "", kind: "string" }); continue; }
    const m = CLAUSE.exec(part);
    if (!m) return null;
    const [, field, op, raw] = m as unknown as [string, string, RuleOp, string];
    if (raw.startsWith("'")) clauses.push({ field, op, value: unq(raw), kind: "string" }); else if (raw === "true" || raw === "false") {
      if (op !== "==" && op !== "!=") return null;
      clauses.push({ field, op, value: raw, kind: "bool" });
    } else clauses.push({ field, op, value: raw, kind: "number" });
  }
  return { join, clauses };
}

export function buildClause(c: Clause): string {
  if (c.op === "has") return `${c.field} has ${esc(c.value)}`;
  if (c.op === "in") return `${c.field} in [${(c.values ?? []).map(esc).join(", ")}]`;
  if (c.op === "empty") return `${c.field} empty`;
  if (c.op === "filled") return `${c.field} not empty`;
  if (c.kind === "bool") return `${c.field} ${c.op} ${c.value === "false" ? "false" : "true"}`;
  if (c.kind === "number" && /^-?\d+(\.\d+)?$/.test(c.value)) return `${c.field} ${c.op} ${c.value}`;
  return `${c.field} ${c.op} ${esc(c.value)}`;
}

export function buildCondition(cond: Condition): string {
  return cond.clauses.filter((c) => c.field && !(c.op === "in" && !(c.values ?? []).length)).map(buildClause).join(` ${cond.join} `);
}

/** Operators that make sense for a field kind. */
export function opsFor(kind: string): RuleOp[] {
  if (kind === "roles") return ["has"];
  if (kind === "workspace") return ["==", "!=", "in"];
  if (kind === "boolean") return ["==", "!="];
  if (["number", "integer", "date", "datetime"].includes(kind)) return ["==", "!=", ">", "<", ">=", "<=", "empty", "filled"];
  return ["==", "!=", "empty", "filled"];
}

/** A comparison with a blank text value: it misbehaves for a field that was never filled in. */
export function isBlankComparison(c: Clause): boolean {
  return c.kind === "string" && c.value === "" && (c.op === "==" || c.op === "!=");
}
