import { describe, expect, it, vi } from "vitest";
import { ApiError, SSEParser, StudioApi } from "./client";
import type { StudioEvent } from "./types";

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

describe("StudioApi", () => {
  it("sends the bearer token and the JSON body", async () => {
    const f = vi.fn(async () => json(200, { version: 3, applied: 1, diagnostics: [], changed: [] }));
    const api = new StudioApi({ base: "/x/api/v1", token: () => "tok", fetch: f as unknown as typeof fetch });
    await api.ops("d1", [{ op: "removeField", file: "a.bcl", path: "p" }], 2);
    const [url, init] = f.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/x/api/v1/drafts/d1/ops");
    expect((init.headers as Record<string, string>).Authorization).toBe("Bearer tok");
    expect(JSON.parse(init.body as string)).toEqual({ ops: [{ op: "removeField", file: "a.bcl", path: "p" }], ifVersion: 2 });
  });

  it("maps the contract error shape, including a stale 409", async () => {
    const api = new StudioApi({ base: "/a", fetch: (async () => json(409, { error: { code: "stale", message: "changed", details: { version: 5 } } })) as unknown as typeof fetch });
    const e = await api.undo("d").catch((x) => x);
    expect(e).toBeInstanceOf(ApiError);
    expect(e.isStale).toBe(true);
    expect(e.details).toEqual({ version: 5 });
  });

  it("maps deploy.Admin's string errors and 422 diagnostics", async () => {
    const a = new StudioApi({ base: "/a", fetch: (async () => json(403, { error: "you may not approve your own revision" })) as unknown as typeof fetch });
    const e1 = await a.approve("r").catch((x) => x);
    expect(e1.status).toBe(403);
    expect(e1.message).toMatch(/own revision/);

    const b = new StudioApi({ base: "/a", fetch: (async () => json(422, { error: { code: "invalid", message: "bad", details: { diagnostics: [{ severity: "error", message: "m", path: "route/x" }] } } })) as unknown as typeof fetch });
    const e2 = await b.propose("d", "m").catch((x) => x);
    expect(e2.diagnostics).toHaveLength(1);
  });

  it("calls onUnauthorized on 401", async () => {
    const onUnauthorized = vi.fn();
    const api = new StudioApi({ base: "/a", onUnauthorized, fetch: (async () => json(401, { error: { code: "unauthorized", message: "no" } })) as unknown as typeof fetch });
    await api.meta().catch(() => {});
    expect(onUnauthorized).toHaveBeenCalled();
  });

  it("encodes file names in paths", async () => {
    const f = vi.fn(async () => json(200, []));
    const api = new StudioApi({ base: "/a", fetch: f as unknown as typeof fetch });
    await api.fileTree("d", "00 app.bcl");
    expect((f.mock.calls[0] as unknown as [string])[0]).toBe("/a/drafts/d/files/00%20app.bcl/tree");
  });
});

describe("SSEParser", () => {
  it("assembles events across chunks and ignores comments", () => {
    const got: StudioEvent[] = [];
    const p = new SSEParser((e) => got.push(e));
    p.push(": connected\n\nevent: changed\nda");
    p.push('ta: {"version":4,"changed":["a.bcl"]}\n\n');
    p.push('event: diagnostics\r\ndata: {"diagnostics":[]}\r\n\r\n');
    expect(got).toEqual([
      { type: "changed", data: { version: 4, changed: ["a.bcl"] } },
      { type: "diagnostics", data: { diagnostics: [] } },
    ]);
  });
  it("skips malformed data", () => {
    const got: StudioEvent[] = [];
    new SSEParser((e) => got.push(e)).push("event: changed\ndata: {oops\n\n");
    expect(got).toEqual([]);
  });
});
