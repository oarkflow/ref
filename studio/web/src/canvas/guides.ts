// Alignment guides while dragging: when an edge or the centre of the moving box
// lines up (almost) with another box, draw a guide and pull the box onto it.
import type { Rect } from "./labelPlacement";

export interface Guides {
  /** x positions of vertical guides */
  x: number[];
  /** y positions of horizontal guides */
  y: number[];
  /** how far to move the box to sit exactly on the nearest guide */
  dx: number;
  dy: number;
}

const marks = (a: number, size: number) => [a, a + size / 2, a + size];

export function alignGuides(moving: Rect, others: Rect[], threshold = 6): Guides {
  let dx = 0;
  let dy = 0;
  let bestX = threshold + 1;
  let bestY = threshold + 1;
  const x = new Set<number>();
  const y = new Set<number>();
  const mx = marks(moving.x, moving.w);
  const my = marks(moving.y, moving.h);
  for (const o of others) {
    for (const tx of marks(o.x, o.w)) {
      for (const m of mx) {
        const d = tx - m;
        if (Math.abs(d) <= threshold && Math.abs(d) < bestX) { bestX = Math.abs(d); dx = d; }
      }
    }
    for (const ty of marks(o.y, o.h)) {
      for (const m of my) {
        const d = ty - m;
        if (Math.abs(d) <= threshold && Math.abs(d) < bestY) { bestY = Math.abs(d); dy = d; }
      }
    }
  }
  // collect every guide the (snapped) box now sits on
  const sx = marks(moving.x + dx, moving.w);
  const sy = marks(moving.y + dy, moving.h);
  for (const o of others) {
    for (const tx of marks(o.x, o.w)) if (sx.some((m) => Math.abs(m - tx) < 0.5)) x.add(Math.round(tx));
    for (const ty of marks(o.y, o.h)) if (sy.some((m) => Math.abs(m - ty) < 0.5)) y.add(Math.round(ty));
  }
  return { x: [...x], y: [...y], dx: bestX <= threshold ? dx : 0, dy: bestY <= threshold ? dy : 0 };
}
