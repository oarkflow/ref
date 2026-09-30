import { describe, expect, it } from "vitest";
import { factEdgeLabel, humanDuration, needsField, processEdgeLabel, type EdgeLike } from "./edgeLabels";

const e = (kind: string, settings: Record<string, string> = {}, condition?: string): EdgeLike => ({ kind, settings, condition });

describe("humanDuration", () => {
  it("spells durations out", () => {
    expect(humanDuration("48h")).toBe("48 hours");
    expect(humanDuration("1h")).toBe("1 hour");
    expect(humanDuration("1h30m")).toBe("1 hour 30 minutes");
    expect(humanDuration("250ms")).toBe("250 ms");
  });
  it("returns anything else as written", () => {
    expect(humanDuration("soon")).toBe("soon");
    expect(humanDuration("2x")).toBe("2x");
  });
  it("does not pretend that days are a duration the platform can read", () => {
    expect(humanDuration("2d")).toBe("2d");
    expect(humanDuration("1w")).toBe("1w");
  });
});

describe("processEdgeLabel", () => {
  it("says 'Then' for a plain connection and is quiet about it", () => {
    const l = processEdgeLabel(e("simple"));
    expect(l.text).toBe("Then");
    expect(l.tone).toBe("quiet");
  });

  it("puts a condition into words", () => {
    const l = processEdgeLabel(e("branch", {}, "result.action == 'approve'"));
    expect(l.text.toLowerCase()).toContain("approve");
    expect(l.raw).toContain("result.action == 'approve'");
    expect(l.tip).toContain("When: result.action == 'approve'");
    expect(l.needs).toBeUndefined();
  });

  it("calls a condition-less branch 'Otherwise' only when a sibling has a condition", () => {
    const withCond = e("branch", {}, "x == 1");
    const bare = e("branch");
    expect(processEdgeLabel(bare, { siblings: [withCond, bare] }).text).toBe("Otherwise");
    const alone = processEdgeLabel(bare, { siblings: [bare] });
    expect(alone.text).toBe("Add a condition");
    expect(alone.needs).toEqual({ prompt: "Add a condition", field: "condition" });
    expect(alone.tone).toBe("todo");
  });

  it("does not let a different kind of sibling make a branch 'Otherwise'", () => {
    const other = e("error", {}, "x == 1");
    const bare = e("branch");
    expect(processEdgeLabel(bare, { siblings: [other, bare] }).text).toBe("Add a condition");
  });

  it.each([
    ["error", {}, "On error", "error", "alert"],
    ["fallback", {}, "Fallback", "error", "undo"],
    ["compensate", {}, "To undo", "error", "undo"],
    ["manual", {}, "When a person decides", "wait", "user"],
    ["cancel", {}, "On cancel", "normal", "x"],
  ] as const)("%s", (kind, settings, text, tone, icon) => {
    const l = processEdgeLabel(e(kind, settings));
    expect(l.text).toBe(text);
    expect(l.tone).toBe(tone);
    expect(l.icon).toBe(icon);
  });

  it("reads the time a delay waits, and asks for it when missing", () => {
    expect(processEdgeLabel(e("delayed", { timeout: '"48h"' })).text).toBe("After 48 hours");
    expect(processEdgeLabel(e("delayed", { timeout: "48h" })).text).toBe("After 48 hours");
    const missing = processEdgeLabel(e("delayed"));
    expect(missing.text).toBe("Add a delay");
    expect(missing.needs?.field).toBe("timeout");
    expect(processEdgeLabel(e("escalation", { timeout: "1h" })).text).toBe("Escalate after 1 hour");
    expect(processEdgeLabel(e("timeout", { timeout: "30m" })).text).toBe("If it takes over 30 minutes");
  });

  it("names the event a wait is for", () => {
    expect(processEdgeLabel(e("wait_event", { event: '"payment.received"' })).text).toBe("When “payment.received” arrives");
    expect(processEdgeLabel(e("wait_event", { event: '"paid"', timeout: "48h" })).text).toBe("When “paid” arrives · or after 48 hours");
    const missing = processEdgeLabel(e("wait_event"));
    expect(missing.needs?.field).toBe("event");
  });

  it("describes retries, limits, loops and joins", () => {
    expect(processEdgeLabel(e("retry", { attempts: "3" })).text).toBe("Try again (up to 3 times)");
    expect(processEdgeLabel(e("retry")).text).toBe("Try again");
    expect(processEdgeLabel(e("rate_limited", { limit: "10", window: "1m" })).text).toBe("Slow down (10 per 1 minute)");
    expect(processEdgeLabel(e("iterator", { items_path: '"todo.items"' })).text).toBe("For each item in todo.items");
    expect(processEdgeLabel(e("batch_iterator", { batch_size: "50" })).text).toBe("For each batch of 50");
    expect(processEdgeLabel(e("fanin")).text).toBe("Wait for all");
    expect(processEdgeLabel(e("join", { strategy: '"any"' })).text).toBe("Wait for the first");
    expect(processEdgeLabel(e("quorum", { quorum: "2" })).text).toBe("Wait for 2 of them");
  });

  it("asks for a condition on loops that need one", () => {
    const l = processEdgeLabel(e("loop_until"));
    expect(l.needs?.field).toBe("condition");
    expect(processEdgeLabel(e("loop_until", {}, "done == true")).text).toMatch(/^Repeat until/);
  });

  it("clips a long condition in the raw view and keeps it whole in the tooltip", () => {
    const long = "a".repeat(200) + " == 1";
    const l = processEdgeLabel(e("branch", {}, long));
    expect(l.raw.length).toBeLessThanOrEqual(61);
    expect(l.tip).toContain(long);
  });

  it("falls back to a readable name for a kind it does not know", () => {
    const l = processEdgeLabel(e("some_new_kind"));
    expect(l.text.length).toBeGreaterThan(0);
    expect(l.text).not.toContain("_");
  });
});

describe("factEdgeLabel", () => {
  it("shows the fact, and '+N' when several travel together", () => {
    expect(factEdgeLabel({ fact: "todo", facts: ["todo"], first: true, fromRequest: false }).text).toBe("todo");
    const first = factEdgeLabel({ fact: "a", facts: ["a", "b", "c"], first: true, fromRequest: false });
    expect(first.text).toBe("a +2");
    expect(first.tip).toBe("Carries: a, b, c");
  });
  it("shows a chip on only the first line between the same two steps", () => {
    expect(factEdgeLabel({ fact: "b", facts: ["a", "b"], first: false, fromRequest: false }).text).toBe("");
  });
  it("calls what comes from the caller 'request'", () => {
    expect(factEdgeLabel({ fact: "x", facts: ["x"], first: true, fromRequest: true }).text).toBe("request");
  });
});

describe("needsField", () => {
  it("knows which connections are meaningless without a value", () => {
    expect(needsField("branch")).toBe("condition");
    expect(needsField("delayed")).toBe("timeout");
    expect(needsField("wait_event")).toBe("event");
    expect(needsField("simple")).toBeUndefined();
  });
});
