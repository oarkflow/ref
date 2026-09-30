import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { StudioProvider } from "../state/context";
import { uiStore } from "../state/ui";
import { createStudioStore, type StudioStore } from "../state/store";
import { detail, fakeApi, meta } from "../test/fakeApi";
import { catalog, schemas } from "../test/helpers";
import { FULL, withUnresolved } from "./fixtures";
import { JourneysPage } from "./JourneysPage";
import type { FlowGraph } from "./types";

beforeAll(() => {
  class RO { observe() {} unobserve() {} disconnect() {} }
  Object.assign(globalThis, { ResizeObserver: RO });
  class Matrix { m22 = 1; constructor(_?: string) {} }
  Object.assign(globalThis, { DOMMatrixReadOnly: Matrix });
  Element.prototype.scrollIntoView = () => {};
});
beforeEach(() => localStorage.clear());

function setup(graph: FlowGraph | (() => Promise<FlowGraph>), opts: { draft?: boolean; roles?: ("viewer" | "editor")[]; at?: string } = {}) {
  const flows = vi.fn(typeof graph === "function" ? graph : async () => graph);
  const store: StudioStore = createStudioStore(fakeApi({ flows }));
  store.setState({
    meta: meta(opts.roles ?? ["editor"]), schemas, catalog,
    draft: opts.draft === false ? null : detail({ id: "d1", files: ["04_routes.bcl"] }), nav: {}, trees: {}, diagnostics: [],
  });
  const ui = render(
    <StudioProvider store={store}>
      <MemoryRouter initialEntries={[opts.at ?? "/journeys"]}>
        <Routes><Route path="/journeys" element={<JourneysPage />} /><Route path="*" element={<div data-testid="elsewhere" />} /></Routes>
      </MemoryRouter>
    </StudioProvider>,
  );
  return { store, flows, ...ui };
}

const card = (id: string) => document.querySelector(`[data-card="${id}"]`) as HTMLElement;
const row = (label: string) => screen.getByRole("button", { name: new RegExp(`^(Link|Form|Button|Script call): ${label}`) });
const focusIds = () => (document.querySelector(".journeys-page")!.getAttribute("data-flow-focus") ?? "").split(",").filter(Boolean);

describe("JourneysPage", () => {
  it("draws the pages of the app with what you can do on each", async () => {
    setup(FULL);
    expect(await screen.findByText("New todo", { selector: ".cv-titles strong" })).toBeInTheDocument();
    expect(card("page:pages/todos/list")).toHaveClass("tone-flow", "jn-page");
    expect(within(card("page:pages/todos/new")).getByText("Save draft")).toBeInTheDocument();
    expect(within(card("page:pages/todos/new")).getByText("GET /todos/new")).toBeInTheDocument();
    expect(card("route:web.todos_create")).toHaveClass("tone-plug");
    expect(card("intent:todo.create")).toHaveClass("tone-process");
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("User journeys");
  });

  it("starts with every line still and the requests nobody on a page reaches left out", async () => {
    setup(FULL);
    await screen.findByText("New todo", { selector: ".cv-titles strong" });
    expect(focusIds()).toEqual([]);
    expect(card("route:orders.create")).toBeNull();
    expect(screen.getByText(/Also show \d+ endpoints no page uses/)).toBeInTheDocument();
  });

  it("selecting a button lights up only its lines, and opens what it does", async () => {
    setup(FULL);
    await screen.findByText("New todo", { selector: ".cv-titles strong" });
    fireEvent.click(row("Save draft"));
    await waitFor(() => expect(focusIds().length).toBeGreaterThan(0));
    const ids = focusIds();
    expect(ids.every((id) => id.includes("todos/new") || id.includes("todos_create"))).toBe(true);
    expect(ids.some((id) => id.startsWith("calls:"))).toBe(true);
    expect(row("Save draft").closest("li")).toHaveClass("selected");
    const drawer = screen.getByRole("complementary", { name: "Details" });
    expect(within(drawer).getByText("Sends POST /todos.")).toBeInTheDocument();
    expect(within(drawer).getByText(/templates\/pages\/todos\/new\.html:\d+/)).toBeInTheDocument();
    expect(within(drawer).getByRole("button", { name: /Open at line \d+/ })).toBeInTheDocument();
    expect(within(drawer).getByText("What happens next")).toBeInTheDocument();
  });

  it("selecting a request shows every line that touches it, and Esc puts it all back to rest", async () => {
    setup(FULL);
    await screen.findByText("New todo", { selector: ".cv-titles strong" });
    fireEvent.click(card("route:web.todos_create"));
    await waitFor(() => expect(focusIds().length).toBeGreaterThanOrEqual(2));
    expect(focusIds().some((id) => id.startsWith("runs:"))).toBe(true);
    fireEvent.keyDown(window, { key: "Escape" });
    await waitFor(() => expect(focusIds()).toEqual([]));
  });

  it("stops the movement when 'Show data flow' is off", async () => {
    setup(FULL);
    await screen.findByText("New todo", { selector: ".cv-titles strong" });
    fireEvent.click(screen.getByRole("button", { name: "Show data flow" }));
    fireEvent.click(card("route:web.todos_create"));
    await waitFor(() => expect(screen.getByRole("complementary", { name: "Details" })).toHaveAttribute("aria-hidden", "false"));
    expect(focusIds()).toEqual([]);
  });

  it("searches, dimming what does not match", async () => {
    setup(FULL);
    await screen.findByText("New todo", { selector: ".cv-titles strong" });
    fireEvent.change(screen.getByRole("searchbox", { name: "Find on the map" }), { target: { value: "todos/new" } });
    await waitFor(() => expect(card("route:web.todos_new")).toHaveClass("jn-hit"));
    expect(card("page:pages/auth/login")).toHaveClass("jn-dim");
  });

  it("shows the endpoints no page uses when asked, and remembers it", async () => {
    const { unmount } = setup(FULL);
    await screen.findByText("New todo", { selector: ".cv-titles strong" });
    fireEvent.click(screen.getByRole("button", { name: /Also show \d+ endpoints no page uses/ }));
    await waitFor(() => expect(card("route:orders.create")).not.toBeNull());
    unmount();
    setup(FULL);
    await screen.findByText("New todo", { selector: ".cv-titles strong" });
    expect(card("route:orders.create")).not.toBeNull();
  });

  it("shows a call nothing answers as a red card you can fix", async () => {
    setup(withUnresolved());
    const gone = await waitFor(() => { const c = card("unresolved:dead0001"); expect(c).not.toBeNull(); return c; });
    expect(gone).toHaveClass("jn-unresolved", "has-error");
    expect(within(gone).getByText("Not found")).toBeInTheDocument();
    fireEvent.click(within(gone).getByRole("button", { name: /Fix/ }));
    await waitFor(() => expect(screen.getByTestId("elsewhere")).toBeInTheDocument());
  });

  it("'only problems' on a healthy app says so", async () => {
    setup(FULL);
    await screen.findByText("New todo", { selector: ".cv-titles strong" });
    fireEvent.click(screen.getByRole("button", { name: "What to show" }));
    fireEvent.click(screen.getByLabelText(/Only show problems/));
    expect(await screen.findByText("No problems")).toBeInTheDocument();
  });

  it("explains the notes about the map in plain words", async () => {
    setup(FULL);
    await screen.findByText("New todo", { selector: ".cv-titles strong" });
    fireEvent.click(screen.getByRole("button", { name: /Notes about this map/ }));
    const dlg = screen.getByRole("dialog", { name: "Notes about this map" });
    expect(within(dlg).getByText(/worked out while the app runs/)).toBeInTheDocument();
  });

  it("steps through what happens when someone clicks", async () => {
    setup(FULL);
    await screen.findByText("New todo", { selector: ".cv-titles strong" });
    fireEvent.click(card("page:pages/todos/new"));
    fireEvent.click(screen.getByRole("button", { name: /Follow the user/ }));
    const trace = screen.getByRole("region", { name: "Follow the user" });
    expect(within(trace).getByLabelText("Page to start on")).toHaveValue("page:pages/todos/new");
    expect(within(trace).getByText("You are on “New todo”.")).toBeInTheDocument();
    fireEvent.click(within(trace).getByRole("button", { name: "Next" }));
    expect(within(trace).getByText("Submit the form “Save draft”.")).toBeInTheDocument();
    fireEvent.click(within(trace).getByRole("button", { name: "Next" }));
    await waitFor(() => expect(focusIds().length).toBe(1));
    expect(within(trace).getByText(/The app receives POST \/todos\./)).toBeInTheDocument();
    fireEvent.click(within(trace).getByRole("button", { name: "Close" }));
    await waitFor(() => expect(screen.queryByRole("region", { name: "Follow the user" })).toBeNull());
  });
});

describe("Developer view", () => {
  it("shows the raw ids instead of the plain type line", async () => {
    uiStore.getState().set({ devView: true });
    try {
      setup(FULL);
      await screen.findByText("New todo", { selector: ".cv-titles strong" });
      expect(within(card("page:pages/todos/new")).getByText("page:pages/todos/new")).toBeInTheDocument();
      expect(within(card("route:web.todos_create")).getByText("route:web.todos_create")).toBeInTheDocument();
      expect(within(card("route:web.todos_create")).getByText("web.todos_create")).toBeInTheDocument();
    } finally {
      uiStore.getState().set({ devView: false });
    }
  });
});

describe("JourneysPage states", () => {
  it("asks for a working copy when there is none", async () => {
    setup(FULL, { draft: false });
    expect(await screen.findByText(/Start a working copy/)).toBeInTheDocument();
  });

  it("shows a placeholder while the first map loads", async () => {
    let done: (g: FlowGraph) => void = () => {};
    setup(() => new Promise<FlowGraph>((r) => { done = r; }));
    expect(screen.getByLabelText("Loading the map")).toBeInTheDocument();
    await act(async () => done(FULL));
    expect(await screen.findByText("New todo", { selector: ".cv-titles strong" })).toBeInTheDocument();
  });

  it("says what went wrong and offers to try again", async () => {
    const flows = vi.fn().mockRejectedValueOnce(new Error("boom")).mockResolvedValue(FULL);
    setup(flows);
    expect(await screen.findByRole("alert")).toHaveTextContent("boom");
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("New todo", { selector: ".cv-titles strong" })).toBeInTheDocument();
  });

  it("says when there are no pages yet", async () => {
    setup({ ...FULL, nodes: [], edges: [], warnings: [] });
    expect(await screen.findByText("No pages yet")).toBeInTheDocument();
  });

  it("asks the server for one journey, reaching one line further from a page", async () => {
    const { flows } = setup(FULL, { at: "/journeys?focus=page%3Apages%2Ftodos%2Fnew&depth=2" });
    await waitFor(() => expect(flows).toHaveBeenCalled());
    expect(flows).toHaveBeenCalledWith("d1", { focus: "page:pages/todos/new", depth: 3 });
  });
});
