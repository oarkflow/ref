// Loads the journey graph for the open draft and keeps it fresh without a flash:
// the graph on screen stays until the next one arrives.
import { useCallback, useEffect, useRef, useState } from "react";
import { useStudio } from "../state/context";
import { DEFAULT_FILTERS, type Filters } from "./model";
import type { FlowGraph } from "./types";

export interface FlowsState {
  graph: FlowGraph | null;
  /** First load, nothing to show yet. */
  loading: boolean;
  /** A newer graph is on its way; the old one is still shown. */
  refreshing: boolean;
  error: string | null;
}

export function useFlows(focus: string | undefined, depth: number): FlowsState & { reload(): void } {
  const api = useStudio((s) => s.api);
  const draftId = useStudio((s) => s.draft?.id);
  const version = useStudio((s) => s.draft?.version);
  const [state, setState] = useState<FlowsState>({ graph: null, loading: !!draftId, refreshing: false, error: null });
  const token = useRef(0);
  const [tick, setTick] = useState(0);
  const reload = useCallback(() => setTick((t) => t + 1), []);

  useEffect(() => {
    if (!draftId) {
      setState({ graph: null, loading: false, refreshing: false, error: null });
      return;
    }
    const mine = ++token.current;
    setState((s) => ({ ...s, loading: s.graph === null, refreshing: s.graph !== null, error: null }));
    api.flows(draftId, { focus, depth: serverDepth(focus, depth) })
      .then((graph) => { if (mine === token.current) setState({ graph, loading: false, refreshing: false, error: null }); })
      .catch((e: unknown) => {
        if (mine !== token.current) return;
        const message = e instanceof Error ? e.message : "Could not load the map.";
        setState((s) => ({ ...s, loading: false, refreshing: false, error: message }));
      });
    return () => { token.current++; };
  }, [api, draftId, version, focus, depth, tick]);

  return { ...state, reload };
}

// ---- the filters are a personal preference: kept in this browser --------------------------------

const KEY = "studio.journeys.filters.v1";

export function loadFilters(): Filters {
  try {
    const raw = localStorage.getItem(KEY);
    if (!raw) return DEFAULT_FILTERS;
    const v = JSON.parse(raw) as Partial<Filters>;
    return {
      hideShared: v.hideShared === true,
      hideConnections: v.hideConnections === true,
      onlyProblems: false, // never start on a filtered-down map by accident
      showOrphans: v.showOrphans === true,
      sharedOpen: v.sharedOpen === true,
      sharedInFocus: v.sharedInFocus === true,
    };
  } catch {
    return DEFAULT_FILTERS;
  }
}

export function saveFilters(f: Filters): void {
  try {
    localStorage.setItem(KEY, JSON.stringify(f));
  } catch {
    /* private mode: not remembered */
  }
}

export function useFilters(): [Filters, (patch: Partial<Filters>) => void] {
  const [filters, setFilters] = useState<Filters>(loadFilters);
  const set = useCallback((patch: Partial<Filters>) => setFilters((f) => { const n = { ...f, ...patch }; saveFilters(n); return n; }), []);
  return [filters, set];
}

/**
 * "How far to follow", as a person counts it, to the server's count of lines. A link, form or
 * button sits inside its page, so from a page the first step is one line further than it looks.
 */
export function serverDepth(focus: string | undefined, depth: number): number {
  return focus?.startsWith("page:") ? depth + 1 : depth;
}

/** The focus depth is 1 to 3; anything else in the address falls back to 2. */
export function parseDepth(v: string | null): number {
  const n = Number(v);
  return Number.isInteger(n) && n >= 1 && n <= 3 ? n : 2;
}

/** The address of a focused view, for links. */
export function focusHref(id: string, depth = 2): string {
  return `/journeys?focus=${encodeURIComponent(id)}&depth=${depth}`;
}
