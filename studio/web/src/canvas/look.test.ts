import { describe, expect, it } from "vitest";
import { chipFor, edgeChip, endOutcome, friendlyCondition, intentRows, stepRows, visibleRows, type Row } from "./look";
import type { StepInfo } from "./model";

describe("endOutcome", () => {
  it("reads how a run ends from the names", () => {
    expect(endOutcome("done", "todo.complete")).toBe("success");
    expect(endOutcome("approved")).toBe("success");
    expect(endOutcome("cancelled", "todo.cancel")).toBe("cancelled");
    expect(endOutcome("rejected", "todo.reject")).toBe("failed");
    expect(endOutcome("failed")).toBe("failed");
  });
  it("falls back to neutral when nothing says", () => {
    expect(endOutcome("finalize_step")).toBe("neutral");
    expect(endOutcome()).toBe("neutral");
  });
  it("does not match inside longer words", () => {
    expect(endOutcome("undone_list")).toBe("neutral");
    expect(endOutcome("terror")).toBe("neutral");
  });
});

describe("friendlyCondition", () => {
  it("says an outcome test in words", () => {
    expect(friendlyCondition("result.action == 'approve'")).toBe("If approved");
    expect(friendlyCondition('result.action == "reject"')).toBe("If rejected");
    expect(friendlyCondition("result.action == 'request_changes'")).toBe("If changes requested");
    expect(friendlyCondition("result.action != 'cancel'")).toBe("Unless cancelled");
  });
  it("says other comparisons in words", () => {
    expect(friendlyCondition("order.total > 1000")).toBe("If total is over 1000");
    expect(friendlyCondition("age >= 18")).toBe("If age is at least 18");
    expect(friendlyCondition("user.role == 'admin'")).toBe("If role is admin");
    expect(friendlyCondition("flags.urgent == true")).toBe("If urgent");
    expect(friendlyCondition("valid")).toBe("If valid is set");
    expect(friendlyCondition("!approved")).toBe("If approved is not set");
  });
  it("joins parts", () => {
    expect(friendlyCondition("total > 10 && result.action == 'approve'")).toBe("If total is over 10 and approved");
  });
  it("gives up (null) on anything involved", () => {
    expect(friendlyCondition("len(items) > 0")).toBeNull();
    expect(friendlyCondition("a == b")).toBeNull();
    expect(friendlyCondition("")).toBeNull();
    expect(friendlyCondition(undefined)).toBeNull();
  });
});

describe("edgeChip", () => {
  it("shows the plain text and keeps the raw one for the Developer view", () => {
    const c = edgeChip("branch", "result.action == 'approve'");
    expect(c.text).toBe("If approved");
    expect(c.raw).toBe("result.action == 'approve'");
  });
  it("names the kind unless it is a plain then / if", () => {
    expect(edgeChip("simple", undefined)).toEqual({ text: "", raw: "" });
    expect(edgeChip("error", undefined).text).toBe("If it fails");
    expect(edgeChip("timeout", "elapsed > 5").text).toBe("On timeout · If elapsed is over 5");
  });
  it("never shows raw code for a condition it cannot phrase", () => {
    expect(edgeChip("branch", "len(items) > 0").text).toBe("When a rule matches");
  });
});

describe("chipFor", () => {
  it("turns config flags into short phrases with icons", () => {
    expect(chipFor("timeout 30m")).toMatchObject({ label: "30m limit", icon: "clock" });
    expect(chipFor("retry")).toMatchObject({ label: "Tries again", icon: "loop" });
    expect(chipFor("on_error skip")).toMatchObject({ label: "On error: skip", tone: "warn" });
    expect(chipFor("3 rules").label).toBe("3 rules");
  });
});

const step = (o: Partial<StepInfo>): StepInfo => ({
  id: "s", path: "p", settings: {}, terminal: false, isStart: false, human: false, durable: false, chips: [], typeName: "action", summary: { cases: [] }, ...o,
});

describe("rows", () => {
  it("a step says what it runs and who it waits for, in that order", () => {
    expect(stepRows(step({ intent: "todo.cancel" }))).toEqual([{ label: "Runs", value: "todo.cancel", code: true }]);
    expect(stepRows(step({ human: true, typeName: "approval" }))).toEqual([{ label: "Waits for", value: "a person" }]);
    expect(stepRows(step({ intent: "a.b", human: true })).map((r) => r.label)).toEqual(["Runs", "Waits for"]);
  });
  it("a switch lists its cases with dots", () => {
    const rows = stepRows(step({ typeName: "switch", summary: { cases: [{ label: "big", target: "handle_big" }, { label: "otherwise" }], line: "on size" } }));
    expect(rows[0]).toMatchObject({ label: "Matches", value: "on size" });
    expect(rows[1]).toMatchObject({ label: "big", value: "handle_big", dot: true, code: true });
    expect(rows[2]).toMatchObject({ label: "otherwise", value: "", dot: true });
  });
  it("a loop reads as sentence parts", () => {
    const n = { typeName: "foreach", summary: { cases: [], loop: { items: "todos", intent: "todo.notify", concurrency: "4" } } } as never;
    expect(intentRows(n).map((r) => `${r.label}=${r.value}`)).toEqual(["For each=todos", "Runs=todo.notify", "At once=4"]);
  });
  it("caps what a card shows and says how many are hidden", () => {
    const rows: Row[] = Array.from({ length: 9 }, (_, i) => ({ label: `r${i}`, value: "" }));
    const v = visibleRows(rows);
    expect(v.shown).toHaveLength(4);
    expect(v.more).toBe(5);
    expect(visibleRows(rows.slice(0, 5))).toEqual({ shown: rows.slice(0, 5), more: 0 });
  });
});
