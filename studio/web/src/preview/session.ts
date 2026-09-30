// The preview session: a small state machine that decides when to (re)build
// the draft's preview and when the iframe should reload. It is plain TS with
// injected timers and API so the ordering rules can be tested without React.
//
// Rules:
//  - Edits bump the draft version. Builds are debounced (a burst of edits is
//    one build) and serialised (one POST in flight; a newer version queued
//    while it runs triggers exactly one follow-up).
//  - The frame reloads only when the preview reports `ready` for a version
//    newer than the one the frame shows. A failed rebuild keeps the last good
//    build on screen and raises a banner; it never blanks the frame.
//  - Anything older than what we already applied (a slow POST answer, a late
//    SSE event) is ignored.

import { ApiError } from "../api/client";
import type { Diagnostic, PreviewEventData, PreviewStatus } from "../api/types";

export type Phase = "idle" | "starting" | "ready" | "failed" | "unavailable";

export interface SessionState {
  phase: Phase;
  /** Preview root ("/…/preview/<id>/") of the newest good build. */
  url: string | null;
  /** Draft version the newest good build was made from (null if unknown). */
  builtVersion: number | null;
  /** Draft version being edited. */
  draftVersion: number;
  /** Diagnostics of the latest failed build. */
  errors: Diagnostic[];
  message: string | null;
  /** True when a good build exists but the draft has moved on. */
  stale: boolean;
  /** Bumps whenever the frame should reload. */
  reloadKey: number;
  /** True when a failed rebuild is being masked by the last good build. */
  showingLastGood: boolean;
}

export interface SessionApi {
  startPreview(id: string): Promise<PreviewStatus>;
  stopPreview?(id: string): Promise<void>;
}

export interface SessionTimers {
  set(fn: () => void, ms: number): unknown;
  clear(h: unknown): void;
}

export interface SessionOptions {
  buildDebounceMs?: number;
  reloadDebounceMs?: number;
  timers?: SessionTimers;
}

const realTimers: SessionTimers = {
  set: (fn, ms) => setTimeout(fn, ms),
  clear: (h) => clearTimeout(h as ReturnType<typeof setTimeout>),
};

export class PreviewSession {
  private s: SessionState;
  private listeners = new Set<() => void>();
  private buildTimer: unknown = null;
  private reloadTimer: unknown = null;
  private inFlight = false;
  private wantVersion = -1; // newest version we still owe a build for
  private shownVersion: number | null = null; // version currently in the frame
  private disposed = false;
  private readonly buildMs: number;
  private readonly reloadMs: number;
  private readonly timers: SessionTimers;

  constructor(private api: SessionApi, readonly draftId: string, initialVersion: number, opts: SessionOptions = {}) {
    this.buildMs = opts.buildDebounceMs ?? 400;
    this.reloadMs = opts.reloadDebounceMs ?? 150;
    this.timers = opts.timers ?? realTimers;
    this.s = {
      phase: "idle", url: null, builtVersion: null, draftVersion: initialVersion, errors: [], message: null,
      stale: false, reloadKey: 0, showingLastGood: false,
    };
    this.wantVersion = initialVersion;
  }

  // ---- store plumbing -------------------------------------------------------

  getState = (): SessionState => this.s;

  subscribe = (fn: () => void): (() => void) => {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  };

  private set(patch: Partial<SessionState>) {
    const next = { ...this.s, ...patch };
    next.stale = next.url !== null && next.builtVersion !== null && next.builtVersion < next.draftVersion;
    this.s = next;
    for (const l of [...this.listeners]) l();
  }

  // ---- inputs ---------------------------------------------------------------

  /** Starts the first build immediately (no debounce). */
  start() {
    this.set({ phase: "starting" });
    void this.build();
  }

  /** The draft changed. Rebuilds after a quiet period. */
  setDraftVersion(v: number) {
    if (this.disposed || v === this.s.draftVersion) return;
    this.wantVersion = v;
    this.set({ draftVersion: v });
    if (this.s.phase === "unavailable") return;
    if (this.buildTimer) this.timers.clear(this.buildTimer);
    this.buildTimer = this.timers.set(() => {
      this.buildTimer = null;
      void this.build();
    }, this.buildMs);
  }

  /** A `preview` SSE event. */
  onEvent(ev: PreviewEventData) {
    if (this.disposed) return;
    this.apply({ status: ev.status, url: ev.url, version: ev.version, error: ev.error, message: ev.message });
  }

  dispose() {
    this.disposed = true;
    if (this.buildTimer) this.timers.clear(this.buildTimer);
    if (this.reloadTimer) this.timers.clear(this.reloadTimer);
    this.listeners.clear();
  }

  // ---- building -------------------------------------------------------------

  private async build() {
    if (this.disposed) return;
    if (this.inFlight) return; // the running one re-checks wantVersion when it ends
    this.inFlight = true;
    const target = this.wantVersion;
    if (!this.s.url && this.s.phase !== "starting") this.set({ phase: "starting" });
    try {
      const st = await this.api.startPreview(this.draftId);
      if (!this.disposed) this.apply(st, target);
    } catch (e) {
      if (!this.disposed) this.fail(e);
    } finally {
      this.inFlight = false;
    }
    // Edits arrived while we were building: one follow-up, not one per edit.
    if (!this.disposed && this.wantVersion !== target && this.buildTimer === null) void this.build();
  }

  private fail(e: unknown) {
    if (e instanceof ApiError && (e.status === 501 || e.status === 404)) {
      this.set({ phase: "unavailable", message: e.status === 501 ? "Live preview is not configured on this server." : e.message, errors: [] });
      return;
    }
    const msg = e instanceof Error ? e.message : String(e);
    this.toFailed([{ severity: "error", message: msg }], null);
  }

  /**
   * Applies a server answer (a POST answer, or an SSE event when
   * `requestedVersion` is absent). Answers are ordered by the draft version
   * they describe: a `ready` older than the build we already have is dropped,
   * so a slow POST cannot undo a newer SSE event.
   */
  private apply(st: PreviewStatus | PreviewEventData, requestedVersion?: number) {
    const isPost = requestedVersion !== undefined;
    // Version this status describes; POST answers from servers that don't send
    // a version describe the version we asked for.
    const v = st.version ?? (isPost && st.status === "ready" ? requestedVersion : undefined);
    if (v !== undefined && this.s.builtVersion !== null && st.status === "ready" && v < this.s.builtVersion) return; // stale ready
    switch (st.status) {
      case "starting":
        // With a good build on screen, a rebuild is invisible until it lands.
        if (!this.s.url && this.s.phase !== "starting") this.set({ phase: "starting" });
        return;
      case "stopped":
        this.set({ phase: "idle", url: null, builtVersion: null, showingLastGood: false });
        this.shownVersion = null;
        return;
      case "failed":
        this.toFailed(st.error?.length ? st.error : [{ severity: "error", message: st.message ?? "The preview failed to build." }], st.message ?? null);
        return;
      case "ready": {
        if (!st.url && !this.s.url) return;
        const url = st.url ?? this.s.url;
        const builtVersion = v ?? this.s.draftVersion;
        const first = this.s.url === null;
        this.set({ phase: "ready", url, builtVersion, errors: [], message: st.message ?? null, showingLastGood: false });
        // A frame mounted for the first time already loads the new build.
        if (first) this.shownVersion = builtVersion;
        else this.maybeReload();
        return;
      }
    }
  }

  private toFailed(errors: Diagnostic[], message: string | null) {
    this.set({ phase: "failed", errors, message, showingLastGood: this.s.url !== null });
  }

  private maybeReload() {
    const built = this.s.builtVersion;
    if (built === null) return;
    if (this.shownVersion !== null && built <= this.shownVersion) return; // frame already shows this build
    if (this.reloadTimer) this.timers.clear(this.reloadTimer);
    this.reloadTimer = this.timers.set(() => {
      this.reloadTimer = null;
      if (this.disposed) return;
      // Re-check: a later ready may have superseded this one, or the frame loaded it.
      if (this.s.builtVersion !== null && (this.shownVersion === null || this.s.builtVersion > this.shownVersion)) {
        this.shownVersion = this.s.builtVersion; // optimistic: prevents a double reload on the next ready
        this.set({ reloadKey: this.s.reloadKey + 1 });
      }
    }, this.reloadMs);
  }
}
