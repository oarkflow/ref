import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ApiError } from "../api/client";
import type { BlockNode, RecordedRequest } from "../api/types";
import { useStudio, useStudioStore } from "../state/context";
import {
  filterRequests, formatDuration, resolveRoute, rowKey, routesOf, statusClass, truncate,
  type ConsoleFilter, type KindFilter, type RouteRef,
} from "./console";
import type { SessionState } from "./session";

const POLL_MS = 2000;

interface Props {
  draftId: string;
  state: SessionState;
  prefixUrl: string | null;
  /** The console tab is visible: only then is it worth polling. */
  active: boolean;
  onCount?(n: number): void;
}

export function RequestConsole({ draftId, state, prefixUrl, active, onCount }: Props) {
  const store = useStudioStore();
  const nav = useStudio((s) => s.nav);
  const [rows, setRows] = useState<RecordedRequest[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [live, setLive] = useState(true);
  const [filter, setFilter] = useState<ConsoleFilter>({ text: "", kind: "all", errorsOnly: false });
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [note, setNote] = useState<string | null>(null);
  const stopped = useRef(false); // a 501 will not fix itself
  const deep = useRef<Record<string, BlockNode[]>>({});
  const ready = state.phase === "ready" || (state.phase === "failed" && state.showingLastGood);

  const load = useCallback(async () => {
    if (stopped.current) return;
    try {
      const list = await store.getState().api.previewRequests(draftId);
      setRows(Array.isArray(list) ? list : []);
      setError(null);
    } catch (e) {
      if (e instanceof ApiError && (e.status === 501 || e.status === 404)) {
        stopped.current = true;
        setError(e.status === 501 ? "Live preview is not configured on this server." : "No preview is running for this draft.");
      } else {
        setError(e instanceof Error ? e.message : String(e));
      }
    }
  }, [store, draftId]);

  // Refresh when the tab opens and after every new build.
  useEffect(() => {
    if (active && ready) void load();
  }, [active, ready, state.reloadKey, load]);

  // Live refresh while the tab is visible.
  useEffect(() => {
    if (!active || !ready || !live) return;
    const h = setInterval(() => {
      if (typeof document === "undefined" || document.visibilityState !== "hidden") void load();
    }, POLL_MS);
    return () => clearInterval(h);
  }, [active, ready, live, load]);

  const visible = useMemo(() => filterRequests(rows, filter), [rows, filter]);
  const total = useMemo(() => filterRequests(rows, { text: "", kind: "all", errorsOnly: false, since: filter.since }).length, [rows, filter.since]);
  useEffect(() => onCount?.(total), [total, onCount]);

  const jump = async (req: RecordedRequest) => {
    if (req.kind !== "request") return;
    setNote(null);
    const prefixPath = prefixUrl ? new URL(prefixUrl).pathname : undefined;
    // Cheap first: the recorder names the route, and top-level blocks are in the navigator.
    const fromNav: RouteRef[] = Object.entries(nav).flatMap(([file, list]) => routesOf(file, list));
    let hit = resolveRoute(req, fromNav, prefixPath);
    if (!hit) {
      // Otherwise match method and path against the routes' fields, which needs the full trees.
      const files = Object.entries(nav).filter(([, list]) => list.some((n) => n.type === "route")).map(([f]) => f);
      const st = store.getState();
      const all: RouteRef[] = [];
      for (const file of files) {
        let tree = st.trees[file] ?? deep.current[file];
        if (!tree) {
          try {
            tree = await st.api.fileTree(draftId, file);
            deep.current[file] = tree;
          } catch {
            continue;
          }
        }
        all.push(...routesOf(file, tree));
      }
      hit = resolveRoute(req, all, prefixPath);
    }
    if (!hit) {
      setNote(`No route in this draft matches ${req.method ?? ""} ${req.url ?? ""}.`.replace("  ", " "));
      return;
    }
    await store.getState().select(hit.file, hit.path);
  };

  const toggle = (k: string) =>
    setExpanded((cur) => {
      const n = new Set(cur);
      if (!n.delete(k)) n.add(k);
      return n;
    });

  const kinds: { id: KindFilter; label: string }[] = [
    { id: "all", label: "All" },
    { id: "request", label: "Requests" },
    { id: "outbound", label: "Outbound" },
  ];

  return (
    <div className="console">
      <div className="console-tools">
        <label className="sr-only" htmlFor="console-search">Filter requests</label>
        <input id="console-search" type="search" placeholder="Filter: method, path, route, status…" value={filter.text} onChange={(e) => setFilter({ ...filter, text: e.target.value })} />
        <div className="segmented" role="group" aria-label="Kind">
          {kinds.map((k) => (
            <button key={k.id} type="button" aria-pressed={filter.kind === k.id} onClick={() => setFilter({ ...filter, kind: k.id })}>{k.label}</button>
          ))}
        </div>
        <label className="switch"><input type="checkbox" checked={filter.errorsOnly} onChange={(e) => setFilter({ ...filter, errorsOnly: e.target.checked })} /> <span>Errors only</span></label>
        <label className="switch"><input type="checkbox" checked={live} onChange={(e) => setLive(e.target.checked)} /> <span>Live</span></label>
        <button type="button" onClick={() => void load()} title="Refresh now">Refresh</button>
        <button type="button" onClick={() => setFilter({ ...filter, since: rows.length ? Math.max(...rows.map((r) => Date.parse(r.at))) : Date.now() })} disabled={!rows.length} title="Hide everything recorded so far">Clear</button>
      </div>
      {error && <p className="problem error" role="alert">{error}</p>}
      {note && <p className="preview-note" role="status">{note}</p>}
      {!error && !ready && <p className="empty">The console fills once the preview is running.</p>}
      {!error && ready && visible.length === 0 && (
        <p className="empty">{rows.length ? "Nothing matches the filter." : "No requests yet. Use the preview or send a request to it."}</p>
      )}
      {visible.length > 0 && (
        <div className="console-scroll">
          <table className="console-table">
            <caption className="sr-only">Requests served and outbound calls stubbed by the preview, newest first</caption>
            <thead>
              <tr><th>Time</th><th>Method</th><th>Path / target</th><th>Status</th><th className="duration">Duration</th><th>Route</th><th className="intent">Intent</th></tr>
            </thead>
            <tbody>
              {visible.map((r, i) => {
                const key = rowKey(r, i);
                const d = r.detail ?? {};
                const isReq = r.kind === "request";
                return (
                  <tr
                    key={key}
                    className={`${isReq ? "jumpable" : "outbound"} status-${statusClass(r.status)}`}
                    tabIndex={isReq ? 0 : undefined}
                    onClick={isReq ? () => void jump(r) : undefined}
                    onKeyDown={isReq ? (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); void jump(r); } } : undefined}
                    title={isReq ? "Show this route in the editor" : undefined}
                  >
                    <td className="time">{new Date(r.at).toLocaleTimeString()}</td>
                    <td className="method">{r.method ?? (isReq ? "" : "OUT")}</td>
                    <td className="url">
                      <code>{r.url}</code>
                      {!isReq && (
                        <Payload text={d.preview ?? ""} channel={d.channel} bytes={d.bytes} open={expanded.has(key)} onToggle={() => toggle(key)} />
                      )}
                    </td>
                    <td className="status">{isReq ? r.status ?? "" : <span className="tag">{d.channel ?? "stubbed"}</span>}</td>
                    <td className="duration">{formatDuration(r.durationMs)}</td>
                    <td className="route">{d.route ?? ""}</td>
                    <td className="intent">{d.intent ?? ""}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function Payload({ text, channel, bytes, open, onToggle }: { text: string; channel?: string; bytes?: number; open: boolean; onToggle(): void }) {
  if (!text) return channel || bytes ? <span className="payload muted">{[channel, bytes ? `${bytes} bytes` : ""].filter(Boolean).join(" · ")}</span> : null;
  const t = truncate(text, 100);
  return (
    <div className="payload">
      <pre onClick={(e) => e.stopPropagation()}>{open ? text : t.text}</pre>
      {t.cut && (
        <button type="button" className="link" aria-expanded={open} onClick={(e) => { e.stopPropagation(); onToggle(); }}>
          {open ? "Show less" : "Show all"}
        </button>
      )}
    </div>
  );
}
