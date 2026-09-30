import { describe, expect, it } from "vitest";
import { FULL, FOCUS } from "./fixtures";
import { DEFAULT_FILTERS, SHARED_ID, buildJourney, type JNode } from "./model";
import { LANE_GAP, ROW_GAP, layoutJourney } from "./layout";

const j = buildJourney(FULL);
const L = layoutJourney(j);

const box = (n: JNode) => ({ x: L.positions[n.id]!.x, y: L.positions[n.id]!.y, w: n.width, h: n.height });
const overlap = (a: ReturnType<typeof box>, b: ReturnType<typeof box>) => a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;

describe("layout", () => {
  it("places every card", () => {
    expect(Object.keys(L.positions).sort()).toEqual(j.nodes.map((n) => n.id).sort());
  });

  it("keeps lanes in columns, left to right", () => {
    const x = (id: string) => L.positions[id]!.x;
    expect(x("page:pages/todos/list")).toBeLessThan(x("route:web.todos_create"));
    expect(x("route:web.todos_create")).toBeLessThan(x("intent:todo.create"));
    expect(x("intent:todo.create")).toBeLessThan(x("resource:database"));
    // one x per lane
    for (const lane of new Set(j.nodes.map((n) => n.lane))) {
      expect(new Set(j.nodes.filter((n) => n.lane === lane).map((n) => L.positions[n.id]!.x)).size).toBe(1);
    }
    expect(L.lanes).toBe(new Set(j.nodes.map((n) => n.lane)).size);
  });

  it("never lets two cards overlap, and keeps the gap between them", () => {
    for (let a = 0; a < j.nodes.length; a++) {
      for (let b = a + 1; b < j.nodes.length; b++) {
        const A = box(j.nodes[a]!);
        const B = box(j.nodes[b]!);
        expect(overlap(A, B), `${j.nodes[a]!.id} / ${j.nodes[b]!.id}`).toBe(false);
      }
    }
    const lane0 = j.nodes.filter((n) => n.lane === 0).sort((a, b) => L.positions[a.id]!.y - L.positions[b.id]!.y);
    for (let i = 1; i < lane0.length; i++) {
      const prev = box(lane0[i - 1]!);
      expect(L.positions[lane0[i]!.id]!.y - (prev.y + prev.h)).toBeGreaterThanOrEqual(ROW_GAP);
    }
  });

  it("puts the shared card after the pages", () => {
    const pages = j.nodes.filter((n) => n.kind === "page");
    const shared = L.positions[SHARED_ID]!;
    for (const p of pages) expect(L.positions[p.id]!.y).toBeLessThan(shared.y);
  });

  it("sits a request level with the page that calls it when it can", () => {
    const page = box(j.byId.get("page:pages/todos/new")!);
    const route = box(j.byId.get("route:web.todos_create")!);
    expect(Math.abs((route.y + route.h / 2) - (page.y + page.h / 2))).toBeLessThan(400);
  });

  it("is deterministic", () => {
    expect(layoutJourney(buildJourney(FULL))).toEqual(L);
  });

  it("is wide enough for its lanes", () => {
    expect(L.width).toBeGreaterThan(L.lanes * 280 + (L.lanes - 1) * LANE_GAP);
    expect(L.height).toBeGreaterThan(0);
  });

  it("lays out a focused journey", () => {
    const f = buildJourney(FOCUS, DEFAULT_FILTERS);
    const FL = layoutJourney(f);
    expect(Object.keys(FL.positions).length).toBe(f.nodes.length);
  });

  it("makes the shared card taller when it is opened", () => {
    const closed = buildJourney(FULL).byId.get(SHARED_ID)!.height;
    const open = buildJourney(FULL, { ...DEFAULT_FILTERS, sharedOpen: true }).byId.get(SHARED_ID)!.height;
    expect(open).toBeGreaterThan(closed);
  });
});
