import { beforeEach, describe, expect, it } from "vitest";
import type { Catalog } from "../api/types";
import { catalog as catalogJson } from "../test/helpers";
import { flowElements, diagFor } from "./elements";
import { find } from "./fixtures";
import { autoLayout, clearPositions, layoutKey, loadPositions, mergePositions, savePositions } from "./layout";
import { buildIntentGraph, buildPipelineGraph, buildProcessGraph, INPUT_NODE } from "./model";

const catalog = catalogJson as Catalog;
const FILE = "12_todo_workflow_example.bcl";

describe("auto layout", () => {
  const graph = () => buildProcessGraph(find(FILE, "process", "todo.workflow"), catalog);
  const els = () => flowElements(graph(), { catalog, diagnostics: [], file: FILE, saved: {} });

  it("is deterministic", () => {
    expect(els().auto).toEqual(els().auto);
    expect(els().nodes.map((n) => n.position)).toEqual(els().nodes.map((n) => n.position));
  });

  it("runs left to right: forward edges point rightwards", () => {
    const { auto } = els();
    const g = graph();
    const back = new Set(["revise_resubmit"]); // the only loop back in the todo workflow
    for (const e of g.edges) {
      if (back.has(e.block.id)) continue;
      expect(auto[e.to]!.x, `${e.from} -> ${e.to}`).toBeGreaterThan(auto[e.from]!.x);
    }
  });

  it("never overlaps two nodes", () => {
    const { nodes } = els();
    for (let i = 0; i < nodes.length; i++) {
      for (let j = i + 1; j < nodes.length; j++) {
        const a = nodes[i]!.position;
        const b = nodes[j]!.position;
        const apart = Math.abs(a.x - b.x) >= 210 || Math.abs(a.y - b.y) >= 84;
        expect(apart, `${nodes[i]!.id} vs ${nodes[j]!.id}`).toBe(true);
      }
    }
  });

  it("puts the request node upstream of the nodes that read it, for every real intent", () => {
    for (const id of ["todo.create", "todo.list", "todo.update"]) {
      const g = buildIntentGraph(find(FILE, "intent", id));
      const { auto } = flowElements(g, { catalog, diagnostics: [], file: FILE, saved: {} });
      for (const e of g.edges) {
        expect(auto[e.to]!.x, `${id}: ${e.from} -> ${e.to}`).toBeGreaterThan(auto[e.from]!.x);
      }
      if (g.usesInput) expect(auto[INPUT_NODE]).toBeDefined();
    }
  });

  it("orders pipeline stages left to right and marks first/last", () => {
    const g = buildPipelineGraph(find("passport.bcl", "pipeline", "passport"));
    const { nodes, edges } = flowElements(g, { catalog, diagnostics: [], file: FILE, saved: {} });
    const xs = nodes.map((n) => n.position.x);
    expect([...xs].sort((a, b) => a - b)).toEqual(xs);
    expect(edges).toHaveLength(g.stages.length - 1);
    expect(edges.every((e) => e.selectable === false && e.deletable === false)).toBe(true);
    expect(nodes.map((n) => [(n.data as { first: boolean }).first, (n.data as { last: boolean }).last]).flat().filter(Boolean)).toHaveLength(2);
  });

  it("saved positions win over automatic ones, only for the nodes that have one", () => {
    const auto = autoLayout([{ id: "a", width: 10, height: 10 }, { id: "b", width: 10, height: 10 }], [{ from: "a", to: "b" }]);
    const merged = mergePositions(auto, { b: { x: 500, y: 7 }, gone: { x: 1, y: 1 } });
    expect(merged.b).toEqual({ x: 500, y: 7 });
    expect(merged.a).toEqual(auto.a);
    expect(Object.keys(merged)).toEqual(["a", "b"]);
    const withSaved = flowElements(graph(), { catalog, diagnostics: [], file: FILE, saved: { review: { x: 999, y: 999 } } });
    expect(withSaved.nodes.find((n) => n.id === "review")!.position).toEqual({ x: 999, y: 999 });
    expect(withSaved.auto.review).not.toEqual({ x: 999, y: 999 });
  });
});

describe("saved layout storage", () => {
  beforeEach(() => localStorage.clear());
  it("round-trips per draft, file and block", () => {
    const a = layoutKey("d1", "f.bcl", "process/p");
    savePositions(a, { s: { x: 1, y: 2 } });
    expect(loadPositions(a)).toEqual({ s: { x: 1, y: 2 } });
    expect(loadPositions(layoutKey("d2", "f.bcl", "process/p"))).toEqual({});
    clearPositions(a);
    expect(loadPositions(a)).toEqual({});
  });
  it("ignores corrupt data", () => {
    const k = layoutKey("d", "f", "p");
    localStorage.setItem(k, "{not json");
    expect(loadPositions(k)).toEqual({});
    localStorage.setItem(k, JSON.stringify({ a: { x: "1" }, b: { x: 3, y: 4 } }));
    expect(loadPositions(k)).toEqual({ b: { x: 3, y: 4 } });
  });
});

describe("diagnostics on nodes", () => {
  it("counts diagnostics inside a node's path, in that file only", () => {
    const d = [
      { severity: "error" as const, message: "x", path: "intent/i/node/a/requires", file: "f.bcl" },
      { severity: "warning" as const, message: "y", path: "intent/i/node/a", file: "f.bcl" },
      { severity: "error" as const, message: "other node", path: "intent/i/node/ab", file: "f.bcl" },
      { severity: "error" as const, message: "other file", path: "intent/i/node/a", file: "g.bcl" },
      { severity: "error" as const, message: "no path" },
    ];
    expect(diagFor(d, "f.bcl", "intent/i/node/a")).toEqual({ errors: 1, warnings: 1 });
  });
});
