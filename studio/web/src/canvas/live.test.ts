import { describe, expect, it } from "vitest";
import { createLiveStore, intentOf, lineStates, reduceRun, type LiveRun, type TraceEvent } from "./live";

const ev = (phase: TraceEvent["phase"], node: string | undefined, extra: Partial<TraceEvent> = {}): TraceEvent => ({
  draft: "d1", time: "2026-01-01T00:00:00Z", phase, intent: "orders.create", node, ...extra,
});

describe("reduceRun", () => {
  it("tracks a step from running to done with its time", () => {
    let r = reduceRun(undefined, ev("start", "validate"));
    expect(r.nodes.validate).toMatchObject({ status: "running", order: 1 });
    r = reduceRun(r, ev("end", "validate", { ms: 4.5 }));
    expect(r.nodes.validate).toMatchObject({ status: "done", ms: 4.5, order: 1 });
  });

  it("marks a step that ended with an error as failed and keeps the message", () => {
    let r = reduceRun(undefined, ev("start", "charge"));
    r = reduceRun(r, ev("end", "charge", { error: "card declined", ms: 2 }));
    expect(r.nodes.charge).toMatchObject({ status: "failed", error: "card declined" });
  });

  it("numbers steps in the order they started", () => {
    let r: LiveRun | undefined;
    for (const n of ["a", "b", "c"]) r = reduceRun(r, ev("start", n));
    expect(["a", "b", "c"].map((n) => r!.nodes[n]!.order)).toEqual([1, 2, 3]);
  });

  it("finishes the run on `done` and starts a fresh one on the next start", () => {
    let r = reduceRun(undefined, ev("start", "a"));
    r = reduceRun(r, ev("end", "a", { ms: 1 }));
    r = reduceRun(r, ev("done", undefined, { ms: 3 }));
    expect(r).toMatchObject({ finished: true, ms: 3 });
    r = reduceRun(r, ev("start", "b"));
    expect(r.finished).toBe(false);
    expect(Object.keys(r.nodes)).toEqual(["b"]);
  });

  it("copes with an end that arrives without a start", () => {
    const r = reduceRun(undefined, ev("end", "late", { ms: 1 }));
    expect(r.nodes.late?.status).toBe("done");
  });
});

describe("lineStates", () => {
  const lines = [
    { id: "in-a", from: "__input", to: "a" },
    { id: "a-b", from: "a", to: "b" },
    { id: "b-c", from: "b", to: "c" },
  ];

  it("has nothing live before a run", () => {
    expect(lineStates(lines, undefined, "__input").live.size).toBe(0);
  });

  it("lights the line into a running step once its source has finished", () => {
    let r = reduceRun(undefined, ev("start", "a"));
    expect(lineStates(lines, r, "__input").live).toEqual(new Set(["in-a"]));
    r = reduceRun(r, ev("end", "a", { ms: 1 }));
    r = reduceRun(r, ev("start", "b"));
    const s = lineStates(lines, r, "__input");
    expect(s.live).toEqual(new Set(["a-b"]));
    expect(s.done).toEqual(new Set(["in-a"]));
  });

  it("marks the line into a failed step", () => {
    let r = reduceRun(undefined, ev("start", "a"));
    r = reduceRun(r, ev("end", "a"));
    r = reduceRun(r, ev("start", "b"));
    r = reduceRun(r, ev("end", "b", { error: "boom" }));
    expect(lineStates(lines, r, "__input").failed).toEqual(new Set(["a-b"]));
  });
});

describe("createLiveStore", () => {
  it("normalises the platform prefix and shows only the chosen intent", () => {
    expect(intentOf({ intent: "platform.orders.create" })).toBe("orders.create");
    const s = createLiveStore();
    s.setIntent("orders.create");
    s.push(ev("start", "a", { intent: "platform.orders.create" }));
    s.push(ev("start", "z", { intent: "other.intent" }));
    expect(s.nodeStatus("a")).toBe("running");
    expect(s.nodeStatus("z")).toBe("waiting");
    expect(s.nodeStatus("never")).toBe("waiting");
  });

  it("says nothing before any run, and again after a finished run for steps that never ran", () => {
    const s = createLiveStore();
    s.setIntent("orders.create");
    expect(s.nodeStatus("a")).toBe("");
    s.push(ev("start", "a"));
    s.push(ev("end", "a", { ms: 1 }));
    s.push(ev("done", undefined));
    expect(s.nodeStatus("skipped")).toBe("");
    expect(s.nodeStatus("a")).toBe("done");
  });

  it("notifies subscribers and clears", () => {
    const s = createLiveStore();
    s.setIntent("orders.create");
    let n = 0;
    const off = s.subscribe(() => n++);
    s.push(ev("start", "a"));
    s.clear();
    expect(n).toBe(2);
    expect(s.run()).toBeUndefined();
    off();
    s.push(ev("start", "a"));
    expect(n).toBe(2);
  });
});
