import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api/client";
import type { Diagnostic, PreviewStatus, RecordedRequest } from "../api/types";
import { StudioProvider } from "../state/context";
import { createStudioStore } from "../state/store";
import { fakeApi } from "../test/fakeApi";
import { DEFAULT_LAYOUT, layoutStore } from "./layout";
import { PreviewPanel } from "./PreviewPanel";

const ready = (version = 1): PreviewStatus => ({ status: "ready", url: "/preview/d1/", version });
const at = (s: number) => new Date(Date.UTC(2026, 0, 1, 10, 0, s)).toISOString();
const requests: RecordedRequest[] = [
  { at: at(1), kind: "request", method: "GET", url: "/todos", status: 200, durationMs: 4.2, detail: { route: "web.todos_list", intent: "todo.list" } },
  { at: at(2), kind: "request", method: "GET", url: "/nope", status: 404, durationMs: 1 },
  { at: at(3), kind: "outbound", method: "POST", url: "smtp://mail.internal/send", detail: { channel: "smtp", preview: "x".repeat(300) } },
];

async function mount(over: Record<string, unknown> = {}) {
  const api = fakeApi({
    startPreview: vi.fn(async () => ready()),
    stopPreview: vi.fn(async () => undefined),
    previewRequests: vi.fn(async () => requests),
    ...over,
  });
  const store = createStudioStore(api, { batchDelay: 5, validateDelay: 5000 });
  await store.getState().init();
  render(
    <StudioProvider store={store}>
      <PreviewPanel />
    </StudioProvider>,
  );
  return { api, store };
}

beforeEach(() => layoutStore.setState({ ...DEFAULT_LAYOUT }));

describe("PreviewPanel", () => {
  it("builds the draft's preview and shows it in a frame", async () => {
    const { api } = await mount();
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Ready"));
    expect(api.startPreview).toHaveBeenCalledWith("d1");
    const frame = screen.getByTitle("Live preview") as HTMLIFrameElement;
    expect(frame.getAttribute("src")).toMatch(/\/preview\/d1\/$/);
    expect(screen.getByLabelText("Preview address")).toHaveValue("/");
  });

  it("follows SSE status events", async () => {
    const { api } = await mount({ startPreview: vi.fn(() => new Promise<PreviewStatus>(() => {})) });
    expect(screen.getByRole("status")).toHaveTextContent("Starting");
    act(() => api.emit({ type: "preview", data: { status: "ready", url: "/preview/d1/", version: 1 } }));
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Ready"));
    expect(screen.getByTitle("Live preview")).toBeInTheDocument();
  });

  it("lists failure diagnostics; clicking one opens the block in the editor", async () => {
    const err: Diagnostic = { severity: "error", message: "unknown intent", file: "04_routes.bcl", line: 3, path: "route/web.todos_list/intent" };
    const { store } = await mount({ startPreview: vi.fn(async (): Promise<PreviewStatus> => ({ status: "failed", error: [err] })) });
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("failed to build"));
    expect(screen.getByRole("alert")).toHaveTextContent("unknown intent");
    store.setState({ selection: null });
    await userEvent.click(screen.getByRole("button", { name: "04_routes.bcl:3" }));
    await waitFor(() => expect(store.getState().selection).toEqual({ file: "04_routes.bcl", path: "route/web.todos_list" }));
    expect(screen.queryByTitle("Live preview")).toBeNull(); // nothing good to show yet
  });

  it("explains a 501 instead of showing an empty frame", async () => {
    await mount({ startPreview: vi.fn(async () => { throw new ApiError(501, "not_implemented", "no preview manager"); }) });
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("not configured"));
    expect(screen.queryByTitle("Live preview")).toBeNull();
  });

  it("confines the address bar to the preview", async () => {
    await mount();
    await waitFor(() => expect(screen.getByTitle("Live preview")).toBeInTheDocument());
    const bar = screen.getByLabelText("Preview address");
    await userEvent.clear(bar);
    await userEvent.type(bar, "https://evil.example/{enter}");
    expect(screen.getByText(/outside the preview/i)).toBeInTheDocument();
    await userEvent.clear(bar);
    await userEvent.type(bar, "/todos{enter}");
    expect(screen.queryByText(/outside the preview/i)).toBeNull();
    expect(bar).toHaveValue("/todos");
  });

  it("device presets resize the frame and are remembered", async () => {
    await mount();
    await waitFor(() => expect(screen.getByTitle("Live preview")).toBeInTheDocument());
    await userEvent.click(screen.getByRole("button", { name: "Mobile" }));
    expect((screen.getByTitle("Live preview").parentElement as HTMLElement).style.width).toBe("390px");
    expect(layoutStore.getState().device).toBe("mobile");
    expect(JSON.parse(localStorage.getItem("studio.preview.layout")!).device).toBe("mobile");
  });

  it("docks to the bottom and resizes from the keyboard", async () => {
    await mount();
    await userEvent.click(screen.getByRole("button", { name: "Dock at the bottom" }));
    expect(layoutStore.getState().dock).toBe("bottom");
    const handle = screen.getByRole("separator", { name: "Resize preview" });
    expect(handle).toHaveAttribute("aria-orientation", "horizontal");
    const before = layoutStore.getState().sizeBottom;
    handle.focus();
    await userEvent.keyboard("{ArrowUp}");
    expect(layoutStore.getState().sizeBottom).toBe(before + 20);
  });
});

describe("request console", () => {
  async function openConsole(over: Record<string, unknown> = {}) {
    const m = await mount(over);
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Ready"));
    await userEvent.click(screen.getByRole("tab", { name: /Console/ }));
    return m;
  }

  it("shows requests and stubbed outbound calls, newest first", async () => {
    await openConsole();
    const table = await screen.findByRole("table");
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(3);
    expect(rows[0]).toHaveTextContent("smtp://mail.internal/send");
    expect(rows[2]).toHaveTextContent("web.todos_list");
    expect(rows[2]).toHaveTextContent("todo.list");
    expect(rows[2]).toHaveTextContent("200");
    expect(rows[2]).toHaveTextContent("4 ms");
  });

  it("truncates a long payload and expands it on demand", async () => {
    await openConsole();
    const btn = await screen.findByRole("button", { name: "Show all" });
    const pre = btn.parentElement!.querySelector("pre")!;
    expect(pre.textContent!.length).toBeLessThan(150);
    await userEvent.click(btn);
    expect(pre.textContent).toHaveLength(300);
    expect(screen.getByRole("button", { name: "Show less" })).toHaveAttribute("aria-expanded", "true");
  });

  it("filters by text, kind and errors only", async () => {
    await openConsole();
    await screen.findByRole("table");
    await userEvent.type(screen.getByLabelText("Filter requests"), "todos");
    expect(screen.getAllByRole("row")).toHaveLength(2);
    await userEvent.clear(screen.getByLabelText("Filter requests"));
    await userEvent.click(screen.getByRole("button", { name: "Outbound" }));
    expect(screen.getAllByRole("row")).toHaveLength(2);
    await userEvent.click(screen.getByRole("button", { name: "All" }));
    await userEvent.click(screen.getByLabelText("Errors only"));
    const rows = screen.getAllByRole("row").slice(1);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toHaveTextContent("/nope");
  });

  it("Clear hides what has been recorded so far", async () => {
    await openConsole();
    await screen.findByRole("table");
    await userEvent.click(screen.getByRole("button", { name: "Clear" }));
    expect(await screen.findByText(/No requests yet|Nothing matches/)).toBeInTheDocument();
  });

  it("clicking a request opens its route block in the editor", async () => {
    const { store } = await openConsole();
    store.setState({ selection: null });
    await userEvent.click(await screen.findByText("/todos"));
    await waitFor(() => expect(store.getState().selection).toEqual({ file: "04_routes.bcl", path: "route/web.todos_list" }));
  });

  it("says so when a request matches no route", async () => {
    const { store } = await openConsole();
    store.setState({ selection: null });
    await userEvent.click(await screen.findByText("/nope"));
    expect(await screen.findByText(/No route in this draft matches GET \/nope/)).toBeInTheDocument();
    expect(store.getState().selection).toBeNull();
  });

  it("polls while visible and stops when paused", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const { api } = await openConsole();
      await screen.findByRole("table");
      const n = vi.mocked(api.previewRequests).mock.calls.length;
      await act(async () => { await vi.advanceTimersByTimeAsync(2100); });
      expect(vi.mocked(api.previewRequests).mock.calls.length).toBeGreaterThan(n);
      await userEvent.click(screen.getByLabelText("Live"));
      const m = vi.mocked(api.previewRequests).mock.calls.length;
      await act(async () => { await vi.advanceTimersByTimeAsync(6000); });
      expect(vi.mocked(api.previewRequests).mock.calls.length).toBe(m);
    } finally {
      vi.useRealTimers();
    }
  });

  it("shows the server's message when the console is unavailable", async () => {
    await openConsole({ previewRequests: vi.fn(async () => { throw new ApiError(501, "not_implemented", "x"); }) });
    expect(await screen.findByText(/not configured/i)).toBeInTheDocument();
  });
});
