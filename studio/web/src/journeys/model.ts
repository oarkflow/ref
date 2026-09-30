// Turns the server's journey graph into the cards and lines of the map.
//
// The server describes every link, form and button as a node of its own. On the map they
// are rows inside the page that holds them (or inside one shared card for the navbar and
// other shared parts), each row with its own connection point, so a page reads as "here
// is what you can do on it" and the lines leave from the very button that sends them.
// Everything here is pure: the React side only draws what this returns.
import { routeTitle, typeText, cleanLabel, edgeWords, elementKind, pageTitle, type EdgeWords, type ElementKind } from "./text";
import type { FlowEdge, FlowEdgeKind, FlowGraph, FlowNode, FlowWarning } from "./types";

export const SHARED_ID = "shared:everywhere";

export type JKind = "page" | "shared" | "route" | "intent" | "resource" | "external" | "unresolved";

export interface ElementRow {
  /** The server's id for the element: also the id of the row's connection point. */
  id: string;
  kind: ElementKind;
  label: string;
  /** The label was made up from the address because the template had only a placeholder. */
  guessed: boolean;
  method?: string;
  url?: string;
  line?: number;
  file?: string;
  /** Which shared part it comes from ("navbar"), for the shared card. */
  component?: string;
  node: FlowNode;
}

export interface Fact {
  k: string;
  v: string;
  /** Shown in a code chip (an address, a name). */
  code?: boolean;
}

export interface Chip {
  text: string;
  tone: "ok" | "info" | "warn" | "muted" | "danger";
  icon?: "lock" | "globe" | "gauge" | "user" | "alert";
  title?: string;
}

export interface JNode {
  id: string;
  kind: JKind;
  flow?: FlowNode;
  title: string;
  sub: string;
  rows: ElementRow[];
  /** Left to right: 0 pages, 1 requests, 2.. logic flows, last connections. */
  lane: number;
  width: number;
  height: number;
  /** Real problems on this card or on one of its rows. */
  problems: number;
  /** Problems by element id, for the rows. */
  rowProblems: Record<string, number>;
  /** Label / value lines under the title. */
  facts: Fact[];
  chips: Chip[];
  /** A page: the addresses that show it ("GET /todos"). */
  routes: string[];
  /** The shared card lists its rows (and they have connection points) instead of a summary. */
  open?: boolean;
}

export interface JEdge {
  id: string;
  kind: FlowEdgeKind;
  source: string;
  /** The row the line leaves from, when it leaves from a link, form or button. */
  sourceHandle?: string;
  target: string;
  words: EdgeWords;
  /** Elements that cause the line (a redirect comes from the forms that ask for it). */
  via: string[];
  /** Runs against the left-to-right flow (a request showing a page that sits to its left). */
  back: boolean;
  /** Drawn faintly until something it touches is selected. */
  quiet: boolean;
  server: FlowEdge;
}

export interface Filters {
  hideShared: boolean;
  hideConnections: boolean;
  onlyProblems: boolean;
  /** Also show the endpoints no page reaches (APIs, hooks). */
  showOrphans: boolean;
  /** The shared card lists its rows instead of a summary. */
  sharedOpen: boolean;
  /** A focused journey leaves the shared navbar out (it links to everything); this brings it back. */
  sharedInFocus: boolean;
}

export const DEFAULT_FILTERS: Filters = { hideShared: false, hideConnections: false, onlyProblems: false, showOrphans: false, sharedOpen: false, sharedInFocus: false };

export interface Journey {
  nodes: JNode[];
  edges: JEdge[];
  byId: Map<string, JNode>;
  /** Element id -> the card that holds it. */
  rowHost: Map<string, string>;
  hidden: { endpoints: number; shared: number; connections: number };
  /** All warnings, with `node` pointing at the card that holds the element it was about. */
  warnings: FlowWarning[];
}

// ---- card sizes (the CSS uses the same numbers) --------------------------------------------

export const CARD_W = 280;
export const HEAD_H = 56;
export const META_H = 36;
export const ROW_H = 30;
export const ROWS_PAD = 8;
export const FOOT_H = 36;
export const BORDER = 2;

const str = (v: unknown): string => (typeof v === "string" ? v : "");
const bool = (v: unknown): boolean => v === true;
const num = (v: unknown): number => (typeof v === "number" ? v : 0);

export function pageHeight(rows: number): number {
  return HEAD_H + META_H + (rows > 0 ? rows * ROW_H + ROWS_PAD : 0) + BORDER;
}
/** The shared card is a summary line until it is opened. */
export function sharedHeight(rows: number, open: boolean, components: number): number {
  return HEAD_H + (open ? rows * ROW_H + components * 24 + ROWS_PAD : ROW_H + ROWS_PAD) + BORDER;
}
export const ROUTE_H = HEAD_H + META_H + BORDER;
export const INTENT_H = HEAD_H + META_H + BORDER;
export const SMALL_H = HEAD_H + BORDER;
export const UNRESOLVED_H = HEAD_H + META_H + FOOT_H + BORDER;

const EDGE_ORDER: FlowEdgeKind[] = ["navigates", "calls", "runs", "uses", "renders", "redirects"];

function componentOf(group: string | undefined): string | undefined {
  const m = group && /^shared:(.+)$/.exec(group);
  return m ? m[1]!.split("/").pop() : undefined;
}

/** The warnings that are real problems, not notes. */
export const isProblem = (w: FlowWarning) => w.severity === "warning";

export function buildJourney(graph: FlowGraph, given: Filters = DEFAULT_FILTERS, opts: { reach?: number } = {}): Journey {
  // In a focused journey the shared navbar would drag in the whole app, so it stays out unless asked for.
  const filters: Filters = graph.focus && !given.sharedInFocus ? { ...given, hideShared: true } : given;
  const nodes = new Map<string, FlowNode>(graph.nodes.map((n) => [n.id, n]));
  const focused = !!graph.focus;

  // ---- which card holds each link, form and button --------------------------------------------
  const containedBy = new Map<string, string>();
  for (const e of graph.edges) if (e.kind === "contains" && !e.shared) containedBy.set(e.to, e.from);
  const hostOf = (el: FlowNode): string | undefined => {
    if (el.shared) return SHARED_ID;
    const g = el.group && nodes.get(el.group)?.kind === "page" ? el.group : undefined;
    return g ?? containedBy.get(el.id);
  };
  const rowHost = new Map<string, string>();
  const rowsBy = new Map<string, ElementRow[]>();
  for (const n of graph.nodes) {
    if (n.kind !== "element") continue;
    const host = hostOf(n);
    if (!host || (host !== SHARED_ID && !nodes.has(host))) continue;
    if (host === SHARED_ID && filters.hideShared) continue;
    const c = cleanLabel(n);
    const row: ElementRow = {
      id: n.id, kind: elementKind(n), label: c.text, guessed: c.guessed,
      method: str(n.data?.method) || undefined, url: str(n.data?.url) || undefined,
      line: n.line, file: n.file, component: n.shared ? componentOf(n.group) : undefined, node: n,
    };
    rowHost.set(n.id, host);
    (rowsBy.get(host) ?? rowsBy.set(host, []).get(host)!).push(row);
  }
  for (const rows of rowsBy.values()) rows.sort((a, b) => (a.file ?? "").localeCompare(b.file ?? "") || (a.line ?? 0) - (b.line ?? 0) || a.label.localeCompare(b.label));

  // ---- warnings, pointed at cards ----------------------------------------------------------------
  const warnings = graph.warnings.map((w) => ({ ...w, node: w.node ? (rowHost.get(w.node) ?? w.node) : w.node }));
  const problemsBy = new Map<string, number>();
  const rowProblems = new Map<string, Record<string, number>>();
  for (const w of graph.warnings) {
    if (!isProblem(w) || !w.node) continue;
    const host = rowHost.get(w.node) ?? w.node;
    problemsBy.set(host, (problemsBy.get(host) ?? 0) + 1);
    if (rowHost.has(w.node)) {
      const r = rowProblems.get(host) ?? {};
      r[w.node] = (r[w.node] ?? 0) + 1;
      rowProblems.set(host, r);
    }
  }
  for (const n of graph.nodes) if (n.kind === "unresolved") problemsBy.set(n.id, Math.max(1, problemsBy.get(n.id) ?? 0));

  // ---- cards ---------------------------------------------------------------------------------------
  const jnodes = new Map<string, JNode>();
  const add = (n: Omit<JNode, "facts" | "chips" | "routes"> & Partial<Pick<JNode, "facts" | "chips" | "routes">>) =>
    jnodes.set(n.id, { facts: [], chips: [], routes: [], ...n });
  const routeText = (name: string): string | undefined => {
    const r = nodes.get(`route:${name}`);
    return r ? `${str(r.data?.method).toUpperCase()} ${str(r.data?.path)}`.trim() : undefined;
  };
  const strings = (v: unknown): string[] => (Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : []);
  for (const n of graph.nodes) {
    const common = { flow: n, problems: problemsBy.get(n.id) ?? 0, rowProblems: rowProblems.get(n.id) ?? {} };
    switch (n.kind) {
      case "page": {
        const rows = rowsBy.get(n.id) ?? [];
        const d = n.data ?? {};
        const chips: Chip[] = [];
        if (bool(d.public)) chips.push({ text: "Public", tone: "info", icon: "globe", title: "Anyone can open this page" });
        else if (bool(d.protected)) chips.push({ text: "Sign-in", tone: "ok", icon: "lock", title: "People must sign in to open this page" });
        add({ id: n.id, kind: "page", title: pageTitle(n), sub: typeText(n), rows, lane: 0, width: CARD_W, height: pageHeight(rows.length), routes: strings(d.routes).map(routeText).filter((x): x is string => !!x), chips, ...common });
        break;
      }
      case "route": {
        const d = n.data ?? {};
        const chips: Chip[] = [];
        if (bool(d.public)) chips.push({ text: "Public", tone: "info", icon: "globe", title: "Anyone can send this" });
        else if (bool(d.protected)) chips.push({ text: "Sign-in", tone: "ok", icon: "lock", title: "People must sign in to use this" });
        else chips.push({ text: "No access rules", tone: "muted", title: "Nothing limits who can send this" });
        const roles = strings(d.roles);
        if (roles.length) chips.push({ text: roles.slice(0, 2).join(", ") + (roles.length > 2 ? ` +${roles.length - 2}` : ""), tone: "muted", icon: "user", title: `Who may use it: ${roles.join(", ")}` });
        if (bool(d.rateLimited)) chips.push({ text: "Limited", tone: "muted", icon: "gauge", title: "Turns away visitors who ask too often" });
        const facts: Fact[] = str(d.name) ? [{ k: "Named", v: str(d.name), code: true }] : [];
        add({ id: n.id, kind: "route", title: routeTitle(n), sub: typeText(n), rows: [], lane: 1, width: CARD_W, height: ROUTE_H, facts, chips, ...common });
        break;
      }
      case "intent": {
        const d = n.data ?? {};
        const steps = num(d.steps);
        const chips: Chip[] = [];
        if (steps) chips.push({ text: `${steps} ${n.subkind === "process" ? (steps === 1 ? "stage" : "stages") : steps === 1 ? "step" : "steps"}`, tone: "muted" });
        const kinds = d.stepKinds && typeof d.stepKinds === "object" ? (d.stepKinds as Record<string, unknown>) : {};
        for (const [k, c] of Object.entries(kinds)
          .filter(([, c]) => typeof c === "number")
          .sort((a, b) => (b[1] as number) - (a[1] as number) || a[0].localeCompare(b[0]))
          .slice(0, 2)) chips.push({ text: `${c} ${k}`, tone: "muted", title: `${c} ${k} ${(c as number) === 1 ? "step" : "steps"}` });
        add({ id: n.id, kind: "intent", title: n.label, sub: typeText(n), rows: [], lane: 2, width: CARD_W, height: INTENT_H, facts: [], chips, ...common });
        break;
      }
      case "resource":
        add({ id: n.id, kind: "resource", title: n.label, sub: typeText(n), rows: [], lane: 3, width: CARD_W, height: SMALL_H, ...common });
        break;
      case "external":
        add({ id: n.id, kind: "external", title: n.label, sub: typeText(n), rows: [], lane: 1, width: CARD_W, height: SMALL_H, ...common });
        break;
      case "unresolved": {
        const callers = graph.edges.filter((e) => e.to === n.id && e.kind !== "contains").map((e) => nodes.get(e.from)).filter((x): x is FlowNode => !!x);
        const facts: Fact[] = callers.slice(0, 2).map((c) => {
          const host = c.kind === "element" ? nodes.get(c.group ?? "") ?? nodes.get(containedBy.get(c.id) ?? "") : c;
          return { k: "Called by", v: `${host?.label ?? c.label}${c.line ? ` · line ${c.line}` : ""}` };
        });
        add({ id: n.id, kind: "unresolved", title: n.label, sub: typeText(n), rows: [], lane: 1, width: CARD_W, height: UNRESOLVED_H, facts, chips: [{ text: "Not found", tone: "danger", icon: "alert" }], ...common });
        break;
      }
    }
  }
  const sharedRows = rowsBy.get(SHARED_ID) ?? [];
  if (sharedRows.length && !filters.hideShared) {
    const comps = new Set(sharedRows.map((r) => r.component ?? ""));
    add({
      id: SHARED_ID, kind: "shared", title: "Shared on every page", sub: `${sharedRows.length} ${sharedRows.length === 1 ? "button or link" : "buttons and links"} from layouts and parts`,
      rows: sharedRows, lane: 0, width: CARD_W, height: sharedHeight(sharedRows.length, filters.sharedOpen, comps.size),
      problems: problemsBy.get(SHARED_ID) ?? 0, rowProblems: rowProblems.get(SHARED_ID) ?? {}, open: filters.sharedOpen,
    });
  }

  // ---- lines -------------------------------------------------------------------------------------
  const jedges: JEdge[] = [];
  const seen = new Set<string>();
  for (const e of graph.edges) {
    if (e.kind === "contains") continue;
    const fromEl = nodes.get(e.from)?.kind === "element";
    const source = fromEl ? rowHost.get(e.from) : e.from;
    const target = nodes.get(e.to)?.kind === "element" ? rowHost.get(e.to) : e.to;
    if (!source || !target || !jnodes.has(source) || !jnodes.has(target) || source === target || seen.has(e.id)) continue;
    seen.add(e.id);
    const via = Array.isArray(e.data?.via) ? (e.data!.via as unknown[]).filter((x): x is string => typeof x === "string") : [];
    const category = str(jnodes.get(target)?.flow?.data?.category);
    jedges.push({
      // a closed shared card has no row connection points: its lines leave from the card
      id: e.id, kind: e.kind, source, sourceHandle: fromEl && (source !== SHARED_ID || filters.sharedOpen) ? e.from : undefined, target,
      words: edgeWords(e.kind, e.label, { category }), via: fromEl ? [e.from, ...via] : via,
      back: e.kind === "renders" || e.kind === "redirects", quiet: e.kind === "renders" || source === SHARED_ID, server: e,
    });
  }
  jedges.sort((a, b) => EDGE_ORDER.indexOf(a.kind) - EDGE_ORDER.indexOf(b.kind) || a.id.localeCompare(b.id));

  // ---- what is shown -----------------------------------------------------------------------------
  let keep = new Set(jnodes.keys());
  let hiddenEndpoints = 0;
  if (!focused && !filters.showOrphans) {
    const reach = reachableFromPages(jnodes, jedges);
    const dropped = [...keep].filter((id) => !reach.has(id));
    hiddenEndpoints = dropped.filter((id) => jnodes.get(id)!.kind === "route").length;
    keep = new Set([...keep].filter((id) => reach.has(id)));
  }
  let hiddenConnections = 0;
  if (filters.hideConnections) {
    for (const id of [...keep]) if (jnodes.get(id)!.kind === "resource") { keep.delete(id); hiddenConnections++; }
  }
  // a focused journey is what connects to the thing in focus: leaving the shared card out must not leave its destinations behind
  if (focused && graph.focus && jnodes.has(graph.focus)) keep = connectedTo(graph.focus, jedges, keep, opts.reach);
  if (filters.onlyProblems) keep = problemNeighbourhood(jnodes, jedges, keep);

  const nodesOut = [...keep].map((id) => jnodes.get(id)!);
  const edgesOut = jedges.filter((e) => keep.has(e.source) && keep.has(e.target));
  assignLanes(nodesOut, edgesOut);
  return {
    nodes: nodesOut, edges: edgesOut, byId: new Map(nodesOut.map((n) => [n.id, n])), rowHost,
    hidden: { endpoints: hiddenEndpoints, shared: filters.hideShared ? countShared(graph) : 0, connections: hiddenConnections },
    warnings,
  };
}

const countShared = (g: FlowGraph) => g.nodes.filter((n) => n.kind === "element" && n.shared).length;

/** Everything a visitor can reach from a page: what its buttons send, what those run, and where they land. */
export function reachableFromPages(nodes: Map<string, JNode>, edges: JEdge[]): Set<string> {
  const out = new Map<string, JEdge[]>();
  for (const e of edges) (out.get(e.source) ?? out.set(e.source, []).get(e.source)!).push(e);
  const seen = new Set<string>();
  const queue: string[] = [];
  const visit = (id: string) => { if (!seen.has(id)) { seen.add(id); queue.push(id); } };
  for (const n of nodes.values()) if (n.kind === "page" || n.kind === "shared") visit(n.id);
  // the request that shows a page is part of that page's story
  for (const e of edges) if (e.kind === "renders" && seen.has(e.target)) visit(e.source);
  for (let i = 0; i < queue.length; i++) for (const e of out.get(queue[i]!) ?? []) visit(e.target);
  // an unresolved call or a foreign site is always worth seeing
  for (const n of nodes.values()) if (n.kind === "unresolved" || n.kind === "external") visit(n.id);
  return seen;
}

/** The cards joined to `start` by lines, in either direction, among `within`, at most `reach` lines away (a page's buttons count as inside it). */
export function connectedTo(start: string, edges: JEdge[], within: Set<string>, reach = Infinity): Set<string> {
  const near = new Map<string, string[]>();
  for (const e of edges) {
    if (!within.has(e.source) || !within.has(e.target)) continue;
    (near.get(e.source) ?? near.set(e.source, []).get(e.source)!).push(e.target);
    (near.get(e.target) ?? near.set(e.target, []).get(e.target)!).push(e.source);
  }
  const seen = new Set<string>(within.has(start) ? [start] : []);
  const dist = new Map<string, number>([...seen].map((id) => [id, 0]));
  const queue = [...seen];
  for (let i = 0; i < queue.length; i++) {
    const d = dist.get(queue[i]!)!;
    if (d >= reach) continue;
    for (const n of near.get(queue[i]!) ?? []) if (!seen.has(n)) { seen.add(n); dist.set(n, d + 1); queue.push(n); }
  }
  return seen;
}

/** Cards with a problem, what holds them, and what sits right next to them. */
export function problemNeighbourhood(nodes: Map<string, JNode>, edges: JEdge[], within: Set<string>): Set<string> {
  const bad = new Set([...within].filter((id) => (nodes.get(id)?.problems ?? 0) > 0));
  const out = new Set(bad);
  for (const e of edges) {
    if (!within.has(e.source) || !within.has(e.target)) continue;
    if (bad.has(e.source)) out.add(e.target);
    if (bad.has(e.target)) out.add(e.source);
  }
  return out;
}

/**
 * Lane per card: pages and the shared card first, then requests, then logic flows (a flow
 * that runs another sits one lane further right), connections last.
 */
export function assignLanes(nodes: JNode[], edges: JEdge[]): void {
  const by = new Map(nodes.map((n) => [n.id, n]));
  const depth = new Map<string, number>();
  for (const n of nodes) if (n.kind === "intent") depth.set(n.id, 0);
  const inner = edges.filter((e) => e.kind === "runs" && by.get(e.source)?.kind === "intent" && by.get(e.target)?.kind === "intent");
  for (let pass = 0; pass < 6; pass++) {
    let changed = false;
    for (const e of inner) {
      const want = (depth.get(e.source) ?? 0) + 1;
      if (want > (depth.get(e.target) ?? 0) && want <= 4) { depth.set(e.target, want); changed = true; }
    }
    if (!changed) break;
  }
  let lastIntentLane = 1;
  for (const n of nodes) {
    if (n.kind === "page" || n.kind === "shared") n.lane = 0;
    else if (n.kind === "route" || n.kind === "external" || n.kind === "unresolved") n.lane = 1;
    else if (n.kind === "intent") { n.lane = 2 + (depth.get(n.id) ?? 0); lastIntentLane = Math.max(lastIntentLane, n.lane); }
  }
  for (const n of nodes) if (n.kind === "resource") n.lane = lastIntentLane + 1;
}

// ---- reading the map ------------------------------------------------------------------------------

export interface Neighbour {
  edge: JEdge;
  node: JNode;
  /** The row on the other end, when the line leaves from one. */
  row?: ElementRow;
}

/** Lines into and out of a card (or one of its rows). */
export function neighbours(j: Journey, id: string): { incoming: Neighbour[]; outgoing: Neighbour[] } {
  const host = j.rowHost.get(id);
  const card = host ?? id;
  const rowOf = (hostId: string, handle?: string) => (handle ? j.byId.get(hostId)?.rows.find((r) => r.id === handle) : undefined);
  const incoming: Neighbour[] = [];
  const outgoing: Neighbour[] = [];
  for (const e of j.edges) {
    if (e.target === card) {
      const n = j.byId.get(e.source);
      if (n) incoming.push({ edge: e, node: n, row: rowOf(e.source, e.sourceHandle) });
    }
    if (e.source === card && (!host || e.sourceHandle === id || e.via.includes(id))) {
      const n = j.byId.get(e.target);
      if (n) outgoing.push({ edge: e, node: n });
    }
  }
  return { incoming, outgoing };
}

/** The lines that animate when `id` (a card or a row) is selected: the ones that touch it. */
export function edgesTouching(j: Journey, id: string | null): string[] {
  if (!id) return [];
  const isRow = j.rowHost.has(id);
  return j.edges
    .filter((e) => (isRow ? e.sourceHandle === id || e.via.includes(id) : e.source === id || e.target === id))
    .map((e) => e.id);
}

/** Every card whose name or address contains the text. A row that matches brings its card. */
export function searchJourney(j: Journey, query: string): Set<string> {
  const q = query.trim().toLowerCase();
  const hit = new Set<string>();
  if (!q) return hit;
  const has = (...xs: (string | undefined)[]) => xs.some((x) => x && x.toLowerCase().includes(q));
  for (const n of j.nodes) {
    const d = n.flow?.data ?? {};
    if (has(n.title, n.flow?.label, str(d.path), str(d.name), str(d.template), str(d.heading), n.flow?.id)) hit.add(n.id);
    for (const r of n.rows) if (has(r.label, r.url, r.method)) hit.add(n.id);
  }
  return hit;
}

export const stepCount = (n: JNode) => num(n.flow?.data?.steps);
