// Live execution: what the preview server reports while an intent runs, folded
// into a per-step status the canvas can draw. Everything here is pure or a tiny
// external store (so it is unit tested); the cards and lines only read answers.
import { createContext, useContext, useSyncExternalStore } from "react";
import type { TraceEvent } from "../api/types";

export type { TraceEvent };

export type NodeStatus = "running" | "done" | "failed";

export interface NodeRun {
  status: NodeStatus;
  /** wall time once it has finished */
  ms?: number;
  error?: string;
  /** order the step started in, 1-based */
  order: number;
}

export interface LiveRun {
  nodes: Readonly<Record<string, NodeRun>>;
  /** the whole intent has finished */
  finished: boolean;
  ms?: number;
  error?: string;
  /** steps that started so far */
  started: number;
}

const stripPlatform = (s: string) => s.replace(/^platform\./, "");

/** The intent an event is about, in the form its `intent` block is named. */
export const intentOf = (ev: Pick<TraceEvent, "intent">): string => stripPlatform(ev.intent);

/**
 * Folds one event into a run. A step that starts after the run finished begins
 * a new run, so the canvas always shows the latest one.
 */
export function reduceRun(run: LiveRun | undefined, ev: TraceEvent): LiveRun {
  const base: LiveRun = !run || (run.finished && ev.phase === "start") ? { nodes: {}, finished: false, started: 0 } : run;
  if (ev.phase === "done") return { ...base, finished: true, ms: ev.ms, error: ev.error };
  if (!ev.node) return base;
  const prev = base.nodes[ev.node];
  if (ev.phase === "start") {
    const order = base.started + 1;
    return { ...base, started: order, nodes: { ...base.nodes, [ev.node]: { status: "running", order } } };
  }
  const order = prev?.order ?? base.started + 1;
  const next: NodeRun = { status: ev.error ? "failed" : "done", ms: ev.ms, error: ev.error, order };
  return { ...base, started: Math.max(base.started, order), nodes: { ...base.nodes, [ev.node]: next } };
}

export interface LineLike {
  id: string;
  from: string;
  to: string;
}

/**
 * Which lines a run is on. A line is `live` while data is moving along it (its
 * source has finished and its target is working), `done` once both ends have
 * finished, and `failed` when its target failed. A line from `origin` (the
 * request) counts as finished as soon as the run has started.
 */
export function lineStates(lines: readonly LineLike[], run: LiveRun | undefined, origin?: string): { live: Set<string>; done: Set<string>; failed: Set<string> } {
  const out = { live: new Set<string>(), done: new Set<string>(), failed: new Set<string>() };
  if (!run) return out;
  const status = (id: string): NodeStatus | "origin" | undefined => (id === origin ? "origin" : run.nodes[id]?.status);
  for (const l of lines) {
    const a = status(l.from);
    const b = status(l.to);
    if (!b) continue;
    const srcDone = a === "origin" || a === "done";
    if (b === "failed" && srcDone) out.failed.add(l.id);
    else if (b === "running" && srcDone) out.live.add(l.id);
    else if (b === "done" && srcDone) out.done.add(l.id);
  }
  return out;
}

// ---------------------------------------------------------------------------
// store

export interface LiveState {
  /** the intent the canvas is showing ("" when it shows no intent) */
  intent: string;
  runs: Readonly<Record<string, LiveRun>>;
}

export interface LiveStore {
  get(): LiveState;
  /** The run being shown, or undefined when nothing ran yet. */
  run(): LiveRun | undefined;
  push(ev: TraceEvent): void;
  setIntent(intent: string): void;
  /** Forget the shown intent's run. */
  clear(): void;
  subscribe(fn: () => void): () => void;
  nodeStatus(id: string): NodeStatus | "waiting" | "";
  nodeRun(id: string): NodeRun | undefined;
}

export function createLiveStore(): LiveStore {
  let state: LiveState = { intent: "", runs: {} };
  const subs = new Set<() => void>();
  const emit = () => subs.forEach((f) => f());
  const run = () => (state.intent ? state.runs[state.intent] : undefined);
  return {
    get: () => state,
    run,
    push(ev) {
      const k = intentOf(ev);
      if (!k) return;
      state = { ...state, runs: { ...state.runs, [k]: reduceRun(state.runs[k], ev) } };
      emit();
    },
    setIntent(intent) {
      if (intent === state.intent) return;
      state = { ...state, intent };
      emit();
    },
    clear() {
      if (!state.intent || !state.runs[state.intent]) return;
      const { [state.intent]: _gone, ...rest } = state.runs;
      void _gone;
      state = { ...state, runs: rest };
      emit();
    },
    subscribe(fn) {
      subs.add(fn);
      return () => void subs.delete(fn);
    },
    nodeStatus(id) {
      const r = run();
      if (!r) return "";
      return r.nodes[id]?.status ?? (r.finished ? "" : "waiting");
    },
    nodeRun: (id) => run()?.nodes[id],
  };
}

const LiveCtx = createContext<LiveStore>(createLiveStore());
export const LiveProvider = LiveCtx.Provider;
export const useLiveStore = () => useContext(LiveCtx);

/** A step's status in the run being shown: a primitive, so only steps whose answer changed re-render. */
export function useNodeLive(id: string): { status: NodeStatus | "waiting" | ""; ms?: number } {
  const s = useContext(LiveCtx);
  const status = useSyncExternalStore(s.subscribe, () => s.nodeStatus(id));
  const ms = useSyncExternalStore(s.subscribe, () => s.nodeRun(id)?.ms);
  return { status, ms };
}

/** The run being shown, for the panel and the lines. */
export function useLiveRun(): LiveRun | undefined {
  const s = useContext(LiveCtx);
  return useSyncExternalStore(s.subscribe, s.run);
}
