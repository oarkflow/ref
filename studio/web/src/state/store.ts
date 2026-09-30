import { createStore, type StoreApi } from "zustand/vanilla";
import { ApiError, StudioApi, TOKEN_KEY } from "../api/client";
import type {
  BlockNode, BlockSchemas, Catalog, Diagnostic, DraftDetail, DraftSummary, Meta, Op, OpResult, PreviewEventData, Role, Revision,
} from "../api/types";
import { isWithin } from "../lib/paths";
import { OpQueue, type RollbackInfo } from "./opQueue";

export interface Notice {
  id: number;
  level: "info" | "error" | "success";
  text: string;
}

export interface Selection {
  file: string;
  path: string;
}

export interface StudioState {
  api: StudioApi;
  token: string | null;
  authError: string | null;
  meta: Meta | null;
  schemas: BlockSchemas | null;
  catalog: Catalog | null;
  drafts: DraftSummary[];
  draft: DraftDetail | null;
  /** Top-level statements per file (navigator). */
  nav: Record<string, BlockNode[]>;
  /** Full trees of files that were opened. */
  trees: Record<string, BlockNode[]>;
  selection: Selection | null;
  /** The path a diagnostic pointed at; the inspector scrolls to and highlights it. */
  focusPath: string | null;
  /** A source line to reveal in the raw view (diagnostics that name no block). */
  focusLine: number | null;
  /** Edits shown before the server has acknowledged them, keyed "file|path"; null = removed. */
  overlay: Record<string, { op: Op; raw: string | null }>;
  diagnostics: Diagnostic[];
  validating: boolean;
  undoDepth: number;
  redoDepth: number;
  connected: boolean;
  loading: boolean;
  notices: Notice[];
  previewOn: boolean;
  /** The latest `preview` SSE event for the open draft; `seq` distinguishes repeats. */
  previewEvent: { seq: number; draftId: string; data: PreviewEventData } | null;

  // lifecycle
  init(): Promise<void>;
  signIn(token: string): Promise<void>;
  signOut(): void;
  refreshDrafts(): Promise<void>;
  createDraft(from: string, name?: string): Promise<void>;
  openDraft(id: string): Promise<void>;
  /** Quietly pulls in changes made elsewhere: no loading state, no resubscribe, selection kept. */
  syncDraft(id: string): Promise<void>;
  deleteDraft(id: string): Promise<void>;
  // selection
  select(file: string, path: string, focus?: string | null): Promise<void>;
  selectFromDiagnostic(d: Diagnostic): Promise<void>;
  // editing
  edit(ops: Op[]): void;
  flush(): Promise<void>;
  undo(): Promise<void>;
  redo(): Promise<void>;
  format(): Promise<void>;
  validate(): Promise<void>;
  scheduleValidate(): void;
  setFileText(file: string, content: string): Promise<boolean>;
  propose(message: string): Promise<Revision | null>;
  refreshTrees(changed?: string[]): Promise<void>;
  /** Loads the full tree of every file (the friendly lists summarise field values). */
  ensureTrees(): Promise<void>;
  // ui
  notify(level: Notice["level"], text: string): void;
  dismiss(id: number): void;
  setPreview(on: boolean): void;
}

export type StudioStore = StoreApi<StudioState>;

const RANK: Record<Role, number> = { viewer: 1, editor: 2, reviewer: 3, admin: 4 };

export function hasRole(meta: Meta | null, min: Role): boolean {
  if (!meta) return false;
  return meta.identity.roles.some((r) => (RANK[r] ?? 0) >= RANK[min]);
}

export interface StoreOptions {
  /** Quiet period before queued edits are sent (ms). */
  batchDelay?: number;
  /** Quiet period before a full validation is requested after edits (ms). */
  validateDelay?: number;
}

let noticeSeq = 0;

export function createStudioStore(api: StudioApi, opts: StoreOptions = {}): StudioStore {
  let unsubscribe: (() => void) | null = null;
  let syncTimer: ReturnType<typeof setTimeout> | undefined;
  let validateTimer: ReturnType<typeof setTimeout> | null = null;
  let treeSeq = 0;

  const store: StudioStore = createStore<StudioState>((set, get) => {
    const notify = (level: Notice["level"], text: string) => {
      const id = ++noticeSeq;
      set((s) => ({ notices: [...s.notices, { id, level, text }].slice(-5) }));
      if (level !== "error") setTimeout(() => get().dismiss(id), 5000);
    };
    const fail = (e: unknown, prefix = "") => {
      const msg = e instanceof Error ? e.message : String(e);
      notify("error", prefix ? `${prefix}: ${msg}` : msg);
    };

    const queue = new OpQueue({
      delay: opts.batchDelay ?? 250,
      version: () => get().draft?.version ?? 0,
      send: (ops, v) => api.ops(get().draft!.id, ops, v),
      onPending: (pending) => {
        // Drop overlay entries whose op is no longer pending and not awaiting a tree refresh.
        void pending;
      },
      onApplied: (result, ops) => void applied(result, ops),
      onRollback: (info) => void rolledBack(info),
    });

    const setOverlay = (ops: Op[]) => {
      const next = { ...get().overlay };
      for (const op of ops) {
        if (op.op === "setField") next[`${op.file}|${op.path}`] = { op, raw: op.value };
        else if (op.op === "removeField") next[`${op.file}|${op.path}`] = { op, raw: null };
      }
      set({ overlay: next });
    };

    const clearOverlay = (ops: Op[]) => {
      const next = { ...get().overlay };
      for (const op of ops) {
        if (op.op !== "setField" && op.op !== "removeField") continue;
        const k = `${op.file}|${op.path}`;
        if (next[k]?.op === op) delete next[k]; // a newer edit of the same field stays
      }
      set({ overlay: next });
    };

    async function applied(result: OpResult, ops: Op[]) {
      const d = get().draft;
      if (!d) return;
      set({
        draft: { ...d, version: result.version, dirty: true },
        diagnostics: result.diagnostics ?? get().diagnostics,
        undoDepth: get().undoDepth + 1,
        redoDepth: 0,
      });
      await get().refreshTrees(result.changed);
      clearOverlay(ops);
      get().scheduleValidate();
    }

    async function rolledBack(info: RollbackInfo) {
      clearOverlay(info.ops);
      const err = info.error;
      if (info.reason === "stale") {
        notify("error", "The draft was changed elsewhere; your last edit was not applied. Reloaded the latest version.");
      } else if (err instanceof ApiError) {
        notify("error", `Edit rejected: ${err.message}`);
        if (err.diagnostics.length) set({ diagnostics: err.diagnostics });
      } else {
        fail(err, "Could not save the edit");
      }
      const id = get().draft?.id;
      if (id) await get().openDraft(id);
    }

    async function loadTrees(id: string, files: string[]) {
      const seq = ++treeSeq;
      const [nav, ...full] = await Promise.all([api.tree(id), ...files.map((f) => api.fileTree(id, f))]);
      if (seq !== treeSeq || get().draft?.id !== id) return;
      const trees = { ...get().trees };
      files.forEach((f, i) => (trees[f] = full[i]!));
      for (const f of Object.keys(trees)) if (!(f in nav)) delete trees[f];
      set({ nav, trees });
    }

    return {
      api,
      token: safeGet(TOKEN_KEY),
      authError: null,
      meta: null,
      schemas: null,
      catalog: null,
      drafts: [],
      draft: null,
      nav: {},
      trees: {},
      selection: null,
      focusPath: null,
      focusLine: null,
      overlay: {},
      diagnostics: [],
      validating: false,
      undoDepth: 0,
      redoDepth: 0,
      connected: false,
      loading: false,
      notices: [],
      previewOn: false,
      previewEvent: null,

      notify,
      dismiss: (id) => set((s) => ({ notices: s.notices.filter((n) => n.id !== id) })),
      setPreview: (on) => set({ previewOn: on }),

      async init() {
        set({ loading: true, authError: null });
        try {
          const [meta, schemas, catalog] = await Promise.all([api.meta(), api.blockSchemas(), api.catalog()]);
          set({ meta, schemas, catalog });
          if (meta.identity.roles.some((r) => RANK[r] >= RANK.editor)) {
            await get().refreshDrafts();
            const first = get().drafts[0];
            if (first) await get().openDraft(first.id);
          }
        } catch (e) {
          if (e instanceof ApiError && e.status === 401) set({ meta: null, authError: "Sign in with a studio token." });
          else fail(e);
        } finally {
          set({ loading: false });
        }
      },

      async signIn(token) {
        try {
          localStorage.setItem(TOKEN_KEY, token);
        } catch {
          /* private mode: the token lives in memory only */
        }
        set({ token });
        await get().init();
      },

      signOut() {
        if (syncTimer) clearTimeout(syncTimer);
        try {
          localStorage.removeItem(TOKEN_KEY);
        } catch {
          /* ignore */
        }
        unsubscribe?.();
        queue.reset();
        set({ token: null, meta: null, draft: null, drafts: [], nav: {}, trees: {}, selection: null, diagnostics: [] });
      },

      async refreshDrafts() {
        try {
          set({ drafts: await api.listDrafts() });
        } catch (e) {
          fail(e, "Could not list drafts");
        }
      },

      async createDraft(from, name) {
        try {
          const d = await api.createDraft({ from, name });
          await get().refreshDrafts();
          await get().openDraft(d.id);
        } catch (e) {
          fail(e, "Could not create the draft");
        }
      },

      async openDraft(id) {
        if (syncTimer) clearTimeout(syncTimer);
        queue.reset();
        unsubscribe?.();
        set({ loading: true });
        try {
          const draft = await api.getDraft(id);
          const prevSel = get().draft?.id === id ? get().selection : null;
          set({ draft, diagnostics: draft.diagnostics ?? [], overlay: {}, trees: get().draft?.id === id ? get().trees : {},
                selection: prevSel, undoDepth: get().draft?.id === id ? get().undoDepth : 0, redoDepth: get().draft?.id === id ? get().redoDepth : 0 });
          const files = prevSel && draft.files.includes(prevSel.file) ? [prevSel.file] : [];
          await loadTrees(id, files);
          if (!get().selection) {
            const nav = get().nav;
            const f = draft.files.find((x) => (nav[x]?.length ?? 0) > 0);
            const first = f ? nav[f]!.find((n) => n.kind === "block") : undefined;
            if (f && first) await get().select(f, first.path);
          }
          unsubscribe = api.subscribe(
            id,
            (ev) => {
              if (ev.type === "changed") {
                const cur = get().draft;
                if (cur && cur.id === id && ev.data.version > cur.version && !queue.pending().length) {
                  // Coalesce bursts (undo, batches, other editors) into one quiet refresh.
                  if (syncTimer) clearTimeout(syncTimer);
                  syncTimer = setTimeout(() => void get().syncDraft(id), 300);
                }
              } else if (ev.type === "diagnostics") {
                set({ diagnostics: ev.data.diagnostics });
              } else if (ev.type === "preview") {
                set({ previewEvent: { seq: (get().previewEvent?.seq ?? 0) + 1, draftId: id, data: ev.data } });
              }
            },
            (connected) => set({ connected }),
          );
        } catch (e) {
          fail(e, "Could not open the draft");
        } finally {
          set({ loading: false });
        }
      },

      async syncDraft(id) {
        try {
          const draft = await api.getDraft(id);
          const cur = get().draft;
          if (!cur || cur.id !== id || draft.version <= cur.version || queue.pending().length) return;
          set({ draft, diagnostics: draft.diagnostics ?? [], overlay: {} });
          await loadTrees(id, Object.keys(get().trees).filter((f) => draft.files.includes(f)));
        } catch {
          /* non-fatal: the next event triggers another sync */
        }
      },

      async deleteDraft(id) {
        try {
          await api.deleteDraft(id);
          if (get().draft?.id === id) {
            unsubscribe?.();
            queue.reset();
            set({ draft: null, nav: {}, trees: {}, selection: null, diagnostics: [] });
          }
          await get().refreshDrafts();
          const next = get().drafts[0];
          if (!get().draft && next) await get().openDraft(next.id);
        } catch (e) {
          fail(e, "Could not delete the draft");
        }
      },

      async select(file, path, focus = null) {
        const d = get().draft;
        if (!d) return;
        set({ selection: { file, path }, focusPath: focus, focusLine: null });
        if (!get().trees[file]) {
          try {
            const tree = await api.fileTree(d.id, file);
            if (get().draft?.id === d.id) set({ trees: { ...get().trees, [file]: tree } });
          } catch (e) {
            fail(e, `Could not load ${file}`);
          }
        }
      },

      async selectFromDiagnostic(dg) {
        const { nav } = get();
        let file = dg.file;
        let node: BlockNode | undefined;
        const search = (f: string) => {
          const list = nav[f] ?? [];
          if (dg.path) {
            const hit = list.find((n) => isWithin(dg.path!, n.path));
            if (hit) return hit;
          }
          if (dg.offset !== undefined) return list.find((n) => dg.offset! >= n.start && dg.offset! <= n.end);
          if (dg.line !== undefined) {
            // The statement that starts closest above the reported line.
            let best: BlockNode | undefined;
            for (const n of list) if (n.line <= dg.line && (!best || n.line > best.line)) best = n;
            return best;
          }
          return undefined;
        };
        if (file) node = search(file);
        else if (dg.path) {
          for (const f of Object.keys(nav)) {
            node = search(f);
            if (node) {
              file = f;
              break;
            }
          }
        }
        if (!file) return;
        if (!node) {
          // No block to open (e.g. a parse error); still show the file so the raw view can jump to the line.
          set({ selection: { file, path: "" }, focusPath: null, focusLine: dg.line ?? null });
          return;
        }
        await get().select(file, node.path, dg.path ?? null);
      },

      edit(ops) {
        if (!get().draft || !ops.length) return;
        setOverlay(ops);
        queue.enqueue(ops);
      },

      async flush() {
        await queue.flush();
        await queue.idle();
      },

      async undo() {
        await get().flush();
        const d = get().draft;
        if (!d) return;
        try {
          const r = await api.undo(d.id);
          set({ draft: { ...d, version: r.version }, diagnostics: r.diagnostics ?? [],
                undoDepth: Math.max(0, get().undoDepth - 1), redoDepth: get().redoDepth + 1 });
          await get().refreshTrees(r.changed);
          get().scheduleValidate();
        } catch (e) {
          if (e instanceof ApiError && e.status === 409 && e.code.startsWith("nothing")) notify("info", "Nothing to undo.");
          else fail(e, "Undo failed");
        }
      },

      async redo() {
        await get().flush();
        const d = get().draft;
        if (!d) return;
        try {
          const r = await api.redo(d.id);
          set({ draft: { ...d, version: r.version }, diagnostics: r.diagnostics ?? [],
                undoDepth: get().undoDepth + 1, redoDepth: Math.max(0, get().redoDepth - 1) });
          await get().refreshTrees(r.changed);
          get().scheduleValidate();
        } catch (e) {
          if (e instanceof ApiError && e.status === 409 && e.code.startsWith("nothing")) notify("info", "Nothing to redo.");
          else fail(e, "Redo failed");
        }
      },

      async format() {
        await get().flush();
        const d = get().draft;
        if (!d) return;
        try {
          const r = await api.format(d.id);
          set({ draft: { ...d, version: r.version, dirty: true }, diagnostics: r.diagnostics ?? get().diagnostics });
          if (r.changed.length) set({ undoDepth: get().undoDepth + 1, redoDepth: 0 });
          await get().refreshTrees(r.changed);
          notify("success", r.changed.length ? `Formatted ${r.changed.length} file(s).` : "Already formatted.");
        } catch (e) {
          fail(e, "Format failed");
        }
      },

      async validate() {
        const d = get().draft;
        if (!d) return;
        if (validateTimer) clearTimeout(validateTimer);
        validateTimer = null;
        set({ validating: true });
        try {
          await get().flush();
          const r = await api.validate(d.id);
          if (get().draft?.id === d.id) set({ diagnostics: r.diagnostics ?? [] });
        } catch (e) {
          fail(e, "Validation failed");
        } finally {
          set({ validating: false });
        }
      },

      scheduleValidate() {
        if (validateTimer) clearTimeout(validateTimer);
        validateTimer = setTimeout(() => {
          validateTimer = null;
          if (!queue.pending().length) void get().validate();
        }, opts.validateDelay ?? 800);
      },

      async setFileText(file, content) {
        await get().flush();
        const d = get().draft;
        if (!d) return false;
        try {
          const r = await api.putFile(d.id, file, content, d.version);
          set({ draft: { ...d, version: r.version, dirty: true }, diagnostics: r.diagnostics ?? [],
                undoDepth: get().undoDepth + 1, redoDepth: 0 });
          await get().refreshTrees([file]);
          return true;
        } catch (e) {
          if (e instanceof ApiError && e.diagnostics.length) set({ diagnostics: e.diagnostics });
          fail(e, `Could not save ${file}`);
          return false;
        }
      },

      async propose(message) {
        await get().flush();
        const d = get().draft;
        if (!d) return null;
        try {
          const rev = await api.propose(d.id, message);
          notify("success", `Proposed revision ${rev.seq ?? ""}. It needs review before it can go live.`);
          return rev;
        } catch (e) {
          if (e instanceof ApiError && e.diagnostics.length) set({ diagnostics: e.diagnostics });
          fail(e, "Could not propose");
          return null;
        }
      },

      async ensureTrees() {
        const d = get().draft;
        if (!d) return;
        const missing = d.files.filter((f) => !get().trees[f]);
        if (!missing.length) return;
        try {
          const full = await Promise.all(missing.map((f) => api.fileTree(d.id, f)));
          if (get().draft?.id !== d.id) return;
          const trees = { ...get().trees };
          missing.forEach((f, i) => (trees[f] = full[i]!));
          set({ trees });
        } catch (e) {
          fail(e, "Could not load the configuration");
        }
      },

      async refreshTrees(changed) {
        const d = get().draft;
        if (!d) return;
        const sel = get().selection;
        const open = Object.keys(get().trees).filter((f) => !changed || changed.includes(f) || f === sel?.file);
        try {
          await loadTrees(d.id, open.filter((f) => d.files.includes(f) || changed?.includes(f)));
        } catch (e) {
          fail(e, "Could not refresh the tree");
        }
        // Files may have been added or removed.
        try {
          const fresh = await api.getDraft(d.id);
          if (get().draft?.id === d.id) set({ draft: { ...get().draft!, files: fresh.files, dirty: fresh.dirty } });
        } catch {
          /* non-fatal */
        }
      },
    };
  });

  return store;
}

function safeGet(k: string): string | null {
  try {
    return localStorage.getItem(k);
  } catch {
    return null;
  }
}
