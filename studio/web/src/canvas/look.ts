// What a node says about itself, in plain words. Pure, so it is unit tested:
// the node components only render what this returns.
import { unquote } from "../lib/bcl";
import { edgeLabel } from "./labels";
import type { StepInfo, IntentNodeInfo } from "./model";

// ---------------------------------------------------------------------------
// how a run ends

export type Outcome = "success" | "cancelled" | "failed" | "neutral";

const SUCCESS = /(^|[_\-.\s])(done|complete[d]?|success(ful)?|approved|accept(ed)?|paid|publish(ed)?|finish(ed)?|resolved|ok)($|[_\-.\s])/i;
const CANCELLED = /(^|[_\-.\s])(cancel(l?ed)?|abort(ed)?|withdraw[n]?|expire[d]?|timed?_?out|abandon(ed)?|skipp?ed)($|[_\-.\s])/i;
const FAILED = /(^|[_\-.\s])(reject(ed)?|fail(ed|ure)?|error|den(y|ied)|declin(e|ed)|refus(e|ed)|invalid)($|[_\-.\s])/i;

/** Which way a finishing step ends, guessed from its name and what it runs. `neutral` when nothing says. */
export function endOutcome(...names: (string | undefined)[]): Outcome {
  const text = names.filter(Boolean).join(" ");
  if (!text) return "neutral";
  // the most specific reading wins: "rejected" is a failure even though "approve" appears in a sibling name
  if (FAILED.test(text)) return "failed";
  if (CANCELLED.test(text)) return "cancelled";
  if (SUCCESS.test(text)) return "success";
  return "neutral";
}

export const OUTCOME_TEXT: Record<Outcome, string> = {
  success: "Ends the run · success",
  cancelled: "Ends the run · cancelled",
  failed: "Ends the run · failed",
  neutral: "Ends the run",
};

// ---------------------------------------------------------------------------
// conditions in plain words

const PAST: Record<string, string> = {
  approve: "approved", reject: "rejected", cancel: "cancelled", resubmit: "resubmitted", request_changes: "changes requested",
  escalate: "escalated", accept: "accepted", decline: "declined", deny: "denied", complete: "completed", submit: "submitted",
  confirm: "confirmed", retry: "retried", skip: "skipped", revise: "revised", close: "closed", reopen: "reopened", pass: "passed", fail: "failed",
};

const human = (s: string) => s.replace(/[_\-.]+/g, " ").replace(/([a-z])([A-Z])/g, "$1 $2").trim().toLowerCase();
const lastSegment = (path: string) => path.split(".").pop() ?? path;
const OUTCOME_FIELDS = new Set(["action", "decision", "outcome", "status", "result", "state", "verdict", "choice"]);

function literal(raw: string): string | null {
  const t = raw.trim();
  if (t.length >= 2 && t.startsWith("'") && t.endsWith("'") && !t.slice(1, -1).includes("'")) return t.slice(1, -1);
  const q = unquote(t);
  if (q !== null) return q;
  if (/^-?\d+(\.\d+)?$/.test(t) || t === "true" || t === "false") return t;
  return null;
}

const OPS: [RegExp, string][] = [
  [/^(.+?)\s*==\s*(.+)$/, "=="],
  [/^(.+?)\s*!=\s*(.+)$/, "!="],
  [/^(.+?)\s*>=\s*(.+)$/, ">="],
  [/^(.+?)\s*<=\s*(.+)$/, "<="],
  [/^(.+?)\s*>\s*(.+)$/, ">"],
  [/^(.+?)\s*<\s*(.+)$/, "<"],
];

function one(expr: string): string | null {
  const t = expr.trim().replace(/^\((.*)\)$/, "$1").trim();
  if (!t) return null;
  if (t === "true") return "always";
  if (t.startsWith("!") && /^!\s*[\w.]+$/.test(t)) return `${human(lastSegment(t.slice(1)))} is not set`;
  if (/^[\w.]+$/.test(t)) return `${human(lastSegment(t))} is set`;
  for (const [re, op] of OPS) {
    const m = re.exec(t);
    if (!m) continue;
    const lhs = m[1]!.trim();
    const rhs = literal(m[2]!);
    if (!/^[\w.]+$/.test(lhs) || rhs === null) return null;
    const field = lastSegment(lhs);
    if (op === "==" || op === "!=") {
      const not = op === "!=";
      if (OUTCOME_FIELDS.has(field)) return `${not ? "not " : ""}${PAST[rhs] ?? human(rhs)}`;
      if (rhs === "true") return `${human(field)}${not ? " is off" : ""}`;
      if (rhs === "false") return `${human(field)}${not ? "" : " is off"}`;
      return `${human(field)} ${not ? "is not" : "is"} ${human(rhs)}`;
    }
    const word = op === ">" ? "over" : op === ">=" ? "at least" : op === "<" ? "under" : "at most";
    return `${human(field)} is ${word} ${rhs}`;
  }
  return null;
}

/** "result.action == 'approve'" -> "If approved". `null` when the expression is too involved to say in words. */
export function friendlyCondition(raw: string | undefined): string | null {
  const t = (raw ?? "").trim();
  if (!t) return null;
  const parts = t.split(/\s*(&&|\|\|)\s*/);
  const words: string[] = [];
  for (let i = 0; i < parts.length; i += 2) {
    const w = one(parts[i]!);
    if (w === null) return null;
    words.push(w);
    const join = parts[i + 1];
    if (join) words.push(join === "&&" ? "and" : "or");
  }
  const text = words.join(" ");
  return `If ${text}`.replace("If always", "Always").replace("If not ", "Unless ");
}

const clip = (s: string, n: number) => (s.length > n ? s.slice(0, n - 1) + "…" : s);

/** The text on a connection. `raw` is what the Developer view shows instead. */
export function edgeChip(kind: string, condition: string | undefined): { text: string; raw: string } {
  const kindText = kind === "simple" || kind === "branch" ? "" : edgeLabel(kind);
  const cond = condition ? (friendlyCondition(condition) ?? "When a rule matches") : "";
  const text = [kindText, cond].filter(Boolean).join(" · ");
  const raw = [kindText, condition ? clip(condition, 30) : ""].filter(Boolean).join(" · ");
  return { text, raw };
}

// ---------------------------------------------------------------------------
// the small facts on a card

export interface Row {
  label: string;
  value: string;
  /** looks like code (an id, a URL): shown in a small neutral chip */
  code?: boolean;
  /** a coloured dot before the value (cases, outcomes) */
  dot?: boolean;
}

export interface Chip {
  key: string;
  label: string;
  icon: string;
  tone?: "neutral" | "warn" | "info";
  title?: string;
}

export const MAX_ROWS = 5;

export function chipFor(raw: string): Chip {
  const [head, ...rest] = raw.split(" ");
  const arg = rest.join(" ");
  switch (head) {
    case "timeout": return { key: raw, label: `${arg} limit`, icon: "clock", title: `Gives up after ${arg}` };
    case "retry": return { key: raw, label: "Tries again", icon: "loop", title: "Retries when it fails" };
    case "bulkhead": return { key: raw, label: "Limited", icon: "gauge", title: "Only so many at a time" };
    case "authz": return { key: raw, label: "Restricted", icon: "shield", title: "Checks who may use this" };
    case "on_error": return { key: raw, label: `On error: ${arg}`, icon: "alert", tone: "warn", title: `When it fails: ${arg}` };
    case "sensitive": return { key: raw, label: "Sensitive", icon: "lock", title: "Values are hidden in logs" };
    case "compensate": return { key: raw, label: "Can be undone", icon: "undo", title: "Has an undo step" };
    case "skip": return { key: raw, label: "Can be skipped", icon: "skip", title: "Skipped when its condition holds" };
    case "sla": return { key: raw, label: "Deadline", icon: "clock", title: "Has a deadline" };
    case "confirm": return { key: raw, label: "Asks to confirm", icon: "check", title: "Asks before submitting" };
    default: return { key: raw, label: raw, icon: "gear" };
  }
}

export function stepRows(s: StepInfo): Row[] {
  const rows: Row[] = [];
  if (s.intent) rows.push({ label: "Runs", value: s.intent, code: true });
  else if (s.process) rows.push({ label: "Starts", value: s.process, code: true });
  if (s.human) rows.push({ label: "Waits for", value: "a person" });
  if (s.summary.line) rows.push({ label: rowLabel(s.typeName), value: s.summary.line });
  for (const c of s.summary.cases) rows.push({ label: c.label, value: c.target ?? "", code: !!c.target, dot: true });
  return rows;
}

export function intentRows(n: IntentNodeInfo): Row[] {
  const rows: Row[] = [];
  const s = n.summary;
  // the action a plain step runs; the special kinds (branch, loop, ...) say more than the action name does
  if (n.uses && n.uses !== "collect" && !s.cases.length && !s.loop && !s.line) rows.push({ label: "Action", value: n.uses, code: true });
  if (s.line) rows.push({ label: rowLabel(n.typeName), value: s.line });
  if (s.loop) {
    rows.push({ label: "For each", value: s.loop.items ?? "…", code: true });
    rows.push({ label: "Runs", value: s.loop.intent ?? "…", code: true });
    if (s.loop.concurrency && s.loop.concurrency !== "1") rows.push({ label: "At once", value: s.loop.concurrency });
  }
  for (const c of s.cases) rows.push({ label: c.label, value: c.target ?? "", code: !!c.target, dot: true });
  return rows;
}

function rowLabel(typeName: string): string {
  switch (typeName) {
    case "switch": return "Matches";
    case "decision": case "condition": case "decision_matrix": case "rules": return "Rule";
    case "http": case "service": case "tool": return "Calls";
    case "script": return "Formula";
    default: return "Details";
  }
}

/** Rows to show on a card, and how many more there are. */
export function visibleRows(rows: Row[]): { shown: Row[]; more: number } {
  return rows.length > MAX_ROWS ? { shown: rows.slice(0, MAX_ROWS - 1), more: rows.length - (MAX_ROWS - 1) } : { shown: rows, more: 0 };
}
