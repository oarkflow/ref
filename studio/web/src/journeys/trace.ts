// "Follow the user": for each link, form or button on a page, the hops a visitor's
// click sets off - the request it sends, the logic flow that runs, the connections
// it uses, and the page they land on. Pure, so the order is tested.
import type { ElementRow, JEdge, Journey, JNode } from "./model";
import { ELEMENT_WORDS } from "./text";

export type StepKind = "page" | "element" | "route" | "intent" | "resource" | "result" | "problem" | "external";

export interface TraceStep {
  /** What is highlighted: a card id, or a row id (then `card` is the page holding it). */
  id: string;
  card: string;
  kind: StepKind;
  /** The line that leads into this step; it is the one that animates. */
  via?: string;
  /** One plain sentence. */
  note: string;
}

export interface Trace {
  /** The row that starts it. */
  id: string;
  title: string;
  steps: TraceStep[];
  /** The click leads nowhere on the map (a script-only button, say). */
  dead: boolean;
}

const VERB: Record<string, string> = { link: "Click the link", form: "Submit the form", button: "Press the button", fetch: "The page sends a request" };

const outgoing = (j: Journey, id: string) => j.edges.filter((e) => e.source === id);
const quote = (s: string) => `“${s}”`;

/** Pages that have something to follow, in the order the map lists them. */
export function pagesToFollow(j: Journey): JNode[] {
  return j.nodes.filter((n) => n.kind === "page" && n.rows.length > 0).sort((a, b) => a.title.localeCompare(b.title));
}

export function tracesFrom(j: Journey, pageId: string, opts: { includeShared?: boolean } = {}): Trace[] {
  const page = j.byId.get(pageId);
  if (!page) return [];
  const rows: ElementRow[] = [...page.rows, ...(opts.includeShared ? (j.byId.get("shared:everywhere")?.rows ?? []) : [])];
  return rows.map((row) => traceOf(j, page, row));
}

function traceOf(j: Journey, page: JNode, row: ElementRow): Trace {
  const steps: TraceStep[] = [
    { id: page.id, card: page.id, kind: "page", note: `You are on ${quote(page.title)}.` },
    { id: row.id, card: row.node.shared ? "shared:everywhere" : page.id, kind: "element", note: `${VERB[row.kind] ?? "Click"} ${quote(row.label)}.` },
  ];
  const first = j.edges.find((e) => e.sourceHandle === row.id);
  if (!first) {
    return { id: row.id, title: row.label, steps, dead: true };
  }
  const target = j.byId.get(first.target);
  if (!target) return { id: row.id, title: row.label, steps, dead: true };
  if (target.kind === "unresolved" || target.kind === "external") {
    steps.push({ id: target.id, card: target.id, kind: target.kind === "unresolved" ? "problem" : "external", via: first.id, note: target.kind === "unresolved" ? `It goes to ${quote(target.title)}, but nothing answers there.` : `It leaves the app for ${quote(target.title)}.` });
    return { id: row.id, title: row.label, steps, dead: false };
  }
  if (target.kind === "route") {
    steps.push({ id: target.id, card: target.id, kind: "route", via: first.id, note: `The app receives ${target.title === "" ? target.flow?.label ?? "" : requestText(target)}.` });
    followRoute(j, target, row, steps);
  } else {
    steps.push({ id: target.id, card: target.id, kind: target.kind === "page" ? "result" : "route", via: first.id, note: `It goes to ${quote(target.title)}.` });
  }
  return { id: row.id, title: row.label, steps, dead: false };
}

const requestText = (r: JNode) => `${String(r.flow?.data?.method ?? "").toUpperCase()} ${r.title}`.trim();

function followRoute(j: Journey, route: JNode, row: ElementRow, steps: TraceStep[]): void {
  const seen = new Set<string>(steps.map((s) => s.id));
  const add = (s: TraceStep) => { if (!seen.has(s.id)) { seen.add(s.id); steps.push(s); } };
  // the logic flow it runs, and the flows that one runs, then the connections they use
  let cur: JNode = route;
  const flows: JNode[] = [];
  for (let depth = 0; depth < 4; depth++) {
    const run = outgoing(j, cur.id).find((e) => e.kind === "runs" && !seen.has(e.target));
    const next = run && j.byId.get(run.target);
    if (!run || !next) break;
    add({ id: next.id, card: next.id, kind: "intent", via: run.id, note: `It runs ${quote(next.title)}.` });
    flows.push(next);
    cur = next;
  }
  for (const f of flows) {
    for (const u of outgoing(j, f.id).filter((e) => e.kind === "uses").slice(0, 3)) {
      const res = j.byId.get(u.target);
      if (res) add({ id: res.id, card: res.id, kind: "resource", via: u.id, note: `It uses ${quote(res.title)} (${res.sub.toLowerCase()}).` });
    }
  }
  // where the visitor ends up: a redirect on success wins over the page the request itself shows
  const redirect = pickRedirect(outgoing(j, route.id), row.id);
  const shows = outgoing(j, route.id).find((e) => e.kind === "renders");
  const end: JEdge | undefined = redirect ?? shows;
  const page = end && j.byId.get(end.target);
  if (end && page) {
    add({
      id: page.id, card: page.id, kind: "result", via: end.id,
      note: end.kind === "redirects" ? `When it works, you land on ${quote(page.title)}.` : `It answers with ${quote(page.title)}.`,
    });
  }
}

/** A redirect asked for by this very element, else the route's only one. */
function pickRedirect(edges: JEdge[], rowId: string): JEdge | undefined {
  const all = edges.filter((e) => e.kind === "redirects");
  return all.find((e) => e.via.includes(rowId)) ?? (all.length === 1 ? all[0] : undefined);
}

export const traceWord = (kind: string) => ELEMENT_WORDS[kind as keyof typeof ELEMENT_WORDS]?.one ?? "Item";
