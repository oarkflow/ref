import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { forwardRef } from "react";
import { Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api/client";
import { mount, pagesApi } from "./testKit";
import { TemplateEditor } from "./TemplateEditor";

// CodeMirror needs a real layout engine; the editor's behaviour is what is under test here.
vi.mock("./CodeEditor", () => ({
  default: forwardRef(function FakeEditor(p: { value: string; readOnly?: boolean; onChange?(v: string): void; diagnostics?: { message: string }[] }, _ref) {
    return (
      <div>
        <textarea aria-label="code" value={p.value} readOnly={p.readOnly} onChange={(e) => p.onChange?.(e.target.value)} />
        <output aria-label="gutter">{(p.diagnostics ?? []).map((d) => d.message).join("|")}</output>
      </div>
    );
  }),
}));

const PATH = "templates/pages/todos/list.html";
const ui = () => (
  <Routes>
    <Route path="/pages/edit" element={<TemplateEditor />} />
  </Routes>
);
const route = `/pages/edit?path=${encodeURIComponent(PATH)}`;

afterEach(() => vi.useRealTimers());

async function open(over: Record<string, unknown> = {}, roles?: ("viewer" | "editor")[], customized = false) {
  const api = pagesApi(over);
  if (customized) api.files.set(PATH, { content: "hello", source: "draft" });
  const m = await mount(ui(), { api, roles, route });
  const code = (await screen.findByLabelText("code")) as HTMLTextAreaElement;
  return { ...m, code };
}

describe("the app's own file", () => {
  it("is read only and offers to customize", async () => {
    const { code } = await open();
    expect(code).toHaveAttribute("readonly");
    expect(screen.getByRole("button", { name: /Customize/ })).toBeInTheDocument();
    expect(screen.getByText(/the original stays untouched/)).toBeInTheDocument();
    expect(screen.getByText("Original")).toBeInTheDocument();
  });

  it("becomes editable after customizing", async () => {
    const { api, code } = await open();
    await userEvent.click(screen.getByRole("button", { name: /Customize/ }));
    await waitFor(() => expect(api.importFromDisk).toHaveBeenCalledWith("d1", [PATH], 1));
    await waitFor(() => expect(screen.getByLabelText("code")).not.toHaveAttribute("readonly"));
    expect(screen.getByText("Customized")).toBeInTheDocument();
    expect(code).toBeDefined();
  });

  it("is not shown to a viewer, who cannot open a working copy at all", async () => {
    const api = pagesApi();
    await mount(ui(), { api, roles: ["viewer"], route });
    expect(await screen.findByText("Page designs are for editors")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Customize/ })).toBeNull();
    expect(api.getAsset).not.toHaveBeenCalled();
  });
});

describe("autosave", () => {
  beforeEach(() => vi.useFakeTimers({ shouldAdvanceTime: true }));

  it("waits for a pause, then saves once with everything typed", async () => {
    const { api, code } = await open({}, undefined, true);
    const typed = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    await typed.clear(code);
    await typed.type(code, "abc");
    expect(api.putAsset).not.toHaveBeenCalled();
    await act(async () => { await vi.advanceTimersByTimeAsync(900); });
    await waitFor(() => expect(api.putAsset).toHaveBeenCalledTimes(1));
    expect(vi.mocked(api.putAsset).mock.calls[0]![2]).toBe("abc");
    await waitFor(() => expect(screen.getByText("All changes saved")).toBeInTheDocument());
  });

  it("does not resend text the server refused, and shows why", async () => {
    const diag = { severity: "error", code: "studio.pages.syntax", message: "@if: expected '{'", line: 1, column: 1 };
    const { api, code } = await open({ putAsset: vi.fn(async () => { throw new ApiError(422, "invalid_template", "invalid", { diagnostics: [diag] }); }) }, undefined, true);
    const typed = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    await typed.clear(code);
    await typed.type(code, "@if(");
    await act(async () => { await vi.advanceTimersByTimeAsync(900); });
    await waitFor(() => expect(screen.getByText(/Not saved: it has a mistake/)).toBeInTheDocument());
    expect(screen.getByLabelText("gutter")).toHaveTextContent("@if: expected '{'");
    expect(screen.getByRole("list", { name: "Problems in this design" })).toHaveTextContent("Line 1: @if: expected '{'");
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    expect(api.putAsset).toHaveBeenCalledTimes(1); // no retry loop
  });

  it("lets the person save it anyway", async () => {
    const put = vi.fn().mockRejectedValueOnce(new ApiError(422, "invalid_template", "invalid", { diagnostics: [] })).mockResolvedValue({ version: 3, applied: 1, diagnostics: [], changed: [] });
    const { code } = await open({ putAsset: put }, undefined, true);
    const typed = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    await typed.clear(code);
    await typed.type(code, "x");
    await act(async () => { await vi.advanceTimersByTimeAsync(900); });
    await userEvent.setup({ advanceTimers: vi.advanceTimersByTime }).click(await screen.findByRole("button", { name: "Save anyway" }));
    await waitFor(() => expect(put).toHaveBeenCalledTimes(2));
    expect(put.mock.calls[1]![4]).toBe(true);
  });

  it("offers to try again after a network failure", async () => {
    const put = vi.fn().mockRejectedValueOnce(new ApiError(500, "x", "disk full")).mockResolvedValue({ version: 3, applied: 1, diagnostics: [], changed: [] });
    const { code } = await open({ putAsset: put }, undefined, true);
    const typed = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    await typed.clear(code);
    await typed.type(code, "y");
    await act(async () => { await vi.advanceTimersByTimeAsync(900); });
    expect(await screen.findByText("disk full")).toBeInTheDocument();
    await userEvent.setup({ advanceTimers: vi.advanceTimersByTime }).click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(screen.getByText("All changes saved")).toBeInTheDocument());
  });
});

describe("reverting and deleting", () => {
  it("asks first, then reverts a customized file", async () => {
    const { api } = await open({}, undefined, true);
    await userEvent.click(screen.getByRole("button", { name: "More actions" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: /Revert to original/ }));
    const dialog = await screen.findByRole("dialog");
    expect(api.deleteAsset).not.toHaveBeenCalled();
    expect(within(dialog).getByText(/You can undo this from the top bar/)).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Revert to original" }));
    await waitFor(() => expect(api.deleteAsset).toHaveBeenCalledWith("d1", PATH, 1));
  });

  it("keeps the file if the person cancels", async () => {
    const { api } = await open({}, undefined, true);
    await userEvent.click(screen.getByRole("button", { name: "More actions" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: /Revert to original/ }));
    await userEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Cancel" }));
    expect(api.deleteAsset).not.toHaveBeenCalled();
  });
});

describe("following changes made elsewhere", () => {
  it("shows the new content after an undo, when there are no unsaved edits", async () => {
    const { api, store, code } = await open({}, undefined, true);
    expect(code.value).toBe("hello");
    api.files.set(PATH, { content: "from the server", source: "draft" });
    store.setState((s) => ({ draft: s.draft ? { ...s.draft, version: s.draft.version + 5 } : s.draft }));
    await waitFor(() => expect((screen.getByLabelText("code") as HTMLTextAreaElement).value).toBe("from the server"));
  });
});
