// Turns the raw block trees into what the friendly navigation shows: items
// grouped by category, with one-line summaries.
import type { BlockNode } from "../api/types";
import { CATEGORIES, blockInfo, count, humanize, type CategoryId } from "../labels";
import { unquote } from "../lib/bcl";
import { blocksOf, fieldOf } from "../lib/paths";
import type { Tone } from "../ui/primitives";

export interface Item {
  key: string;
  file: string;
  path: string;
  type: string;
  id: string;
  /** Shown name: the block's id, or its type for id-less blocks. */
  name: string;
  category: CategoryId;
  node: BlockNode;
}

/** Top-level blocks of every file. Prefers the full tree (which has field values) when it is loaded. */
export function collectItems(nav: Record<string, BlockNode[]>, trees: Record<string, BlockNode[]> = {}): Item[] {
  const out: Item[] = [];
  for (const file of Object.keys(nav).sort()) {
    const full = new Map((trees[file] ?? []).map((n) => [n.path, n]));
    for (const n of nav[file] ?? []) {
      if (n.kind !== "block") continue;
      const node = full.get(n.path) ?? n;
      const type = n.type ?? "";
      out.push({ key: `${file}|${n.path}`, file, path: n.path, type, id: n.id ?? "", name: n.id || type, category: blockInfo(type).category, node });
    }
  }
  return out;
}

export function groupByCategory(items: Item[]): Record<CategoryId, Item[]> {
  const g = Object.fromEntries(CATEGORIES.map((c) => [c.id, [] as Item[]])) as Record<CategoryId, Item[]>;
  for (const it of items) g[it.category].push(it);
  return g;
}

export function categoryCounts(items: Item[]): Record<CategoryId, number> {
  const g = groupByCategory(items);
  return Object.fromEntries(CATEGORIES.map((c) => [c.id, g[c.id].length])) as Record<CategoryId, number>;
}

export interface Chip {
  text: string;
  tone: Tone;
  title?: string;
  mono?: boolean;
  /** Status chip: draws a leading dot so colour is never the only signal. */
  dot?: boolean;
}

export interface Summary {
  chips: Chip[];
  /** A secondary line: a description or the linked flow. */
  line?: string;
}

const val = (n: BlockNode, name: string) => {
  const raw = fieldOf(n, name)?.raw;
  return raw === undefined ? undefined : (unquote(raw) ?? raw.trim());
};

export function summarize(item: Item): Summary {
  const n = item.node;
  const chips: Chip[] = [];
  let line: string | undefined = val(n, "description");
  const complete = !!n.children;
  switch (item.type) {
    case "route": {
      const m = val(n, "method");
      if (m) chips.push({ text: m.toUpperCase(), tone: m.toUpperCase() === "GET" ? "info" : "accent", mono: true, title: "How the request is made" });
      const p = val(n, "path");
      if (p) chips.push({ text: p, tone: "muted", mono: true, title: "Address" });
      const anon = fieldOf(n, "allow_anonymous")?.raw?.trim() === "true";
      if (complete) {
        if (anon) chips.push({ text: "Open to everyone", tone: "info", dot: true, title: "Anyone can use this without signing in" });
        else if (fieldOf(n, "auth") || blocksOf(n, "authz").length) chips.push({ text: "Sign-in required", tone: "ok", dot: true });
        else chips.push({ text: "No sign-in set", tone: "muted", dot: true });
      }
      const flow = val(n, "intent") ?? val(n, "process");
      if (flow) line = `Runs ${val(n, "process") ? "workflow" : "flow"} ${flow}`;
      else if (val(n, "template")) line = `Shows page ${val(n, "template")}`;
      break;
    }
    case "resource": {
      const k = val(n, "kind");
      if (k) chips.push({ text: k, tone: "accent", mono: true, title: "Type of connection" });
      break;
    }
    case "intent": {
      const steps = blocksOf(n, "node").length;
      if (complete) chips.push({ text: count(steps, "step"), tone: "muted" });
      break;
    }
    case "process": {
      if (complete) chips.push({ text: count(blocksOf(n, "step").length, "step"), tone: "muted" });
      break;
    }
    case "pipeline": {
      if (complete) chips.push({ text: count(blocksOf(n, "stage").length, "stage"), tone: "muted" });
      break;
    }
    case "worker": {
      const q = val(n, "queue");
      if (q) chips.push({ text: `Queue ${q}`, tone: "muted" });
      if (fieldOf(n, "disabled")?.raw?.trim() === "true") chips.push({ text: "Switched off", tone: "warn" });
      const flow = val(n, "intent") ?? val(n, "process");
      if (flow) line = `Runs ${flow}`;
      break;
    }
    case "schedule": {
      const every = val(n, "every");
      const cron = val(n, "cron");
      if (every) chips.push({ text: `Every ${every}`, tone: "info" });
      else if (cron) chips.push({ text: cron, tone: "info", mono: true });
      if (fieldOf(n, "disabled")?.raw?.trim() === "true") chips.push({ text: "Switched off", tone: "warn" });
      const flow = val(n, "intent") ?? val(n, "process");
      if (flow) line = `Runs ${flow}`;
      break;
    }
    case "trigger": {
      const p = val(n, "path");
      if (p) chips.push({ text: p, tone: "muted", mono: true });
      const flow = val(n, "intent") ?? val(n, "process");
      if (flow) line = `Runs ${flow}`;
      break;
    }
    case "entity": {
      const t = val(n, "table");
      if (t) chips.push({ text: `Table ${t}`, tone: "muted" });
      const p = val(n, "path");
      if (p) chips.push({ text: p, tone: "muted", mono: true });
      break;
    }
    case "static": {
      const p = val(n, "prefix");
      if (p) chips.push({ text: p, tone: "muted", mono: true });
      break;
    }
    case "role": {
      const list = fieldOf(n, "permissions")?.raw;
      if (list) chips.push({ text: count((list.match(/"/g)?.length ?? 0) / 2 || 0, "permission"), tone: "muted" });
      break;
    }
    case "flag": {
      const d = fieldOf(n, "default")?.raw?.trim();
      if (d) chips.push({ text: d === "true" ? "On by default" : d === "false" ? "Off by default" : `Default ${d}`, tone: d === "true" ? "ok" : "muted" });
      break;
    }
    case "secret": {
      const e = val(n, "env");
      if (e) chips.push({ text: e, tone: "muted", mono: true });
      break;
    }
    case "shape": {
      const k = val(n, "kind");
      if (k) chips.push({ text: humanize(k), tone: "muted" });
      break;
    }
  }
  return { chips, line };
}

/** The file that already holds the most blocks of this type; else the first file. */
export function suggestFile(type: string, nav: Record<string, BlockNode[]>, files: string[]): string | null {
  let best: string | null = null;
  let n = 0;
  for (const f of files) {
    const c = (nav[f] ?? []).filter((b) => b.type === type).length;
    if (c > n) {
      best = f;
      n = c;
    }
  }
  return best ?? [...files].sort()[0] ?? null;
}
