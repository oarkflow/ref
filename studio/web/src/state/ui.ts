// Per-browser UI preferences: panel sizes, developer view, theme-independent
// layout. Kept apart from the studio store because none of it belongs to a draft.
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";

export type RightTab = "problems" | "help" | "changes";

export interface UiPrefs {
  /** Show raw names, paths and source (off = plain-language UI). */
  devView: boolean;
  sidebarCollapsed: boolean;
  sidebarWidth: number;
  rightOpen: boolean;
  rightWidth: number;
  rightTab: RightTab;
}

export const UI_KEY = "studio.ui";
export const LIMITS = {
  sidebar: { min: 208, max: 360, rail: 56 },
  right: { min: 280, max: 560 },
} as const;

export const DEFAULT_PREFS: UiPrefs = {
  devView: false,
  sidebarCollapsed: false,
  sidebarWidth: 248,
  rightOpen: false,
  rightWidth: 340,
  rightTab: "problems",
};

const clamp = (n: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, Math.round(n)));

export function loadPrefs(storage: Pick<Storage, "getItem"> | null = safeStorage()): UiPrefs {
  try {
    const raw = storage?.getItem(UI_KEY);
    if (!raw) return { ...DEFAULT_PREFS };
    const v = JSON.parse(raw) as Partial<UiPrefs>;
    return {
      devView: v.devView === true,
      sidebarCollapsed: v.sidebarCollapsed === true,
      sidebarWidth: typeof v.sidebarWidth === "number" ? clamp(v.sidebarWidth, LIMITS.sidebar.min, LIMITS.sidebar.max) : DEFAULT_PREFS.sidebarWidth,
      rightOpen: v.rightOpen === true,
      rightWidth: typeof v.rightWidth === "number" ? clamp(v.rightWidth, LIMITS.right.min, LIMITS.right.max) : DEFAULT_PREFS.rightWidth,
      rightTab: v.rightTab === "help" || v.rightTab === "changes" ? v.rightTab : "problems",
    };
  } catch {
    return { ...DEFAULT_PREFS };
  }
}

function safeStorage(): Storage | null {
  try {
    return typeof localStorage === "undefined" ? null : localStorage;
  } catch {
    return null;
  }
}

export interface UiState extends UiPrefs {
  /** Narrow screens only: the side panel floats over the content while this is true. Not remembered. */
  rightOverlay: boolean;
  paletteOpen: boolean;
  addOpen: { category?: string; type?: string } | null;
  set(patch: Partial<UiPrefs>): void;
  toggleDev(): void;
  toggleSidebar(): void;
  resizeSidebar(n: number): void;
  toggleRight(open?: boolean): void;
  resizeRight(n: number): void;
  showRight(tab: RightTab): void;
  setPalette(open: boolean): void;
  setRightOverlay(open: boolean): void;
  openAdd(arg?: { category?: string; type?: string }): void;
  closeAdd(): void;
}

export function createUiStore(storage: Pick<Storage, "getItem" | "setItem"> | null = safeStorage()) {
  const persist = (s: UiState) => {
    try {
      const { devView, sidebarCollapsed, sidebarWidth, rightOpen, rightWidth, rightTab } = s;
      storage?.setItem(UI_KEY, JSON.stringify({ devView, sidebarCollapsed, sidebarWidth, rightOpen, rightWidth, rightTab }));
    } catch {
      /* private mode / quota: preferences just aren't remembered */
    }
  };
  return createStore<UiState>((set, get) => {
    const update = (patch: Partial<UiState>) => {
      set(patch);
      persist(get());
    };
    return {
      ...loadPrefs(storage),
      paletteOpen: false,
      rightOverlay: false,
      addOpen: null,
      set: (patch) => update(patch),
      toggleDev: () => update({ devView: !get().devView }),
      toggleSidebar: () => update({ sidebarCollapsed: !get().sidebarCollapsed }),
      resizeSidebar: (n) => update({ sidebarWidth: clamp(n, LIMITS.sidebar.min, LIMITS.sidebar.max), sidebarCollapsed: false }),
      toggleRight: (open) => update({ rightOpen: open ?? !get().rightOpen }),
      resizeRight: (n) => update({ rightWidth: clamp(n, LIMITS.right.min, LIMITS.right.max), rightOpen: true }),
      showRight: (tab) => { update({ rightTab: tab, rightOpen: true }); set({ rightOverlay: true }); },
      setPalette: (open) => set({ paletteOpen: open }),
      setRightOverlay: (open) => set({ rightOverlay: open }),
      openAdd: (arg = {}) => set({ addOpen: arg }),
      closeAdd: () => set({ addOpen: null }),
    };
  });
}

export const uiStore = createUiStore();

export function useUi<T>(selector: (s: UiState) => T): T {
  return useStore(uiStore, selector);
}
