import { describe, expect, it } from "vitest";
import { journeyElements, withHiddenLoops } from "./elements";
import { FOCUS, FULL } from "./fixtures";
import { layoutJourney } from "./layout";
import { DEFAULT_FILTERS, SHARED_ID, buildJourney } from "./model";

const whole = buildJourney(FULL, DEFAULT_FILTERS);
const els = journeyElements(whole, layoutJourney(whole));
const kind = (id: string) => id.split(":")[0];

describe("journeyElements", () => {
  it("makes a node per card and an edge per line, all of the flow kind", () => {
    expect(els.nodes).toHaveLength(whole.nodes.length);
    expect(els.edges).toHaveLength(whole.edges.length);
    expect(els.edges.every((e) => e.type === "flow")).toBe(true);
    expect(els.nodes.map((n) => n.type).sort()).toEqual(whole.nodes.map((n) => n.kind).sort());
  });

  it("starts each line at the button that sends it, and ends it at the left of the card", () => {
    const save = els.edges.find((e) => e.id.startsWith("calls:") && e.target === "route:web.todos_create" && e.source === "page:pages/todos/new")!;
    expect(save.sourceHandle).toMatch(/^element:page:pages\/todos\/new#/);
    expect(save.targetHandle).toBe("in");
    const runs = els.edges.find((e) => kind(e.id) === "runs")!;
    expect(runs.sourceHandle).toBe("out");
  });

  it("curves forward lines and gives loops room", () => {
    const fwd = els.edges.find((e) => kind(e.id) === "calls")!;
    expect(fwd.data?.curve).toBe("bezier");
    const back = els.edges.find((e) => kind(e.id) === "redirects")!;
    expect(back.data?.curve).toBeUndefined();
    expect(back.data?.backOffset).toBeGreaterThan(40);
  });

  it("labels lines in plain words, or as written in the Developer view", () => {
    expect(els.edges.find((e) => kind(e.id) === "runs")!.data?.label).toBe("runs");
    const dev = journeyElements(whole, layoutJourney(whole), { dev: true });
    expect(dev.edges.find((e) => kind(e.id) === "calls")!.data?.label).toBe("calls · POST");
  });

  it("gives the flow animation both ends of every line", () => {
    expect(els.flowEdges).toHaveLength(whole.edges.length);
    const closed = els.edges.filter((e) => e.source === SHARED_ID);
    expect(closed.length).toBeGreaterThan(0);
    expect(closed.every((e) => e.sourceHandle === "out")).toBe(true);
  });
});

describe("withHiddenLoops", () => {
  const ids = (edges: { id: string; hidden?: boolean }[]) => edges.filter((e) => e.hidden).map((e) => kind(e.id));
  it("hides 'shows' and 'on success' lines in the overview", () => {
    const hidden = ids(withHiddenLoops(els.edges, { focused: false, revealed: new Set() }));
    expect(new Set(hidden)).toEqual(new Set(["renders", "redirects"]));
  });
  it("draws the redirects of a focused journey, but still not the 'shows' lines", () => {
    const f = buildJourney(FOCUS, DEFAULT_FILTERS);
    const fe = journeyElements(f, layoutJourney(f));
    const hidden = ids(withHiddenLoops(fe.edges, { focused: true, revealed: new Set() }));
    expect(new Set(hidden)).toEqual(new Set(["renders"]));
    expect(fe.edges.some((e) => kind(e.id) === "redirects")).toBe(true);
  });
  it("brings back a hidden line when something it touches is selected", () => {
    const loop = els.edges.find((e) => kind(e.id) === "redirects")!;
    const shown = withHiddenLoops(els.edges, { focused: false, revealed: new Set([loop.id]) });
    expect(shown.find((e) => e.id === loop.id)!.hidden).toBeUndefined();
  });
  it("leaves the other lines as they were (same objects), so they do not re-render", () => {
    const out = withHiddenLoops(els.edges, { focused: false, revealed: new Set() });
    const calls = els.edges.find((e) => kind(e.id) === "calls")!;
    expect(out.find((e) => e.id === calls.id)).toBe(calls);
  });
});
