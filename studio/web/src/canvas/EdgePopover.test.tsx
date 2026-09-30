import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Catalog, Op } from "../api/types";
import { StudioProvider } from "../state/context";
import { createStudioStore } from "../state/store";
import { fakeApi } from "../test/fakeApi";
import { catalog as catalogJson } from "../test/helpers";
import { EdgePopover, emitEdgeValue } from "./EdgePopover";
import type { EdgeBlockInfo } from "./model";

const catalog = catalogJson as unknown as Catalog;
afterEach(cleanup);

const list = { raws: [], editable: true, absent: true } as unknown as EdgeBlockInfo["targets"];
const edge = (over: Partial<EdgeBlockInfo>): EdgeBlockInfo => ({
  id: "e1", path: "process/p/edge/e1", kind: "branch", from: "review", to: "done", settings: {},
  sources: list, targets: list, ...over,
});

function mount(block: EdgeBlockInfo, opts: { readOnly?: boolean; siblings?: EdgeBlockInfo[] } = {}) {
  const edit = vi.fn<(ops: Op[]) => void>();
  const onClose = vi.fn();
  const onMore = vi.fn();
  const store = createStudioStore(fakeApi(), { batchDelay: 5, validateDelay: 20 });
  render(
    <StudioProvider store={store}>
      <EdgePopover anchor={new DOMRect(100, 100, 40, 20)} file="t.bcl" block={block} catalog={catalog}
        readOnly={opts.readOnly} siblings={opts.siblings ?? [block]} facts={["result.action"]} edit={edit} onMore={onMore} onClose={onClose} />
    </StudioProvider>,
  );
  return { edit, onClose, onMore };
}

describe("emitEdgeValue", () => {
  it("writes numbers, durations and booleans as they are, and quotes the rest", () => {
    expect(emitEdgeValue("3")).toBe("3");
    expect(emitEdgeValue("48h")).toBe("48h");
    expect(emitEdgeValue("15m")).toBe("15m");
    expect(emitEdgeValue("true")).toBe("true");
    expect(emitEdgeValue("payment.received")).toBe('"payment.received"');
    expect(emitEdgeValue("  hi there ")).toBe('"hi there"');
  });
});

describe("EdgePopover", () => {
  it("says what the connection means and where it goes", () => {
    mount(edge({ kind: "delayed", settings: { timeout: '"48h"' } }));
    expect(screen.getByRole("dialog", { name: "Connection settings" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "After 48 hours" })).toBeTruthy();
    expect(screen.getByRole("dialog").textContent).toMatch(/review\s*→\s*done/);
  });

  it("flags a connection that needs a value", () => {
    mount(edge({ kind: "delayed" }));
    expect(screen.getByRole("heading", { name: "Add a delay" })).toBeTruthy();
    expect(screen.getByText(/needs a value/)).toBeTruthy();
  });

  it("writes a duration setting when you leave the field", () => {
    const { edit } = mount(edge({ kind: "delayed" }));
    const input = screen.getByLabelText("How long") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "48h" } });
    fireEvent.blur(input);
    expect(edit).toHaveBeenCalledWith([{ op: "setField", file: "t.bcl", path: "process/p/edge/e1/timeout", value: "48h" }]);
  });

  it("refuses a length of time the app could not read, and says why", () => {
    const { edit } = mount(edge({ kind: "delayed" }));
    const input = screen.getByLabelText("How long") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "2d" } });
    fireEvent.blur(input);
    expect(edit).not.toHaveBeenCalled();
    expect(screen.getByRole("alert").textContent).toMatch(/Days aren’t supported/);
    expect(input.getAttribute("aria-invalid")).toBe("true");
  });

  it("removes a setting that you empty", () => {
    const { edit } = mount(edge({ kind: "delayed", settings: { timeout: "48h" } }));
    const input = screen.getByLabelText("How long") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "" } });
    fireEvent.blur(input);
    expect(edit).toHaveBeenCalledWith([{ op: "removeField", file: "t.bcl", path: "process/p/edge/e1/timeout" }]);
  });

  it("quotes the name of an event", () => {
    const { edit } = mount(edge({ kind: "wait_event" }));
    const input = screen.getByLabelText("Wait for this event") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "payment.received" } });
    fireEvent.blur(input);
    expect(edit).toHaveBeenCalledWith([{ op: "setField", file: "t.bcl", path: "process/p/edge/e1/event", value: '"payment.received"' }]);
  });

  it("changes the kind of connection without touching where it goes", () => {
    const { edit } = mount(edge({ kind: "branch" }));
    fireEvent.change(screen.getByLabelText("How it connects"), { target: { value: "error" } });
    expect(edit).toHaveBeenCalledTimes(1);
    expect(edit.mock.calls[0]![0]).toEqual([{ op: "setField", file: "t.bcl", path: "process/p/edge/e1/kind", value: "error" }]);
  });

  it("offers a condition builder for a branch", () => {
    mount(edge({ kind: "branch", condition: "result.action == 'approve'" }));
    expect(screen.getByText("Follow this when")).toBeTruthy();
  });

  it("cannot change anything for a viewer", () => {
    mount(edge({ kind: "delayed", settings: { timeout: "48h" } }), { readOnly: true });
    expect((screen.getByLabelText("How long") as HTMLInputElement).disabled).toBe(true);
    expect((screen.getByLabelText("How it connects") as HTMLSelectElement).disabled).toBe(true);
  });

  it("closes on Escape and on Done, and opens the full settings on request", () => {
    const { onClose, onMore } = mount(edge({}));
    fireEvent.keyDown(document, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(onClose).toHaveBeenCalledTimes(2);
    fireEvent.click(screen.getByRole("button", { name: "More settings" }));
    expect(onMore).toHaveBeenCalled();
  });
});
