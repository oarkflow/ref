import type { Op, OpResult } from "../api/types";
import { ApiError } from "../api/client";

export type RollbackReason = "stale" | "rejected" | "network";

export interface RollbackInfo {
  reason: RollbackReason;
  error: unknown;
  /** Every op that was queued or in flight, in order; none of them was applied. */
  ops: Op[];
}

export interface OpQueueOptions {
  /** Sends one atomic batch. */
  send: (ops: Op[], ifVersion: number) => Promise<OpResult>;
  /** The draft version to send as ifVersion. */
  version: () => number;
  onApplied: (result: OpResult, ops: Op[]) => void;
  onRollback: (info: RollbackInfo) => void;
  /** Called whenever the set of pending ops changes (for optimistic overlays). */
  onPending?: (pending: Op[]) => void;
  /** Quiet period before a batch is sent (ms). 0 sends on the next tick. */
  delay?: number;
}

const key = (o: Op) => ("path" in o ? `${o.file}|${o.path}` : "");

/**
 * Batches edits into atomic /ops calls.
 *
 *  - Edits made within `delay` ms of each other go out as one batch.
 *  - Within a batch, a later setField/removeField on the same path replaces the
 *    earlier one, so typing in a field sends only its final value.
 *  - One batch is in flight at a time, always sent with the latest known version.
 *  - If a batch fails (409 stale, 422 rejected, or the network), everything
 *    queued behind it is dropped too: those edits were made against a view that
 *    no longer exists. onRollback lets the caller restore its optimistic state.
 */
export class OpQueue {
  private buffer: Op[] = [];
  private inflight: Op[] = [];
  private timer: ReturnType<typeof setTimeout> | null = null;
  private waiters: (() => void)[] = [];
  private epoch = 0;

  constructor(private o: OpQueueOptions) {}

  /** Ops not yet acknowledged: in flight first, then queued. */
  pending(): Op[] {
    return [...this.inflight, ...this.buffer];
  }

  enqueue(ops: Op[]) {
    for (const op of ops) this.add(op);
    this.o.onPending?.(this.pending());
    this.schedule();
  }

  private add(op: Op) {
    if (op.op === "setField" || op.op === "removeField") {
      const k = key(op);
      const i = this.buffer.findIndex((b) => (b.op === "setField" || b.op === "removeField") && key(b) === k);
      if (i >= 0) {
        // Only coalesce when nothing in between depends on the earlier value.
        const between = this.buffer.slice(i + 1);
        if (!between.some((b) => b.op !== "setField" && b.op !== "removeField")) {
          this.buffer.splice(i, 1);
        }
      }
    }
    this.buffer.push(op);
  }

  private schedule() {
    if (this.inflight.length) return; // sent when the current batch settles
    if (this.timer) clearTimeout(this.timer);
    this.timer = setTimeout(() => void this.flush(), this.o.delay ?? 250);
  }

  /** Sends what is queued now (used by tests and before undo/propose). */
  async flush(): Promise<void> {
    if (this.timer) {
      clearTimeout(this.timer);
      this.timer = null;
    }
    if (this.inflight.length || !this.buffer.length) return;
    const batch = this.buffer;
    this.buffer = [];
    this.inflight = batch;
    const epoch = this.epoch;
    try {
      const result = await this.o.send(batch, this.o.version());
      if (epoch !== this.epoch) return;
      this.inflight = [];
      this.o.onApplied(result, batch);
    } catch (error) {
      if (epoch !== this.epoch) return;
      const ops = [...batch, ...this.buffer];
      this.inflight = [];
      this.buffer = [];
      const reason: RollbackReason =
        error instanceof ApiError ? (error.isStale ? "stale" : "rejected") : "network";
      this.o.onPending?.([]);
      this.o.onRollback({ reason, error, ops });
      this.resolveIdle();
      return;
    }
    this.o.onPending?.(this.pending());
    if (this.buffer.length) await this.flush();
    else this.resolveIdle();
  }

  /** Resolves once nothing is queued or in flight. */
  idle(): Promise<void> {
    if (!this.buffer.length && !this.inflight.length) return Promise.resolve();
    return new Promise((r) => this.waiters.push(r));
  }

  private resolveIdle() {
    if (this.buffer.length || this.inflight.length) return;
    const w = this.waiters;
    this.waiters = [];
    w.forEach((r) => r());
  }

  /** Forgets everything (draft switched or store torn down). */
  reset() {
    this.epoch++;
    if (this.timer) clearTimeout(this.timer);
    this.timer = null;
    this.buffer = [];
    this.inflight = [];
    this.o.onPending?.([]);
    this.resolveIdle();
  }
}
