// Auto-layout (left to right) and the manual positions kept beside it.
//
// Layout is a property of the editor, not of the configuration: positions are
// never written to BCL. They are remembered per draft, file and block in
// localStorage so a reload keeps a hand-arranged canvas.
import dagre from "@dagrejs/dagre";

export interface Size {
  width: number;
  height: number;
}

export interface LayoutNode extends Size {
  id: string;
}

export interface Point {
  x: number;
  y: number;
}

export interface LayoutEdge {
  from: string;
  to: string;
  /** reserve room for a label of this size on the line */
  label?: Size;
  /** identifies the line in the returned label map */
  key?: string;
}

export interface Layout {
  /** top-left corner per node */
  positions: Record<string, Point>;
  /** centre of each reserved label, by edge key */
  labels: Record<string, Point>;
}

/** Rough pixel size of a label so the layout can leave room for it. */
export function labelSize(text: string): Size {
  return { width: Math.min(184, Math.round(text.length * 6.3) + 26), height: 24 };
}

/**
 * Deterministic: the same nodes and edges in the same order give the same result.
 * Edge labels are laid out as if they were tiny nodes between the ranks, so a
 * label never lands on top of a step.
 */
export function layoutGraph(nodes: LayoutNode[], edges: LayoutEdge[], dir: "LR" | "TB" = "LR"): Layout {
  const g = new dagre.graphlib.Graph({ multigraph: true });
  g.setGraph({ rankdir: dir, nodesep: 56, ranksep: 112, marginx: 24, marginy: 24, ranker: "network-simplex" });
  g.setDefaultEdgeLabel(() => ({}));
  const ids = new Set(nodes.map((n) => n.id));
  for (const n of nodes) g.setNode(n.id, { width: n.width, height: n.height });
  const names: string[] = [];
  edges.forEach((e, i) => {
    if (!ids.has(e.from) || !ids.has(e.to) || e.from === e.to) return;
    const name = String(i);
    names[i] = name;
    g.setEdge(e.from, e.to, e.label ? { width: e.label.width, height: e.label.height, labelpos: "c", minlen: 1 } : {}, name);
  });
  dagre.layout(g);
  const positions: Record<string, Point> = {};
  for (const n of nodes) {
    const p = g.node(n.id);
    // dagre reports centres; the canvas wants top-left corners.
    positions[n.id] = { x: Math.round(p.x - n.width / 2), y: Math.round(p.y - n.height / 2) };
  }
  const labels: Record<string, Point> = {};
  edges.forEach((e, i) => {
    if (!e.label || !names[i]) return;
    const d = g.edge(e.from, e.to, names[i]!) as { x?: number; y?: number } | undefined;
    if (d?.x !== undefined && d.y !== undefined) labels[e.key ?? String(i)] = { x: Math.round(d.x), y: Math.round(d.y) };
  });
  return { positions, labels };
}

export function autoLayout(nodes: LayoutNode[], edges: LayoutEdge[], dir: "LR" | "TB" = "LR"): Record<string, Point> {
  return layoutGraph(nodes, edges, dir).positions;
}

export const layoutKey = (draftId: string, file: string, path: string) => `studio.canvas.v1|${draftId}|${file}|${path}`;

export function loadPositions(key: string): Record<string, Point> {
  try {
    const raw = localStorage.getItem(key);
    if (!raw) return {};
    const v = JSON.parse(raw) as unknown;
    if (!v || typeof v !== "object") return {};
    const out: Record<string, Point> = {};
    for (const [id, p] of Object.entries(v as Record<string, unknown>)) {
      const pt = p as Partial<Point>;
      if (typeof pt?.x === "number" && typeof pt?.y === "number") out[id] = { x: pt.x, y: pt.y };
    }
    return out;
  } catch {
    return {};
  }
}

export function savePositions(key: string, positions: Record<string, Point>): void {
  try {
    localStorage.setItem(key, JSON.stringify(positions));
  } catch {
    /* private mode or quota: the layout just is not remembered */
  }
}

export function clearPositions(key: string): void {
  try {
    localStorage.removeItem(key);
  } catch {
    /* ignore */
  }
}

/** Saved positions win; anything without one takes the automatic position. */
export function mergePositions(auto: Record<string, Point>, saved: Record<string, Point>): Record<string, Point> {
  const out: Record<string, Point> = {};
  for (const id of Object.keys(auto)) out[id] = saved[id] ?? auto[id]!;
  return out;
}

// Node footprints. Every card is 264px wide and its height is a sum of fixed
// parts (header, port rows, fact rows, footer), so the layout knows a node's size
// before it is drawn and the CSS uses the same numbers.
export const CARD_W = 264;
export const HEAD_H = 56;
export const ROW_H = 24;
export const SEC_PAD = 8;
export const FOOT_H = 36;
/** the 1px border on each side */
export const BORDER = 2;

export interface CardParts {
  /** port rows (fact names beside the handles) */
  ports?: number;
  /** fact rows under the header, including the "+ n more" row */
  rows?: number;
  /** a footer: chips and/or the "ends the run" band */
  foot?: boolean;
}

export function cardSize(p: CardParts): Size {
  const ports = p.ports ? p.ports * ROW_H + SEC_PAD : 0;
  const rows = p.rows ? p.rows * ROW_H + SEC_PAD : 0;
  return { width: CARD_W, height: HEAD_H + ports + rows + (p.foot ? FOOT_H : 0) + BORDER };
}

/** Handles sit level with the middle of the header, so every card connects at the same height. */
export const HANDLE_Y = 30;
export const INTENT_ROW = ROW_H;
export const INPUT_SIZE: Size = cardSize({});
export const STEP_SIZE: Size = cardSize({ rows: 1 });
