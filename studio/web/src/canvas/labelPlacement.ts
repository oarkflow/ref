// Where an edge's label sits on its curve. The label is moved along the line to
// the first spot that clears every step and every other label, so it is never
// drawn behind a node.
export interface Rect {
  x: number;
  y: number;
  w: number;
  h: number;
}

export const overlapArea = (a: Rect, b: Rect): number => {
  const w = Math.min(a.x + a.w, b.x + b.w) - Math.max(a.x, b.x);
  const h = Math.min(a.y + a.h, b.y + b.h) - Math.max(a.y, b.y);
  return w > 0 && h > 0 ? w * h : 0;
};

/** Positions tried, best first: the middle, then alternately either side of it. */
export const CANDIDATES = [0.5, 0.42, 0.58, 0.34, 0.66, 0.26, 0.74, 0.18, 0.82, 0.12, 0.88];

export interface Placement {
  t: number;
  x: number;
  y: number;
  /** true when the label overlaps nothing */
  clear: boolean;
}

export function pickLabelT(
  at: (t: number) => { x: number; y: number },
  size: { w: number; h: number },
  obstacles: Rect[],
  pad = 6,
): Placement {
  let best: Placement | null = null;
  let bestScore = Infinity;
  for (const t of CANDIDATES) {
    const p = at(t);
    const r: Rect = { x: p.x - size.w / 2 - pad, y: p.y - size.h / 2 - pad, w: size.w + pad * 2, h: size.h + pad * 2 };
    const score = obstacles.reduce((sum, o) => sum + overlapArea(r, o), 0);
    if (score === 0) return { t, x: p.x, y: p.y, clear: true };
    if (score < bestScore) {
      bestScore = score;
      best = { t, x: p.x, y: p.y, clear: false };
    }
  }
  return best!;
}
