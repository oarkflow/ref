import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { App } from "./App";
import { StudioProvider } from "./state/context";
import { createStudioStore } from "./state/store";
import { DEFAULT_PREFS, uiStore } from "./state/ui";
import { fakeApi } from "./test/fakeApi";

async function mount() {
  const api = fakeApi();
  const store = createStudioStore(api, { batchDelay: 5, validateDelay: 5000 });
  store.setState({ token: "t" });
  render(
    <StudioProvider store={store}>
      <App />
    </StudioProvider>,
  );
  const nav = await screen.findByLabelText("Main navigation");
  await waitFor(() => expect(store.getState().draft).not.toBeNull());
  await waitFor(() => expect(Object.keys(store.getState().nav).length).toBeGreaterThan(0));
  return { api, store, nav };
}

beforeEach(() => {
  window.location.hash = "";
  uiStore.setState({ ...DEFAULT_PREFS, paletteOpen: false, addOpen: null });
});
afterEach(() => uiStore.setState({ ...DEFAULT_PREFS, paletteOpen: false, addOpen: null }));

describe("app shell", () => {
  it("opens on the overview and lists friendly categories with counts", async () => {
    const { nav } = await mount();
    expect(await screen.findByRole("heading", { level: 1, name: "starter" })).toBeInTheDocument();
    const pages = within(nav).getByRole("link", { name: /Pages & APIs/ });
    expect(pages).toHaveTextContent("1");
    expect(within(nav).getByRole("link", { name: /Logic flows/ })).toHaveTextContent("0");
    expect(within(nav).getByRole("link", { name: /Versions & reviews/ })).toBeInTheDocument();
    // no raw file names in the plain-language UI
    expect(within(nav).queryByText("04_routes.bcl")).toBeNull();
  });

  it("shows the pages list with a method chip and opens an item in the editor", async () => {
    const { nav, store } = await mount();
    await userEvent.click(within(nav).getByRole("link", { name: /Pages & APIs/ }));
    const row = await screen.findByRole("button", { name: /web\.todos_list/ });
    expect(within(row).getByText("GET")).toBeInTheDocument();
    expect(within(row).getByText("/todos")).toBeInTheDocument();
    await userEvent.click(row);
    expect(store.getState().selection).toEqual({ file: "04_routes.bcl", path: "route/web.todos_list" });
    expect(await screen.findByRole("heading", { level: 2, name: "web.todos_list" })).toBeInTheDocument();
    expect(screen.getByText("Address")).toBeInTheDocument(); // friendly label for "path"
  });

  it("collapses the sidebar to an icon rail and remembers it", async () => {
    const { nav } = await mount();
    await userEvent.click(within(nav).getByRole("button", { name: "Collapse sidebar" }));
    expect(uiStore.getState().sidebarCollapsed).toBe(true);
    expect(within(nav).queryByText("Logic flows")).toBeNull(); // labels hidden…
    expect(within(nav).getByRole("link", { name: "Logic flows" })).toBeInTheDocument(); // …but still named for screen readers
    await userEvent.click(within(nav).getByRole("button", { name: "Expand sidebar" }));
    expect(within(nav).getByText("Logic flows")).toBeInTheDocument();
  });

  it("Ctrl+K opens the palette; typing filters; Enter jumps to the item", async () => {
    const { store } = await mount();
    fireEvent.keyDown(window, { key: "k", ctrlKey: true });
    const dlg = await screen.findByRole("dialog", { name: "Search or jump to anything" });
    const input = within(dlg).getByRole("combobox");
    await userEvent.type(input, "todos");
    expect(within(dlg).getAllByRole("option")).toHaveLength(1);
    expect(within(dlg).getByRole("option")).toHaveTextContent("web.todos_list");
    await userEvent.keyboard("{Enter}");
    await waitFor(() => expect(store.getState().selection?.path).toBe("route/web.todos_list"));
    expect(screen.queryByRole("dialog", { name: "Search or jump to anything" })).toBeNull();
  });

  it("Escape closes the palette", async () => {
    await mount();
    act(() => uiStore.getState().setPalette(true));
    const dlg = await screen.findByRole("dialog", { name: "Search or jump to anything" });
    await userEvent.type(within(dlg).getByRole("combobox"), "{Escape}");
    expect(screen.queryByRole("dialog", { name: "Search or jump to anything" })).toBeNull();
  });

  it("Developer view reveals files and raw names; turning it off hides them again", async () => {
    const { nav } = await mount();
    expect(within(nav).queryByLabelText("Files")).toBeNull();
    act(() => uiStore.getState().toggleDev());
    const files = await within(nav).findByLabelText("Files");
    expect(within(files).getByText("04_routes.bcl")).toBeInTheDocument();
    await userEvent.click(within(nav).getByRole("link", { name: /Pages & APIs/ }));
    expect(await screen.findByText(/route · 04_routes\.bcl/)).toBeInTheDocument();
    act(() => uiStore.getState().toggleDev());
    await waitFor(() => expect(within(nav).queryByLabelText("Files")).toBeNull());
    expect(screen.queryByText(/route · 04_routes\.bcl/)).toBeNull();
  });

  it("the add flow offers plain-language types and creates the block with your answers", async () => {
    const { api, nav } = await mount();
    await userEvent.click(within(nav).getByRole("button", { name: "Add to Pages & APIs" }));
    const dlg = await screen.findByRole("dialog");
    await userEvent.click(within(dlg).getByRole("button", { name: /Page or API endpoint/ }));
    await userEvent.type(within(dlg).getByLabelText(/Endpoint name/), "web.reports");
    const path = within(dlg).getByLabelText(/Address/);
    await userEvent.clear(path);
    await userEvent.type(path, "/reports");
    await userEvent.click(within(dlg).getByRole("button", { name: /Add and keep editing/ }));
    await waitFor(() => expect(api.ops).toHaveBeenCalled());
    const ops = (api.ops as unknown as { mock: { calls: unknown[][] } }).mock.calls.at(-1)![1] as { op: string; type: string; id: string; body: string }[];
    expect(ops[0]).toMatchObject({ op: "addBlock", type: "route", id: "web.reports" });
    expect(ops[0]!.body).toBe('method GET\npath "/reports"');
  });
});
