// The journey as React Flow wants it: one node per card, one edge per line.
// Pure, so the wiring (which point a line leaves from, what it is called) is tested.
import { MarkerType, type Edge, type Node } from "@xyflow/react";
import type { Journey } from "./model";
import type { JourneyLayout } from "./layout";
import type { CardData } from "./nodes";

const ARROW = { type: MarkerType.ArrowClosed, width: 16, height: 16, color: "var(--cv-arrow, #94a3b8)" };

export interface JourneyElements {
  nodes: Node<CardData>[];
  edges: Edge[];
  /** id -> the two ends, for the flow animation */
  flowEdges: { id: string; from: string; to: string }[];
}

export function journeyElements(j: Journey, layout: JourneyLayout, opts: { dev?: boolean } = {}): JourneyElements {
  const nodes: Node<CardData>[] = j.nodes.map((n) => ({
    id: n.id,
    type: n.kind,
    position: layout.positions[n.id] ?? { x: 0, y: 0 },
    data: { node: n },
    width: n.width,
    height: n.height,
    selectable: true,
    deletable: false,
    connectable: false,
  }));
  const edges: Edge[] = j.edges.map((e) => ({
    id: e.id,
    type: "flow",
    source: e.source,
    sourceHandle: e.sourceHandle ?? (j.byId.get(e.source)?.kind === "page" ? undefined : "out"),
    target: e.target,
    targetHandle: "in",
    markerEnd: ARROW,
    selectable: false,
    deletable: false,
    focusable: false,
    data: {
      label: opts.dev ? e.words.raw : e.words.text,
      raw: e.words.raw,
      tip: e.words.tip,
      tone: "normal",
      quiet: e.quiet,
      palette: "journey",
      curve: e.back ? undefined : "bezier",
      backOffset: 76,
      redirects: e.kind === "redirects",
      renders: e.kind === "renders",
      from: e.source,
      insertable: false,
      plain: false,
    },
  }));
  return { nodes, edges, flowEdges: j.edges.map((e) => ({ id: e.id, from: e.source, to: e.target })) };
}

/**
 * Which lines the map draws. "Shows" lines are a page's own address (already on its card) and
 * "on success" lines loop back across the map, so the overview keeps both for when something they
 * touch is selected; a focused journey draws the redirects, because they are where it ends.
 */
export function withHiddenLoops<E extends Edge>(edges: E[], opts: { focused: boolean; revealed: ReadonlySet<string> }): E[] {
  return edges.map((e) => {
    const loop = e.data?.renders === true || (e.data?.redirects === true && !opts.focused);
    return loop && !opts.revealed.has(e.id) ? { ...e, hidden: true } : e;
  });
}
