// Data-flow animation: which dots travel along an edge, how fast, in what
// style. Everything here is pure (so it is unit tested); the edge component only
// renders what this decides. The animation itself is SVG <animateMotion>, driven
// by the browser: there is no per-frame JavaScript.

export type Speed = "slow" | "normal" | "fast";
export const SPEEDS: Speed[] = ["slow", "normal", "fast"];
/** pixels per second along the edge */
export const PX_PER_SEC: Record<Speed, number> = { slow: 46, normal: 100, fast: 210 };

export interface FlowPrefs {
  on: boolean;
  speed: Speed;
}

export const DEFAULT_PREFS: FlowPrefs = { on: true, speed: "normal" };
export const PREFS_KEY = "studio.canvas.flow.v1";

export function loadFlowPrefs(): FlowPrefs {
  try {
    const raw = localStorage.getItem(PREFS_KEY);
    if (!raw) return DEFAULT_PREFS;
    const v = JSON.parse(raw) as Partial<FlowPrefs>;
    return {
      on: typeof v.on === "boolean" ? v.on : DEFAULT_PREFS.on,
      speed: SPEEDS.includes(v.speed as Speed) ? (v.speed as Speed) : DEFAULT_PREFS.speed,
    };
  } catch {
    return DEFAULT_PREFS;
  }
}

export function saveFlowPrefs(p: FlowPrefs): void {
  try {
    localStorage.setItem(PREFS_KEY, JSON.stringify(p));
  } catch {
    /* private mode or quota: the choice just is not remembered */
  }
}

// ---------------------------------------------------------------------------

export type FlowKind = "normal" | "error" | "park" | "request";

export interface FlowInput {
  errorPath?: boolean;
  parks?: boolean;
  fromRequest?: boolean;
}

export interface FlowStyle {
  kind: FlowKind;
  /** travel speed relative to the chosen speed */
  factor: number;
  /** ring instead of a filled dot */
  hollow: boolean;
  dotRadius: number;
  /** how much the dot is faded (1 = full) */
  opacity: number;
}

const STYLES: Record<FlowKind, Omit<FlowStyle, "kind">> = {
  normal: { factor: 1, hollow: false, dotRadius: 3, opacity: 0.95 },
  // to the error handler: the same pace, red
  error: { factor: 1, hollow: false, dotRadius: 3, opacity: 0.95 },
  // waiting on a timer or a person: slow, a ring that "holds" rather than rushes
  park: { factor: 0.4, hollow: true, dotRadius: 4, opacity: 0.9 },
  // arriving from the request: dim and hollow, it is data that has not been produced yet
  request: { factor: 0.8, hollow: true, dotRadius: 3, opacity: 0.55 },
};

/** The look of an edge's dots from what kind of connection it is. */
export function flowStyleFor(i: FlowInput): FlowStyle {
  const kind: FlowKind = i.errorPath ? "error" : i.parks ? "park" : i.fromRequest ? "request" : "normal";
  return { kind, ...STYLES[kind] };
}

// ---------------------------------------------------------------------------

export interface DotPlan {
  /** dots on this edge */
  count: number;
  /** seconds for one dot to cross the whole edge */
  dur: number;
  /** negative start offsets in seconds, so the edge is already mid-flow and desynchronised */
  begins: number[];
}

/** FNV-1a: a stable phase per edge. */
export function hashString(s: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return h >>> 0;
}

export interface PlanOpts {
  length: number;
  speed: Speed;
  style: FlowStyle;
  /** upstream/downstream of what is hovered or selected: faster */
  boosted?: boolean;
}

/** How many dots, how fast, and where each one starts. */
export function planDots(id: string, o: PlanOpts): DotPlan {
  const len = Math.max(24, o.length);
  const pxPerSec = PX_PER_SEC[o.speed] * o.style.factor * (o.boosted ? 1.9 : 1);
  // steady flow: duration follows length, within limits so short edges are not frantic and long ones do not crawl
  const dur = Math.min(14, Math.max(1.4, len / pxPerSec));
  const count = Math.max(1, Math.min(3, Math.round(len / 240)));
  const phase = (hashString(id) % 1000) / 1000;
  const begins = Array.from({ length: count }, (_, i) => -((phase + i / count) % 1) * dur);
  return { count, dur, begins };
}

// ---------------------------------------------------------------------------

export interface FlowEdgeRef {
  id: string;
  from: string;
  to: string;
}

/**
 * The edges directly connected to the selected steps (coming in and going out),
 * the union when several are selected. This is what animates: nothing moves
 * until something is selected.
 */
export function connectedEdges(edges: FlowEdgeRef[], nodeIds: readonly string[]): Set<string> {
  const ids = new Set(nodeIds);
  const out = new Set<string>();
  if (ids.size === 0) return out;
  for (const e of edges) if (ids.has(e.from) || ids.has(e.to)) out.add(e.id);
  return out;
}

/** How long the dots take to fade once nothing is selected (ms). */
export const FADE_MS = 200;

/** Below this zoom the dots are too small to read and are not drawn. */
export const MIN_DOT_ZOOM = 0.32;
