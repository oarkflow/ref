// The Page designs data layer: the template catalog and the draft's assets,
// plus the write actions (customize, save, revert, rename, create). It sits
// beside the main studio store and writes through the same draft: every write
// bumps the draft version, is one undo step on the server, and is reflected in
// the main store so the top bar, validation and preview follow along.
import { useEffect } from "react";
import { useStore } from "zustand";
import { createStore, type StoreApi } from "zustand/vanilla";
import { ApiError } from "../api/client";
import type { AssetContent, AssetInfo, TemplateCatalog, TemplateInfo } from "../api/pageTypes";
import type { Diagnostic } from "../api/types";
import { useStudio, useStudioStore } from "../state/context";
import type { StudioStore } from "../state/store";
import { templateName, templatePath } from "./lib";

export type SaveOutcome =
  | { ok: true; version: number; diagnostics: Diagnostic[] }
  | { ok: false; reason: "invalid" | "stale" | "error"; message: string; diagnostics: Diagnostic[] };

export interface PagesState {
  draftId: string | null;
  loadedVersion: number;
  loading: boolean;
  error: string | null;
  catalog: TemplateCatalog | null;
  assets: AssetInfo[];
  /** Templates keyed by name, for quick lookups. */
  byName: Record<string, TemplateInfo>;

  load(): Promise<void>;
  read(path: string): Promise<AssetContent>;
  /** Copies host files into the draft as overrides (Customize). */
  customize(paths: string[]): Promise<boolean>;
  create(path: string, content: string): Promise<boolean>;
  save(path: string, content: string, opts?: { force?: boolean }): Promise<SaveOutcome>;
  /** Removes the draft's copy: reverts a customized file, deletes a new one. */
  remove(path: string): Promise<boolean>;
  rename(from: string, to: string): Promise<boolean>;
}

export type PagesStore = StoreApi<PagesState>;

const failMessage = (e: unknown, fallback: string) => (e instanceof ApiError ? e.message : e instanceof Error ? e.message : fallback);

export function createPagesStore(studio: StudioStore): PagesStore {
  let inflight: Promise<void> | null = null;

  const draft = () => studio.getState().draft;
  const api = () => studio.getState().api;
  const notify = (level: "info" | "error" | "success", text: string) => studio.getState().notify(level, text);

  /** Mirrors a successful write into the main store. */
  const wrote = (version: number) => {
    const d = draft();
    if (!d) return;
    studio.setState((s) => ({
      draft: s.draft ? { ...s.draft, version, dirty: true } : s.draft,
      undoDepth: s.undoDepth + 1,
      redoDepth: 0,
    }));
    studio.getState().scheduleValidate();
  };

  return createStore<PagesState>((set, get) => {
    /** Runs a write with the current version; retries once if only the version moved. */
    async function write<T extends { version: number }>(run: (id: string, ifVersion: number) => Promise<T>): Promise<T> {
      const d = draft();
      if (!d) throw new Error("There is no working copy open.");
      try {
        return await run(d.id, d.version);
      } catch (e) {
        if (e instanceof ApiError && e.isStale) {
          const fresh = await api().getDraft(d.id);
          studio.setState((s) => ({ draft: s.draft ? { ...s.draft, version: fresh.version } : s.draft }));
          return await run(d.id, fresh.version);
        }
        throw e;
      }
    }

    return {
      draftId: null,
      loadedVersion: -1,
      loading: false,
      error: null,
      catalog: null,
      assets: [],
      byName: {},

      async load() {
        const d = draft();
        if (!d) {
          set({ draftId: null, catalog: null, assets: [], byName: {}, loadedVersion: -1, loading: false });
          return;
        }
        if (inflight) return inflight;
        const id = d.id;
        set({ loading: true, error: null, draftId: id });
        inflight = (async () => {
          try {
            const [catalog, list] = await Promise.all([api().templates(id), api().listAssets(id)]);
            if (draft()?.id !== id) return;
            set({
              catalog, assets: list.assets, loadedVersion: Math.max(catalog.version, list.version),
              byName: Object.fromEntries(catalog.templates.map((t) => [t.name, t])), loading: false,
            });
          } catch (e) {
            set({ loading: false, error: failMessage(e, "Could not load the page designs.") });
          } finally {
            inflight = null;
          }
        })();
        return inflight;
      },

      read: (path) => api().getAsset(draft()!.id, path),

      async customize(paths) {
        try {
          const r = await write((id, v) => api().importFromDisk(id, paths, v));
          if (r.imported?.length) wrote(r.version);
          else {
            // Nothing was copied (already customized): still refresh the version we hold.
            studio.setState((s) => ({ draft: s.draft ? { ...s.draft, version: r.version } : s.draft }));
          }
          await get().load();
          return true;
        } catch (e) {
          notify("error", failMessage(e, "Could not customize that file."));
          return false;
        }
      },

      async create(path, content) {
        const out = await get().save(path, content);
        if (!out.ok) notify("error", out.message);
        return out.ok;
      },

      async save(path, content, opts = {}) {
        try {
          const r = await write((id, v) => api().putAsset(id, path, content, v, opts.force));
          wrote(r.version);
          await get().load();
          return { ok: true, version: r.version, diagnostics: r.template?.diagnostics ?? [] };
        } catch (e) {
          if (e instanceof ApiError) {
            if (e.code === "invalid_template") return { ok: false, reason: "invalid", message: "This design has a mistake, so it wasn’t saved.", diagnostics: e.diagnostics };
            if (e.isStale) return { ok: false, reason: "stale", message: "Someone changed this version at the same time. Try again.", diagnostics: [] };
          }
          return { ok: false, reason: "error", message: failMessage(e, "Could not save."), diagnostics: [] };
        }
      },

      async remove(path) {
        try {
          const r = await write((id, v) => api().deleteAsset(id, path, v));
          wrote(r.version);
          await get().load();
          return true;
        } catch (e) {
          notify("error", failMessage(e, "Could not remove that file."));
          return false;
        }
      },

      async rename(from, to) {
        try {
          const r = await write((id, v) => api().renameAsset(id, from, to, v));
          wrote(r.version);
          await get().load();
          return true;
        } catch (e) {
          notify("error", failMessage(e, "Could not rename that file."));
          return false;
        }
      },
    };
  });
}

// One pages store per studio store, created on first use.
const stores = new WeakMap<StudioStore, PagesStore>();

export function pagesStoreFor(studio: StudioStore): PagesStore {
  let s = stores.get(studio);
  if (!s) {
    s = createPagesStore(studio);
    stores.set(studio, s);
  }
  return s;
}

export function usePagesStore(): PagesStore {
  return pagesStoreFor(useStudioStore());
}

export function usePages<T>(selector: (s: PagesState) => T): T {
  return useStore(usePagesStore(), selector);
}

/**
 * Keeps the catalog in step with the open draft. Call it from a screen that
 * needs the data: it loads on mount and again whenever the draft version moves
 * (an undo, an edit elsewhere, another tab), never twice for the same version.
 */
export function usePagesSync(): void {
  const store = usePagesStore();
  const draftId = useStudio((s) => s.draft?.id ?? null);
  const version = useStudio((s) => s.draft?.version ?? -1);
  useEffect(() => {
    const s = store.getState();
    if (!draftId) return;
    if (s.draftId !== draftId || s.loadedVersion !== version) void s.load();
  }, [store, draftId, version]);
}

export { templateName, templatePath };
