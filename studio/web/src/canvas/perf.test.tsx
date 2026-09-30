// Performance properties of the canvas that do not need a browser: on a big flow,
// changing the selection must touch only what is involved, never every step.
import { act, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeAll, beforeEach, describe, expect, it } from "vitest";
import type { BlockNode } from "../api/types";
import { StudioProvider } from "../state/context";
import { createStudioStore, type StudioStore } from "../state/store";
import { detail, fakeApi, meta } from "../test/fakeApi";
import { catalog, schemas } from "../test/helpers";
import { CanvasPage } from "./CanvasPage";
import { probeCounts, probeReset } from "./probe";

const FILE = "99_big.bcl";
const LAYERS = 12;
const WIDTH = 10; // 120 steps + a response

beforeAll(() => {
  class RO { observe() {} unobserve() {} disconnect() {} }
  Object.assign(globalThis, { ResizeObserver: RO });
  class Matrix { m22 = 1; constructor(_?: string) {} }
  Object.assign(globalThis, { DOMMatrixReadOnly: Matrix });
  Element.prototype.scrollIntoView = () => {};
  Object.defineProperty(window, "innerWidth", { value: 1600, configurable: true });
});
beforeEach(() => localStorage.clear());

const field = (parent: string, name: string, raw: string): BlockNode => ({ kind: "field", path: `${parent}/${name}`, name, raw, line: 1, start: 0, end: 0 });
const step = (root: string, id: string, f: Record<string, string>): BlockNode => {
  const path = `${root}/node/${id}`;
  return { kind: "block", path, type: "node", id, line: 1, start: 0, end: 0, children: Object.entries(f).map(([k, v]) => field(path, k, v)) };
};

/** A layered flow: every step needs two steps of the layer before it. */
function bigIntent(): BlockNode {
  const root = "intent/perf.big";
  const kids: BlockNode[] = [field(root, "response", '"out"')];
  for (let l = 0; l < LAYERS; l++) {
    for (let i = 0; i < WIDTH; i++) {
      const req = l === 0 ? "input" : `f${l - 1}_${i}, f${l - 1}_${(i + 1) % WIDTH}`;
      kids.push(step(root, `n${l}_${i}`, { uses: '"database.query"', requires: `[${req}]`, provides: `[f${l}_${i}]` }));
    }
  }
  kids.push(step(root, "out", { uses: '"collect"', requires: `[${Array.from({ length: WIDTH }, (_, i) => `f${LAYERS - 1}_${i}`).join(", ")}]`, provides: "[out]" }));
  return { kind: "block", path: root, type: "intent", id: "perf.big", line: 1, start: 0, end: 0, children: kids };
}

function open(): StudioStore {
  const tree = [bigIntent()];
  const store = createStudioStore(fakeApi());
  store.setState({ meta: meta(["editor"]), schemas, catalog, draft: detail({ id: "d1", files: [FILE] }), trees: { [FILE]: tree }, nav: { [FILE]: tree }, diagnostics: [], edit: () => {} });
  render(
    <StudioProvider store={store}>
      <MemoryRouter initialEntries={[`/canvas?file=${FILE}&path=${encodeURIComponent("intent/perf.big")}`]}>
        <Routes><Route path="/canvas" element={<CanvasPage />} /></Routes>
      </MemoryRouter>
    </StudioProvider>,
  );
  return store;
}

const select = (store: StudioStore, id: string) => act(async () => { await store.getState().select(FILE, `intent/perf.big/node/${id}`); });

describe("a 120-step flow", () => {
  it("draws every step", async () => {
    open();
    expect(await screen.findByText("n11_9", { selector: ".cv-titles strong" })).toBeInTheDocument();
    expect(document.querySelectorAll(".react-flow__node").length).toBeGreaterThan(100);
  });

  it("selecting a step re-renders only the steps it involves, not all of them", async () => {
    const store = open();
    await screen.findByText("n11_9", { selector: ".cv-titles strong" });
    await select(store, "n3_4"); // first selection also opens the details drawer
    probeReset();
    await select(store, "n6_2");
    const c = probeCounts();
    const steps = document.querySelectorAll(".react-flow__node").length;
    // the step that was selected and the one that is now: never anything like `steps` (it was ~4 x steps)
    expect(c.node ?? 0, `node renders after one selection (of ${steps} steps): ${JSON.stringify(c)}`).toBeLessThanOrEqual(8);
  });

  it("the canvas itself renders a bounded number of times per selection", async () => {
    const store = open();
    await screen.findByText("n11_9", { selector: ".cv-titles strong" });
    await select(store, "n3_4");
    probeReset();
    await select(store, "n6_2");
    expect(probeCounts().canvas ?? 0).toBeLessThanOrEqual(8);
  });
});
