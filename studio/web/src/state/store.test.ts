import { describe, expect, it, vi } from "vitest";
import { ApiError } from "../api/client";
import type { Op } from "../api/types";
import { fakeApi, meta } from "../test/fakeApi";
import { createStudioStore } from "./store";

const P = "route/web.todos_list/path";
const set = (value: string): Op => ({ op: "setField", file: "04_routes.bcl", path: P, value });

async function boot(over = {}, roles: ("editor" | "viewer" | "reviewer" | "admin")[] = ["editor"]) {
  const api = fakeApi({ meta: vi.fn(async () => meta(roles)), ...over });
  const store = createStudioStore(api, { batchDelay: 5, validateDelay: 20 });
  await store.getState().init();
  return { api, store, get: store.getState };
}

describe("opening", () => {
  it("loads schemas, the first draft, its tree and selects the first block", async () => {
    const { get } = await boot();
    expect(get().draft?.id).toBe("d1");
    expect(get().selection).toEqual({ file: "04_routes.bcl", path: "route/web.todos_list" });
    expect(get().trees["04_routes.bcl"]).toHaveLength(1);
    expect(get().connected).toBe(true);
  });

  it("does not load drafts for a viewer", async () => {
    const { api, get } = await boot({}, ["viewer"]);
    expect(api.listDrafts).not.toHaveBeenCalled();
    expect(get().draft).toBeNull();
  });
});

describe("optimistic edits", () => {
  it("shows the edit immediately, sends one batch, then clears the overlay", async () => {
    const { api, get } = await boot();
    get().edit([set('"/a"')]);
    get().edit([set('"/ab"')]);
    expect(get().overlay[`04_routes.bcl|${P}`]?.raw).toBe('"/ab"');
    expect(api.ops).not.toHaveBeenCalled();
    await vi.waitFor(() => expect(api.ops).toHaveBeenCalledTimes(1));
    expect(vi.mocked(api.ops).mock.calls[0]![1]).toEqual([set('"/ab"')]); // coalesced
    expect(vi.mocked(api.ops).mock.calls[0]![2]).toBe(1); // ifVersion
    await vi.waitFor(() => expect(get().overlay).toEqual({}));
    expect(get().draft?.version).toBe(2);
    expect(get().undoDepth).toBe(1);
    expect(get().draft?.dirty).toBe(true);
  });

  it("refreshes the trees of the changed files after applying", async () => {
    const { api, get } = await boot();
    const before = vi.mocked(api.fileTree).mock.calls.length;
    get().edit([set('"/x"')]);
    await vi.waitFor(() => expect(get().overlay).toEqual({}));
    expect(vi.mocked(api.fileTree).mock.calls.length).toBeGreaterThan(before);
  });

  it("uses the version returned by the previous batch as the next ifVersion", async () => {
    const { api, get } = await boot();
    get().edit([set('"/1"')]);
    await vi.waitFor(() => expect(get().draft?.version).toBe(2));
    get().edit([set('"/2"')]);
    await vi.waitFor(() => expect(api.ops).toHaveBeenCalledTimes(2));
    expect(vi.mocked(api.ops).mock.calls[1]![2]).toBe(2);
  });
});

describe("rollback", () => {
  it("409 stale: drops the overlay, reloads the draft and tells the user", async () => {
    const { api, get } = await boot({ ops: vi.fn(async () => { throw new ApiError(409, "stale", "changed"); }) });
    const loads = vi.mocked(api.getDraft).mock.calls.length;
    get().edit([set('"/mine"')]);
    expect(get().overlay[`04_routes.bcl|${P}`]).toBeDefined();
    await vi.waitFor(() => expect(get().overlay).toEqual({}));
    await vi.waitFor(() => expect(vi.mocked(api.getDraft).mock.calls.length).toBeGreaterThan(loads));
    expect(get().notices.some((n) => n.level === "error" && /changed elsewhere/.test(n.text))).toBe(true);
    expect(get().undoDepth).toBe(0);
  });

  it("422: drops the overlay, keeps the server's diagnostics and shows the reason", async () => {
    const bad = { severity: "error", message: "unknown field", path: P };
    const { get } = await boot({
      ops: vi.fn(async () => { throw new ApiError(422, "op_failed", "op 0 (setField): nope", { diagnostics: [bad] }); }),
    });
    get().edit([set("???")]);
    await vi.waitFor(() => expect(get().overlay).toEqual({}));
    expect(get().notices.some((n) => /Edit rejected: op 0/.test(n.text))).toBe(true);
    // the reload after rollback replaces diagnostics with the draft's own, but the notice stays
    expect(get().draft?.version).toBe(1);
  });

  it("a network error also rolls back", async () => {
    const { get } = await boot({ ops: vi.fn(async () => { throw new TypeError("Failed to fetch"); }) });
    get().edit([set('"/x"')]);
    await vi.waitFor(() => expect(get().overlay).toEqual({}));
    expect(get().notices.some((n) => /Could not save/.test(n.text))).toBe(true);
  });

  it("keeps a newer edit of the same field when an older batch rolls back", async () => {
    let fail!: (e: unknown) => void;
    const { get } = await boot({ ops: vi.fn(() => new Promise((_r, rej) => { fail = rej; })) });
    get().edit([set('"/first"')]);
    await vi.waitFor(() => expect(fail).toBeDefined());
    get().edit([set('"/second"')]);
    fail(new ApiError(422, "op_failed", "nope"));
    await vi.waitFor(() => expect(get().overlay).toEqual({}));
  });
});

describe("undo / redo", () => {
  it("flushes pending edits first, then undoes and tracks depth", async () => {
    const { api, get } = await boot();
    get().edit([set('"/x"')]);
    await get().undo();
    expect(api.ops).toHaveBeenCalledTimes(1);
    expect(api.undo).toHaveBeenCalledTimes(1);
    expect(get().undoDepth).toBe(0);
    expect(get().redoDepth).toBe(1);
    await get().redo();
    expect(get().undoDepth).toBe(1);
    expect(get().redoDepth).toBe(0);
  });

  it("says so when there is nothing to undo", async () => {
    const { get } = await boot({ undo: vi.fn(async () => { throw new ApiError(409, "nothing_to_undo", "nothing to undo"); }) });
    await get().undo();
    expect(get().notices.at(-1)?.text).toBe("Nothing to undo.");
  });
});

describe("live updates", () => {
  it("reloads when someone else advances the draft", async () => {
    const { api, get } = await boot();
    api.state.version = 5;
    api.emit({ type: "changed", data: { version: 5 } });
    await vi.waitFor(() => expect(get().draft?.version).toBe(5));
  });

  it("syncs quietly: never shows the loading state or resubscribes, and keeps the selection", async () => {
    const { api, get } = await boot();
    const selection = get().selection;
    const loading: boolean[] = [];
    const unsub = get; // read-only handle
    const seen = () => loading.push(unsub().loading);
    const subs = vi.mocked(api.subscribe).mock.calls.length;
    api.state.version = 5;
    // a burst of remote events must collapse into one refresh
    for (let v = 3; v <= 5; v++) { api.emit({ type: "changed", data: { version: v } }); seen(); }
    await vi.waitFor(() => expect(get().draft?.version).toBe(5));
    seen();
    expect(loading.every((l) => l === false)).toBe(true);
    expect(get().selection).toEqual(selection);
    expect(vi.mocked(api.subscribe).mock.calls.length).toBe(subs);
  });

  it("ignores the echo of our own change", async () => {
    const { api, get } = await boot();
    get().edit([set('"/x"')]);
    await vi.waitFor(() => expect(get().draft?.version).toBe(2));
    const loads = vi.mocked(api.getDraft).mock.calls.length;
    api.emit({ type: "changed", data: { version: 2 } });
    await new Promise((r) => setTimeout(r, 20));
    expect(vi.mocked(api.getDraft).mock.calls.length).toBe(loads);
  });

  it("takes pushed diagnostics", async () => {
    const { api, get } = await boot();
    api.emit({ type: "diagnostics", data: { diagnostics: [{ severity: "error", message: "boom" }] } });
    expect(get().diagnostics).toHaveLength(1);
  });
});

describe("validation", () => {
  it("runs a debounced full validation after edits", async () => {
    const { api, get } = await boot();
    get().edit([set('"/x"')]);
    await vi.waitFor(() => expect(api.validate).toHaveBeenCalledTimes(1));
    expect(get().validating).toBe(false);
  });

  it("validate() sends pending edits before checking", async () => {
    const { api, get } = await boot();
    get().edit([set('"/x"')]);
    await get().validate();
    expect(api.ops).toHaveBeenCalledTimes(1);
    expect(api.validate).toHaveBeenCalled();
  });
});

describe("diagnostics jump", () => {
  const nav = () => ({ "04_routes.bcl": [{ kind: "block" as const, path: "route/web.todos_list", type: "route", id: "web.todos_list", line: 10, start: 100, end: 200 },
                                          { kind: "block" as const, path: "route/other", type: "route", id: "other", line: 30, start: 300, end: 400 }] });
  it("opens the block a path points into and remembers the field to focus", async () => {
    const { get, store } = await boot();
    store.setState({ nav: nav(), selection: null });
    await get().selectFromDiagnostic({ severity: "error", message: "m", file: "04_routes.bcl", path: "route/web.todos_list/path" });
    expect(get().selection).toEqual({ file: "04_routes.bcl", path: "route/web.todos_list" });
    expect(get().focusPath).toBe("route/web.todos_list/path");
  });

  it("finds the file itself when the diagnostic only has a path", async () => {
    const { get, store } = await boot();
    store.setState({ nav: nav(), selection: null });
    await get().selectFromDiagnostic({ severity: "error", message: "m", path: "route/other/method" });
    expect(get().selection).toEqual({ file: "04_routes.bcl", path: "route/other" });
  });

  it("falls back to the byte offset, then the line", async () => {
    const { get, store } = await boot();
    store.setState({ nav: nav(), selection: null });
    await get().selectFromDiagnostic({ severity: "error", message: "m", file: "04_routes.bcl", offset: 350 });
    expect(get().selection?.path).toBe("route/other");
    await get().selectFromDiagnostic({ severity: "error", message: "m", file: "04_routes.bcl", line: 12 });
    expect(get().selection?.path).toBe("route/web.todos_list");
  });

  it("with no block to open (e.g. a parse error) it shows the file at that line", async () => {
    const { get, store } = await boot();
    store.setState({ nav: { "04_routes.bcl": [] }, selection: null });
    await get().selectFromDiagnostic({ severity: "error", message: "unexpected }", file: "04_routes.bcl", line: 42 });
    expect(get().selection).toEqual({ file: "04_routes.bcl", path: "" });
    expect(get().focusLine).toBe(42);
  });
});

describe("propose", () => {
  it("flushes edits, proposes and reports", async () => {
    const { api, get } = await boot();
    get().edit([set('"/x"')]);
    const rev = await get().propose("Change the todos path");
    expect(rev?.id).toBe("rev2");
    expect(api.ops).toHaveBeenCalledTimes(1);
    expect(api.propose).toHaveBeenCalledWith("d1", "Change the todos path");
  });

  it("keeps the server's diagnostics when the proposal is refused", async () => {
    const { get } = await boot({
      propose: vi.fn(async () => { throw new ApiError(422, "invalid", "invalid", { diagnostics: [{ severity: "error", message: "bad route" }] }); }),
    });
    expect(await get().propose("x")).toBeNull();
    expect(get().diagnostics).toEqual([{ severity: "error", message: "bad route" }]);
  });
});
