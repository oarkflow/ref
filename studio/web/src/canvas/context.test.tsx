import { act, render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { FlowProvider, createCollapsedStore, createFlowStore, CollapsedProvider, useCollapsed, useEdgeFlow } from "./context";

describe("flow store", () => {
  const state = (focus: string[] | null, over = {}) => ({ on: true, speed: "normal" as const, focus: focus ? new Set(focus) : null, total: 10, ...over });

  it("answers per edge: focused, dimmed behind a focused one, or neither when nothing is selected", () => {
    const s = createFlowStore(state(null));
    expect(s.markOf("a")).toBe("none");
    s.set(state(["a", "b"]));
    expect(s.markOf("a")).toBe("focus");
    expect(s.markOf("z")).toBe("dim");
    s.set(state(null));
    expect(s.markOf("z")).toBe("none");
  });

  it("does not notify when the new state says the same thing", () => {
    const s = createFlowStore(state(["a"]));
    let n = 0;
    s.subscribe(() => n++);
    s.set(state(["a"])); // a new Set with the same members
    expect(n).toBe(0);
    s.set(state(["a", "b"]));
    expect(n).toBe(1);
    s.set(state(["a", "b"], { speed: "fast" }));
    expect(n).toBe(2);
  });

  it("re-renders an edge only when its own answer changes", () => {
    const s = createFlowStore(state(["e1"]));
    let renders = 0;
    function Edge({ id }: { id: string }) {
      renders++;
      const f = useEdgeFlow(id);
      return <span data-mark={f.mark} />;
    }
    const { container } = render(<FlowProvider value={s}><Edge id="e1" /><Edge id="e2" /></FlowProvider>);
    expect(container.querySelectorAll("[data-mark=focus]")).toHaveLength(1);
    const first = renders;
    // e1 stays focused, e2 stays dimmed, only the selection grows to an edge neither of them is: no re-render
    act(() => s.set(state(["e1", "e3"])));
    expect(renders).toBe(first);
    // e2 becomes focused: only e2 renders
    act(() => s.set(state(["e1", "e2"])));
    expect(renders).toBe(first + 1);
    expect(container.querySelectorAll("[data-mark=focus]")).toHaveLength(2);
  });
});

describe("collapsed store", () => {
  it("re-renders only the step that was folded", () => {
    const s = createCollapsedStore();
    let renders = 0;
    function Step({ id }: { id: string }) {
      renders++;
      return <i data-c={String(useCollapsed(id))} />;
    }
    const { container } = render(<CollapsedProvider value={s}><Step id="a" /><Step id="b" /></CollapsedProvider>);
    const first = renders;
    act(() => s.toggle("a"));
    expect(renders).toBe(first + 1);
    expect(container.querySelectorAll("[data-c=true]")).toHaveLength(1);
    act(() => s.toggle("a"));
    expect(container.querySelectorAll("[data-c=true]")).toHaveLength(0);
  });
});
