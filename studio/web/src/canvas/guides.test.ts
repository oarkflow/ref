import { describe, expect, it } from "vitest";
import { alignGuides } from "./guides";

const box = (x: number, y: number, w = 100, h = 50) => ({ x, y, w, h });

describe("alignGuides", () => {
  it("does nothing when the box is nowhere near another", () => {
    expect(alignGuides(box(0, 0), [box(500, 500)])).toEqual({ x: [], y: [], dx: 0, dy: 0 });
  });

  it("snaps left edges together and reports the guide", () => {
    // different sizes, so only the left edges line up
    const g = alignGuides(box(203, 300, 100, 50), [box(200, 0, 160, 90)]);
    expect(g.dx).toBe(-3);
    expect(g.x).toEqual([200]);
    expect(g.dy).toBe(0);
  });

  it("snaps centres: a narrow box centred on a wide one", () => {
    // wide box centre = 100 + 100 = 200; narrow box centre = 152 + 50 = 202
    const g = alignGuides(box(152, 400, 100, 50), [box(0, 0, 400, 90)]);
    expect(g.dx).toBe(-2);
    expect(g.x).toEqual([200]);
  });

  it("snaps right edges", () => {
    const g = alignGuides(box(297, 400, 100, 50), [box(100, 0, 300, 90)]); // right 397 vs 400
    expect(g.dx).toBe(3);
    expect(g.x).toContain(400);
  });

  it("aligns tops on the horizontal axis", () => {
    const g = alignGuides(box(0, 104, 100, 50), [box(300, 100, 160, 90)]);
    expect(g.dy).toBe(-4);
    expect(g.y).toEqual([100]);
  });

  it("picks the closest match, and ignores anything beyond the threshold", () => {
    expect(alignGuides(box(0, 0), [box(9, 500)], 6).dx).toBe(0);
    expect(alignGuides(box(0, 0), [box(4, 500, 80, 50), box(2, 900, 60, 50)], 6).dx).toBe(2);
  });
});
