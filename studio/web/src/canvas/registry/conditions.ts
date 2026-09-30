// The visual condition builder's model. A condition is a list of simple clauses
// joined by "all of" or "any of"; it compiles to the formula the platform reads
// (`a == 'x' && b > 3`). Anything the builder cannot represent (calls, brackets,
// mixed && and ||) is refused by `parseCondition`, and the form falls back to the
// raw formula: nothing is ever rewritten in a way that changes its meaning.

export type Op = "==" | "!=" | ">" | ">=" | "<" | "<=" | "set" | "unset";
export type LitKind = "text" | "number" | "bool";

export interface Clause {
  left: string;
  op: Op;
  /** Ignored for `set` / `unset`. */
  right: string;
  kind: LitKind;
}

export interface Condition {
  join: "and" | "or";
  clauses: Clause[];
}

export const OPS: { op: Op; label: string; needsValue: boolean }[] = [
  { op: "==", label: "is", needsValue: true },
  { op: "!=", label: "is not", needsValue: true },
  { op: ">", label: "is over", needsValue: true },
  { op: ">=", label: "is at least", needsValue: true },
  { op: "<", label: "is under", needsValue: true },
  { op: "<=", label: "is at most", needsValue: true },
  { op: "set", label: "is set", needsValue: false },
  { op: "unset", label: "is not set", needsValue: false },
];

export const needsValue = (op: Op) => OPS.find((o) => o.op === op)?.needsValue ?? true;

const PATH = /^[A-Za-z_][\w]*(\.[A-Za-z_][\w]*)*$/;
const NUMBER = /^-?\d+(\.\d+)?$/;

/** Splits on a top-level operator, ignoring quoted text. Returns null when the operator appears inside brackets. */
function splitTop(expr: string, op: "&&" | "||"): string[] | null {
  const parts: string[] = [];
  let depth = 0;
  let quote: string | null = null;
  let start = 0;
  for (let i = 0; i < expr.length; i++) {
    const c = expr[i]!;
    if (quote) {
      if (c === "\\") i++;
      else if (c === quote) quote = null;
      continue;
    }
    if (c === "'" || c === '"') quote = c;
    else if (c === "(" || c === "[" || c === "{") depth++;
    else if (c === ")" || c === "]" || c === "}") depth--;
    else if (depth === 0 && expr.startsWith(op, i)) {
      parts.push(expr.slice(start, i));
      start = i + op.length;
      i += op.length - 1;
    }
  }
  if (quote || depth !== 0) return null;
  parts.push(expr.slice(start));
  return parts;
}

function unquoteLiteral(s: string): string | null {
  const q = s[0];
  if ((q !== "'" && q !== '"') || s.length < 2 || s[s.length - 1] !== q) return null;
  const body = s.slice(1, -1);
  let out = "";
  for (let i = 0; i < body.length; i++) {
    const c = body[i]!;
    if (c === "\\") {
      const n = body[++i];
      if (n === undefined) return null;
      out += n;
    } else if (c === q) return null; // an unescaped quote ends the literal early
    else out += c;
  }
  return out;
}

function parseClause(piece: string): Clause | null {
  const t = piece.trim();
  if (!t) return null;
  if (/^!\s*[A-Za-z_]/.test(t)) {
    const left = t.slice(1).trim();
    return PATH.test(left) ? { left, op: "unset", right: "", kind: "text" } : null;
  }
  if (PATH.test(t)) return { left: t, op: "set", right: "", kind: "text" };
  const m = /^([A-Za-z_][\w.]*)\s*(==|!=|>=|<=|>|<)\s*(.+)$/.exec(t);
  if (!m || !PATH.test(m[1]!)) return null;
  const rhs = m[3]!.trim();
  const op = m[2] as Op;
  const str = unquoteLiteral(rhs);
  if (str !== null) return { left: m[1]!, op, right: str, kind: "text" };
  if (NUMBER.test(rhs)) return { left: m[1]!, op, right: rhs, kind: "number" };
  if (rhs === "true" || rhs === "false") return { left: m[1]!, op, right: rhs, kind: "bool" };
  return null;
}

/** null when the formula is too involved for the builder. An empty formula is a condition with no clauses. */
export function parseCondition(expr: string): Condition | null {
  const t = expr.trim();
  if (!t) return { join: "and", clauses: [] };
  if (/^\(.*\)$/.test(t) && splitTop(t.slice(1, -1), "&&")?.length === 1 && splitTop(t.slice(1, -1), "||")?.length === 1) return parseCondition(t.slice(1, -1));
  const ands = splitTop(t, "&&");
  const ors = splitTop(t, "||");
  if (!ands || !ors) return null;
  if (ands.length > 1 && ors.length > 1) return null; // mixed: precedence would be lost
  const pieces = ors.length > 1 ? ors : ands;
  const clauses: Clause[] = [];
  for (const p of pieces) {
    const c = parseClause(p);
    if (!c) return null;
    clauses.push(c);
  }
  return { join: ors.length > 1 ? "or" : "and", clauses };
}

const single = (s: string) => `'${s.replace(/\\/g, "\\\\").replace(/'/g, "\\'")}'`;

export function compileClause(c: Clause): string {
  if (c.op === "set") return c.left;
  if (c.op === "unset") return `!${c.left}`;
  const rhs = c.kind === "text" ? single(c.right) : c.right;
  return `${c.left} ${c.op} ${rhs}`;
}

export function compileCondition(c: Condition): string {
  return c.clauses.filter((x) => x.left.trim()).map(compileClause).join(c.join === "or" ? " || " : " && ");
}

/** Round trip: parse then compile. Used by tests, and to decide whether the builder may take over a formula. */
export function normalise(expr: string): string | null {
  const c = parseCondition(expr);
  return c ? compileCondition(c) : null;
}

export const blankClause = (left = ""): Clause => ({ left, op: "==", right: "", kind: "text" });

/** Change a clause's operator, fixing up the kind of its value. */
export function withOp(c: Clause, op: Op): Clause {
  if (!needsValue(op)) return { ...c, op, right: "", kind: "text" };
  const ordered = op === ">" || op === ">=" || op === "<" || op === "<=";
  if (ordered && c.kind === "text" && NUMBER.test(c.right.trim())) return { ...c, op, right: c.right.trim(), kind: "number" };
  return { ...c, op };
}

/** Change the value text; a value that looks like a number or true/false becomes one, anything else is text. */
export function withValue(c: Clause, text: string): Clause {
  if (NUMBER.test(text.trim())) return { ...c, right: text.trim(), kind: "number" };
  if (text.trim() === "true" || text.trim() === "false") return { ...c, right: text.trim(), kind: "bool" };
  return { ...c, right: text, kind: "text" };
}
