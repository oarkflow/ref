import { vi } from "vitest";
import type { StudioApi } from "../api/client";
import type { BlockNode, DraftDetail, DraftSummary, Meta, OpResult, StudioEvent } from "../api/types";
import { catalog, schemas, topBlock } from "./helpers";

export const meta = (roles: Meta["identity"]["roles"] = ["editor"]): Meta => ({
  app: "starter", identity: { name: "alice", roles }, features: { preview: false, pages: false }, activeRevision: "rev1",
});

export const routeTree = (): BlockNode[] => [
  topBlock("route", "web.todos_list", [["method", "GET"], ["path", '"/todos"'], ["intent", '"todo.list"']]),
];

export function detail(over: Partial<DraftDetail> = {}): DraftDetail {
  return {
    id: "d1", owner: "alice", name: "Draft", version: 1, files: ["04_routes.bcl"], dirty: false,
    createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z", diagnostics: [], ...over,
  };
}

export type FakeApi = StudioApi & { emit(e: StudioEvent): void; state: { version: number; tree: BlockNode[] } };

/** An in-memory StudioApi. Override any method to script a failure. */
export function fakeApi(over: Partial<Record<keyof StudioApi, unknown>> = {}): FakeApi {
  const state = { version: 1, tree: routeTree() };
  let listener: ((e: StudioEvent) => void) | null = null;
  const summary = (): DraftSummary => ({ ...detail({ version: state.version }), diagnostics: { errors: 0, warnings: 0 } });
  const opResult = (changed = ["04_routes.bcl"]): OpResult => ({ version: ++state.version, applied: 1, diagnostics: [], changed });
  const api = {
    meta: vi.fn(async () => meta()),
    blockSchemas: vi.fn(async () => schemas),
    catalog: vi.fn(async () => catalog),
    listDrafts: vi.fn(async () => [summary()]),
    createDraft: vi.fn(async () => summary()),
    getDraft: vi.fn(async () => detail({ version: state.version, dirty: state.version > 1 })),
    deleteDraft: vi.fn(async () => undefined),
    tree: vi.fn(async () => ({ "04_routes.bcl": state.tree.map((n) => ({ ...n, children: undefined })) })),
    fileTree: vi.fn(async () => state.tree),
    ops: vi.fn(async () => opResult()),
    undo: vi.fn(async () => opResult()),
    redo: vi.fn(async () => opResult()),
    format: vi.fn(async () => opResult([])),
    validate: vi.fn(async () => ({ valid: true, diagnostics: [] })),
    putFile: vi.fn(async () => opResult()),
    propose: vi.fn(async () => ({ id: "rev2", seq: 2 })),
    listRevisions: vi.fn(async () => []),
    diff: vi.fn(async () => ({ files: [], changes: [] })),
    getFile: vi.fn(async () => ({ path: "04_routes.bcl", content: "", version: 1 })),
    audit: vi.fn(async () => []),
    subscribe: vi.fn((_id: string, on: (e: StudioEvent) => void, state?: (c: boolean) => void) => {
      listener = on;
      state?.(true);
      return () => { listener = null; };
    }),
    ...over,
  } as unknown as FakeApi;
  api.emit = (e) => listener?.(e);
  api.state = state;
  return api;
}
