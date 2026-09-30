// What is selected and what a search matched, kept in a tiny external store so that
// selecting one card re-renders that card (and the one before it), not all of them.
import { createContext, useContext, useSyncExternalStore } from "react";
import type { ElementRow, JNode } from "./model";

export interface ViewState {
  /** A card id or a row (link, form, button) id. */
  selected: string | null;
  /** Cards that match the search; null when there is no search. */
  matches: ReadonlySet<string> | null;
}

export interface ViewStore {
  get(): ViewState;
  set(next: Partial<ViewState>): void;
  subscribe(fn: () => void): () => void;
}

export function createViewStore(initial: Partial<ViewState> = {}): ViewStore {
  let state: ViewState = { selected: null, matches: null, ...initial };
  const subs = new Set<() => void>();
  return {
    get: () => state,
    set(next) {
      const merged = { ...state, ...next };
      if (merged.selected === state.selected && merged.matches === state.matches) return;
      state = merged;
      subs.forEach((f) => f());
    },
    subscribe(fn) {
      subs.add(fn);
      return () => void subs.delete(fn);
    },
  };
}

/** What cards call back into. Stable for the life of the page. */
export interface JourneyActions {
  selectCard(id: string): void;
  selectRow(id: string): void;
  toggleShared(): void;
  /** Open whatever can fix a card (for a call nothing answers: the template at that line). */
  fix(node: JNode): void;
  /** Which card holds a row. */
  hostOf(id: string): string | undefined;
  readOnly: boolean;
}

const ViewCtx = createContext<ViewStore>(createViewStore());
export const ViewProvider = ViewCtx.Provider;

const ActionsCtx = createContext<JourneyActions | null>(null);
export const ActionsProvider = ActionsCtx.Provider;
export function useJourneyActions(): JourneyActions {
  const a = useContext(ActionsCtx);
  if (!a) throw new Error("useJourneyActions outside ActionsProvider");
  return a;
}

/** Is this card itself the selection? */
export function useCardSelected(cardId: string): boolean {
  const s = useContext(ViewCtx);
  return useSyncExternalStore(s.subscribe, () => s.get().selected === cardId);
}

/** The selected row, when it is one of this card's rows; otherwise "". */
export function useSelectedRow(cardId: string): string {
  const s = useContext(ViewCtx);
  const a = useContext(ActionsCtx);
  return useSyncExternalStore(s.subscribe, () => {
    const sel = s.get().selected;
    return sel && a?.hostOf(sel) === cardId ? sel : "";
  });
}

/** True when a search is on and this card is not part of it. */
export function useDimmed(cardId: string): boolean {
  const s = useContext(ViewCtx);
  return useSyncExternalStore(s.subscribe, () => {
    const m = s.get().matches;
    return m !== null && !m.has(cardId);
  });
}

/** A search is on and this card matches. */
export function useMatched(cardId: string): boolean {
  const s = useContext(ViewCtx);
  return useSyncExternalStore(s.subscribe, () => s.get().matches?.has(cardId) ?? false);
}

export const rowOf = (n: JNode, id: string): ElementRow | undefined => n.rows.find((r) => r.id === id);
