import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";

export type Dock = "right" | "bottom";
export type Device = "desktop" | "tablet" | "mobile";
export type PreviewTab = "preview" | "console";

export const DEVICES: Record<Device, { label: string; width: number | null }> = {
  desktop: { label: "Desktop", width: null },
  tablet: { label: "Tablet", width: 820 },
  mobile: { label: "Mobile", width: 390 },
};

export interface PreviewLayout {
  dock: Dock;
  /** Pixels: width when docked right, height when docked bottom. */
  sizeRight: number;
  sizeBottom: number;
  device: Device;
  tab: PreviewTab;
}

export const LAYOUT_KEY = "studio.preview.layout";
export const LIMITS = { right: { min: 280, max: 1200 }, bottom: { min: 160, max: 900 } } as const;

export const DEFAULT_LAYOUT: PreviewLayout = { dock: "right", sizeRight: 480, sizeBottom: 320, device: "desktop", tab: "preview" };

export function clampSize(dock: Dock, n: number, viewport = typeof window !== "undefined" ? (dock === "right" ? window.innerWidth : window.innerHeight) : 1600): number {
  const l = LIMITS[dock];
  const max = Math.max(l.min, Math.min(l.max, Math.floor(viewport * 0.7)));
  return Math.round(Math.min(max, Math.max(l.min, n)));
}

/** Reads a stored layout, tolerating missing storage and junk values. */
export function loadLayout(storage: Pick<Storage, "getItem"> | null = safeStorage()): PreviewLayout {
  try {
    const raw = storage?.getItem(LAYOUT_KEY);
    if (!raw) return { ...DEFAULT_LAYOUT };
    const v = JSON.parse(raw) as Partial<PreviewLayout>;
    return {
      dock: v.dock === "bottom" ? "bottom" : "right",
      sizeRight: typeof v.sizeRight === "number" ? clampSize("right", v.sizeRight) : DEFAULT_LAYOUT.sizeRight,
      sizeBottom: typeof v.sizeBottom === "number" ? clampSize("bottom", v.sizeBottom) : DEFAULT_LAYOUT.sizeBottom,
      device: v.device === "tablet" || v.device === "mobile" ? v.device : "desktop",
      tab: v.tab === "console" ? "console" : "preview",
    };
  } catch {
    return { ...DEFAULT_LAYOUT };
  }
}

function safeStorage(): Storage | null {
  try {
    return typeof localStorage === "undefined" ? null : localStorage;
  } catch {
    return null;
  }
}

export interface LayoutState extends PreviewLayout {
  set(patch: Partial<PreviewLayout>): void;
  /** Size for the current dock. */
  size(): number;
  resize(n: number): void;
}

export function createLayoutStore(storage: Pick<Storage, "getItem" | "setItem"> | null = safeStorage()) {
  return createStore<LayoutState>((set, get) => ({
    ...loadLayout(storage),
    set(patch) {
      set(patch);
      try {
        const { dock, sizeRight, sizeBottom, device, tab } = get();
        storage?.setItem(LAYOUT_KEY, JSON.stringify({ dock, sizeRight, sizeBottom, device, tab }));
      } catch {
        /* private mode / quota: the layout just isn't remembered */
      }
    },
    size: () => (get().dock === "right" ? get().sizeRight : get().sizeBottom),
    resize(n) {
      const dock = get().dock;
      get().set(dock === "right" ? { sizeRight: clampSize("right", n) } : { sizeBottom: clampSize("bottom", n) });
    },
  }));
}

export const layoutStore = createLayoutStore();

export function usePreviewLayout<T>(selector: (s: LayoutState) => T): T {
  return useStore(layoutStore, selector);
}
