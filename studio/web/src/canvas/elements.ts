// Turns a graph into React Flow nodes and edges (positions, ports, styling).
// Pure, so layout determinism and diagnostics mapping are unit tested.
import { MarkerType, type Edge, type Node } from "@xyflow/react";
import type { Catalog, Diagnostic } from "../api/types";
import { isWithin } from "../lib/paths";
import { visualFor } from "./kinds";
import {
  INPUT_NODE, type CanvasGraph, type IntentGraph, type IntentNodeInfo, type PipelineGraph, type ProcessGraph, type StepInfo,
} from "./model";
import {
  INPUT_SIZE, cardSize, labelSize, layoutGraph, mergePositions, type Layout, type LayoutEdge, type LayoutNode, type Point,
} from "./layout";
import { MAX_ROWS, endOutcome, type Outcome } from "./look";
import { factEdgeLabel, processEdgeLabel } from "./edgeLabels";
import { rowsFor } from "./registry";
import type { DiagBadge } from "./nodes";
import { typeLabel } from "./labels";

export interface Elements {
  nodes: Node[];
  edges: Edge[];
  /** Automatic positions (before saved ones are applied). */
  auto: Record<string, Point>;
  layout: Layout;
}

/** Errors and warnings whose path lies inside `path` in `file`. */
export function diagFor(diagnostics: Diagnostic[], file: string, path: string): DiagBadge {
  let errors = 0;
  let warnings = 0;
  for (const d of diagnostics) {
    if (!d.path || (d.file && d.file !== file) || !isWithin(d.path, path)) continue;
    if (d.severity === "error") errors++;
    else if (d.severity === "warning") warnings++;
  }
  return { errors, warnings };
}

// The arrowheads take their colour from CSS variables, so both themes and the highlight state recolour them.
const ARROW = { type: MarkerType.ArrowClosed, width: 16, height: 16, color: "var(--cv-arrow, #94a3b8)" };
const ARROW_RED = { ...ARROW, color: "var(--cv-arrow-error, #dc2626)" };

export interface ElementOpts {
  catalog?: Catalog | null;
  diagnostics: Diagnostic[];
  file: string;
  saved: Record<string, Point>;
}

export function flowElements(graph: CanvasGraph, opts: ElementOpts): Elements {
  switch (graph.kind) {
    case "intent": return intentElements(graph, opts);
    case "process": return processElements(graph, opts);
    case "pipeline": return pipelineElements(graph, opts);
  }
}

function finish(layoutNodes: LayoutNode[], layoutEdges: LayoutEdge[], nodes: Node[], edges: Edge[], saved: Record<string, Point>): Elements {
  const layout = layoutGraph(layoutNodes, layoutEdges);
  const pos = mergePositions(layout.positions, saved);
  return { nodes: nodes.map((n) => ({ ...n, position: pos[n.id] ?? { x: 0, y: 0 } })), edges, auto: layout.positions, layout };
}

const rowCount = (n: number) => (n > MAX_ROWS ? MAX_ROWS : n);

function intentSizeOf(n: IntentNodeInfo, rows: number): { width: number; height: number } {
  const ports = Math.max(new Set(n.requires).size + 1, new Set(n.provides).size, 1);
  return cardSize({ ports, rows: rowCount(rows), foot: n.chips.length > 0 });
}

function intentElements(g: IntentGraph, o: ElementOpts): Elements {
  const nodes: Node[] = [];
  const layout: LayoutNode[] = [];
  if (g.usesInput) {
    nodes.push({ id: INPUT_NODE, type: "request", position: { x: 0, y: 0 }, data: { label: "Incoming request" }, deletable: false });
    layout.push({ id: INPUT_NODE, ...INPUT_SIZE });
  }
  const consumed = new Set(g.edges.map((e) => e.from));
  for (const n of g.nodes) {
    const unresolved = g.unresolved.filter((u) => u.node === n.id).map((u) => u.fact);
    const visual = visualFor(n.typeName, o.catalog, { terminal: n.isResponse });
    const rows = rowsFor(n, o.catalog);
    nodes.push({
      id: n.id,
      type: "intent",
      position: { x: 0, y: 0 },
      data: { info: n, rows, unresolved, diag: diagFor(o.diagnostics, o.file, n.path), visual, typeText: typeLabel(n.typeName), outcome: n.isResponse ? endOutcome(n.id) : undefined, canAppend: !n.isResponse && !consumed.has(n.id) },
    });
    layout.push({ id: n.id, ...intentSizeOf(n, rows.length) });
  }
  const providesCount = new Map(g.nodes.map((n) => [n.id, new Set(n.provides).size]));
  // several facts between the same two steps: each keeps its own line, the first says how many
  const pairs = new Map<string, string[]>();
  for (const e of g.edges) pairs.set(`${e.from}>${e.to}`, [...(pairs.get(`${e.from}>${e.to}`) ?? []), e.fact]);
  const firstOfPair = new Set<string>();
  const seenPair = new Set<string>();
  for (const e of g.edges) {
    const k = `${e.from}>${e.to}`;
    if (!seenPair.has(k)) { seenPair.add(k); firstOfPair.add(e.id); }
  }
  const edges: Edge[] = g.edges.map((e) => {
    const facts = pairs.get(`${e.from}>${e.to}`) ?? [e.fact];
    // every line says what travels along it; a step that hands one thing to another needs no chip unless it publishes several
    const multi = (providesCount.get(e.from) ?? 0) > 1 || facts.length > 1;
    const fl = factEdgeLabel({ fact: e.fact, facts, first: firstOfPair.has(e.id), fromRequest: e.from === INPUT_NODE });
    const label = e.from === INPUT_NODE || !multi ? "" : fl.text;
    return {
      id: e.id,
      type: "labeled",
      source: e.from,
      target: e.to,
      sourceHandle: `out:${e.fact}`,
      targetHandle: `in:${e.fact}`,
      markerEnd: e.from === INPUT_NODE ? undefined : ARROW,
      ariaLabel: `${e.fact}: ${e.from === INPUT_NODE ? "request" : e.from} to ${e.to}`,
      data: { label, raw: label, tip: fl.tip, tone: fl.tone, fact: e.fact, from: e.from, to: e.to, quiet: e.from === INPUT_NODE, insertable: true, carries: facts, count: firstOfPair.has(e.id) ? facts.length : 1 },
      className: e.fact === "input" ? "cv-edge-input" : undefined,
    };
  });
  const le: LayoutEdge[] = g.edges.map((e, i) => ({ from: e.from, to: e.to, key: edges[i]!.id, label: edges[i]!.data?.label ? labelSize(String(edges[i]!.data!.label)) : undefined }));
  return finish(layout, le, nodes, edges, o.saved);
}

function stepSizeOf(s: StepInfo, rows: number): { width: number; height: number } {
  return cardSize({ rows: rowCount(rows), foot: s.chips.length > 0 || s.terminal });
}

/** How a finishing step ends, from its own name and what it runs. */
export const outcomeOfStep = (s: StepInfo): Outcome => (s.terminal ? endOutcome(s.id, s.intent) : "neutral");

function processElements(g: ProcessGraph, o: ElementOpts): Elements {
  const stepRowsOf = new Map(g.steps.map((s) => [s.id, rowsFor({ ...s, uses: undefined, requires: [], provides: [] }, o.catalog, "process")]));
  const nodes: Node[] = g.steps.map((s) => ({
    id: s.id,
    type: "step",
    position: { x: 0, y: 0 },
    data: {
      info: s,
      rows: stepRowsOf.get(s.id) ?? [],
      diag: diagFor(o.diagnostics, o.file, s.path),
      visual: visualFor(s.typeName, o.catalog, { terminal: s.terminal, start: s.isStart, human: s.human }),
      typeText: typeLabel(s.typeName),
      outcome: s.terminal ? outcomeOfStep(s) : undefined,
      hasOutgoing: g.edges.some((e) => e.from === s.id),
      canAppend: !s.terminal && !g.edges.some((e) => e.from === s.id),
    },
  }));
  const layout: LayoutNode[] = g.steps.map((s) => ({ id: s.id, ...stepSizeOf(s, stepRowsOf.get(s.id)?.length ?? 0) }));
  const typeOf = (k: string) => o.catalog?.edge_types.find((t) => t.name === k);
  const edges: Edge[] = g.edges.map((e) => {
    const siblings = g.edgeBlocks.filter((b) => b.from === e.block.from && b.kind === e.block.kind);
    const lab = processEdgeLabel(e.block, { siblings, typeInfo: typeOf(e.block.kind) });
    return {
      id: e.key,
      type: "labeled",
      source: e.from,
      target: e.to,
      sourceHandle: "out",
      targetHandle: "in",
      markerEnd: e.errorPath ? ARROW_RED : ARROW,
      ariaLabel: `${e.block.kind} connection ${e.block.id}: ${e.from} to ${e.to}`,
      className: [e.parks ? "cv-edge-parks" : "", e.errorPath ? "cv-edge-error" : ""].filter(Boolean).join(" ") || undefined,
      data: { label: lab.text, raw: lab.raw, tip: lab.tip, tone: lab.tone, icon: lab.icon, needs: lab.needs?.prompt, blockPath: e.block.path, blockId: e.block.id, kind: e.block.kind, from: e.from, to: e.to, parks: e.parks, errorPath: e.errorPath, insertable: true, carries: e.block.condition ? [`when ${e.block.condition}`] : [] },
    };
  });
  const le: LayoutEdge[] = g.edges.map((e, i) => ({ from: e.from, to: e.to, key: e.key, label: edges[i]!.data?.label ? labelSize(String(edges[i]!.data!.label)) : undefined }));
  return finish(layout, le, nodes, edges, o.saved);
}

function pipelineElements(g: PipelineGraph, o: ElementOpts): Elements {
  const nodes: Node[] = g.stages.map((s, i) => ({
    id: s.id,
    type: "stage",
    position: { x: 0, y: 0 },
    data: { info: s, first: i === 0, last: i === g.stages.length - 1, diag: diagFor(o.diagnostics, o.file, s.path) },
  }));
  const layout: LayoutNode[] = g.stages.map((s) => ({ id: s.id, ...cardSize({ rows: Math.min(s.reviews.length, 4) + (s.reviews.length > 4 ? 1 : 0), foot: s.chips.length > 0 }) }));
  const seq = g.stages.slice(1).map((s, i) => ({ from: g.stages[i]!.id, to: s.id, key: `seq:${i}` }));
  const edges: Edge[] = seq.map((e) => ({
    id: e.key, type: "labeled", source: e.from, target: e.to, sourceHandle: "out", targetHandle: "in",
    markerEnd: ARROW, selectable: false, deletable: false, focusable: false, data: { label: "", plain: true },
  }));
  return finish(layout, seq, nodes, edges, o.saved);
}

export type { StepInfo };
