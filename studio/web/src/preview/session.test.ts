import { describe, expect, it, vi } from "vitest";
import { ApiError } from "../api/client";
import type { PreviewStatus } from "../api/types";
import { PreviewSession, type SessionTimers } from "./session";

/** Manual clock: nothing fires until `tick` says so. */
function clock() {
  let now = 0;
  let nextId = 1;
  const pending = new Map<number, { at: number; fn: () => void }>();
  const timers: SessionTimers = {
    set(fn, ms) {
      const id = nextId++;
      pending.set(id, { at: now + ms, fn });
      return id;
    },
    clear(h) {
      pending.delete(h as number);
    },
  };
  return {
    timers,
    pending: () => pending.size,
    async tick(ms: number) {
      const end = now + ms;
      for (;;) {
        const due = [...pending.entries()].filter(([, t]) => t.at <= end).sort((a, b) => a[1].at - b[1].at)[0];
        if (!due) break;
        pending.delete(due[0]);
        now = due[1].at;
        due[1].fn();
        await flush();
      }
      now = end;
    },
  };
}
const flush = async () => { for (let i = 0; i < 5; i++) await Promise.resolve(); };

const ready = (version: number, url = "/preview/d1/"): PreviewStatus => ({ status: "ready", url, version });
const failed = (msg = "boom"): PreviewStatus => ({ status: "failed", error: [{ severity: "error", message: msg, file: "04_routes.bcl", line: 3 }] });

/** A startPreview whose answers the test releases by hand. */
function deferredApi() {
  const calls: { resolve(s: PreviewStatus): void; reject(e: unknown): void }[] = [];
  const startPreview = vi.fn(() => new Promise<PreviewStatus>((resolve, reject) => calls.push({ resolve, reject })));
  return { api: { startPreview }, calls, startPreview };
}

function make(api: { startPreview: () => Promise<PreviewStatus> }, version = 1) {
  const c = clock();
  const s = new PreviewSession(api, "d1", version, { buildDebounceMs: 400, reloadDebounceMs: 100, timers: c.timers });
  return { s, c };
}

describe("PreviewSession building", () => {
  it("starts immediately, then reports ready without reloading the fresh frame", async () => {
    const { api, calls } = deferredApi();
    const { s, c } = make(api);
    s.start();
    expect(s.getState().phase).toBe("starting");
    calls[0]!.resolve(ready(1));
    await flush();
    expect(s.getState()).toMatchObject({ phase: "ready", url: "/preview/d1/", builtVersion: 1, stale: false, reloadKey: 0 });
    await c.tick(1000);
    expect(s.getState().reloadKey).toBe(0); // the frame mounted on the new build, no double load
  });

  it("debounces a burst of edits into one build", async () => {
    const { api, calls, startPreview } = deferredApi();
    const { s, c } = make(api);
    s.start();
    calls[0]!.resolve(ready(1));
    await flush();
    s.setDraftVersion(2);
    await c.tick(200);
    s.setDraftVersion(3);
    await c.tick(200);
    s.setDraftVersion(4);
    await c.tick(399);
    expect(startPreview).toHaveBeenCalledTimes(1); // still quiet
    await c.tick(2);
    expect(startPreview).toHaveBeenCalledTimes(2);
    expect(s.getState().stale).toBe(true);
  });

  it("serialises builds: edits during a build cause one follow-up, not one each", async () => {
    const { api, calls, startPreview } = deferredApi();
    const { s, c } = make(api);
    s.start();
    calls[0]!.resolve(ready(1));
    await flush();
    s.setDraftVersion(2);
    await c.tick(400); // build for v2 in flight
    expect(startPreview).toHaveBeenCalledTimes(2);
    s.setDraftVersion(3);
    await c.tick(400); // debounce fires while v2 still building
    s.setDraftVersion(4);
    await c.tick(400);
    expect(startPreview).toHaveBeenCalledTimes(2); // never two at once
    calls[1]!.resolve(ready(2));
    await flush();
    await c.tick(0);
    expect(startPreview).toHaveBeenCalledTimes(3); // exactly one follow-up
    calls[2]!.resolve(ready(4));
    await flush();
    expect(s.getState().builtVersion).toBe(4);
    expect(startPreview).toHaveBeenCalledTimes(3);
  });
});

describe("PreviewSession reload ordering", () => {
  it("reloads once per newer ready build, after a debounce", async () => {
    const { api, calls } = deferredApi();
    const { s, c } = make(api);
    s.start();
    calls[0]!.resolve(ready(1));
    await flush();
    s.setDraftVersion(2);
    await c.tick(400);
    calls[1]!.resolve(ready(2));
    await flush();
    expect(s.getState().reloadKey).toBe(0); // debounced
    await c.tick(100);
    expect(s.getState().reloadKey).toBe(1);
    // The same build announced again over SSE does not reload again.
    s.onEvent({ status: "ready", url: "/preview/d1/", version: 2 });
    await c.tick(500);
    expect(s.getState().reloadKey).toBe(1);
  });

  it("collapses ready events that land inside the reload debounce", async () => {
    const { api, calls } = deferredApi();
    const { s, c } = make(api);
    s.start();
    calls[0]!.resolve(ready(1));
    await flush();
    s.onEvent({ status: "ready", url: "/preview/d1/", version: 2 });
    await c.tick(50);
    s.onEvent({ status: "ready", url: "/preview/d1/", version: 3 });
    await c.tick(500);
    expect(s.getState().reloadKey).toBe(1);
    expect(s.getState().builtVersion).toBe(3);
  });

  it("ignores a late answer for an older build", async () => {
    const { api, calls } = deferredApi();
    const { s, c } = make(api);
    s.start();
    calls[0]!.resolve(ready(3));
    await flush();
    s.onEvent({ status: "ready", url: "/preview/d1/", version: 2 }); // arrives late
    await c.tick(500);
    expect(s.getState().builtVersion).toBe(3);
    expect(s.getState().reloadKey).toBe(0);
  });

  it("a slow POST answer cannot undo a newer SSE event", async () => {
    const { api, calls } = deferredApi();
    const { s, c } = make(api);
    s.start();
    calls[0]!.resolve(ready(1));
    await flush();
    s.setDraftVersion(2);
    await c.tick(400); // POST for v2 in flight
    s.onEvent({ status: "ready", url: "/preview/d1/", version: 3 }); // a newer build lands over SSE
    await c.tick(100);
    expect(s.getState()).toMatchObject({ builtVersion: 3, reloadKey: 1 });
    calls[1]!.resolve({ status: "ready", url: "/preview/d1/" }); // answers for the v2 it was asked about
    await flush();
    await c.tick(500);
    expect(s.getState()).toMatchObject({ builtVersion: 3, reloadKey: 1 });
  });

  it("uses the requested version when a server sends none", async () => {
    const { api, calls } = deferredApi();
    const { s } = make(api, 5);
    s.start();
    calls[0]!.resolve({ status: "ready", url: "/preview/d1/" });
    await flush();
    expect(s.getState().builtVersion).toBe(5);
  });
});

describe("PreviewSession failure transitions", () => {
  it("failed on the first build has no frame to fall back on", async () => {
    const { api, calls } = deferredApi();
    const { s } = make(api);
    s.start();
    calls[0]!.resolve(failed());
    await flush();
    expect(s.getState()).toMatchObject({ phase: "failed", url: null, showingLastGood: false });
    expect(s.getState().errors[0]).toMatchObject({ file: "04_routes.bcl", line: 3 });
  });

  it("failed after a good build keeps the last good url, then failed -> ready reloads", async () => {
    const { api, calls } = deferredApi();
    const { s, c } = make(api);
    s.start();
    calls[0]!.resolve(ready(1));
    await flush();

    s.setDraftVersion(2);
    await c.tick(400);
    calls[1]!.resolve(failed("bad route"));
    await flush();
    expect(s.getState()).toMatchObject({ phase: "failed", url: "/preview/d1/", builtVersion: 1, showingLastGood: true, stale: true });
    await c.tick(500);
    expect(s.getState().reloadKey).toBe(0); // a failed build never reloads the frame

    s.setDraftVersion(3);
    await c.tick(400);
    calls[2]!.resolve(ready(3));
    await flush();
    await c.tick(100);
    expect(s.getState()).toMatchObject({ phase: "ready", builtVersion: 3, errors: [], showingLastGood: false, stale: false, reloadKey: 1 });
  });

  it("a network error becomes a failure with a message", async () => {
    const { api, calls } = deferredApi();
    const { s } = make(api);
    s.start();
    calls[0]!.reject(new Error("network down"));
    await flush();
    expect(s.getState().phase).toBe("failed");
    expect(s.getState().errors[0]!.message).toBe("network down");
  });

  it("501 means preview is not configured and stops rebuilding", async () => {
    const { api, calls, startPreview } = deferredApi();
    const { s, c } = make(api);
    s.start();
    calls[0]!.reject(new ApiError(501, "not_implemented", "no preview manager"));
    await flush();
    expect(s.getState().phase).toBe("unavailable");
    expect(s.getState().message).toMatch(/not configured/i);
    s.setDraftVersion(2);
    await c.tick(1000);
    expect(startPreview).toHaveBeenCalledTimes(1);
  });

  it("an SSE 'stopped' resets the session; an SSE 'failed' after ready keeps the frame", async () => {
    const { api, calls } = deferredApi();
    const { s } = make(api);
    s.start();
    calls[0]!.resolve(ready(1));
    await flush();
    s.onEvent({ status: "failed", error: [{ severity: "error", message: "x" }], version: 2 });
    expect(s.getState()).toMatchObject({ phase: "failed", showingLastGood: true, url: "/preview/d1/" });
    s.onEvent({ status: "stopped" });
    expect(s.getState()).toMatchObject({ phase: "idle", url: null, builtVersion: null });
  });

  it("a starting event does not hide the last good build", async () => {
    const { api, calls } = deferredApi();
    const { s } = make(api);
    s.start();
    calls[0]!.resolve(ready(1));
    await flush();
    s.onEvent({ status: "starting", version: 2 });
    expect(s.getState()).toMatchObject({ phase: "ready", url: "/preview/d1/" });
  });
});

describe("PreviewSession lifecycle", () => {
  it("dispose cancels timers and drops late answers", async () => {
    const { api, calls, startPreview } = deferredApi();
    const { s, c } = make(api);
    s.start();
    s.setDraftVersion(2);
    s.dispose();
    expect(c.pending()).toBe(0);
    calls[0]!.resolve(ready(1));
    await flush();
    expect(s.getState().url).toBeNull();
    expect(startPreview).toHaveBeenCalledTimes(1);
  });

  it("notifies subscribers on change", async () => {
    const { api, calls } = deferredApi();
    const { s } = make(api);
    const seen = vi.fn();
    s.subscribe(seen);
    s.start();
    calls[0]!.resolve(ready(1));
    await flush();
    expect(seen).toHaveBeenCalled();
  });
});
