import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { Inspector, focusField } from "../forms/Inspector";
import { StudioProvider } from "../state/context";
import { createStudioStore } from "../state/store";
import { fakeApi } from "../test/fakeApi";
import { DiagnosticsPanel, location } from "./DiagnosticsPanel";

async function mount(diagnostics = [] as import("../api/types").Diagnostic[]) {
  const api = fakeApi();
  const store = createStudioStore(api, { batchDelay: 5, validateDelay: 5000 });
  await store.getState().init();
  store.setState({ diagnostics });
  render(
    <StudioProvider store={store}>
      <DiagnosticsPanel />
      <Inspector />
    </StudioProvider>,
  );
  return { api, store };
}

describe("DiagnosticsPanel", () => {
  const list = [
    { severity: "error" as const, message: "path must start with /", file: "04_routes.bcl", line: 19, path: "route/web.todos_list/path" },
    { severity: "warning" as const, message: "APP_ENV is not set here" },
  ];

  it("lists problems and filters by severity", async () => {
    await mount(list);
    const panel = screen.getByRole("region", { name: "Problems" });
    expect(within(panel).getAllByRole("button", { name: /Addresses must start|APP_ENV/ })).toHaveLength(2);
    await userEvent.click(within(panel).getByRole("button", { name: /^Errors/ }));
    expect(within(panel).queryByText(/APP_ENV/)).toBeNull();
    await userEvent.click(within(panel).getByRole("button", { name: /^Warnings/ }));
    expect(within(panel).queryByText(/Addresses must start/)).toBeNull();
  });

  it("clicking a problem opens its block and focuses the field", async () => {
    const { store } = await mount(list);
    await userEvent.click(screen.getByRole("button", { name: /Addresses must start/ }));
    expect(store.getState().selection).toEqual({ file: "04_routes.bcl", path: "route/web.todos_list" });
    await waitFor(() => {
      const box = document.querySelector<HTMLElement>('[data-path="route/web.todos_list/path"]')!;
      expect(box).toHaveClass("flash");
      expect(box.contains(document.activeElement)).toBe(true);
    });
  });

  it("shows the same message inline on the field", async () => {
    await mount(list);
    const box = await waitFor(() => document.querySelector<HTMLElement>('[data-path="route/web.todos_list/path"]')!);
    expect(within(box).getByRole("alert")).toHaveTextContent("Addresses must start with");
  });

  it("formats a location", () => {
    expect(location({ severity: "error", message: "m", file: "a.bcl", line: 3, path: "route/x" })).toBe("a.bcl:3 · route/x");
    expect(location({ severity: "error", message: "m" })).toBe("");
  });
});

describe("focusField", () => {
  it("falls back to the nearest ancestor that is on screen", () => {
    const root = document.createElement("div");
    root.innerHTML = '<div data-path="route/x"><div data-path="route/x/authz"></div></div>';
    document.body.append(root);
    expect(focusField(root, "route/x/authz/roles")?.dataset.path).toBe("route/x/authz");
    expect(focusField(root, "route/x/nothing")?.dataset.path).toBe("route/x");
    expect(focusField(root, "other/y")).toBeNull();
    root.remove();
  });
});
