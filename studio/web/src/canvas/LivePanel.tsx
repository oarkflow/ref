import { useMemo, useState } from "react";
import type { BlockNode } from "../api/types";
import { unquote } from "../lib/bcl";
import { blocksOf, fieldOf } from "../lib/paths";
import { Icon } from "./icons";
import { useLiveRun, useLiveStore, type LiveRun } from "./live";
import { friendlyKind } from "./labels";

/** A route that runs the intent, read from the app's route blocks. */
export interface RunRoute {
  id: string;
  method: string;
  path: string;
}

/** Every route in `trees` that runs `intent`. */
export function routesFor(trees: Record<string, BlockNode[]>, intent: string): RunRoute[] {
  const out: RunRoute[] = [];
  const walk = (nodes: BlockNode[] | undefined) => {
    for (const n of nodes ?? []) {
      if (n.kind !== "block") continue;
      if (n.type === "route") {
        const target = unquote(fieldOf(n, "intent")?.raw ?? "");
        const path = unquote(fieldOf(n, "path")?.raw ?? "");
        if (target === intent && path) {
          const raw = (fieldOf(n, "method")?.raw ?? "GET").trim();
          out.push({ id: n.id ?? path, method: (unquote(raw) ?? raw).toUpperCase(), path });
        }
      } else if (blocksOf(n, "route").length) walk(n.children);
    }
  };
  for (const tree of Object.values(trees)) walk(tree);
  return out;
}

export interface RunResult {
  status: number;
  ms: number;
  error?: string;
}

/** Fills `{name}` / `:name` segments from a JSON object of inputs, so a route like /orders/{id} can be run. */
export function fillPath(path: string, inputs: Record<string, unknown>): string {
  return path.replace(/\{([^}]+)\}|:([A-Za-z_]\w*)/g, (m, a?: string, b?: string) => {
    const v = inputs[(a ?? b)!];
    return v === undefined ? m : encodeURIComponent(String(v));
  });
}

const label = (run: LiveRun | undefined) =>
  !run ? "Not run yet" : !run.finished ? "Running…" : run.error ? "Failed" : `Done in ${fmtMs(run.ms)}`;

export const fmtMs = (ms?: number) => (ms === undefined ? "" : ms < 1 ? "<1 ms" : ms < 1000 ? `${Math.round(ms * 10) / 10} ms` : `${(ms / 1000).toFixed(2)} s`);

export function LivePanel({
  intent, stepLabel, routes, canRun, running, result, onRun, onSelect,
}: {
  intent: string;
  /** a step's display name */
  stepLabel(id: string): string;
  routes: RunRoute[];
  canRun: boolean;
  running: boolean;
  result: RunResult | null;
  onRun(route: RunRoute, inputs: Record<string, unknown>): void;
  onSelect(nodeId: string): void;
}) {
  const live = useLiveStore();
  const run = useLiveRun();
  const [open, setOpen] = useState(true);
  const [pick, setPick] = useState(0);
  const [text, setText] = useState("{}");
  const parsed = useMemo(() => {
    try {
      const v: unknown = JSON.parse(text || "{}");
      return v && typeof v === "object" && !Array.isArray(v) ? { ok: true as const, value: v as Record<string, unknown> } : { ok: false as const };
    } catch {
      return { ok: false as const };
    }
  }, [text]);
  const route = routes[Math.min(pick, routes.length - 1)];
  const steps = useMemo(() => Object.entries(run?.nodes ?? {}).sort((a, b) => a[1].order - b[1].order), [run]);
  const state = run ? (run.finished ? (run.error ? "failed" : "done") : "running") : "idle";

  return (
    <section className="cv-live" data-state={state} aria-label="Live run">
      <header>
        <button type="button" className="cv-livetoggle" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
          <span className={`cv-livedot ${state}`} aria-hidden />
          <strong>Live run</strong>
          <small>{label(run)}</small>
          <Icon name="chevron" size={12} />
        </button>
      </header>
      {open && (
        <div className="cv-livebody">
          {routes.length === 0 ? (
            <p className="cv-none">No route runs “{intent}” yet. Add a route that uses it, or call the intent from the app preview: steps still light up as they run.</p>
          ) : (
            <div className="cv-liverun">
              {routes.length > 1 && (
                <select aria-label="Route to call" value={pick} onChange={(e) => setPick(Number(e.target.value))}>
                  {routes.map((r, i) => <option key={r.id + i} value={i}>{r.method} {r.path}</option>)}
                </select>
              )}
              {routes.length === 1 && <code>{route!.method} {route!.path}</code>}
              <textarea
                aria-label="Inputs as JSON"
                spellCheck={false}
                rows={2}
                value={text}
                onChange={(e) => setText(e.target.value)}
                placeholder='{"id": 1}'
                aria-invalid={!parsed.ok}
              />
              <div className="cv-liveactions">
                <button type="button" className="primary" disabled={!canRun || running || !parsed.ok} onClick={() => parsed.ok && route && onRun(route, parsed.value)} title={canRun ? "Rebuild the preview from your changes, then call the route" : "You can look, not run"}>
                  <Icon name="play" size={11} /> {running ? "Running…" : "Run"}
                </button>
                <button type="button" onClick={() => live.clear()} disabled={!run}>Clear</button>
                {result && (
                  <span className={`cv-liveresult ${result.error || result.status >= 400 ? "bad" : "ok"}`} title={result.error}>
                    {result.error ? "No answer" : `HTTP ${result.status}`} · {fmtMs(result.ms)}
                  </span>
                )}
              </div>
              {!parsed.ok && <p className="cv-none">Inputs must be a JSON object.</p>}
            </div>
          )}
          {steps.length > 0 && (
            <ol className="cv-livesteps" aria-label="Steps in the order they started">
              {steps.map(([id, n]) => (
                <li key={id} data-status={n.status}>
                  <button type="button" onClick={() => onSelect(id)} title={n.error}>
                    <span className="mark" aria-hidden>{n.status === "running" ? <i className="spin" /> : n.status === "done" ? <Icon name="check" size={11} /> : <Icon name="x" size={11} />}</span>
                    <span className="name">{stepLabel(id)}</span>
                    <small>{n.status === "running" ? "…" : fmtMs(n.ms)}</small>
                  </button>
                  {n.error && <p className="err">{n.error}</p>}
                </li>
              ))}
            </ol>
          )}
          {run?.finished && run.error && <p className="err">The run stopped: {run.error}</p>}
          <small className="cv-livehint">Showing the latest run of this {friendlyKind("intent")}. Calls from the app preview appear here too.</small>
        </div>
      )}
    </section>
  );
}
