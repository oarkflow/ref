import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { useStudio, useStudioStore } from "../state/context";
import { PreviewSession, type SessionState } from "./session";

const IDLE: SessionState = {
  phase: "idle", url: null, builtVersion: null, draftVersion: 0, errors: [], message: null,
  stale: false, reloadKey: 0, showingLastGood: false,
};
const noopSubscribe = () => () => {};

// A generation costs memory server-side, so it is stopped when the panel
// closes or the draft changes. The stop is delayed: React's dev remount (and a
// quick off/on toggle) must not tear down a preview that is about to be
// asked for again.
const STOP_DELAY_MS = 3000;
const pendingStops = new Map<string, ReturnType<typeof setTimeout>>();

/**
 * Runs a PreviewSession for the open draft: builds on mount, rebuilds
 * (debounced) when the draft version changes, and feeds it the `preview` SSE
 * events. Returns null before the session exists.
 */
export function usePreviewSession(draftId: string | undefined, version: number | undefined) {
  const store = useStudioStore();
  const [session, setSession] = useState<PreviewSession | null>(null);
  const event = useStudio((s) => s.previewEvent);
  const seenSeq = useRef(0);

  useEffect(() => {
    if (!draftId) return;
    const api = store.getState().api;
    const pending = pendingStops.get(draftId);
    if (pending) {
      clearTimeout(pending);
      pendingStops.delete(draftId);
    }
    const s = new PreviewSession(api, draftId, store.getState().draft?.id === draftId ? store.getState().draft!.version : (version ?? 0));
    // Events from before this session existed describe an older generation.
    seenSeq.current = store.getState().previewEvent?.seq ?? 0;
    setSession(s);
    s.start();
    return () => {
      s.dispose();
      setSession((cur) => (cur === s ? null : cur));
      pendingStops.set(
        draftId,
        setTimeout(() => {
          pendingStops.delete(draftId);
          void api.stopPreview(draftId).catch(() => {});
        }, STOP_DELAY_MS),
      );
    };
    // The version is read once for the initial build; later changes go through setDraftVersion.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [draftId, store]);

  useEffect(() => {
    if (session && version !== undefined) session.setDraftVersion(version);
  }, [session, version]);

  useEffect(() => {
    if (!session || !event || event.draftId !== session.draftId || event.seq <= seenSeq.current) return;
    seenSeq.current = event.seq;
    session.onEvent(event.data);
  }, [session, event]);

  const state = useSyncExternalStore(session ? session.subscribe : noopSubscribe, session ? session.getState : () => IDLE);
  return { session, state };
}
