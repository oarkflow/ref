import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api/client";
import type { Op, OpResult } from "../api/types";
import { OpQueue, type RollbackInfo } from "./opQueue";

const set = (path: string, value: string, file = "a.bcl"): Op => ({ op: "setField", file, path, value });
const result = (version: number): OpResult => ({ version, applied: 1, diagnostics: [], changed: ["a.bcl"] });

function harness(send: (ops: Op[], v: number) => Promise<OpResult>) {
  let version = 1;
  const applied: { r: OpResult; ops: Op[] }[] = [];
  const rollbacks: RollbackInfo[] = [];
  const pendings: Op[][] = [];
  const q = new OpQueue({
    delay: 100,
    send: (ops, v) => send(ops, v),
    version: () => version,
    onApplied: (r, ops) => { version = r.version; applied.push({ r, ops }); },
    onRollback: (i) => rollbacks.push(i),
    onPending: (p) => pendings.push(p),
  });
  return { q, applied, rollbacks, pendings, setVersion: (v: number) => (version = v) };
}

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe("OpQueue batching", () => {
  it("sends edits made within the quiet period as one batch", async () => {
    const send = vi.fn(async (_o: Op[], _v: number) => result(2));
    const h = harness(send);
    h.q.enqueue([set("r/a", "1")]);
    await vi.advanceTimersByTimeAsync(50);
    h.q.enqueue([set("r/b", "2")]);
    expect(send).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(100);
    expect(send).toHaveBeenCalledTimes(1);
    expect(send.mock.calls[0]![0]).toHaveLength(2);
    expect(send.mock.calls[0]![1]).toBe(1);
    expect(h.applied[0]!.r.version).toBe(2);
  });

  it("keeps only the last value typed into the same field", async () => {
    const send = vi.fn(async (_o: Op[], _v: number) => result(2));
    const h = harness(send);
    for (const v of ['"a"', '"ab"', '"abc"']) h.q.enqueue([set("r/x", v)]);
    await vi.advanceTimersByTimeAsync(200);
    expect(send.mock.calls[0]![0]).toEqual([set("r/x", '"abc"')]);
  });

  it("does not coalesce across a structural op that may depend on the earlier value", async () => {
    const send = vi.fn(async (_o: Op[], _v: number) => result(2));
    const h = harness(send);
    h.q.enqueue([set("r/x", "1"), { op: "renameBlock", file: "a.bcl", path: "r", newId: "s" }, set("r/x", "2")]);
    await vi.advanceTimersByTimeAsync(200);
    expect(send.mock.calls[0]![0]).toHaveLength(3);
  });

  it("sends one batch at a time, each with the newest version", async () => {
    let release!: () => void;
    const send = vi.fn((ops: Op[], _v: number) =>
      new Promise<OpResult>((res) => {
        release = () => res(result(ops.length === 1 && ops[0]!.op === "setField" && (ops[0] as { value: string }).value === "1" ? 2 : 3));
      }),
    );
    const h = harness(send);
    h.q.enqueue([set("r/a", "1")]);
    await vi.advanceTimersByTimeAsync(150);
    expect(send).toHaveBeenCalledTimes(1);
    h.q.enqueue([set("r/b", "2")]); // arrives while the first is in flight
    await vi.advanceTimersByTimeAsync(500);
    expect(send).toHaveBeenCalledTimes(1);
    release();
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(0);
    expect(send).toHaveBeenCalledTimes(2);
    expect(send.mock.calls[1]![1]).toBe(2); // version returned by the first batch
    release();
    await h.q.idle();
    expect(h.applied.map((a) => a.r.version)).toEqual([2, 3]);
  });

  it("reports pending ops for the optimistic overlay", async () => {
    const h = harness(async () => result(2));
    h.q.enqueue([set("r/a", "1")]);
    expect(h.q.pending()).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(200);
    await h.q.idle();
    expect(h.q.pending()).toHaveLength(0);
  });
});

describe("OpQueue rollback", () => {
  it("rolls back everything on a stale 409", async () => {
    const h = harness(async () => { throw new ApiError(409, "stale", "changed elsewhere"); });
    h.q.enqueue([set("r/a", "1")]);
    await vi.advanceTimersByTimeAsync(150);
    await h.q.idle();
    expect(h.rollbacks).toHaveLength(1);
    expect(h.rollbacks[0]!.reason).toBe("stale");
    expect(h.rollbacks[0]!.ops).toEqual([set("r/a", "1")]);
    expect(h.applied).toHaveLength(0);
    expect(h.q.pending()).toHaveLength(0);
  });

  it("drops edits queued behind a rejected batch (422)", async () => {
    let reject!: (e: unknown) => void;
    const h = harness(() => new Promise<OpResult>((_res, rej) => { reject = rej; }));
    h.q.enqueue([set("r/a", "1")]);
    await vi.advanceTimersByTimeAsync(150); // first batch is now in flight
    h.q.enqueue([set("r/b", "2")]); // queued behind it
    reject(new ApiError(422, "op_failed", "op 0 (setField): not found"));
    await h.q.idle();
    expect(h.rollbacks).toHaveLength(1);
    expect(h.rollbacks[0]!.reason).toBe("rejected");
    expect(h.rollbacks[0]!.ops).toEqual([set("r/a", "1"), set("r/b", "2")]);
    expect(h.applied).toHaveLength(0);
    expect(h.q.pending()).toHaveLength(0);
  });

  it("treats a network failure as a rollback too", async () => {
    const h = harness(async () => { throw new TypeError("Failed to fetch"); });
    h.q.enqueue([set("r/a", "1")]);
    await vi.advanceTimersByTimeAsync(150);
    await h.q.idle();
    expect(h.rollbacks[0]!.reason).toBe("network");
  });

  it("works again after a rollback", async () => {
    let n = 0;
    const h = harness(async () => {
      if (n++ === 0) throw new ApiError(409, "stale", "x");
      return result(9);
    });
    h.q.enqueue([set("r/a", "1")]);
    await vi.advanceTimersByTimeAsync(150);
    await h.q.idle();
    h.q.enqueue([set("r/a", "2")]);
    await vi.advanceTimersByTimeAsync(150);
    await h.q.idle();
    expect(h.applied).toHaveLength(1);
  });
});
