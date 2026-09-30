import { beforeEach, describe, expect, it } from "vitest";
import {
  DEFAULT_PREFS, PREFS_KEY, PX_PER_SEC, connectedEdges, flowStyleFor, hashString, loadFlowPrefs, planDots, saveFlowPrefs,
} from "./flow";

describe("flow style from the kind of connection", () => {
  it("normal flow is a filled dot at full pace", () => {
    expect(flowStyleFor({})).toEqual({ kind: "normal", factor: 1, hollow: false, dotRadius: 3, opacity: 0.95 });
  });
  it("an error path is its own kind, at the normal pace", () => {
    expect(flowStyleFor({ errorPath: true })).toMatchObject({ kind: "error", factor: 1, hollow: false });
  });
  it("waiting connections are slow rings", () => {
    const s = flowStyleFor({ parks: true });
    expect(s).toMatchObject({ kind: "park", hollow: true });
    expect(s.factor).toBeLessThan(0.5);
  });
  it("data arriving from the request is dim and hollow", () => {
    const s = flowStyleFor({ fromRequest: true });
    expect(s).toMatchObject({ kind: "request", hollow: true });
    expect(s.opacity).toBeLessThan(0.7);
  });
  it("an error beats waiting beats the request", () => {
    expect(flowStyleFor({ errorPath: true, parks: true, fromRequest: true }).kind).toBe("error");
    expect(flowStyleFor({ parks: true, fromRequest: true }).kind).toBe("park");
  });
});

describe("planDots", () => {
  const normal = flowStyleFor({});
  it("keeps a steady pace: longer edges take longer, at the same speed", () => {
    const short = planDots("a", { length: 200, speed: "normal", style: normal });
    const long = planDots("a", { length: 800, speed: "normal", style: normal });
    expect(long.dur).toBeGreaterThan(short.dur);
    expect(long.dur / short.dur).toBeCloseTo(4, 0);
    expect(800 / long.dur).toBeCloseTo(PX_PER_SEC.normal, 0);
  });
  it("speed setting changes the pace", () => {
    const d = (speed: "slow" | "normal" | "fast") => planDots("a", { length: 600, speed, style: normal }).dur;
    expect(d("slow")).toBeGreaterThan(d("normal"));
    expect(d("normal")).toBeGreaterThan(d("fast"));
  });
  it("waiting edges are slower than normal ones; boosted edges are faster", () => {
    const base = planDots("a", { length: 600, speed: "normal", style: normal }).dur;
    expect(planDots("a", { length: 600, speed: "normal", style: flowStyleFor({ parks: true }) }).dur).toBeGreaterThan(base);
    expect(planDots("a", { length: 600, speed: "normal", style: normal, boosted: true }).dur).toBeLessThan(base);
  });
  it("never runs frantically fast or crawls", () => {
    expect(planDots("a", { length: 10, speed: "fast", style: normal }).dur).toBeGreaterThanOrEqual(1.4);
    expect(planDots("a", { length: 5000, speed: "slow", style: flowStyleFor({ parks: true }) }).dur).toBeLessThanOrEqual(14);
  });
  it("longer edges carry more dots, up to three", () => {
    const c = (len: number) => planDots("a", { length: len, speed: "normal", style: normal }).count;
    expect(c(100)).toBe(1);
    expect(c(500)).toBe(2);
    expect(c(2000)).toBe(3);
  });
  it("starts every edge at its own phase, so they do not pulse in lockstep", () => {
    const starts = new Set(["a:x->b", "b:y->c", "c:z->d", "d:w->e", "e:v->f", "f:u->g"].map((id) => planDots(id, { length: 300, speed: "normal", style: normal }).begins[0]));
    expect(starts.size).toBe(6);
  });
  it("uses negative offsets within one crossing, spread evenly between dots", () => {
    const p = planDots("x", { length: 1000, speed: "normal", style: normal });
    expect(p.count).toBe(3);
    for (const b of p.begins) {
      expect(b).toBeLessThanOrEqual(0);
      expect(b).toBeGreaterThan(-p.dur - 1e-9);
    }
    const sorted = [...p.begins].map((b) => ((-b % p.dur) + p.dur) % p.dur).sort((a, b) => a - b);
    expect(sorted[1]! - sorted[0]!).toBeCloseTo(p.dur / 3, 5);
  });
  it("is deterministic", () => {
    expect(planDots("q", { length: 400, speed: "fast", style: normal })).toEqual(planDots("q", { length: 400, speed: "fast", style: normal }));
    expect(hashString("q")).toBe(hashString("q"));
  });
});

describe("connectedEdges (what animates once something is selected)", () => {
  // a -> b -> c -> d, b -> e, an unrelated x -> y, and a loop d -> b
  const edges = [
    { id: "ab", from: "a", to: "b" }, { id: "bc", from: "b", to: "c" }, { id: "cd", from: "c", to: "d" },
    { id: "be", from: "b", to: "e" }, { id: "xy", from: "x", to: "y" }, { id: "db", from: "d", to: "b" },
  ];
  it("is nothing when nothing is selected: every edge stays still", () => {
    expect(connectedEdges(edges, []).size).toBe(0);
  });
  it("is only the edges touching the selected step, coming in and going out, not the whole path", () => {
    expect([...connectedEdges(edges, ["c"])].sort()).toEqual(["bc", "cd"]);
    expect([...connectedEdges(edges, ["b"])].sort()).toEqual(["ab", "bc", "be", "db"]);
  });
  it("is the union when several steps are selected", () => {
    expect([...connectedEdges(edges, ["a", "y"])].sort()).toEqual(["ab", "xy"]);
    expect([...connectedEdges(edges, ["c", "e"])].sort()).toEqual(["bc", "be", "cd"]);
  });
  it("leaves unrelated edges alone", () => {
    expect(connectedEdges(edges, ["c"]).has("xy")).toBe(false);
  });
  it("finds nothing for a step with no edges", () => {
    expect(connectedEdges(edges, ["zzz"]).size).toBe(0);
  });
});

describe("saved preference (show data flow, speed)", () => {
  beforeEach(() => localStorage.clear());
  it("starts on, at normal speed", () => {
    expect(loadFlowPrefs()).toEqual(DEFAULT_PREFS);
    expect(DEFAULT_PREFS).toEqual({ on: true, speed: "normal" });
  });
  it("round-trips", () => {
    saveFlowPrefs({ on: false, speed: "fast" });
    expect(loadFlowPrefs()).toEqual({ on: false, speed: "fast" });
  });
  it("ignores garbage and unknown speeds", () => {
    localStorage.setItem(PREFS_KEY, "{nope");
    expect(loadFlowPrefs()).toEqual(DEFAULT_PREFS);
    localStorage.setItem(PREFS_KEY, JSON.stringify({ on: "yes", speed: "warp" }));
    expect(loadFlowPrefs()).toEqual(DEFAULT_PREFS);
    localStorage.setItem(PREFS_KEY, JSON.stringify({ on: false }));
    expect(loadFlowPrefs()).toEqual({ on: false, speed: "normal" });
  });
  it("does not throw when storage is blocked", () => {
    const orig = Storage.prototype.setItem;
    const get = Storage.prototype.getItem;
    Storage.prototype.setItem = () => { throw new Error("blocked"); };
    Storage.prototype.getItem = () => { throw new Error("blocked"); };
    try {
      expect(() => saveFlowPrefs({ on: false, speed: "slow" })).not.toThrow();
      expect(loadFlowPrefs()).toEqual(DEFAULT_PREFS);
    } finally {
      Storage.prototype.setItem = orig;
      Storage.prototype.getItem = get;
    }
  });
});
