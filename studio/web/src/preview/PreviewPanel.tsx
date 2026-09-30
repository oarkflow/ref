import { useCallback, useEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type PointerEvent as ReactPointerEvent } from "react";
import { useStudio, useStudioStore } from "../state/context";
import { RequestConsole } from "./RequestConsole";
import { DEVICES, LIMITS, clampSize, layoutStore, usePreviewLayout, type Device } from "./layout";
import type { SessionState } from "./session";
import { appPathOf, confinePreviewUrl } from "./urlbar";
import { usePreviewSession } from "./usePreviewSession";

const PHASE_LABEL: Record<SessionState["phase"], string> = {
  idle: "Idle", starting: "Starting…", ready: "Ready", failed: "Build failed", unavailable: "Unavailable",
};

/** The dockable live-preview panel: page preview and request console. */
export function PreviewPanel() {
  const draft = useStudio((s) => s.draft);
  const dock = usePreviewLayout((s) => s.dock);
  const tab = usePreviewLayout((s) => s.tab);
  const device = usePreviewLayout((s) => s.device);
  const { state } = usePreviewSession(draft?.id, draft?.version);
  const [reloadNonce, setReloadNonce] = useState(0);
  const [requestCount, setRequestCount] = useState(0);
  const prefixUrl = state.url ? new URL(state.url, document.baseURI).href : null;

  if (!draft) return null;
  const set = layoutStore.getState().set;

  return (
    <aside className={`preview dock-${dock}`} aria-label="Live preview">
      <ResizeHandle />
      <div className="preview-main">
        <header className="preview-head">
          <div className="tabs" role="tablist" aria-label="Preview views">
            <button role="tab" type="button" id="ptab-preview" aria-selected={tab === "preview"} aria-controls="ppanel-preview" onClick={() => set({ tab: "preview" })}>Preview</button>
            <button role="tab" type="button" id="ptab-console" aria-selected={tab === "console"} aria-controls="ppanel-console" onClick={() => set({ tab: "console" })}>
              Console{requestCount ? <span className="count">{requestCount}</span> : null}
            </button>
          </div>
          <StatusChip state={state} />
          <div className="preview-tools">
            <div className="segmented" role="group" aria-label="Device size">
              {(Object.keys(DEVICES) as Device[]).map((d) => (
                <button key={d} type="button" aria-pressed={device === d} onClick={() => set({ device: d })} title={`${DEVICES[d].label}${DEVICES[d].width ? ` (${DEVICES[d].width}px)` : ""}`}>
                  {DEVICES[d].label}
                </button>
              ))}
            </div>
            <button type="button" onClick={() => setReloadNonce((n) => n + 1)} disabled={!state.url} title="Reload the preview" aria-label="Reload preview">⟳</button>
            <button type="button" onClick={() => set({ dock: dock === "right" ? "bottom" : "right" })} title={dock === "right" ? "Dock at the bottom" : "Dock on the right"} aria-label={dock === "right" ? "Dock at the bottom" : "Dock on the right"}>
              {dock === "right" ? "⬓" : "◨"}
            </button>
          </div>
        </header>

        <div id="ppanel-preview" role="tabpanel" aria-labelledby="ptab-preview" hidden={tab !== "preview"} className="preview-body">
          <PreviewView state={state} prefixUrl={prefixUrl} reloadNonce={reloadNonce} device={device} />
        </div>
        <div id="ppanel-console" role="tabpanel" aria-labelledby="ptab-console" hidden={tab !== "console"} className="preview-body">
          <RequestConsole draftId={draft.id} state={state} prefixUrl={prefixUrl} active={tab === "console"} onCount={setRequestCount} />
        </div>
      </div>
    </aside>
  );
}

function StatusChip({ state }: { state: SessionState }) {
  const label = state.phase === "ready" && state.stale ? "Updating…" : state.phase === "failed" && state.showingLastGood ? "Build failed · showing last good" : PHASE_LABEL[state.phase];
  const cls = state.phase === "ready" && !state.stale ? "ok" : state.phase === "failed" ? "bad" : state.phase === "unavailable" ? "muted" : "warn";
  return (
    <span className={`chip ${cls}`} role="status" aria-live="polite" data-phase={state.phase}>
      <span className="dot" aria-hidden="true" />
      {label}
    </span>
  );
}

// ---- the page view ----------------------------------------------------------

function PreviewView({ state, prefixUrl, reloadNonce, device }: { state: SessionState; prefixUrl: string | null; reloadNonce: number; device: Device }) {
  const store = useStudioStore();
  const frameRef = useRef<HTMLIFrameElement>(null);
  const [src, setSrc] = useState<string | null>(null);
  const [address, setAddress] = useState("/");
  const [note, setNote] = useState<string | null>(null);
  const lastGood = useRef<string | null>(null); // last href confined to the prefix
  const restore = useRef<{ x: number; y: number } | null>(null);
  const lastReloadKey = useRef(state.reloadKey);
  const lastNonce = useRef(reloadNonce);

  // First build: point the frame at the preview root.
  useEffect(() => {
    if (prefixUrl && src === null) {
      setSrc(prefixUrl);
      lastGood.current = prefixUrl;
    }
    if (!prefixUrl) setSrc(null);
  }, [prefixUrl, src]);

  const reload = useCallback(() => {
    const f = frameRef.current;
    if (!f) return;
    try {
      const w = f.contentWindow!;
      restore.current = { x: w.scrollX, y: w.scrollY };
      w.location.reload();
    } catch {
      // Cross-origin or detached: fall back to reassigning src (scroll is lost).
      restore.current = null;
      if (lastGood.current) f.src = lastGood.current;
    }
  }, []);

  useEffect(() => {
    if (state.reloadKey !== lastReloadKey.current) {
      lastReloadKey.current = state.reloadKey;
      reload();
    }
  }, [state.reloadKey, reload]);

  useEffect(() => {
    if (reloadNonce !== lastNonce.current) {
      lastNonce.current = reloadNonce;
      reload();
    }
  }, [reloadNonce, reload]);

  const onLoad = () => {
    const f = frameRef.current;
    if (!f || !prefixUrl) return;
    let href: string;
    try {
      href = f.contentWindow!.location.href;
    } catch {
      return; // cross-origin: leave the bar alone
    }
    if (href === "about:blank") return;
    const app = appPathOf(href, prefixUrl);
    if (app === null) {
      // A link or redirect took the frame out of the preview: put it back.
      setNote("Navigation outside the preview was blocked.");
      if (lastGood.current) f.contentWindow!.location.replace(lastGood.current);
      return;
    }
    setNote(null);
    lastGood.current = href;
    setAddress(app);
    const r = restore.current;
    restore.current = null;
    if (r) {
      try {
        f.contentWindow!.scrollTo(r.x, r.y);
      } catch {
        /* ignore */
      }
    }
  };

  const go = (input: string) => {
    if (!prefixUrl) return;
    const c = confinePreviewUrl(input, prefixUrl);
    if (!c) {
      setNote("That address is outside the preview. Enter a path of the app, such as /todos.");
      return;
    }
    setNote(null);
    setAddress(c.appPath);
    const f = frameRef.current;
    try {
      f?.contentWindow?.location.replace(c.href);
    } catch {
      if (f) f.src = c.href;
    }
  };

  const jumpToDiagnostic = (i: number) => void store.getState().selectFromDiagnostic(state.errors[i]!);

  if (state.phase === "unavailable") {
    return <p className="empty preview-msg" role="alert">{state.message ?? "Live preview is not available."}</p>;
  }
  if (!prefixUrl) {
    if (state.phase === "failed") return <FailureList state={state} onJump={jumpToDiagnostic} />;
    return <p className="empty preview-msg" aria-busy="true">Starting preview…</p>;
  }

  const width = DEVICES[device].width;
  return (
    <>
      <form className="urlbar" onSubmit={(e) => { e.preventDefault(); go(address); }}>
        <label className="sr-only" htmlFor="preview-address">Preview address</label>
        <input id="preview-address" value={address} onChange={(e) => setAddress(e.target.value)} spellCheck={false} autoComplete="off" />
        <button type="submit">Go</button>
        <a className="button" href={lastGood.current ?? prefixUrl} target="_blank" rel="noreferrer" title="Open in a new tab" aria-label="Open in a new tab">↗</a>
      </form>
      {note && <p className="preview-note" role="status">{note}</p>}
      {state.phase === "failed" && <FailureList state={state} onJump={jumpToDiagnostic} banner />}
      <div className={`frame-wrap device-${device}`}>
        <div className="frame" style={width ? { width, maxWidth: "100%" } : undefined}>
          {src && <iframe ref={frameRef} title="Live preview" src={src} onLoad={onLoad} sandbox="allow-scripts allow-forms allow-same-origin allow-popups" />}
        </div>
      </div>
    </>
  );
}

function FailureList({ state, onJump, banner }: { state: SessionState; onJump(i: number): void; banner?: boolean }) {
  return (
    <div className={`preview-fail${banner ? " banner" : ""}`} role="alert">
      <p className="problem error">
        {banner ? "The latest change did not build. Showing the last good preview." : "The preview failed to build."}
      </p>
      <ul>
        {state.errors.map((d, i) => {
          const where = d.file ? `${d.file}${d.line ? `:${d.line}` : ""}` : d.path ?? "";
          const jumpable = !!(d.file || d.path);
          return (
            <li key={i}>
              {jumpable ? (
                <button type="button" className="link" onClick={() => onJump(i)} title="Show in the editor">{where}</button>
              ) : null}{" "}
              <span>{d.message}</span>
            </li>
          );
        })}
      </ul>
    </div>
  );
}

// ---- resizing --------------------------------------------------------------

function ResizeHandle() {
  const dock = usePreviewLayout((s) => s.dock);
  const size = usePreviewLayout((s) => (s.dock === "right" ? s.sizeRight : s.sizeBottom));
  const drag = useRef<{ start: number; size: number } | null>(null);
  const right = dock === "right";

  const onDown = (e: ReactPointerEvent<HTMLDivElement>) => {
    (e.currentTarget as HTMLDivElement).setPointerCapture?.(e.pointerId);
    drag.current = { start: right ? e.clientX : e.clientY, size };
    document.body.classList.add("resizing");
  };
  const onMove = (e: ReactPointerEvent<HTMLDivElement>) => {
    const d = drag.current;
    if (!d) return;
    // The panel grows as the handle moves away from its docked edge.
    const delta = (right ? e.clientX : e.clientY) - d.start;
    layoutStore.getState().resize(d.size - delta);
  };
  const end = () => {
    drag.current = null;
    document.body.classList.remove("resizing");
  };
  const onKey = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    const grow = right ? "ArrowLeft" : "ArrowUp";
    const shrink = right ? "ArrowRight" : "ArrowDown";
    const step = e.shiftKey ? 80 : 20;
    if (e.key === grow) layoutStore.getState().resize(size + step);
    else if (e.key === shrink) layoutStore.getState().resize(size - step);
    else return;
    e.preventDefault();
  };

  const limits = LIMITS[dock];
  return (
    <div
      className={`resize-handle ${right ? "vertical" : "horizontal"}`}
      role="separator"
      aria-orientation={right ? "vertical" : "horizontal"}
      aria-label="Resize preview"
      aria-valuenow={size}
      aria-valuemin={limits.min}
      aria-valuemax={clampSize(dock, limits.max)}
      tabIndex={0}
      onPointerDown={onDown}
      onPointerMove={onMove}
      onPointerUp={end}
      onPointerCancel={end}
      onKeyDown={onKey}
    />
  );
}
