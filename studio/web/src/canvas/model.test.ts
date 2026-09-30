import { describe, expect, it } from "vitest";
import type { BlockNode, Catalog, Op } from "../api/types";
import { catalog as catalogJson } from "../test/helpers";
import { find, intents, trees } from "./fixtures";
import {
  INPUT_NODE, addIntentNode, addStage, addStep, buildIntentGraph, buildPipelineGraph, buildProcessGraph, connectIntent,
  appendIntentNode, appendStep, connectProcess, deleteSteps, disconnectIntent, insertIntentNode, insertStep, traceOrder, disconnectProcess, edgeShape, moveStage, removeNodes, renameIntentNode,
  renameStep, setStart, wouldCycle,
} from "./model";

const catalog = catalogJson as Catalog;
const FILE = "12_todo_workflow_example.bcl";
const nodeType = (name: string) => catalog.node_types.find((t) => t.name === name)!;

const field = (parent: string, name: string, raw: string): BlockNode => ({ kind: "field", path: `${parent}/${name}`, name, raw, line: 1, start: 0, end: 0 });
const node = (root: string, id: string, f: Record<string, string>): BlockNode => {
  const path = `${root}/node/${id}`;
  return { kind: "block", path, type: "node", id, line: 1, start: 0, end: 0, children: Object.entries(f).map(([k, v]) => field(path, k, v)) };
};
const intent = (id: string, response: string, nodes: [string, Record<string, string>][]): BlockNode => {
  const path = `intent/${id}`;
  return {
    kind: "block", path, type: "intent", id, line: 1, start: 0, end: 0,
    children: [field(path, "response", `"${response}"`), ...nodes.map(([n, f]) => node(path, n, f))],
  };
};

describe("intent graph derivation on real configs", () => {
  it("resolves every requires of every starter intent (they compile, so nothing may be unresolved)", () => {
    let count = 0;
    for (const file of ["03_intents.bcl", FILE]) {
      for (const it of intents(file)) {
        const g = buildIntentGraph(it);
        count++;
        expect({ intent: it.id, unresolved: g.unresolved, duplicates: g.duplicates }).toEqual({ intent: it.id, unresolved: [], duplicates: [] });
        // every node is drawn and every edge points at real nodes
        const ids = new Set([INPUT_NODE, ...g.nodes.map((n) => n.id)]);
        for (const e of g.edges) {
          expect(ids.has(e.from)).toBe(true);
          expect(ids.has(e.to)).toBe(true);
        }
      }
    }
    expect(count).toBeGreaterThan(10);
  });

  it("draws producer -> consumer edges for todo.list", () => {
    const g = buildIntentGraph(find(FILE, "intent", "todo.list"));
    const edge = (from: string, to: string, fact: string) => g.edges.some((e) => e.from === from && e.to === to && e.fact === fact);
    expect(edge("user-rows", "user", "user_rows")).toBe(true);
    expect(edge("todos", "response", "todos")).toBe(true);
    expect(edge("user", "response", "user")).toBe(true);
    expect(g.nodes.find((n) => n.id === "response")?.isResponse).toBe(true);
    expect(g.nodes.map((n) => n.id)).toContain("user-id");
  });

  it("reads the request input as a virtual producer", () => {
    const g = buildIntentGraph(intent("t", "out", [
      ["a", { uses: '"collect"', requires: "[input]", provides: "[x]" }],
      ["b", { uses: '"collect"', requires: "[x]", provides: "[out]" }],
    ]));
    expect(g.usesInput).toBe(true);
    expect(g.edges.map((e) => e.id)).toEqual([`${INPUT_NODE}:input->a`, "a:x->b"]);
  });

  it("flags unresolved requires and duplicate producers", () => {
    const g = buildIntentGraph(intent("t", "out", [
      ["a", { uses: '"collect"', requires: "[missing]", provides: "[x]" }],
      ["b", { uses: '"collect"', requires: "[x]", provides: "[x, out]" }],
    ]));
    expect(g.unresolved).toEqual([{ node: "a", fact: "missing" }]);
    expect(g.duplicates).toEqual([{ fact: "x", nodes: ["a", "b"] }]);
  });

  it("treats quoted and bare fact names alike and collects chips", () => {
    const g = buildIntentGraph(intent("t", "out", [
      ["a", { uses: '"collect"', provides: '["x"]', timeout: "5s", on_error: "continue" }],
      ["b", { uses: '"collect"', requires: "[x]", provides: "[out]" }],
    ]));
    expect(g.edges).toHaveLength(1);
    expect(g.nodes[0]!.chips).toEqual(["timeout 5s", "on_error continue"]);
  });
});

describe("intent ops", () => {
  const base = () => buildIntentGraph(intent("t", "out", [
    ["a", { uses: '"collect"', provides: "[x]" }],
    ["b", { uses: '"collect"', requires: "[x]", provides: "[y]" }],
    ["c", { uses: '"collect"', provides: "[out]" }],
  ]));

  it("connect appends the fact to the consumer's requires, preserving existing items", () => {
    const r = connectIntent(base(), FILE, "b", "y", "c");
    expect(r).toEqual({ ok: true, ops: [{ op: "setField", file: FILE, path: "intent/t/node/c/requires", value: "[y]" }] });
    const r2 = connectIntent(base(), FILE, "a", "x", "c");
    expect(r2.ok && r2.ops[0]).toEqual({ op: "setField", file: FILE, path: "intent/t/node/c/requires", value: "[x]" });
    const g = buildIntentGraph(intent("t", "out", [
      ["a", { provides: "[x]" }],
      ["b", { provides: "[y]" }],
      ["c", { requires: '[x, "old"]', provides: "[out]" }],
    ]));
    const r3 = connectIntent(g, FILE, "b", "y", "c");
    expect(r3.ok && r3.ops[0]).toEqual({ op: "setField", file: FILE, path: "intent/t/node/c/requires", value: '[x, "old", y]' });
  });

  it("connect from the request uses the input fact", () => {
    const r = connectIntent(base(), FILE, INPUT_NODE, "input", "a");
    expect(r.ok && r.ops[0]).toEqual({ op: "setField", file: FILE, path: "intent/t/node/a/requires", value: "[input]" });
  });

  it("refuses self, duplicate, cyclic and expression-list connections", () => {
    const g = base();
    expect(connectIntent(g, FILE, "a", "x", "a")).toMatchObject({ ok: false });
    expect(connectIntent(g, FILE, "a", "x", "b")).toMatchObject({ ok: false, error: expect.stringContaining("already") });
    const cyc = connectIntent(g, FILE, "b", "y", "a");
    expect(cyc).toMatchObject({ ok: false, error: expect.stringContaining("cycle") });
    expect(wouldCycle(g, "b", "a")).toBe(true);
    expect(wouldCycle(g, "a", "c")).toBe(false);
    const expr = buildIntentGraph(intent("t", "out", [["a", { provides: "[x]" }], ["b", { requires: "concat(a, b)", provides: "[out]" }]]));
    expect(connectIntent(expr, FILE, "a", "x", "b")).toMatchObject({ ok: false });
  });

  it("disconnect removes one fact, or the field when it was the last", () => {
    const g = buildIntentGraph(intent("t", "out", [
      ["a", { provides: "[x]" }],
      ["b", { provides: "[y]" }],
      ["c", { requires: "[x, y]", provides: "[out]" }],
    ]));
    const r = disconnectIntent(g, FILE, { to: "c", fact: "x" });
    expect(r.ok && r.ops).toEqual([{ op: "setField", file: FILE, path: "intent/t/node/c/requires", value: "[y]" }]);
    const last = disconnectIntent(base(), FILE, { to: "b", fact: "x" });
    expect(last.ok && last.ops).toEqual([{ op: "removeField", file: FILE, path: "intent/t/node/b/requires" }]);
  });

  it("add builds a node from the catalog with a unique id and fact", () => {
    const g = base();
    const t = nodeType("transform");
    const r = addIntentNode(g, FILE, t);
    expect(r.id).toBe("transform");
    expect(r.ops).toEqual([
      { op: "addBlock", file: FILE, parent: "intent/t", type: "node", id: "transform", body: `family transform\nuses "${t.default_action}"\nprovides [transform]` },
    ]);
    const withT = buildIntentGraph(intent("t", "out", [["transform", { provides: "[transform]" }]]));
    expect(addIntentNode(withT, FILE, t).id).toBe("transform_2");
    expect(addIntentNode(withT, FILE, t).ops[0]).toMatchObject({ body: expect.stringContaining("provides [transform_2]") });
  });

  it("delete and rename map to block ops", () => {
    const g = base();
    expect(removeNodes(g, FILE, ["a", "c"])).toEqual([
      { op: "removeBlock", file: FILE, path: "intent/t/node/a" },
      { op: "removeBlock", file: FILE, path: "intent/t/node/c" },
    ]);
    expect(renameIntentNode(g, FILE, "a", "first")).toEqual({ ok: true, ops: [{ op: "renameBlock", file: FILE, path: "intent/t/node/a", newId: "first" }] });
    expect(renameIntentNode(g, FILE, "a", "b")).toMatchObject({ ok: false });
    expect(renameIntentNode(g, FILE, "a", "  ")).toMatchObject({ ok: false });
    expect(renameIntentNode(g, FILE, "a", "a")).toEqual({ ok: true, ops: [] });
  });

  it("deleting a producer leaves its consumers unresolved (shown in red, not silently rewritten)", () => {
    const g = base();
    const after = intent("t", "out", [
      ["b", { uses: '"collect"', requires: "[x]", provides: "[y]" }],
      ["c", { uses: '"collect"', provides: "[out]" }],
    ]);
    expect(removeNodes(g, FILE, ["a"])).toHaveLength(1);
    expect(buildIntentGraph(after).unresolved).toEqual([{ node: "b", fact: "x" }]);
  });
});

describe("process graph on the todo workflow", () => {
  const root = find(FILE, "process", "todo.workflow");
  const g = buildProcessGraph(root, catalog);

  it("derives steps, start, terminals and human tasks", () => {
    expect(g.steps.map((s) => s.id)).toEqual(["review", "mark_pending_approval", "approval", "revise", "done", "rejected", "cancelled"]);
    expect(g.start).toBe("review");
    expect(g.steps.find((s) => s.id === "review")).toMatchObject({ isStart: true, human: true, family: "approval" });
    expect(g.steps.filter((s) => s.terminal).map((s) => s.id)).toEqual(["done", "rejected", "cancelled"]);
    expect(g.steps.find((s) => s.id === "done")).toMatchObject({ intent: "todo.complete", human: false });
  });

  it("derives every edge with kind and condition labels, and none dangling", () => {
    expect(g.unlinked).toEqual([]);
    expect(g.edges).toHaveLength(7);
    const e = g.edges.find((x) => x.block.id === "review_approved")!;
    expect([e.from, e.to]).toEqual(["review", "mark_pending_approval"]);
    expect(e.label).toBe("result.action == 'approve'"); // plain sequencing kinds are implied
    expect(g.edges.find((x) => x.block.id === "to_approval")!.label).toBe("");
    const other = buildProcessGraph(processWith([{ id: "w", kind: "fanout", from: "a", targets: '["b"]' }]), catalog);
    expect(other.edges[0]!.label).toBe("Split into parallel"); // meaningful kinds are shown, in plain words
    // the loop back from revise to review is a real edge
    expect(g.edges.some((x) => x.from === "revise" && x.to === "review")).toBe(true);
  });

  it("styles by the edge type catalog", () => {
    const g2 = buildProcessGraph(processWith([{ id: "w", kind: "wait_event", from: "a", to: "b" }, { id: "r", kind: "retry", from: "a", to: "b" }]), catalog);
    const byKind = (k: string) => g2.edges.find((e) => e.block.kind === k)!;
    const parks = catalog.edge_types.filter((t) => t.parks).map((t) => t.name);
    const errs = catalog.edge_types.filter((t) => t.error_path).map((t) => t.name);
    expect(parks.length).toBeGreaterThan(0);
    expect(errs.length).toBeGreaterThan(0);
    for (const k of ["wait_event", "retry"]) {
      expect(byKind(k).parks).toBe(parks.includes(k));
      expect(byKind(k).errorPath).toBe(errs.includes(k));
    }
  });

  it("reports edges whose endpoints are not steps", () => {
    const g2 = buildProcessGraph(processWith([{ id: "x", kind: "simple", from: "a", to: "ghost" }]), catalog);
    expect(g2.edges).toEqual([]);
    expect(g2.unlinked.map((u) => [u.block.id, u.missing])).toEqual([["x", ["ghost"]]]);
  });
});

function processWith(edges: { id: string; kind: string; from?: string; to?: string; sources?: string; targets?: string }[]): BlockNode {
  const path = "process/p";
  const step = (id: string): BlockNode => ({ kind: "block", path: `${path}/step/${id}`, type: "step", id, line: 1, start: 0, end: 0, children: [] });
  return {
    kind: "block", path, type: "process", id: "p", line: 1, start: 0, end: 0,
    children: [
      field(path, "start", '"a"'), step("a"), step("b"), step("c"),
      ...edges.map((e): BlockNode => {
        const p = `${path}/edge/${e.id}`;
        const f: [string, string][] = [["kind", e.kind]];
        if (e.from) f.push(["from", `"${e.from}"`]);
        if (e.to) f.push(["to", `"${e.to}"`]);
        if (e.sources) f.push(["sources", e.sources]);
        if (e.targets) f.push(["targets", e.targets]);
        return { kind: "block", path: p, type: "edge", id: e.id, line: 1, start: 0, end: 0, children: f.map(([k, v]) => field(p, k, v)) };
      }),
    ],
  };
}

describe("process ops", () => {
  const conn = (g: ReturnType<typeof buildProcessGraph>, from: string, to: string, kind: string) => connectProcess(g, catalog, FILE, from, to, kind);

  it("classifies edge types by the catalog's field list", () => {
    const shape = (n: string) => edgeShape(catalog.edge_types.find((t) => t.name === n));
    expect(shape("simple")).toBe("single");
    expect(shape("fanout")).toBe("forward");
    expect(shape("join")).toBe("backward");
    expect(shape("threshold")).toBe("none");
    expect(edgeShape(undefined)).toBe("single");
  });

  it("connect creates a single edge block with a unique id", () => {
    const g = buildProcessGraph(processWith([]), catalog);
    const r = conn(g, "a", "b", "simple");
    expect(r).toEqual({
      ok: true, id: "a_to_b",
      ops: [{ op: "addBlock", file: FILE, parent: "process/p", type: "edge", id: "a_to_b", body: 'kind simple\nfrom "a"\nto "b"' }],
    });
    const g2 = buildProcessGraph(processWith([{ id: "a_to_b", kind: "simple", from: "a", to: "c" }]), catalog);
    const r2 = conn(g2, "a", "b", "branch");
    expect(r2.ok && r2.id).toBe("a_to_b_2");
  });

  it("connect creates forward and backward multi edges and merges into existing ones", () => {
    const g = buildProcessGraph(processWith([]), catalog);
    const fwd = conn(g, "a", "b", "fanout");
    expect(fwd.ok && fwd.ops[0]).toMatchObject({ op: "addBlock", id: "a_to_b", body: 'kind fanout\nfrom "a"\ntargets ["b"]' });
    const bwd = conn(g, "a", "c", "join");
    expect(bwd.ok && bwd.ops[0]).toMatchObject({ op: "addBlock", body: 'kind join\nsources ["a"]\nto "c"' });
    const g2 = buildProcessGraph(processWith([{ id: "f", kind: "fanout", from: "a", targets: '["b"]' }, { id: "j", kind: "join", sources: '["a"]', to: "c" }]), catalog);
    const m1 = conn(g2, "a", "c", "fanout");
    expect(m1.ok && m1.ops).toEqual([{ op: "setField", file: FILE, path: "process/p/edge/f/targets", value: '["b", "c"]' }]);
    const m2 = conn(g2, "b", "c", "join");
    expect(m2.ok && m2.ops).toEqual([{ op: "setField", file: FILE, path: "process/p/edge/j/sources", value: '["a", "b"]' }]);
  });

  it("connect refuses duplicates, unknown steps and targetless edge types", () => {
    const g = buildProcessGraph(processWith([{ id: "x", kind: "simple", from: "a", to: "b" }]), catalog);
    expect(conn(g, "a", "b", "simple")).toMatchObject({ ok: false, error: expect.stringContaining("already") });
    expect(conn(g, "a", "zzz", "simple")).toMatchObject({ ok: false });
    expect(conn(g, "a", "b", "threshold")).toMatchObject({ ok: false });
    // self loops are legitimate in a process (a retry, a loop_until)
    expect(conn(g, "a", "a", "loop_until")).toMatchObject({ ok: true });
  });

  it("disconnect removes single edges, shrinks multi edges, and drops emptied ones", () => {
    const g = buildProcessGraph(processWith([
      { id: "s", kind: "simple", from: "a", to: "b" },
      { id: "f", kind: "fanout", from: "a", targets: '["b", "c"]' },
      { id: "j", kind: "join", sources: '["a", "b"]', to: "c" },
      { id: "one", kind: "fanout", from: "b", targets: '["c"]' },
    ]), catalog);
    const of = (id: string, to: string) => g.edges.find((e) => e.block.id === id && e.to === to)!;
    expect(disconnectProcess(g, FILE, of("s", "b"))).toEqual([{ op: "removeBlock", file: FILE, path: "process/p/edge/s" }]);
    expect(disconnectProcess(g, FILE, of("f", "c"))).toEqual([{ op: "setField", file: FILE, path: "process/p/edge/f/targets", value: '["b"]' }]);
    expect(disconnectProcess(g, FILE, g.edges.find((e) => e.block.id === "j" && e.from === "b")!)).toEqual([
      { op: "setField", file: FILE, path: "process/p/edge/j/sources", value: '["a"]' },
    ]);
    expect(disconnectProcess(g, FILE, of("one", "c"))).toEqual([{ op: "removeBlock", file: FILE, path: "process/p/edge/one" }]);
  });

  it("add step, set start", () => {
    const g = buildProcessGraph(processWith([]), catalog);
    const terminal = catalog.node_types.find((t) => t.terminal)!;
    const r = addStep(g, FILE, terminal);
    expect(r.ops[0]).toEqual({ op: "addBlock", file: FILE, parent: "process/p", type: "step", id: terminal.name, body: `family ${terminal.name}\nterminal true` });
    expect(setStart(g, FILE, "b")).toEqual([{ op: "setField", file: FILE, path: "process/p/start", value: '"b"' }]);
  });

  it("deleting a step removes or trims every edge that references it, and the start", () => {
    const g = buildProcessGraph(processWith([
      { id: "s", kind: "simple", from: "a", to: "b" },
      { id: "t", kind: "simple", from: "b", to: "c" },
      { id: "f", kind: "fanout", from: "a", targets: '["b", "c"]' },
      { id: "j", kind: "join", sources: '["b"]', to: "c" },
      { id: "keep", kind: "simple", from: "c", to: "c" },
    ]), catalog);
    expect(deleteSteps(g, FILE, ["b"])).toEqual([
      { op: "removeBlock", file: FILE, path: "process/p/edge/s" },
      { op: "removeBlock", file: FILE, path: "process/p/edge/t" },
      { op: "setField", file: FILE, path: "process/p/edge/f/targets", value: '["c"]' },
      { op: "removeBlock", file: FILE, path: "process/p/edge/j" },
      { op: "removeBlock", file: FILE, path: "process/p/step/b" },
    ]);
    expect(deleteSteps(g, FILE, ["a"]).slice(-2)).toEqual([
      { op: "removeField", file: FILE, path: "process/p/start" },
      { op: "removeBlock", file: FILE, path: "process/p/step/a" },
    ]);
  });

  it("renaming a step rewrites every reference", () => {
    const g = buildProcessGraph(processWith([
      { id: "s", kind: "simple", from: "a", to: "b" },
      { id: "f", kind: "fanout", from: "b", targets: '["a", "c"]' },
      { id: "j", kind: "join", sources: '["a", "b"]', to: "a" },
    ]), catalog);
    const r = renameStep(g, FILE, "a", "first");
    expect(r).toEqual({
      ok: true,
      ops: [
        { op: "renameBlock", file: FILE, path: "process/p/step/a", newId: "first" },
        { op: "setField", file: FILE, path: "process/p/edge/s/from", value: '"first"' },
        { op: "setField", file: FILE, path: "process/p/edge/f/targets", value: '["first", "c"]' },
        { op: "setField", file: FILE, path: "process/p/edge/j/to", value: '"first"' },
        { op: "setField", file: FILE, path: "process/p/edge/j/sources", value: '["first", "b"]' },
        { op: "setField", file: FILE, path: "process/p/start", value: '"first"' },
      ],
    });
    expect(renameStep(g, FILE, "a", "b")).toMatchObject({ ok: false });
  });
});

describe("pipeline graph and reorder", () => {
  const root = find("passport.bcl", "pipeline", "passport");
  const g = buildPipelineGraph(root);

  it("lists stages in file order with their positions among the block's statements", () => {
    expect(g.stages.map((s) => s.id)).toEqual(["application", "verification", "biometrics", "approval", "issuance"]);
    for (const s of g.stages) expect(root.children![s.index]!.path).toBe(s.path);
    expect(g.size).toBe(root.children!.length);
  });

  it("reads reviews and chips from a stage", () => {
    const sp = "pipeline/p/stage/s";
    const stage: BlockNode = {
      kind: "block", path: sp, type: "stage", id: "s", line: 1, start: 0, end: 0,
      children: [
        field(sp, "title", '"Check"'),
        { kind: "block", path: `${sp}/review/r1`, type: "review", id: "r1", line: 1, start: 0, end: 0, children: [field(`${sp}/review/r1`, "mode", "gate")] },
        { kind: "block", path: `${sp}/rule/x`, type: "rule", id: "x", line: 1, start: 0, end: 0, children: [] },
        field(sp, "confirm_submit", "true"),
      ],
    };
    const pg = buildPipelineGraph({ kind: "block", path: "pipeline/p", type: "pipeline", id: "p", line: 1, start: 0, end: 0, children: [stage] });
    expect(pg.stages[0]).toMatchObject({ id: "s", title: "Check", reviews: [{ label: "gate" }], chips: ["1 rule", "confirm submit"] });
  });

  it("moving up inserts before the previous stage; down inserts after the next one", () => {
    const [a, b, c] = g.stages;
    expect(moveStage(g, FILE, "verification", -1)).toEqual([{ op: "moveBlock", file: FILE, path: b!.path, index: a!.index }]);
    expect(moveStage(g, FILE, "verification", 1)).toEqual([{ op: "moveBlock", file: FILE, path: b!.path, index: c!.index + 1 }]);
    expect(moveStage(g, FILE, "application", -1)).toEqual([]);
    expect(moveStage(g, FILE, "issuance", 1)).toEqual([]);
  });

  it("adds a stage with a unique id", () => {
    const r = addStage(g, FILE);
    expect(r.id).toBe("stage");
    expect(r.ops[0]).toEqual({ op: "addBlock", file: FILE, parent: "pipeline/passport", type: "stage", id: "stage", body: 'title "stage"' });
  });
});

describe("fixtures are real", () => {
  it("cover every intent, process and pipeline used above", () => {
    expect(Object.keys(trees)).toEqual(expect.arrayContaining(["03_intents.bcl", FILE, "passport.bcl"]));
  });
});

// keeps the unused-type import honest
export type _Ops = Op[];


describe("typed information on graph nodes", () => {
  it("knows each node's type and self-summary on the real branch example", () => {
    const g = buildIntentGraph(find("09_workflow_example.bcl", "intent", "orders.create"), catalog);
    const classify = g.nodes.find((n) => n.id === "classify")!;
    expect(classify.typeName).toBe("branch");
    expect(classify.summary.cases).toEqual([
      { label: "large", target: "orders.tier_large" },
      { label: "small", target: "orders.tier_small" },
    ]);
    const t = buildIntentGraph(find("09_workflow_example.bcl", "intent", "orders.transition"), catalog);
    expect(t.nodes.find((n) => n.id === "apply")!.summary.cases.map((c) => c.label)).toEqual(["apply", "apply_priority", "cancel"]);
    expect(t.nodes.find((n) => n.id === "apply")!.summary.line).toBe("on transition.outcome");
    expect(t.nodes.find((n) => n.id === "classify")).toMatchObject({ typeName: "decision", summary: { line: "5 rules" } });
  });

  it("infers a type from the action when there is no family", () => {
    const g = buildIntentGraph(find(FILE, "intent", "todo.list"), catalog);
    expect(g.nodes.map((n) => n.typeName).every(Boolean)).toBe(true);
    expect(g.nodes.find((n) => n.id === "response")!.typeName).toBe("join"); // uses "collect"
  });

  it("gives process steps a type from their family", () => {
    const p = buildProcessGraph(find(FILE, "process", "todo.workflow"), catalog);
    expect(p.steps.find((s) => s.id === "review")!.typeName).toBe("approval");
    expect(p.steps.find((s) => s.id === "done")!.typeName).toBe("action");
  });
});

describe("insert in place", () => {
  const g = () => buildIntentGraph(intent("t", "out", [
    ["a", { uses: '"collect"', requires: "[input]", provides: "[x]" }],
    ["b", { uses: '"collect"', requires: "[x, other]", provides: "[out]" }],
    ["o", { uses: '"collect"', provides: "[other]" }],
  ]));

  it("puts a new step between a producer and a consumer and rewires only that dependency", () => {
    const t = nodeType("transform");
    const r = insertIntentNode(g(), FILE, { from: "a", to: "b", fact: "x" }, t);
    expect(r.ok).toBe(true);
    if (!r.ok) return;
    expect(r.id).toBe("transform");
    expect(r.ops[0]).toEqual({ op: "addBlock", file: FILE, parent: "intent/t", type: "node", id: "transform", body: `family transform\nuses "${t.default_action}"\nrequires [x]\nprovides [transform]` });
    expect(r.ops[1]).toEqual({ op: "setField", file: FILE, path: "intent/t/node/b/requires", value: "[transform, other]" });
  });

  it("refuses when the consumer's Needs cannot be rewritten", () => {
    const expr = buildIntentGraph(intent("t", "out", [["a", { provides: "[x]" }], ["b", { requires: "concat(a)", provides: "[out]" }]]));
    expect(insertIntentNode(expr, FILE, { from: "a", to: "b", fact: "x" }, nodeType("transform"))).toMatchObject({ ok: false });
  });

  it("appends a step that needs what the last one produced", () => {
    const r = appendIntentNode(g(), FILE, "b", nodeType("email"));
    expect(r.ok && r.ops).toEqual([{ op: "addBlock", file: FILE, parent: "intent/t", type: "node", id: "email", body: expect.stringContaining("requires [out]") }]);
    expect(appendIntentNode(g(), FILE, "zzz", nodeType("email"))).toMatchObject({ ok: false });
  });

  it("splits a process connection A -> B into A -> X -> B", () => {
    const pg = buildProcessGraph(processWith([{ id: "ab", kind: "branch", from: "a", to: "b" }]), catalog);
    const r = insertStep(pg, catalog, FILE, pg.edges[0]!, nodeType("delay"));
    expect(r.ok && r.ops).toEqual([
      { op: "addBlock", file: FILE, parent: "process/p", type: "step", id: "delay", body: "family delay" },
      { op: "setField", file: FILE, path: "process/p/edge/ab/to", value: '"delay"' },
      { op: "addBlock", file: FILE, parent: "process/p", type: "edge", id: "delay_to_b", body: 'kind simple\nfrom "delay"\nto "b"' },
    ]);
  });

  it("splits one target of a fan-out, and declines joins", () => {
    const pg = buildProcessGraph(processWith([{ id: "f", kind: "fanout", from: "a", targets: '["b", "c"]' }, { id: "j", kind: "join", sources: '["a", "b"]', to: "c" }]), catalog);
    const fan = insertStep(pg, catalog, FILE, pg.edges.find((e) => e.block.id === "f" && e.to === "c")!, nodeType("delay"));
    expect(fan.ok && fan.ops[1]).toEqual({ op: "setField", file: FILE, path: "process/p/edge/f/targets", value: '["b", "delay"]' });
    expect(insertStep(pg, catalog, FILE, pg.edges.find((e) => e.block.id === "j")!, nodeType("delay"))).toMatchObject({ ok: false });
  });

  it("appends a step after another with a plain connection", () => {
    const pg = buildProcessGraph(processWith([]), catalog);
    const r = appendStep(pg, FILE, "c", nodeType("delay"));
    expect(r.ok && r.ops).toEqual([
      { op: "addBlock", file: FILE, parent: "process/p", type: "step", id: "delay", body: "family delay" },
      { op: "addBlock", file: FILE, parent: "process/p", type: "edge", id: "c_to_delay", body: 'kind simple\nfrom "c"\nto "delay"' },
    ]);
  });
});

describe("trace order", () => {
  it("walks a process breadth first from its start, once per line, even around a loop", () => {
    const g = buildProcessGraph(find(FILE, "process", "todo.workflow"), catalog);
    const order = traceOrder(g);
    const flat = order.flat();
    expect(new Set(flat).size).toBe(flat.length);
    expect(flat).toHaveLength(g.edges.length);
    expect(order[0]!.sort()).toEqual(["review_approved:review->mark_pending_approval", "review_changes:review->revise"]);
  });

  it("walks an intent by dependency level", () => {
    const g = buildIntentGraph(find(FILE, "intent", "todo.list"), catalog);
    const order = traceOrder(g);
    expect(order.flat().sort()).toEqual(g.edges.map((e) => e.id).sort());
    const level = (id: string) => order.findIndex((grp) => grp.includes(id));
    for (const e of g.edges) {
      for (const up of g.edges.filter((x) => x.to === e.from)) expect(level(up.id)).toBeLessThan(level(e.id));
    }
  });

  it("walks a pipeline stage by stage", () => {
    const g = buildPipelineGraph(find("passport.bcl", "pipeline", "passport"));
    expect(traceOrder(g)).toEqual([["seq:0"], ["seq:1"], ["seq:2"], ["seq:3"]]);
  });
});
