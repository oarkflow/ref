import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { FLOW_PALETTES, FlowDots, edgeClass, flowEdgeTypes, FlowEdge } from "./edges";
import { flowStyleFor } from "./flow";

const D = "M0,0 C100,0 100,100 200,100";

describe("FlowDots", () => {
  const dots = (over: Partial<Parameters<typeof FlowDots>[0]> = {}) =>
    render(<svg><FlowDots id="e1" d={D} length={500} speed="normal" style={flowStyleFor({})} on {...over} /></svg>).container;

  it("moves dots along the edge's own path, from source to target, with no JavaScript per frame", () => {
    const c = dots();
    const anims = c.querySelectorAll("animateMotion");
    expect(anims.length).toBe(2); // a 500px edge carries two dots
    for (const a of anims) {
      expect(a.getAttribute("path")).toBe(D);
      expect(a.getAttribute("repeatCount")).toBe("indefinite");
      expect(a.getAttribute("begin")!.startsWith("-") || a.getAttribute("begin") === "0.00s" || a.getAttribute("begin") === "-0.00s").toBe(true);
    }
  });

  it("draws nothing at all when flow is off (reduced motion, test, or 'Show data flow' switched off)", () => {
    const c = dots({ on: false });
    expect(c.querySelector("circle")).toBeNull();
    expect(c.querySelector("animateMotion")).toBeNull();
  });

  it("styles the dots by the kind of connection", () => {
    expect(dots({ style: flowStyleFor({ errorPath: true }) }).querySelector(".cv-dots")).toHaveClass("error");
    const park = dots({ style: flowStyleFor({ parks: true }) });
    expect(park.querySelector(".cv-dots")).toHaveClass("park");
    expect(park.querySelector("circle")).toHaveClass("hollow");
    const req = dots({ style: flowStyleFor({ fromRequest: true }) });
    expect(req.querySelector(".cv-dots")).toHaveClass("request");
    expect(req.querySelector("circle")).toHaveClass("hollow");
    expect(dots().querySelector("circle")).not.toHaveClass("hollow");
  });

  it("waiting edges move more slowly than normal ones, and a boosted edge is faster", () => {
    const dur = (over: Partial<Parameters<typeof FlowDots>[0]>) => parseFloat(dots(over).querySelector("animateMotion")!.getAttribute("dur")!);
    const base = dur({});
    expect(dur({ style: flowStyleFor({ parks: true }) })).toBeGreaterThan(base);
    expect(dur({ boosted: true })).toBeLessThan(base);
    expect(dur({ speed: "slow" })).toBeGreaterThan(base);
    expect(dur({ speed: "fast" })).toBeLessThan(base);
  });

  it("fades out (rather than cutting off) when the selection goes away", () => {
    const c = dots({ fading: true });
    expect(c.querySelector(".cv-dots")).toHaveClass("fading");
    expect((c.querySelector(".cv-dots") as SVGElement).style.opacity).toBe("0");
    expect(dots().querySelector(".cv-dots")).not.toHaveClass("fading");
  });

  it("starts each edge at its own phase", () => {
    const begin = (id: string) => dots({ id }).querySelector("animateMotion")!.getAttribute("begin");
    expect(new Set(["a", "b", "c", "d", "e"].map(begin)).size).toBe(5);
  });
});

describe("edgeClass", () => {
  const base = { kind: "normal", motion: true, flowOn: true };
  it("marks reduced motion / animations off as static", () => {
    expect(edgeClass({ ...base, motion: false })).toContain("cv-edge-static");
    expect(edgeClass(base)).not.toContain("cv-edge-static");
  });
  it("marks flow switched off, and the highlight states", () => {
    expect(edgeClass({ ...base, flowOn: false })).toContain("flow-off");
    expect(edgeClass({ ...base, focused: true })).toContain("focus");
    expect(edgeClass({ ...base, dimmed: true })).toContain("dim");
    expect(edgeClass({ ...base, hover: true })).toContain("hover");
    expect(edgeClass({ ...base, kind: "error" })).toContain("kind-error");
  });
});

describe("reuse", () => {
  it("exports the flow edge, its edge types and colour sets for other views", () => {
    expect(flowEdgeTypes.flow).toBe(FlowEdge);
    expect(flowEdgeTypes.labeled).toBe(FlowEdge);
    expect(FLOW_PALETTES.journey!.dot).toBeTruthy();
    expect(FLOW_PALETTES.default).toEqual({});
  });
});

import type { BlockNode, Catalog } from "../api/types";
import { catalog as catalogJson } from "../test/helpers";
import { flowElements } from "./elements";
import { buildIntentGraph, buildProcessGraph } from "./model";
import { find } from "./fixtures";

const field = (parent: string, name: string, raw: string): BlockNode => ({ kind: "field", path: `${parent}/${name}`, name, raw, line: 1, start: 0, end: 0 });
const nodeOf = (root: string, id: string, f: Record<string, string>): BlockNode => {
  const path = `${root}/node/${id}`;
  return { kind: "block", path, type: "node", id, line: 1, start: 0, end: 0, children: Object.entries(f).map(([k, v]) => field(path, k, v)) };
};

describe("what an edge says it carries", () => {
  const cat = catalogJson as Catalog;
  const g = buildIntentGraph({
    kind: "block", path: "intent/i", type: "intent", id: "i", line: 1, start: 0, end: 0,
    children: [
      field("intent/i", "response", '"out"'),
      nodeOf("intent/i", "a", { uses: '"collect"', requires: "[input]", provides: "[x, y]" }),
      nodeOf("intent/i", "b", { uses: '"collect"', requires: "[x, y]", provides: "[out]" }),
    ],
  });
  const els = flowElements(g, { catalog: cat, diagnostics: [], file: "f", saved: {} });
  const byId = Object.fromEntries(els.edges.map((e) => [e.id, e.data as { carries: string[]; count: number; label: string; from: string }]));

  it("lists every fact that flows between two steps on each of their lines", () => {
    expect(byId["a:x->b"]!.carries).toEqual(["x", "y"]);
    expect(byId["a:y->b"]!.carries).toEqual(["x", "y"]);
  });
  it("says how many on the first line only, so the pill is not repeated", () => {
    expect(byId["a:x->b"]!.count).toBe(2);
    expect(byId["a:y->b"]!.count).toBe(1);
  });
  it("marks data that comes from the request", () => {
    const fromReq = els.edges.find((e) => (e.data as { from: string }).from === "__input")!;
    expect(fromReq).toBeTruthy();
    expect((fromReq.data as { carries: string[] }).carries).toEqual(["input"]);
  });
  it("flags waiting and failure connections for their own dot style", () => {
    const p = buildProcessGraph(find("12_todo_workflow_example.bcl", "process", "todo.workflow"), cat);
    const pe = flowElements(p, { catalog: cat, diagnostics: [], file: "f", saved: {} });
    expect(pe.edges.every((e) => "parks" in (e.data as object) && "errorPath" in (e.data as object))).toBe(true);
    const cond = pe.edges.find((e) => (e.data as { blockId: string }).blockId === "review_approved")!;
    expect((cond.data as { carries: string[] }).carries).toEqual(["when result.action == 'approve'"]);
  });
});
