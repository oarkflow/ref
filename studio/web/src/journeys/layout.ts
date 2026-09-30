// Places the cards in lanes, left to right: pages, requests, logic flows, connections.
//
// Not a general graph layout. The lane of a card is fixed by what it is (model.assignLanes),
// and inside a lane cards are ordered by where the cards they connect to already sit, so lines
// stay short and mostly parallel. Deterministic: the same journey always lays out the same.
import type { JEdge, Journey, JNode } from "./model";

export interface Point {
  x: number;
  y: number;
}

export const LANE_GAP = 150;
export const ROW_GAP = 28;
export const MARGIN = 40;

export interface JourneyLayout {
  positions: Record<string, Point>;
  width: number;
  height: number;
  lanes: number;
}

const byTitle = (a: JNode, b: JNode) => a.title.localeCompare(b.title) || a.id.localeCompare(b.id);

/** Pages first in a steady order (shared card last); the rest follow their neighbours. */
export function layoutJourney(j: Journey): JourneyLayout {
  const lanes = new Map<number, JNode[]>();
  for (const n of j.nodes) (lanes.get(n.lane) ?? lanes.set(n.lane, []).get(n.lane)!).push(n);
  const order = [...lanes.keys()].sort((a, b) => a - b);

  const neighbours = new Map<string, string[]>();
  const link = (a: string, b: string) => (neighbours.get(a) ?? neighbours.set(a, []).get(a)!).push(b);
  for (const e of j.edges) { link(e.source, e.target); link(e.target, e.source); }

  const top = new Map<string, number>();
  const centre = (n: JNode) => (top.get(n.id) ?? 0) + n.height / 2;
  const laneOf = new Map(j.nodes.map((n) => [n.id, n.lane]));
  const sequence = new Map<number, JNode[]>();

  // 1. a first pass, lane by lane: order by where the neighbours already placed sit
  order.forEach((lane, li) => {
    const cards = lanes.get(lane)!;
    const want = (n: JNode): number | undefined => {
      const ys = (neighbours.get(n.id) ?? []).filter((id) => placedBefore(id, lane)).map((id) => centre(j.byId.get(id)!));
      return ys.length ? ys.reduce((a, b) => a + b, 0) / ys.length : undefined;
    };
    const placedBefore = (id: string, l: number) => (laneOf.get(id) ?? l) < l && top.has(id);
    const sorted = li === 0
      ? [...cards].sort((a, b) => (a.kind === "shared" ? 1 : 0) - (b.kind === "shared" ? 1 : 0) || byTitle(a, b))
      : [...cards].sort((a, b) => {
          const wa = want(a);
          const wb = want(b);
          if (wa !== undefined && wb !== undefined && wa !== wb) return wa - wb;
          if (wa === undefined && wb !== undefined) return 1;
          if (wb === undefined && wa !== undefined) return -1;
          return byTitle(a, b);
        });
    sequence.set(lane, sorted);
    const desired = sorted.map((n) => (li === 0 ? undefined : want(n)));
    place(sorted, desired.map((d, i) => (d === undefined ? undefined : d - sorted[i]!.height / 2)), top);
  });

  // 2. relax: pull every card toward the middle of what it connects to, in both directions,
  //    keeping each lane's order and removing overlaps with the least movement
  for (let sweep = 0; sweep < 6; sweep++) {
    const lanesInOrder = sweep % 2 === 0 ? order : [...order].reverse();
    for (const lane of lanesInOrder) {
      const cards = sequence.get(lane)!;
      const desired = cards.map((n) => {
        const ys = (neighbours.get(n.id) ?? []).filter((id) => laneOf.get(id) !== lane).map((id) => centre(j.byId.get(id)!));
        return ys.length ? ys.reduce((a, b) => a + b, 0) / ys.length - n.height / 2 : top.get(n.id);
      });
      place(cards, desired, top);
    }
  }

  // 3. lay out on the page
  let minY = Infinity;
  let maxY = 0;
  for (const n of j.nodes) { minY = Math.min(minY, top.get(n.id) ?? 0); maxY = Math.max(maxY, (top.get(n.id) ?? 0) + n.height); }
  if (!Number.isFinite(minY)) minY = 0;
  const positions: Record<string, Point> = {};
  order.forEach((lane, li) => {
    for (const n of lanes.get(lane)!) positions[n.id] = { x: MARGIN + li * (n.width + LANE_GAP), y: Math.round((top.get(n.id) ?? 0) - minY + MARGIN) };
  });
  const width = MARGIN * 2 + order.length * 280 + Math.max(0, order.length - 1) * LANE_GAP;
  return { positions, width, height: maxY - minY + MARGIN * 2, lanes: order.length };
}

/**
 * Stack `cards` (in order) as close to their wanted tops as a gap allows. Cards that want to
 * overlap form a cluster that sits at the average of their wishes (the Abacus method).
 * A card with no wish goes right under the one before.
 */
export function place(cards: JNode[], wanted: (number | undefined)[], top: Map<string, number>): void {
  interface Cluster { start: number; end: number; y: number; h: number; sum: number; n: number }
  const clusters: Cluster[] = [];
  let cursor = 0;
  cards.forEach((c, i) => {
    const w = wanted[i] ?? cursor;
    // a wish is relative to the cluster's top: subtract the height stacked above the card in it
    const cl: Cluster = { start: i, end: i, y: w, h: c.height, sum: w, n: 1 };
    clusters.push(cl);
    cursor = w + c.height + ROW_GAP;
    let prev = clusters[clusters.length - 2];
    let cur = clusters[clusters.length - 1]!;
    while (prev && prev.y + prev.h + ROW_GAP > cur.y) {
      // merge: the joined cluster's best top averages every member's wish less the height above it
      prev.sum += cur.sum - cur.n * (prev.h + ROW_GAP);
      prev.n += cur.n;
      prev.h += ROW_GAP + cur.h;
      prev.end = cur.end;
      prev.y = prev.sum / prev.n;
      clusters.pop();
      cur = prev;
      prev = clusters[clusters.length - 2];
    }
    cursor = cur.y + cur.h + ROW_GAP;
  });
  for (const cl of clusters) {
    let y = cl.y;
    for (let i = cl.start; i <= cl.end; i++) {
      top.set(cards[i]!.id, y);
      y += cards[i]!.height + ROW_GAP;
    }
  }
}

/** Edges as React Flow wants them, for the layout-independent parts. */
export const isForward = (e: JEdge) => !e.back;
