import { describe, expect, it } from "vitest";
import { DEFAULT_PREFS, LIMITS, UI_KEY, createUiStore, loadPrefs } from "./ui";

const memory = () => {
  const m = new Map<string, string>();
  return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v), m };
};

describe("ui preferences", () => {
  it("developer view is off by default and the toggle is remembered", () => {
    const storage = memory();
    const s = createUiStore(storage);
    expect(s.getState().devView).toBe(false);
    s.getState().toggleDev();
    expect(s.getState().devView).toBe(true);
    expect(JSON.parse(storage.m.get(UI_KEY)!).devView).toBe(true);
    expect(createUiStore(storage).getState().devView).toBe(true); // a fresh session restores it
  });

  it("clamps panel sizes and expands a collapsed sidebar when resized", () => {
    const s = createUiStore(memory());
    s.getState().toggleSidebar();
    expect(s.getState().sidebarCollapsed).toBe(true);
    s.getState().resizeSidebar(5000);
    expect(s.getState().sidebarWidth).toBe(LIMITS.sidebar.max);
    expect(s.getState().sidebarCollapsed).toBe(false);
    s.getState().resizeRight(1);
    expect(s.getState().rightWidth).toBe(LIMITS.right.min);
  });

  it("survives junk and missing storage", () => {
    expect(loadPrefs({ getItem: () => "{not json" })).toEqual(DEFAULT_PREFS);
    expect(loadPrefs({ getItem: () => { throw new Error("blocked"); } })).toEqual(DEFAULT_PREFS);
    expect(loadPrefs(null)).toEqual(DEFAULT_PREFS);
    const s = createUiStore({ getItem: () => null, setItem: () => { throw new Error("quota"); } });
    expect(() => s.getState().toggleDev()).not.toThrow();
    expect(s.getState().devView).toBe(true);
  });

  it("opens the side panel on a chosen tab", () => {
    const s = createUiStore(memory());
    s.getState().toggleRight(false);
    s.getState().showRight("changes");
    expect(s.getState()).toMatchObject({ rightOpen: true, rightTab: "changes" });
  });
});
