import { describe, expect, it } from "vitest";
import type { Catalog } from "../api/types";
import { catalog as catalogJson } from "../test/helpers";
import { flowElements } from "./elements";
import { find } from "./fixtures";
import { labelSize, layoutGraph } from "./layout";
import { overlapArea, pickLabelT, type Rect } from "./labelPlacement";
import { buildProcessGraph } from "./model";
import { STEP_SIZE } from "./layout";

const catalog = catalogJson as Catalog;
const line = (t: number) => ({ x: 100 * t, y: 0 }); // a horizontal line 100px long

describe("pickLabelT", () => {
  const size = { w: 40, h: 20 };
  it("takes the middle when nothing is in the way", () => {
    expect(pickLabelT(line, size, [])).toEqual({ t: 0.5, x: 50, y: 0, clear: true });
  });
  it("slides along the line to clear a node sitting on the middle", () => {
    const node: Rect = { x: 30, y: -30, w: 40, h: 60 }; // covers x 30..70
    const p = pickLabelT((t) => ({ x: 300 * t, y: 0 }), size, [{ x: 130, y: -30, w: 40, h: 60 }]);
    expect(p.clear).toBe(true);
    expect(p.t).not.toBe(0.5);
    void node;
  });
  it("reports when nowhere is clear, and picks the least bad spot", () => {
    const wall: Rect = { x: -10, y: -50, w: 200, h: 100 };
    const p = pickLabelT(line, size, [wall]);
    expect(p.clear).toBe(false);
    expect(overlapArea({ x: p.x - 20, y: p.y - 10, w: 40, h: 20 }, wall)).toBeGreaterThan(0);
  });
  it("is deterministic", () => {
    expect(pickLabelT(line, size, [{ x: 40, y: -10, w: 20, h: 20 }])).toEqual(pickLabelT(line, size, [{ x: 40, y: -10, w: 20, h: 20 }]));
  });
});

describe("layout reserves room for labels", () => {
  const g = buildProcessGraph(find("12_todo_workflow_example.bcl", "process", "todo.workflow"), catalog);
  const els = () => flowElements(g, { catalog, diagnostics: [], file: "f", saved: {} });

  it("puts no label on top of a step or of another label", () => {
    const { layout } = els();
    const nodeRects: Rect[] = g.steps.map((s) => ({ ...layout.positions[s.id]!, w: STEP_SIZE.width, h: STEP_SIZE.height }));
    const labelled = g.edges.filter((e) => e.label);
    expect(labelled.length).toBeGreaterThan(3);
    const labelRects: Rect[] = [];
    for (const e of labelled) {
      const c = layout.labels[e.key];
      expect(c, e.key).toBeDefined();
      const sz = labelSize(e.label);
      const r: Rect = { x: c!.x - sz.width / 2, y: c!.y - sz.height / 2, w: sz.width, h: sz.height };
      for (const n of nodeRects) expect(overlapArea(r, n), `${e.key} on a step`).toBe(0);
      for (const o of labelRects) expect(overlapArea(r, o), `${e.key} on another label`).toBe(0);
      labelRects.push(r);
    }
  });

  it("is deterministic with labels", () => {
    expect(els().layout).toEqual(els().layout);
    const nodes = [{ id: "a", width: 10, height: 10 }, { id: "b", width: 10, height: 10 }];
    const edges = [{ from: "a", to: "b", label: { width: 80, height: 20 }, key: "ab" }];
    expect(layoutGraph(nodes, edges)).toEqual(layoutGraph(nodes, edges));
  });
});
