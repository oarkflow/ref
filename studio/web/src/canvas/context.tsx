import { createContext, useContext, useSyncExternalStore } from "react";
import type { Rect } from "./labelPlacement";
import type { Speed } from "./flow";

/** Where every edge label currently sits, so neighbouring labels keep apart. */
export interface LabelRegistry {
  set(id: string, r: Rect): void;
  /** Rects of labels an edge should avoid. Only lower ids count, so placement settles. */
  others(id: string): Rect[];
}

export function createLabelRegistry(): LabelRegistry {
  const boxes = new Map<string, Rect>();
  return {
    set: (id, r) => void boxes.set(id, r),
    others: (id) => [...boxes.entries()].filter(([k]) => k < id).map(([, r]) => r),
  };
}

// ---------------------------------------------------------------------------
// Volatile canvas state lives in tiny external stores, not in the actions
// context. A context value that changes re-renders every consumer; on a graph of
// a hundred steps, selecting one step would redraw them all. Through a store each
// node and edge subscribes to just its own answer (a primitive), so only the ones
// whose answer changed render.

/** What the data-flow animation should do right now. */
export interface FlowState {
  /** "Show data flow" */
  on: boolean;
  speed: Speed;
  /** edges connected to the selected step(s); null = nothing selected, so nothing moves */
  focus: ReadonlySet<string> | null;
  /** number of edges drawn */
  total: number;
}

export const NO_FLOW: FlowState = { on: false, speed: "normal", focus: null, total: 0 };

export type EdgeMark = "none" | "focus" | "dim";

export interface FlowStore {
  get(): FlowState;
  set(next: FlowState): void;
  subscribe(fn: () => void): () => void;
  /** whether an edge is animated (focus), dimmed behind one that is, or neither */
  markOf(edgeId: string): EdgeMark;
}

export function createFlowStore(initial: FlowState = NO_FLOW): FlowStore {
  let state = initial;
  const subs = new Set<() => void>();
  return {
    get: () => state,
    set(next) {
      if (next.on === state.on && next.speed === state.speed && next.total === state.total && sameSet(next.focus, state.focus)) return;
      state = next;
      subs.forEach((f) => f());
    },
    subscribe(fn) {
      subs.add(fn);
      return () => void subs.delete(fn);
    },
    markOf: (id) => (state.focus === null ? "none" : state.focus.has(id) ? "focus" : "dim"),
  };
}

function sameSet(a: ReadonlySet<string> | null, b: ReadonlySet<string> | null): boolean {
  if (a === b) return true;
  if (!a || !b || a.size !== b.size) return false;
  for (const x of a) if (!b.has(x)) return false;
  return true;
}

const FlowCtx = createContext<FlowStore>(createFlowStore());
export const FlowProvider = FlowCtx.Provider;

/** The flow state for one edge: three primitives, so an edge re-renders only when its own answer changes. */
export function useEdgeFlow(edgeId: string): { mark: EdgeMark; on: boolean; speed: Speed } {
  const s = useContext(FlowCtx);
  const mark = useSyncExternalStore(s.subscribe, () => s.markOf(edgeId));
  const on = useSyncExternalStore(s.subscribe, () => s.get().on);
  const speed = useSyncExternalStore(s.subscribe, () => s.get().speed);
  return { mark, on, speed };
}

export interface CollapsedStore {
  has(id: string): boolean;
  toggle(id: string): void;
  subscribe(fn: () => void): () => void;
}

export function createCollapsedStore(): CollapsedStore {
  let set: ReadonlySet<string> = new Set();
  const subs = new Set<() => void>();
  return {
    has: (id) => set.has(id),
    toggle(id) {
      const n = new Set(set);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      set = n;
      subs.forEach((f) => f());
    },
    subscribe(fn) {
      subs.add(fn);
      return () => void subs.delete(fn);
    },
  };
}

const CollapsedCtx = createContext<CollapsedStore>(createCollapsedStore());
export const CollapsedProvider = CollapsedCtx.Provider;

/** Whether one step's details are folded away. Re-renders only the step whose answer changed. */
export function useCollapsed(id: string): boolean {
  const s = useContext(CollapsedCtx);
  return useSyncExternalStore(s.subscribe, () => s.has(id));
}

export function useToggleCollapse(): (id: string) => void {
  return useContext(CollapsedCtx).toggle;
}

// ---------------------------------------------------------------------------

/**
 * Actions node and edge components call back into; kept out of node `data` so
 * nodes stay plain. The object is stable for the life of the canvas (each method
 * reads the latest closure), so providing it never re-renders its consumers.
 */
export interface CanvasActions {
  readOnly: boolean;
  /** animations on (off for reduced motion and under test) */
  motion: boolean;
  labels: LabelRegistry;
  moveStage(id: string, dir: -1 | 1): void;
  setStart(id: string): void;
  select(path: string): void;
  /** open the "add a step" popover on a line */
  insertOnEdge(edgeId: string, anchor: DOMRect): void;
  /** open the "add a step" popover after a step */
  appendAfter(nodeId: string, anchor: DOMRect): void;
  removeEdge(edgeId: string): void;
  selectEdge(edgeId: string): void;
  /** open the small editor for a connection's condition and type */
  editEdge(edgeId: string, anchor: DOMRect): void;
  /** can a line dragged from this handle end on that handle? (for dimming) */
  canConnect(fromNode: string, fromHandle: string | null, toNode: string, toHandle: string | null): boolean;
}

const Ctx = createContext<CanvasActions | null>(null);
export const CanvasActionsProvider = Ctx.Provider;

export function useCanvasActions(): CanvasActions {
  const c = useContext(Ctx);
  if (!c) throw new Error("useCanvasActions outside CanvasActionsProvider");
  return c;
}

/**
 * Actions for a read-only host of the flow edge (for example the Journeys
 * view): nothing to edit, nothing to insert. Provide a flow store of its own with
 * FlowProvider to drive the animation from its own selection.
 */
export function readOnlyActions(over: Partial<CanvasActions> = {}): CanvasActions {
  return {
    readOnly: true,
    motion: true,
    labels: createLabelRegistry(),
    moveStage: () => {},
    setStart: () => {},
    select: () => {},
    insertOnEdge: () => {},
    appendAfter: () => {},
    removeEdge: () => {},
    selectEdge: () => {},
    editEdge: () => {},
    canConnect: () => false,
    ...over,
  };
}
