// Pure graph model for the canvas: derives nodes and edges from the block tree
// of an intent, process or pipeline, and turns canvas gestures into ops.
//
// Nothing here prints BCL beyond small raw field values (a list, a quoted name)
// and the short body of a new block. Everything else is left to the server's
// splice engine, so comments and untouched content survive every edit.
import type { BlockNode, Catalog, Op } from "../api/types";
import { emitList, parseList, quote, unquote } from "../lib/bcl";
import { blocksOf, childPath, fieldOf } from "../lib/paths";
import { summarize, typeNameOf, type NodeSummary } from "./kinds";
import { edgeLabel } from "./labels";

export type NodeTypeInfo = Catalog["node_types"][number];
export type EdgeTypeInfo = Catalog["edge_types"][number];

/** The one fact an intent's request decoder always provides. */
export const INPUT_FACT = "input";
/** Id of the virtual node that stands for the request decoder. */
export const INPUT_NODE = "__input";

export type Result<T> = ({ ok: true } & T) | { ok: false; error: string };

// ---------------------------------------------------------------------------
// small readers

function scalar(n: BlockNode | undefined, name: string): string | undefined {
  const raw = fieldOf(n, name)?.raw;
  if (raw === undefined) return undefined;
  return unquote(raw) ?? raw.trim();
}

function flag(n: BlockNode | undefined, name: string): boolean {
  return fieldOf(n, name)?.raw?.trim() === "true";
}

/** The name a list item stands for: `foo` and `"foo"` are the same fact. */
export function itemName(raw: string): string {
  return unquote(raw) ?? raw.trim();
}

/** How a name is written as a list item: bare when it is an identifier. */
export function itemRaw(name: string): string {
  return /^[A-Za-z_][A-Za-z0-9_]*$/.test(name) ? name : quote(name);
}

export interface NameList {
  /** The list is absent from the block. */
  absent: boolean;
  /** Item names, in order. */
  names: string[];
  /** Item source text, in order (preserved when the list is rewritten). */
  raws: string[];
  /** The list can be rewritten without losing content (no expressions/comments). */
  editable: boolean;
}

export function nameList(n: BlockNode | undefined, name: string): NameList {
  const f = fieldOf(n, name);
  if (!f || f.raw === undefined) return { absent: true, names: [], raws: [], editable: true };
  const items = parseList(f.raw);
  if (!items) return { absent: false, names: [], raws: [], editable: false };
  return { absent: false, names: items.map(itemName), raws: items, editable: true };
}

function uniqueId(base: string, taken: Iterable<string>): string {
  const set = new Set(taken);
  const clean = base.replace(/[^A-Za-z0-9_-]+/g, "_").replace(/^_+|_+$/g, "") || "node";
  if (!set.has(clean)) return clean;
  for (let i = 2; ; i++) if (!set.has(`${clean}_${i}`)) return `${clean}_${i}`;
}

// ---------------------------------------------------------------------------
// intent

/**
 * The step's settings as raw BCL by place: `timeout`, `config.url`, `task.role`.
 * Cards describe themselves from this, so they need no access to the block tree.
 */
export type Settings = Record<string, string>;

export function settingsOf(b: BlockNode): Settings {
  const out: Settings = {};
  for (const c of b.children ?? []) {
    if (c.kind === "field" && c.name && c.raw !== undefined) out[c.name] = c.raw;
    else if (c.kind === "block" && c.type) for (const g of c.children ?? []) if (g.kind === "field" && g.name && g.raw !== undefined) out[`${c.type}.${g.name}`] = g.raw;
  }
  return out;
}

export interface IntentNodeInfo {
  id: string;
  path: string;
  settings: Settings;
  family?: string;
  uses?: string;
  kind?: string;
  requires: string[];
  provides: string[];
  requiresRaw: string[];
  /** The requires list can be rewritten by a connect/disconnect gesture. */
  editable: boolean;
  chips: string[];
  isResponse: boolean;
  /** the catalog type ("branch", "http", ...) */
  typeName: string;
  summary: NodeSummary;
}

export interface IntentEdge {
  id: string;
  /** Producer node id, or INPUT_NODE. */
  from: string;
  /** Consumer node id. */
  to: string;
  fact: string;
}

export interface IntentGraph {
  kind: "intent";
  path: string;
  id: string;
  nodes: IntentNodeInfo[];
  edges: IntentEdge[];
  /** Requires that no node provides (and that are not the request input). */
  unresolved: { node: string; fact: string }[];
  /** Facts more than one node provides. */
  duplicates: { fact: string; nodes: string[] }[];
  usesInput: boolean;
  response?: string;
}

export function buildIntentGraph(root: BlockNode, catalog?: Catalog | null): IntentGraph {
  const response = scalar(root, "response");
  const nodes: IntentNodeInfo[] = blocksOf(root, "node").map((b) => {
    const req = nameList(b, "requires");
    const prov = nameList(b, "provides");
    const chips: string[] = [];
    const timeout = scalar(b, "timeout");
    if (timeout) chips.push(`timeout ${timeout}`);
    if (blocksOf(b, "retry").length) chips.push("retry");
    if (blocksOf(b, "bulkhead").length) chips.push("bulkhead");
    if (blocksOf(b, "authz").length) chips.push("authz");
    const onError = scalar(b, "on_error");
    if (onError && onError !== "fail") chips.push(`on_error ${onError}`);
    if (flag(b, "sensitive")) chips.push("sensitive");
    return {
      id: b.id ?? "",
      path: b.path,
      settings: settingsOf(b),
      family: scalar(b, "family"),
      uses: scalar(b, "uses"),
      kind: scalar(b, "kind"),
      requires: req.names,
      provides: prov.names,
      requiresRaw: req.raws,
      editable: req.editable,
      chips,
      isResponse: !!response && prov.names.includes(response),
      typeName: typeNameOf(b, catalog),
      summary: summarize(b, typeNameOf(b, catalog)),
    };
  });

  const providers = new Map<string, string[]>();
  for (const n of nodes) for (const f of n.provides) providers.set(f, [...(providers.get(f) ?? []), n.id]);

  const edges: IntentEdge[] = [];
  const unresolved: IntentGraph["unresolved"] = [];
  let usesInput = false;
  for (const c of nodes) {
    for (const f of c.requires) {
      const ps = (providers.get(f) ?? []).filter((p) => p !== c.id);
      if (ps.length) {
        for (const p of ps) edges.push({ id: `${p}:${f}->${c.id}`, from: p, to: c.id, fact: f });
      } else if (f === INPUT_FACT) {
        usesInput = true;
        edges.push({ id: `${INPUT_NODE}:${f}->${c.id}`, from: INPUT_NODE, to: c.id, fact: f });
      } else {
        unresolved.push({ node: c.id, fact: f });
      }
    }
  }
  const duplicates = [...providers.entries()].filter(([, ns]) => ns.length > 1).map(([fact, ns]) => ({ fact, nodes: ns }));
  return { kind: "intent", path: root.path, id: root.id ?? "", nodes, edges, unresolved, duplicates, usesInput, response };
}

/** True when adding producer -> consumer would close a loop. */
export function wouldCycle(g: IntentGraph, producer: string, consumer: string): boolean {
  if (producer === consumer) return true;
  // A cycle exists if the producer is already downstream of the consumer.
  const next = new Map<string, string[]>();
  for (const e of g.edges) next.set(e.from, [...(next.get(e.from) ?? []), e.to]);
  const seen = new Set<string>();
  const stack = [consumer];
  while (stack.length) {
    const cur = stack.pop()!;
    if (cur === producer) return true;
    if (seen.has(cur)) continue;
    seen.add(cur);
    stack.push(...(next.get(cur) ?? []));
  }
  return false;
}

/** connect: `fact` from `source` becomes one of `target`'s requires. */
export function connectIntent(g: IntentGraph, file: string, source: string, fact: string, target: string): Result<{ ops: Op[] }> {
  const t = g.nodes.find((n) => n.id === target);
  if (!t) return { ok: false, error: "Unknown target node." };
  if (source === target) return { ok: false, error: "A node cannot depend on itself." };
  if (source !== INPUT_NODE && !g.nodes.some((n) => n.id === source)) return { ok: false, error: "Unknown source node." };
  if (!t.editable) return { ok: false, error: `“${t.id}” has a requires list that is an expression; edit it in the form.` };
  if (t.requires.includes(fact)) return { ok: false, error: `“${t.id}” already requires ${fact}.` };
  if (wouldCycle(g, source, target)) return { ok: false, error: "That connection would create a cycle." };
  const value = emitList([...t.requiresRaw, itemRaw(fact)]);
  return { ok: true, ops: [{ op: "setField", file, path: childPath(t.path, "requires"), value }] };
}

/** disconnect: the edge's fact is dropped from the consumer's requires. */
export function disconnectIntent(g: IntentGraph, file: string, edge: Pick<IntentEdge, "to" | "fact">): Result<{ ops: Op[] }> {
  const t = g.nodes.find((n) => n.id === edge.to);
  if (!t) return { ok: false, error: "Unknown node." };
  if (!t.editable) return { ok: false, error: `“${t.id}” has a requires list that is an expression; edit it in the form.` };
  const idx = t.requires.indexOf(edge.fact);
  if (idx < 0) return { ok: false, error: "That dependency is no longer there." };
  const rest = t.requiresRaw.filter((_, i) => i !== idx);
  const path = childPath(t.path, "requires");
  return { ok: true, ops: [rest.length ? { op: "setField", file, path, value: emitList(rest) } : { op: "removeField", file, path }] };
}

/** Ops that add a node of the given catalog type to an intent. */
export function addIntentNode(g: IntentGraph, file: string, type: NodeTypeInfo): { ops: Op[]; id: string } {
  const id = uniqueId(type.name, g.nodes.map((n) => n.id));
  const facts = new Set(g.nodes.flatMap((n) => n.provides));
  const fact = uniqueId(id.replace(/-/g, "_"), facts);
  const body = [`family ${type.name}`, type.default_action ? `uses ${quote(type.default_action)}` : "", `provides [${itemRaw(fact)}]`]
    .filter(Boolean)
    .join("\n");
  return { id, ops: [{ op: "addBlock", file, parent: g.path, type: "node", id, body }] };
}

export function removeNodes(g: { nodes: { id: string; path: string }[] }, file: string, ids: string[]): Op[] {
  return g.nodes.filter((n) => ids.includes(n.id)).map((n) => ({ op: "removeBlock", file, path: n.path }));
}

export function renameIntentNode(g: IntentGraph, file: string, id: string, newId: string): Result<{ ops: Op[] }> {
  const n = g.nodes.find((x) => x.id === id);
  const next = newId.trim();
  if (!n) return { ok: false, error: "Unknown node." };
  if (!next) return { ok: false, error: "A node needs a name." };
  if (next === id) return { ok: true, ops: [] };
  if (g.nodes.some((x) => x.id === next)) return { ok: false, error: `There is already a node named “${next}”.` };
  return { ok: true, ops: [{ op: "renameBlock", file, path: n.path, newId: next }] };
}

// ---------------------------------------------------------------------------
// process

export interface StepInfo {
  id: string;
  path: string;
  settings: Settings;
  family?: string;
  intent?: string;
  process?: string;
  terminal: boolean;
  isStart: boolean;
  /** The step parks for a human (has a task block). */
  human: boolean;
  durable: boolean;
  chips: string[];
  typeName: string;
  summary: NodeSummary;
}

export type EdgeShape = "single" | "forward" | "backward" | "none";

export interface EdgeBlockInfo {
  id: string;
  path: string;
  /** every field of the edge as raw BCL: `timeout`, `event`, `attempts`... */
  settings: Settings;
  kind: string;
  from?: string;
  to?: string;
  sources: NameList;
  targets: NameList;
  condition?: string;
}

export interface ProcessEdge {
  /** Unique per drawn line. */
  key: string;
  block: EdgeBlockInfo;
  from: string;
  to: string;
  label: string;
  parks: boolean;
  errorPath: boolean;
}

export interface ProcessGraph {
  kind: "process";
  path: string;
  id: string;
  start?: string;
  steps: StepInfo[];
  edges: ProcessEdge[];
  edgeBlocks: EdgeBlockInfo[];
  /** Edge blocks with an endpoint that is not a step, or with nothing to draw. */
  unlinked: { block: EdgeBlockInfo; missing: string[] }[];
}

export function edgeShape(info: Pick<EdgeTypeInfo, "fields"> | undefined): EdgeShape {
  if (!info?.fields) return "single";
  const f = info.fields;
  if (f.includes("from") && f.includes("to")) return "single";
  if (f.includes("from") && f.includes("targets")) return "forward";
  if (f.includes("sources") && f.includes("to")) return "backward";
  return "none";
}

const edgeInfo = (catalog: Catalog | null | undefined, kind: string) => catalog?.edge_types.find((e) => e.name === kind);
const nodeInfo = (catalog: Catalog | null | undefined, family?: string) =>
  family ? catalog?.node_types.find((t) => t.name === family) : undefined;

function clip(s: string, n: number): string {
  const t = s.replace(/\s+/g, " ").trim();
  return t.length > n ? t.slice(0, n - 1) + "…" : t;
}

export function readEdgeBlock(b: BlockNode): EdgeBlockInfo {
  return {
    id: b.id ?? "",
    path: b.path,
    settings: settingsOf(b),
    kind: scalar(b, "kind") ?? "simple",
    from: scalar(b, "from"),
    to: scalar(b, "to"),
    sources: nameList(b, "sources"),
    targets: nameList(b, "targets"),
    condition: scalar(b, "condition") ?? scalar(b, "when"),
  };
}

export const edgeSources = (e: EdgeBlockInfo): string[] => [...(e.from ? [e.from] : []), ...e.sources.names];
export const edgeTargets = (e: EdgeBlockInfo): string[] => [...(e.to ? [e.to] : []), ...e.targets.names];

export function buildProcessGraph(root: BlockNode, catalog?: Catalog | null): ProcessGraph {
  const start = scalar(root, "start");
  const steps: StepInfo[] = blocksOf(root, "step").map((b) => {
    const family = scalar(b, "family");
    const chips: string[] = [];
    const timeout = scalar(b, "timeout");
    if (timeout) chips.push(`timeout ${timeout}`);
    if (blocksOf(b, "retry").length) chips.push("retry");
    if (scalar(b, "compensate")) chips.push("compensate");
    if (scalar(b, "skip_when")) chips.push("skip when");
    if (blocksOf(b, "authz").length) chips.push("authz");
    return {
      id: b.id ?? "",
      path: b.path,
      settings: settingsOf(b),
      family,
      intent: scalar(b, "intent"),
      process: scalar(b, "process"),
      terminal: flag(b, "terminal"),
      isStart: !!start && b.id === start,
      human: blocksOf(b, "task").length > 0,
      durable: !!nodeInfo(catalog, family)?.durable,
      chips,
      typeName: family ?? "action",
      summary: summarize(b, family ?? "action"),
    };
  });
  const stepIds = new Set(steps.map((s) => s.id));

  const edgeBlocks = blocksOf(root, "edge").map(readEdgeBlock);
  const edges: ProcessEdge[] = [];
  const unlinked: ProcessGraph["unlinked"] = [];
  for (const eb of edgeBlocks) {
    const info = edgeInfo(catalog, eb.kind);
    const srcs = edgeSources(eb);
    const dsts = edgeTargets(eb);
    const missing = [...srcs, ...dsts].filter((s) => !stepIds.has(s));
    if (!srcs.length || !dsts.length || missing.length) {
      unlinked.push({ block: eb, missing });
      continue;
    }
    const label = [eb.kind === "simple" || eb.kind === "branch" ? "" : edgeLabel(eb.kind), eb.condition ? clip(eb.condition, 30) : ""].filter(Boolean).join(" · ");
    for (const s of srcs) {
      for (const d of dsts) {
        edges.push({
          key: `${eb.id}:${s}->${d}`,
          block: eb,
          from: s,
          to: d,
          label,
          parks: !!info?.parks,
          errorPath: !!info?.error_path,
        });
      }
    }
  }
  return { kind: "process", path: root.path, id: root.id ?? "", start, steps, edges, edgeBlocks, unlinked };
}

const q = quote;

/** connect: a new edge (or a new member of an existing multi edge) of `kind`. */
export function connectProcess(
  g: ProcessGraph,
  catalog: Catalog | null | undefined,
  file: string,
  from: string,
  to: string,
  kind: string,
): Result<{ ops: Op[]; id?: string }> {
  if (!g.steps.some((s) => s.id === from) || !g.steps.some((s) => s.id === to)) return { ok: false, error: "Unknown step." };
  const shape = edgeShape(edgeInfo(catalog, kind));
  if (shape === "none") {
    return { ok: false, error: `“${kind}” edges have no explicit target; add one and set it in the form.` };
  }
  const dup = g.edges.find((e) => e.from === from && e.to === to && e.block.kind === kind);
  if (dup) return { ok: false, error: `“${from}” already reaches “${to}” through ${dup.block.id}.` };

  if (shape === "forward") {
    const same = g.edgeBlocks.find((e) => e.kind === kind && e.from === from && e.targets.editable && !e.targets.absent);
    if (same) {
      return {
        ok: true,
        ops: [{ op: "setField", file, path: childPath(same.path, "targets"), value: emitList([...same.targets.raws, q(to)]) }],
      };
    }
  }
  if (shape === "backward") {
    const same = g.edgeBlocks.find((e) => e.kind === kind && e.to === to && e.sources.editable && !e.sources.absent);
    if (same) {
      return {
        ok: true,
        ops: [{ op: "setField", file, path: childPath(same.path, "sources"), value: emitList([...same.sources.raws, q(from)]) }],
      };
    }
  }
  const id = uniqueId(`${from}_to_${to}`, g.edgeBlocks.map((e) => e.id));
  const lines =
    shape === "single"
      ? [`kind ${kind}`, `from ${q(from)}`, `to ${q(to)}`]
      : shape === "forward"
        ? [`kind ${kind}`, `from ${q(from)}`, `targets [${q(to)}]`]
        : [`kind ${kind}`, `sources [${q(from)}]`, `to ${q(to)}`];
  return { ok: true, id, ops: [{ op: "addBlock", file, parent: g.path, type: "edge", id, body: lines.join("\n") }] };
}

/** disconnect: removes one drawn line, and the edge block when nothing is left. */
export function disconnectProcess(_g: ProcessGraph, file: string, edge: ProcessEdge): Op[] {
  const b = edge.block;
  const drop = (list: NameList, name: string, field: "sources" | "targets"): Op[] => {
    const keep = list.raws.filter((_, i) => list.names[i] !== name);
    if (!list.editable || keep.length === 0) return [{ op: "removeBlock", file, path: b.path }];
    return [{ op: "setField", file, path: childPath(b.path, field), value: emitList(keep) }];
  };
  const single = b.from !== undefined && b.to !== undefined;
  if (single) return [{ op: "removeBlock", file, path: b.path }];
  if (b.from !== undefined && b.targets.names.length) return drop(b.targets, edge.to, "targets");
  if (b.to !== undefined && b.sources.names.length) return drop(b.sources, edge.from, "sources");
  return [{ op: "removeBlock", file, path: b.path }];
}

export function addStep(g: ProcessGraph, file: string, type: NodeTypeInfo): { ops: Op[]; id: string } {
  const id = uniqueId(type.name, g.steps.map((s) => s.id));
  const lines = [`family ${type.name}`, type.terminal ? "terminal true" : ""].filter(Boolean);
  return { id, ops: [{ op: "addBlock", file, parent: g.path, type: "step", id, body: lines.join("\n") }] };
}

export function setStart(g: ProcessGraph, file: string, id: string): Op[] {
  return [{ op: "setField", file, path: childPath(g.path, "start"), value: q(id) }];
}

/** Ops that remove steps together with every edge reference to them. */
export function deleteSteps(g: ProcessGraph, file: string, ids: string[]): Op[] {
  const gone = new Set(ids);
  const ops: Op[] = [];
  for (const b of g.edgeBlocks) {
    const dead = (b.from && gone.has(b.from)) || (b.to && gone.has(b.to));
    const keepSrc = b.sources.raws.filter((_, i) => !gone.has(b.sources.names[i]!));
    const keepDst = b.targets.raws.filter((_, i) => !gone.has(b.targets.names[i]!));
    const srcEmptied = b.sources.names.length > 0 && keepSrc.length === 0;
    const dstEmptied = b.targets.names.length > 0 && keepDst.length === 0;
    if (dead || srcEmptied || dstEmptied) {
      ops.push({ op: "removeBlock", file, path: b.path });
      continue;
    }
    if (keepSrc.length !== b.sources.names.length && b.sources.editable) {
      ops.push({ op: "setField", file, path: childPath(b.path, "sources"), value: emitList(keepSrc) });
    }
    if (keepDst.length !== b.targets.names.length && b.targets.editable) {
      ops.push({ op: "setField", file, path: childPath(b.path, "targets"), value: emitList(keepDst) });
    }
  }
  if (g.start && gone.has(g.start)) ops.push({ op: "removeField", file, path: childPath(g.path, "start") });
  ops.push(...removeNodes({ nodes: g.steps }, file, ids));
  return ops;
}

/** Renames a step and rewrites every reference to it. */
export function renameStep(g: ProcessGraph, file: string, id: string, newId: string): Result<{ ops: Op[] }> {
  const s = g.steps.find((x) => x.id === id);
  const next = newId.trim();
  if (!s) return { ok: false, error: "Unknown step." };
  if (!next) return { ok: false, error: "A step needs a name." };
  if (next === id) return { ok: true, ops: [] };
  if (g.steps.some((x) => x.id === next)) return { ok: false, error: `There is already a step named “${next}”.` };
  const ops: Op[] = [{ op: "renameBlock", file, path: s.path, newId: next }];
  const swap = (list: NameList) => list.raws.map((r, i) => (list.names[i] === id ? q(next) : r));
  for (const b of g.edgeBlocks) {
    if (b.from === id) ops.push({ op: "setField", file, path: childPath(b.path, "from"), value: q(next) });
    if (b.to === id) ops.push({ op: "setField", file, path: childPath(b.path, "to"), value: q(next) });
    if (b.sources.names.includes(id) && b.sources.editable) {
      ops.push({ op: "setField", file, path: childPath(b.path, "sources"), value: emitList(swap(b.sources)) });
    }
    if (b.targets.names.includes(id) && b.targets.editable) {
      ops.push({ op: "setField", file, path: childPath(b.path, "targets"), value: emitList(swap(b.targets)) });
    }
  }
  if (g.start === id) ops.push({ op: "setField", file, path: childPath(g.path, "start"), value: q(next) });
  return { ok: true, ops };
}

// ---------------------------------------------------------------------------
// pipeline

export interface ReviewInfo {
  path: string;
  label: string;
}

export interface StageInfo {
  id: string;
  path: string;
  /** Position among ALL statements of the pipeline block (moveBlock counts those). */
  index: number;
  title?: string;
  reviews: ReviewInfo[];
  chips: string[];
}

export interface PipelineGraph {
  kind: "pipeline";
  path: string;
  id: string;
  stages: StageInfo[];
  /** Number of statements in the pipeline block. */
  size: number;
}

export function buildPipelineGraph(root: BlockNode): PipelineGraph {
  const kids = root.children ?? [];
  const stages: StageInfo[] = [];
  kids.forEach((b, index) => {
    if (b.kind !== "block" || b.type !== "stage") return;
    const chips: string[] = [];
    if (blocksOf(b, "sla").length || fieldOf(b, "sla")) chips.push("sla");
    const rules = blocksOf(b, "rule").length;
    if (rules) chips.push(`${rules} rule${rules > 1 ? "s" : ""}`);
    if (flag(b, "confirm_submit")) chips.push("confirm submit");
    stages.push({
      id: b.id ?? "",
      path: b.path,
      index,
      title: scalar(b, "title"),
      reviews: blocksOf(b, "review").map((r) => ({ path: r.path, label: scalar(r, "mode") ?? r.id ?? "review" })),
      chips,
    });
  });
  return { kind: "pipeline", path: root.path, id: root.id ?? "", stages, size: kids.length };
}

/** moveBlock ops that swap a stage with its neighbour (dir -1 = earlier). */
export function moveStage(g: PipelineGraph, file: string, id: string, dir: -1 | 1): Op[] {
  const i = g.stages.findIndex((s) => s.id === id);
  const s = g.stages[i];
  if (!s) return [];
  if (dir === -1) {
    const prev = g.stages[i - 1];
    return prev ? [{ op: "moveBlock", file, path: s.path, index: prev.index }] : [];
  }
  const next = g.stages[i + 1];
  // Index counts positions before the move: "before sibling n+1" is "after n".
  return next ? [{ op: "moveBlock", file, path: s.path, index: next.index + 1 }] : [];
}

export function addStage(g: PipelineGraph, file: string): { ops: Op[]; id: string } {
  const id = uniqueId("stage", g.stages.map((s) => s.id));
  return { id, ops: [{ op: "addBlock", file, parent: g.path, type: "stage", id, body: `title ${q(id)}` }] };
}

// ---------------------------------------------------------------------------
// dispatch

export type CanvasGraph = IntentGraph | ProcessGraph | PipelineGraph;

export const CANVAS_TYPES = ["intent", "process", "pipeline"] as const;
export const isCanvasType = (t: string | undefined): boolean => !!t && (CANVAS_TYPES as readonly string[]).includes(t);

export function buildGraph(root: BlockNode, catalog?: Catalog | null): CanvasGraph | null {
  switch (root.type) {
    case "intent": return buildIntentGraph(root, catalog);
    case "process": return buildProcessGraph(root, catalog);
    case "pipeline": return buildPipelineGraph(root);
    default: return null;
  }
}

/** Palette entries for a canvas kind: request intents cannot use durable node types. */
export function paletteTypes(catalog: Catalog | null | undefined, kind: "intent" | "process"): NodeTypeInfo[] {
  const all = catalog?.node_types ?? [];
  return kind === "intent" ? all.filter((t) => !t.durable) : all;
}

// ---------------------------------------------------------------------------
// batch disconnects (deleting several selected lines at once)

/** One op per consumer, however many of its dependencies are dropped. */
export function disconnectIntentMany(g: IntentGraph, file: string, edges: Pick<IntentEdge, "to" | "fact">[]): Op[] {
  const byNode = new Map<string, Set<string>>();
  for (const e of edges) byNode.set(e.to, (byNode.get(e.to) ?? new Set()).add(e.fact));
  const ops: Op[] = [];
  for (const [id, facts] of byNode) {
    const n = g.nodes.find((x) => x.id === id);
    if (!n || !n.editable) continue;
    const keep = n.requiresRaw.filter((_, i) => !facts.has(n.requires[i]!));
    if (keep.length === n.requiresRaw.length) continue;
    const path = childPath(n.path, "requires");
    ops.push(keep.length ? { op: "setField", file, path, value: emitList(keep) } : { op: "removeField", file, path });
  }
  return ops;
}

/** One op per edge block, however many of its drawn lines are dropped. */
export function disconnectProcessMany(_g: ProcessGraph, file: string, edges: ProcessEdge[]): Op[] {
  const byBlock = new Map<string, ProcessEdge[]>();
  for (const e of edges) byBlock.set(e.block.path, [...(byBlock.get(e.block.path) ?? []), e]);
  const ops: Op[] = [];
  for (const [path, list] of byBlock) {
    const b = list[0]!.block;
    const removeAll: Op = { op: "removeBlock", file, path };
    if (b.from !== undefined && b.to !== undefined) {
      ops.push(removeAll);
    } else if (b.from !== undefined && b.targets.names.length) {
      const gone = new Set(list.map((e) => e.to));
      const keep = b.targets.raws.filter((_, i) => !gone.has(b.targets.names[i]!));
      ops.push(!b.targets.editable || !keep.length ? removeAll : { op: "setField", file, path: childPath(path, "targets"), value: emitList(keep) });
    } else if (b.to !== undefined && b.sources.names.length) {
      const gone = new Set(list.map((e) => e.from));
      const keep = b.sources.raws.filter((_, i) => !gone.has(b.sources.names[i]!));
      ops.push(!b.sources.editable || !keep.length ? removeAll : { op: "setField", file, path: childPath(path, "sources"), value: emitList(keep) });
    } else {
      ops.push(removeAll);
    }
  }
  return ops;
}


// ---------------------------------------------------------------------------
// insert in place ("+" on a line, "+" after the last step)

const factOf = (id: string) => id.replace(/[^A-Za-z0-9_]+/g, "_").replace(/^(\d)/, "_$1");

/** Adds a step of `type` between a producer and a consumer that are already connected. */
export function insertIntentNode(g: IntentGraph, file: string, edge: Pick<IntentEdge, "from" | "to" | "fact">, type: NodeTypeInfo): Result<{ ops: Op[]; id: string }> {
  const consumer = g.nodes.find((n) => n.id === edge.to);
  if (!consumer) return { ok: false, error: "Unknown step." };
  if (!consumer.editable) return { ok: false, error: `“${consumer.id}” has a Needs list that is an expression; edit it in the form.` };
  const id = uniqueId(type.name, g.nodes.map((n) => n.id));
  const out = uniqueId(factOf(id), g.nodes.flatMap((n) => n.provides));
  const body = [`family ${type.name}`, type.default_action ? `uses ${quote(type.default_action)}` : "", `requires [${itemRaw(edge.fact)}]`, `provides [${itemRaw(out)}]`]
    .filter(Boolean)
    .join("\n");
  const raws = consumer.requiresRaw.map((r, i) => (consumer.requires[i] === edge.fact ? itemRaw(out) : r));
  return {
    ok: true,
    id,
    ops: [
      { op: "addBlock", file, parent: g.path, type: "node", id, body },
      { op: "setField", file, path: childPath(consumer.path, "requires"), value: emitList(raws) },
    ],
  };
}

/** Adds a step after `from`, consuming the first thing it produces. */
export function appendIntentNode(g: IntentGraph, file: string, from: string, type: NodeTypeInfo): Result<{ ops: Op[]; id: string }> {
  const src = g.nodes.find((n) => n.id === from);
  if (!src) return { ok: false, error: "Unknown step." };
  const id = uniqueId(type.name, g.nodes.map((n) => n.id));
  const out = uniqueId(factOf(id), g.nodes.flatMap((n) => n.provides));
  const body = [
    `family ${type.name}`,
    type.default_action ? `uses ${quote(type.default_action)}` : "",
    src.provides[0] ? `requires [${itemRaw(src.provides[0])}]` : "",
    `provides [${itemRaw(out)}]`,
  ].filter(Boolean).join("\n");
  return { ok: true, id, ops: [{ op: "addBlock", file, parent: g.path, type: "node", id, body }] };
}

/** Splits a simple (or fan-out) connection A -> B into A -> X -> B. */
export function insertStep(g: ProcessGraph, catalog: Catalog | null | undefined, file: string, edge: ProcessEdge, type: NodeTypeInfo): Result<{ ops: Op[]; id: string }> {
  const b = edge.block;
  const shape = edgeShape(edgeInfo(catalog, b.kind));
  const step = addStep(g, file, type);
  const ids = new Set([...g.steps.map((s) => s.id), step.id]);
  const eid = uniqueId(`${step.id}_to_${edge.to}`, g.edgeBlocks.map((e) => e.id));
  const link: Op = { op: "addBlock", file, parent: g.path, type: "edge", id: eid, body: [`kind simple`, `from ${q(step.id)}`, `to ${q(edge.to)}`].join("\n") };
  void ids;
  if (shape === "single") {
    return { ok: true, id: step.id, ops: [...step.ops, { op: "setField", file, path: childPath(b.path, "to"), value: q(step.id) }, link] };
  }
  if (shape === "forward" && b.targets.editable) {
    const raws = b.targets.raws.map((r, i) => (b.targets.names[i] === edge.to ? q(step.id) : r));
    return { ok: true, id: step.id, ops: [...step.ops, { op: "setField", file, path: childPath(b.path, "targets"), value: emitList(raws) }, link] };
  }
  return { ok: false, error: "Add a step to this connection from its form: it joins several steps." };
}

/** Adds a step after `from`, joined by a plain connection. */
export function appendStep(g: ProcessGraph, file: string, from: string, type: NodeTypeInfo): Result<{ ops: Op[]; id: string }> {
  if (!g.steps.some((s) => s.id === from)) return { ok: false, error: "Unknown step." };
  const step = addStep(g, file, type);
  const eid = uniqueId(`${from}_to_${step.id}`, g.edgeBlocks.map((e) => e.id));
  return {
    ok: true,
    id: step.id,
    ops: [...step.ops, { op: "addBlock", file, parent: g.path, type: "edge", id: eid, body: [`kind simple`, `from ${q(from)}`, `to ${q(step.id)}`].join("\n") }],
  };
}

// ---------------------------------------------------------------------------
// trace: the order a run visits the lines (visual only)

/** Groups of line ids; every line in a group is visited at the same time. */
export function traceOrder(g: CanvasGraph): string[][] {
  if (g.kind === "pipeline") return g.stages.slice(1).map((_, i) => [`seq:${i}`]);
  if (g.kind === "intent") {
    const level = new Map<string, number>();
    const lv = (id: string, seen: Set<string>): number => {
      if (level.has(id)) return level.get(id)!;
      if (seen.has(id)) return 0;
      seen.add(id);
      const ins = g.edges.filter((e) => e.to === id);
      const l = ins.length ? 1 + Math.max(...ins.map((e) => lv(e.from, seen))) : 0;
      level.set(id, l);
      return l;
    };
    const groups: string[][] = [];
    for (const e of g.edges) {
      const l = lv(e.to, new Set());
      (groups[l - 1] ??= []).push(e.id);
    }
    return groups.filter(Boolean);
  }
  // process: breadth first from the start step
  const start = g.start ?? g.steps[0]?.id;
  const groups: string[][] = [];
  const seen = new Set<string>(start ? [start] : []);
  const used = new Set<string>();
  let frontier = start ? [start] : [];
  while (frontier.length) {
    const next: string[] = [];
    const ids: string[] = [];
    for (const n of frontier) {
      for (const e of g.edges) {
        if (e.from !== n || used.has(e.key)) continue;
        used.add(e.key);
        ids.push(e.key);
        if (!seen.has(e.to)) {
          seen.add(e.to);
          next.push(e.to);
        }
      }
    }
    if (ids.length) groups.push(ids);
    frontier = next;
  }
  return groups;
}
