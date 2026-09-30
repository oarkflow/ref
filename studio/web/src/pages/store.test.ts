import { describe, expect, it, vi } from "vitest";
import { ApiError } from "../api/client";
import { createStudioStore } from "../state/store";
import { createPagesStore } from "./store";
import { pagesApi } from "./testKit";

async function boot(over: Record<string, unknown> = {}) {
  const api = pagesApi(over);
  const studio = createStudioStore(api, { batchDelay: 5, validateDelay: 60_000 });
  studio.setState({ token: "t" });
  await studio.getState().init();
  const pages = createPagesStore(studio);
  return { api, studio, pages };
}

describe("loading", () => {
  it("loads the catalog and the draft's assets together", async () => {
    const { pages } = await boot();
    await pages.getState().load();
    const s = pages.getState();
    expect(s.catalog?.templates.map((t) => t.name)).toContain("pages/todos/list");
    expect(s.byName["layouts/base"]?.kind).toBe("layout");
    expect(s.assets).toEqual([]);
    expect(s.loading).toBe(false);
  });

  it("does one request when asked twice at once", async () => {
    const { api, pages } = await boot();
    await Promise.all([pages.getState().load(), pages.getState().load()]);
    expect(api.templates).toHaveBeenCalledTimes(1);
  });

  it("reports a failure instead of throwing", async () => {
    const { pages } = await boot({ templates: vi.fn(async () => { throw new ApiError(500, "boom", "The server fell over"); }) });
    await pages.getState().load();
    expect(pages.getState().error).toBe("The server fell over");
    expect(pages.getState().loading).toBe(false);
  });
});

describe("customize", () => {
  it("copies the file into the draft and mirrors the write into the main store", async () => {
    const { api, studio, pages } = await boot();
    const before = studio.getState().undoDepth;
    expect(await pages.getState().customize(["templates/pages/todos/list.html"])).toBe(true);
    expect(api.importFromDisk).toHaveBeenCalledWith("d1", ["templates/pages/todos/list.html"], 1);
    expect(studio.getState().draft?.version).toBe(2);
    expect(studio.getState().draft?.dirty).toBe(true);
    expect(studio.getState().undoDepth).toBe(before + 1);
    expect(pages.getState().byName["pages/todos/list"]?.source).toBe("override");
  });

  it("tells the person when it fails", async () => {
    const { studio, pages } = await boot({ importFromDisk: vi.fn(async () => { throw new ApiError(404, "not_found", "That file is not on disk"); }) });
    expect(await pages.getState().customize(["templates/x.html"])).toBe(false);
    expect(studio.getState().notices.at(-1)).toMatchObject({ level: "error", text: "That file is not on disk" });
  });
});

describe("saving", () => {
  it("writes with the draft version and refreshes", async () => {
    const { api, studio, pages } = await boot();
    const out = await pages.getState().save("templates/pages/todos/list.html", "hello");
    expect(out).toMatchObject({ ok: true, version: 2 });
    expect(api.putAsset).toHaveBeenCalledWith("d1", "templates/pages/todos/list.html", "hello", 1, undefined);
    expect(studio.getState().draft?.version).toBe(2);
  });

  it("explains a refused template and keeps its findings for the gutter", async () => {
    const diag = { severity: "error" as const, code: "studio.pages.syntax", message: "@if: expected '{'", line: 3, column: 5 };
    const { pages } = await boot({ putAsset: vi.fn(async () => { throw new ApiError(422, "invalid_template", "invalid", { diagnostics: [diag] }); }) });
    const out = await pages.getState().save("templates/pages/todos/list.html", "@if(");
    expect(out).toMatchObject({ ok: false, reason: "invalid" });
    expect(out.ok ? [] : out.diagnostics).toEqual([diag]);
  });

  it("can be forced past a syntax error", async () => {
    const { api, pages } = await boot();
    await pages.getState().save("templates/pages/todos/list.html", "@if(", { force: true });
    expect(vi.mocked(api.putAsset).mock.calls[0]![4]).toBe(true);
  });

  it("retries once with the fresh version when only the version moved", async () => {
    let calls = 0;
    const { api, pages } = await boot({
      putAsset: vi.fn(async () => {
        if (calls++ === 0) throw new ApiError(409, "stale", "stale");
        return { version: 9, applied: 1, diagnostics: [], changed: [] };
      }),
    });
    api.state.version = 7;
    const out = await pages.getState().save("templates/a.html", "x");
    expect(out).toMatchObject({ ok: true, version: 9 });
    expect(vi.mocked(api.putAsset).mock.calls.map((c) => c[3])).toEqual([1, 7]);
  });

  it("gives up with a plain message if it is still stale", async () => {
    const { pages } = await boot({ putAsset: vi.fn(async () => { throw new ApiError(409, "stale", "stale"); }) });
    const out = await pages.getState().save("templates/a.html", "x");
    expect(out).toMatchObject({ ok: false, reason: "stale" });
  });

  it("reports any other failure", async () => {
    const { pages } = await boot({ putAsset: vi.fn(async () => { throw new ApiError(500, "x", "disk full"); }) });
    expect(await pages.getState().save("templates/a.html", "x")).toMatchObject({ ok: false, reason: "error", message: "disk full" });
  });
});

describe("revert, remove and rename", () => {
  it("removes the draft's copy, which reverts a customized file", async () => {
    const { api, pages } = await boot();
    await pages.getState().customize(["templates/pages/todos/list.html"]);
    expect(pages.getState().byName["pages/todos/list"]?.source).toBe("override");
    expect(await pages.getState().remove("templates/pages/todos/list.html")).toBe(true);
    expect(api.deleteAsset).toHaveBeenCalledWith("d1", "templates/pages/todos/list.html", 2);
    expect(pages.getState().byName["pages/todos/list"]?.source).toBe("disk"); // back to the app's own
  });

  it("renames", async () => {
    const { api, studio, pages } = await boot();
    expect(await pages.getState().rename("templates/pages/a.html", "templates/pages/b.html")).toBe(true);
    expect(api.renameAsset).toHaveBeenCalledWith("d1", "templates/pages/a.html", "templates/pages/b.html", 1);
    expect(studio.getState().draft?.version).toBe(4);
  });

  it("notifies when a rename fails", async () => {
    const { studio, pages } = await boot({ renameAsset: vi.fn(async () => { throw new ApiError(422, "op_failed", "that name is taken"); }) });
    expect(await pages.getState().rename("templates/a.html", "templates/b.html")).toBe(false);
    expect(studio.getState().notices.at(-1)?.text).toBe("that name is taken");
  });
});
