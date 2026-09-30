// What a connection says, in plain words. Pure, so every edge type is tested:
// the chip on the canvas only renders what this returns.
//
// Process connections: their type, plus the condition and the settings that type
// reads ("If approved", "After 48 hours", "When “paid” arrives"). Flow connections:
// the fact(s) that travel along them.
import { unquote } from "../lib/bcl";
import { humanize } from "../labels";
import { friendlyCondition } from "./look";
import { edgeLabel } from "./labels";
import type { EdgeTypeInfo, Settings } from "./model";

export type EdgeTone = "normal" | "quiet" | "error" | "wait" | "todo";

export interface EdgeLabel {
  /** The chip's text. Empty when the line should show no chip. */
  text: string;
  /** What the Developer view shows instead: the expression and settings as written. */
  raw: string;
  /** The full story, for the tooltip. */
  tip: string;
  icon?: "alert" | "clock" | "loop" | "user" | "branch" | "undo" | "gauge" | "x" | "bell" | "list";
  tone: EdgeTone;
  /** Set when the connection means nothing until this is filled in: the prompt to show instead. */
  needs?: { prompt: string; field: "condition" | "timeout" | "event" };
}

export interface EdgeLike {
  kind: string;
  condition?: string;
  settings: Settings;
}

const UNITS: [RegExp, string, string][] = [
  [/^(\d+(?:\.\d+)?)ms$/, "ms", "ms"],
  [/^(\d+(?:\.\d+)?)s$/, "second", "seconds"],
  [/^(\d+(?:\.\d+)?)m$/, "minute", "minutes"],
  [/^(\d+(?:\.\d+)?)h$/, "hour", "hours"],
];
// Days and weeks are deliberately absent: the platform reads durations with Go's time.ParseDuration
// (ns, us, ms, s, m, h), so "2d" would fail at run time. Write 48h instead.

/** "48h" -> "48 hours", "1h30m" -> "1 hour 30 minutes". Anything else is returned as written. */
export function humanDuration(d: string): string {
  const parts = d.trim().match(/\d+(?:\.\d+)?(?:ms|s|m|h)/g);
  if (!parts || parts.join("") !== d.trim()) return d;
  return parts
    .map((p) => {
      for (const [re, one, many] of UNITS) {
        const m = re.exec(p);
        if (m) return one === "ms" ? `${m[1]} ms` : `${m[1]} ${Number(m[1]) === 1 ? one : many}`;
      }
      return p;
    })
    .join(" ");
}

const clip = (s: string, n: number) => (s.length > n ? s.slice(0, n - 1) + "…" : s);

/**
 * `siblings`: the other connections leaving the same step of the same kind; a branch with no condition is
 * the "otherwise" only when its siblings have one.
 */
export function processEdgeLabel(e: EdgeLike, ctx: { siblings?: EdgeLike[]; typeInfo?: EdgeTypeInfo } = {}): EdgeLabel {
  const get = (k: string): string | undefined => {
    const raw = e.settings[k];
    return raw === undefined ? undefined : (unquote(raw) ?? raw.trim());
  };
  const cond = e.condition?.trim() || undefined;
  const friendly = cond ? (friendlyCondition(cond) ?? "When a rule matches") : undefined;
  const siblingHasCondition = (ctx.siblings ?? []).some((s) => s !== e && (s.kind === e.kind) && !!s.condition?.trim());
  const timeout = get("timeout");
  const dur = timeout ? humanDuration(timeout) : undefined;
  const join = (...xs: (string | undefined)[]) => xs.filter(Boolean).join(" · ");

  let text = "";
  let tone: EdgeTone = "normal";
  let icon: EdgeLabel["icon"];
  let needs: EdgeLabel["needs"];

  switch (e.kind) {
    case "simple":
      text = friendly ?? "Then";
      tone = friendly ? "normal" : "quiet";
      break;
    case "branch":
    case "switch":
      if (friendly) text = friendly;
      else if (siblingHasCondition) text = "Otherwise";
      else {
        text = "Add a condition";
        needs = { prompt: "Add a condition", field: "condition" };
        tone = "todo";
      }
      break;
    case "conditional_fork":
      if (friendly) text = friendly;
      else { text = "Add a condition"; needs = { prompt: "Add a condition", field: "condition" }; tone = "todo"; }
      break;
    case "priority": {
      const p = get("priority");
      text = join(p ? `Priority ${p}` : "By priority", friendly);
      break;
    }
    case "weighted": {
      const w = get("weight");
      text = join(w ? `By chance (weight ${w})` : "By chance", friendly);
      break;
    }
    case "threshold": {
      const t = get("threshold");
      text = t ? `If over ${t}` : "By amount";
      break;
    }
    case "error": text = join("On error", friendly); tone = "error"; icon = "alert"; break;
    case "fallback": text = join("Fallback", friendly); tone = "error"; icon = "undo"; break;
    case "compensate": text = join("To undo", friendly); tone = "error"; icon = "undo"; break;
    case "retry": {
      const n = get("attempts");
      text = n ? `Try again (up to ${n} times)` : "Try again";
      icon = "loop";
      break;
    }
    case "timeout": text = dur ? `If it takes over ${dur}` : "On timeout"; icon = "clock"; tone = "wait"; break;
    case "delayed":
      if (dur) text = `After ${dur}`;
      else { text = "Add a delay"; needs = { prompt: "Add a delay", field: "timeout" }; tone = "todo"; }
      if (!needs) tone = "wait";
      icon = "clock";
      break;
    case "escalation":
      if (dur) text = `Escalate after ${dur}`;
      else { text = "Add a time"; needs = { prompt: "Add a time", field: "timeout" }; tone = "todo"; }
      if (!needs) tone = "wait";
      icon = "bell";
      break;
    case "manual": text = friendly ?? "When a person decides"; tone = "wait"; icon = "user"; break;
    case "wait_event": {
      const ev = get("event");
      if (ev) text = join(`When “${ev}” arrives`, dur ? `or after ${dur}` : undefined);
      else { text = "Add the event"; needs = { prompt: "Add the event", field: "event" }; tone = "todo"; }
      if (!needs) tone = "wait";
      icon = "clock";
      break;
    }
    case "rate_limited": {
      const limit = get("limit");
      const win = get("window");
      text = limit && win ? `Slow down (${limit} per ${humanDuration(win)})` : "Slow down";
      tone = "wait";
      icon = "gauge";
      break;
    }
    case "cancel": text = join("On cancel", friendly); icon = "x"; break;
    case "fanout": text = join("Run together", friendly); break;
    case "parallel": text = join("Run together", get("fail_fast") === "true" ? "stop if one fails" : undefined); break;
    case "dynamic_fanout": text = join("Split by data", friendly); break;
    case "race": text = join("First one wins", dur ? `within ${dur}` : undefined); break;
    case "fanin":
    case "join": {
      const st = get("strategy");
      text = st && /^(any|first)$/i.test(st) ? "Wait for the first" : "Wait for all";
      break;
    }
    case "quorum": {
      const q = get("quorum");
      text = q ? `Wait for ${q} of them` : "Wait for enough";
      break;
    }
    case "iterator": {
      const items = get("items_path");
      text = items ? `For each item in ${items}` : "For each item";
      icon = "loop";
      break;
    }
    case "batch_iterator": {
      const b = get("batch_size");
      text = b ? `For each batch of ${b}` : "For each batch";
      icon = "loop";
      break;
    }
    case "loop_until":
      if (cond) text = `Repeat until ${(friendlyCondition(cond) ?? "a rule matches").replace(/^If /, "")}`;
      else { text = "Add a condition"; needs = { prompt: "Add a condition", field: "condition" }; tone = "todo"; }
      icon = "loop";
      break;
    case "filter": text = join("Only matching", friendly); break;
    case "transform": text = "Reshape"; break;
    case "stream_pipe": text = "Stream"; break;
    default:
      text = join(ctx.typeInfo ? edgeLabel(e.kind) : humanize(e.kind), friendly);
  }

  const about = ctx.typeInfo?.summary ?? "";
  const tip = [text, cond ? `When: ${cond}` : "", about].filter(Boolean).join("\n");
  // The Developer view shows the expression as written, after the kind of connection when that says something.
  const kindPrefix = e.kind === "simple" || e.kind === "branch" || e.kind === "switch" ? "" : `${edgeLabel(e.kind)} · `;
  const raw = needs ? text : cond ? `${kindPrefix}${clip(cond, 60)}` : text;
  return { text, raw, tip, icon, tone, needs };
}

/**
 * The chip on a line of a flow: the fact that travels along it. When several facts pass between the same two
 * steps, the first line says "a +2" and the rest show no chip (the tooltip lists them all).
 */
export function factEdgeLabel(o: { fact: string; facts: readonly string[]; first: boolean; fromRequest: boolean }): EdgeLabel {
  const all = o.facts.length ? o.facts : [o.fact];
  const tip = `Carries: ${all.join(", ")}`;
  if (o.fromRequest) return { text: "request", raw: "request", tip: "What the caller sent", tone: "quiet" };
  if (all.length > 1 && !o.first) return { text: "", raw: "", tip, tone: "quiet" };
  const text = all.length > 1 ? `${all[0]} +${all.length - 1}` : all[0]!;
  return { text, raw: text, tip, tone: "normal" };
}

/** Kinds that only make sense with a condition, a time or an event; the popover offers that field first. */
export function needsField(kind: string): "condition" | "timeout" | "event" | undefined {
  switch (kind) {
    case "branch": case "switch": case "conditional_fork": case "loop_until": return "condition";
    case "delayed": case "escalation": return "timeout";
    case "wait_event": return "event";
    default: return undefined;
  }
}
