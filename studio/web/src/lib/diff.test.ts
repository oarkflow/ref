import { describe, expect, it } from "vitest";
import { diffLines, parseUnified, stats, withContext } from "./diff";

describe("diffLines", () => {
  it("reports added and removed lines", () => {
    const d = diffLines("a\nb\nc", "a\nB\nc\nd");
    expect(d.filter((l) => l.t === "-").map((l) => l.text)).toEqual(["b"]);
    expect(d.filter((l) => l.t === "+").map((l) => l.text)).toEqual(["B", "d"]);
    expect(stats(d)).toEqual({ added: 2, removed: 1 });
  });
  it("handles empty sides", () => {
    expect(diffLines("", "x\ny").every((l) => l.t === "+")).toBe(true);
    expect(diffLines("x\ny", "").every((l) => l.t === "-")).toBe(true);
    expect(diffLines("", "")).toEqual([]);
  });
  it("keeps identical files unchanged", () => {
    expect(stats(diffLines("a\nb", "a\nb"))).toEqual({ added: 0, removed: 0 });
  });
});

describe("withContext", () => {
  it("folds distant unchanged lines", () => {
    const a = Array.from({ length: 30 }, (_, i) => `l${i}`).join("\n");
    const b = a.replace("l15", "CHANGED");
    const lines = withContext(diffLines(a, b), 2);
    expect(lines.some((l) => l.t === "@")).toBe(true);
    expect(lines.length).toBeLessThan(15);
    expect(lines.find((l) => l.t === "+")?.text).toBe("CHANGED");
  });
});

describe("parseUnified", () => {
  it("skips headers and reads marks", () => {
    const l = parseUnified("--- a/x\n+++ b/x\n ctx\n-old\n+new\n");
    expect(l).toEqual([{ t: " ", text: "ctx" }, { t: "-", text: "old" }, { t: "+", text: "new" }]);
  });
});
