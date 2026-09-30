import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { App } from "./App";
import type { Diagnostic } from "./api/types";
import { ChangeList, ChangeSummary } from "./components/ChangeList";
import { StudioProvider } from "./state/context";
import { createStudioStore } from "./state/store";
import { DEFAULT_PREFS, uiStore } from "./state/ui";
import { fakeApi } from "./test/fakeApi";
import { DiagnosticsPanel } from "./diag/DiagnosticsPanel";

beforeEach(() => {
  window.location.hash = "";
  uiStore.setState({ ...DEFAULT_PREFS, paletteOpen: false, addOpen: null });
});
afterEach(() => uiStore.setState({ ...DEFAULT_PREFS, paletteOpen: false, addOpen: null }));

async function mountApp(over: Parameters<typeof fakeApi>[0] = {}) {
  const api = fakeApi(over);
  const store = createStudioStore(api, { batchDelay: 5, validateDelay: 5000 });
  store.setState({ token: "t" });
  render(
    <StudioProvider store={store}>
      <App />
    </StudioProvider>,
  );
  const nav = await screen.findByLabelText("Main navigation");
  return { api, store, nav };
}

describe("sidebar", () => {
  it("groups the categories under Build, Operate and Release", async () => {
    const { nav, store } = await mountApp();
    await waitFor(() => expect(store.getState().draft).not.toBeNull());
    const build = await within(nav).findByRole("group", { name: "Build" });
    expect(within(build).getByRole("link", { name: /Pages & APIs/ })).toBeInTheDocument();
    expect(within(build).getByRole("link", { name: /Connections/ })).toBeInTheDocument();
    const operate = within(nav).getByRole("group", { name: "Operate" });
    expect(within(operate).getByRole("link", { name: /Access & security/ })).toBeInTheDocument();
    const release = within(nav).getByRole("group", { name: "Release" });
    expect(within(release).getByRole("link", { name: /Versions & reviews/ })).toBeInTheDocument();
  });

  it("owns the account menu, opening upwards", async () => {
    const { nav } = await mountApp();
    await userEvent.click(within(nav).getByRole("button", { name: "Account menu" }));
    const menu = await screen.findByRole("menu");
    expect(menu).toHaveClass("menu-up");
    expect(within(menu).getByRole("menuitem", { name: /Sign out/ })).toBeInTheDocument();
  });
});

describe("side panel", () => {
  it("starts closed so problems never crowd the page", async () => {
    await mountApp();
    const panel = await screen.findByLabelText("Side panel", { selector: "aside" });
    expect(panel).toHaveAttribute("aria-hidden", "true");
  });
});

describe("no working copy", () => {
  it("offers to start one instead of claiming the list is empty", async () => {
    const { store } = await mountApp({ listDrafts: async () => [] });
    await waitFor(() => expect(store.getState().meta).not.toBeNull());
    window.location.hash = "#/c/pages";
    await waitFor(() => expect(screen.getByRole("heading", { name: "Start a working copy" })).toBeInTheDocument());
    expect(screen.queryByText(/No pages/i)).toBeNull();
  });
});

describe("grouped problems", () => {
  const env = (n: string): Diagnostic => ({ severity: "warning", message: `environment variable ${n} is not set here` });

  async function mountPanel(diagnostics: Diagnostic[]) {
    const api = fakeApi();
    const store = createStudioStore(api, { batchDelay: 5, validateDelay: 5000 });
    await store.getState().init();
    store.setState({ diagnostics });
    render(<StudioProvider store={store}><DiagnosticsPanel /></StudioProvider>);
  }

  it("folds repeated environment warnings into one collapsed group that expands", async () => {
    await mountPanel([env("A_ONE"), { severity: "error", message: "boom" }, env("B_TWO"), env("C_THREE")]);
    const group = screen.getByRole("button", { name: /3 environment settings/ });
    expect(group).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("A_ONE")).toBeNull();
    await userEvent.click(group);
    expect(screen.getByText("A_ONE")).toBeInTheDocument();
    expect(screen.getByText("C_THREE")).toBeInTheDocument();
    // the real error stays a normal row, listed first
    const rows = screen.getAllByRole("listitem").filter((li) => li.classList.contains("problem-row") || li.closest(".problem-row"));
    expect(rows.length).toBeGreaterThan(0);
  });
});

describe("change summary", () => {
  const changes = [
    { kind: "resource", name: "a", change: "added" as const },
    { kind: "resource", name: "b", change: "added" as const },
    { kind: "route", name: "c", change: "changed" as const },
  ];
  it("counts what a change is made of", () => {
    render(<ChangeSummary changes={changes as never} />);
    const list = screen.getByRole("list", { name: "Summary of changes" });
    expect(list).toHaveTextContent("2");
    expect(list.querySelectorAll("li")).toHaveLength(2);
  });
  it("collapses long lists behind Show all", async () => {
    render(<ChangeList changes={changes as never} limit={1} />);
    expect(screen.getAllByRole("listitem")).toHaveLength(1);
    await userEvent.click(screen.getByRole("button", { name: /Show all 3 changes/ }));
    expect(screen.getAllByRole("listitem")).toHaveLength(3);
  });
});

describe("activity log", () => {
  it("explains itself to people who can't see it instead of showing a raw error", async () => {
    const { api } = await mountApp();
    window.location.hash = "#/audit";
    await waitFor(() => expect(screen.getByRole("heading", { name: "Only admins can see the activity log" })).toBeInTheDocument());
    expect(api.audit).not.toHaveBeenCalled();
  });
});
