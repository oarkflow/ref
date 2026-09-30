import { describe, expect, it } from "vitest";
import { DEFAULT_LAYOUT, LAYOUT_KEY, LIMITS, clampSize, createLayoutStore, loadLayout } from "./layout";

const memory = (init?: string) => {
  const data = new Map<string, string>(init === undefined ? [] : [[LAYOUT_KEY, init]]);
  return { getItem: (k: string) => data.get(k) ?? null, setItem: (k: string, v: string) => void data.set(k, v), data };
};
const throwing = { getItem: () => { throw new Error("denied"); }, setItem: () => { throw new Error("denied"); } };

describe("preview layout persistence", () => {
  it("starts from defaults when nothing is stored", () => {
    expect(loadLayout(memory())).toEqual(DEFAULT_LAYOUT);
  });

  it("remembers dock, sizes, device and tab across stores", () => {
    const m = memory();
    const a = createLayoutStore(m);
    a.getState().set({ dock: "bottom", device: "mobile", tab: "console" });
    a.getState().resize(400);
    const b = createLayoutStore(m);
    expect(b.getState()).toMatchObject({ dock: "bottom", device: "mobile", tab: "console", sizeBottom: 400 });
  });

  it("resizes the current dock only, within limits", () => {
    const s = createLayoutStore(memory());
    s.getState().resize(10);
    expect(s.getState().sizeRight).toBe(LIMITS.right.min);
    s.getState().set({ dock: "bottom" });
    s.getState().resize(99999);
    expect(s.getState().sizeBottom).toBeLessThanOrEqual(LIMITS.bottom.max);
    expect(s.getState().sizeRight).toBe(LIMITS.right.min);
    expect(clampSize("right", 5000, 1000)).toBe(700); // never more than 70% of the viewport
  });

  it("survives unavailable storage (private mode)", () => {
    expect(loadLayout(throwing)).toEqual(DEFAULT_LAYOUT);
    const s = createLayoutStore(throwing);
    expect(() => s.getState().set({ dock: "bottom" })).not.toThrow();
    expect(s.getState().dock).toBe("bottom"); // still works in memory
  });

  it("ignores junk", () => {
    expect(loadLayout(memory("{not json"))).toEqual(DEFAULT_LAYOUT);
    expect(loadLayout(memory(JSON.stringify({ dock: "left", sizeRight: "big", device: "tv", tab: 3 })))).toEqual(DEFAULT_LAYOUT);
  });
});
