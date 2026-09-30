// Small panels that float over the map: what the colours mean, the filters, the warnings,
// and the guided "Follow the user" trace.
import { useEffect, useMemo, useState } from "react";
import { JIcon } from "./icons";
import type { Filters, Journey } from "./model";
import { pagesToFollow, tracesFrom, type Trace, type TraceStep } from "./trace";
import { groupWarnings } from "./text";
import type { FlowWarning } from "./types";

export function Legend({ onClose }: { onClose(): void }) {
  const items: [string, string, string][] = [
    ["flow", "Page", "What visitors see. Its links, forms and buttons are listed inside it."],
    ["compute", "Shared parts", "The navbar and layouts that show on many pages."],
    ["plug", "Request", "What the app receives when something is clicked or sent."],
    ["process", "Logic flow", "The steps the app runs to do the work."],
    ["data", "Connection", "A database, queue, email or other service the work uses."],
    ["end-failed", "Nothing answers", "A button or link that goes somewhere the app doesn’t serve."],
  ];
  return (
    <div className="cv-legend jn-legend" role="dialog" aria-label="What do the colours and lines mean?">
      <h3>How to read this map</h3>
      <p className="jn-flow-hint">Read it left to right: a page, what you can do on it, the request that sends, the logic flow that runs, and what it uses.</p>
      <ul className="jn-legend-list">
        {items.map(([tone, name, text]) => (
          <li key={name}><i className={`sw tone-${tone}`} /><span><strong>{name}</strong><em>{text}</em></span></li>
        ))}
      </ul>
      <ul className="jn-legend-lines">
        <li><span className="line" /> <strong>goes to / sends / runs / uses</strong></li>
        <li><span className="line back" /> <strong>on success, goes to</strong> the page you land on</li>
        <li><span className="line faint" /> <strong>shows</strong> a page - drawn when you select it</li>
      </ul>
      <button type="button" onClick={onClose}>Got it</button>
    </div>
  );
}

export function FiltersPanel({ filters, set, hiddenEndpoints, onClose }: { filters: Filters; set(p: Partial<Filters>): void; hiddenEndpoints: number; onClose(): void }) {
  const row = (key: keyof Filters, title: string, hint: string) => (
    <label className="jn-check">
      <input type="checkbox" checked={filters[key]} onChange={(e) => set({ [key]: e.target.checked } as Partial<Filters>)} />
      <span><strong>{title}</strong><em>{hint}</em></span>
    </label>
  );
  return (
    <div className="cv-legend jn-filters" role="dialog" aria-label="What to show">
      <h3>What to show</h3>
      {row("showOrphans", hiddenEndpoints > 0 ? `Also show ${hiddenEndpoints} endpoints no page uses` : "Also show endpoints no page uses", "APIs, web hooks and other addresses that no page button reaches.")}
      {row("hideShared", "Hide the shared navbar and layouts", "Keeps the map to what is particular to each page.")}
      {row("hideConnections", "Hide connections", "Stops at the logic flow, without the database, queue or email.")}
      {row("onlyProblems", "Only show problems", "Just what needs fixing, and what sits next to it.")}
      <button type="button" onClick={onClose}>Done</button>
    </div>
  );
}

export function WarningsPanel({ warnings, onOpen, onClose }: { warnings: FlowWarning[]; onOpen(w: FlowWarning): void; onClose(): void }) {
  const groups = useMemo(() => groupWarnings(warnings), [warnings]);
  return (
    <div className="cv-legend jn-warnings" role="dialog" aria-label="Notes about this map">
      <h3>Notes about this map</h3>
      {groups.length === 0 ? <p className="jn-muted">Nothing to report. Everything on the map is connected.</p> : (
        <ul className="jn-wlist">
          {groups.map((g) => (
            <li key={g.code} className={g.severity}>
              <div className="jn-whead"><JIcon name={g.severity === "warning" ? "alert" : "info"} size={13} /><strong>{g.title}</strong><span className="n">{g.items.length}</span></div>
              {g.hint && <p>{g.hint}</p>}
              <ul>
                {g.items.slice(0, 4).map((w, i) => (
                  <li key={i}>
                    {w.file ? <button type="button" className="link" onClick={() => onOpen(w)}>{w.file.replace(/^templates\//, "")}{w.line ? `:${w.line}` : ""}</button> : <span>{w.message}</span>}
                  </li>
                ))}
                {g.items.length > 4 && <li className="jn-muted">and {g.items.length - 4} more</li>}
              </ul>
            </li>
          ))}
        </ul>
      )}
      <button type="button" onClick={onClose}>Close</button>
    </div>
  );
}

// ---- Follow the user ----------------------------------------------------------------------------------

export interface TraceProps {
  journey: Journey;
  /** A page to start on, when the trace is opened from a page. */
  startPage?: string | null;
  onStep(step: TraceStep | null): void;
  onClose(): void;
}

export function TracePanel({ journey, startPage, onStep, onClose }: TraceProps) {
  const pages = useMemo(() => pagesToFollow(journey), [journey]);
  const [pageId, setPageId] = useState<string>(() => (startPage && pages.some((p) => p.id === startPage) ? startPage : pages[0]?.id ?? ""));
  const traces = useMemo<Trace[]>(() => (pageId ? tracesFrom(journey, pageId) : []), [journey, pageId]);
  const [ti, setTi] = useState(0);
  const [si, setSi] = useState(0);
  const trace = traces[Math.min(ti, Math.max(0, traces.length - 1))];
  const step = trace?.steps[Math.min(si, (trace?.steps.length ?? 1) - 1)];

  useEffect(() => { setTi(0); setSi(0); }, [pageId]);
  useEffect(() => { setSi(0); }, [ti]);
  useEffect(() => { onStep(step ?? null); }, [step?.id, step?.via]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => () => onStep(null), []); // eslint-disable-line react-hooks/exhaustive-deps

  if (pages.length === 0) {
    return (
      <div className="jn-trace" role="region" aria-label="Follow the user">
        <div className="jn-trace-head"><strong>Follow the user</strong><button type="button" className="jn-x" aria-label="Close" onClick={onClose}><JIcon name="x" size={14} /></button></div>
        <p className="jn-muted">No page has anything to click yet.</p>
      </div>
    );
  }
  const last = (trace?.steps.length ?? 1) - 1;
  return (
    <div className="jn-trace" role="region" aria-label="Follow the user">
      <div className="jn-trace-head">
        <strong><JIcon name="route" size={13} /> Follow the user</strong>
        <button type="button" className="jn-x" aria-label="Close" onClick={onClose}><JIcon name="x" size={14} /></button>
      </div>
      <div className="jn-trace-pick">
        <label>Start on
          <select value={pageId} onChange={(e) => setPageId(e.target.value)} aria-label="Page to start on">
            {pages.map((p) => <option key={p.id} value={p.id}>{p.title}</option>)}
          </select>
        </label>
        <label>What they click
          <select value={ti} onChange={(e) => setTi(Number(e.target.value))} aria-label="What they click">
            {traces.map((t, i) => <option key={t.id} value={i}>{t.title}</option>)}
          </select>
        </label>
      </div>
      {trace && step && (
        <>
          <ol className="jn-steps" aria-label="Steps">
            {trace.steps.map((s, i) => (
              <li key={s.id + i} className={`${i === si ? "on" : ""} ${i < si ? "done" : ""}`}>
                <button type="button" aria-label={`Step ${i + 1}`} aria-current={i === si} onClick={() => setSi(i)}>{i + 1}</button>
              </li>
            ))}
          </ol>
          <p className="jn-trace-note" aria-live="polite">{step.note}</p>
          {trace.dead && <p className="jn-muted">Nothing else on the map happens after this click.</p>}
          <div className="jn-trace-nav">
            <button type="button" onClick={() => setSi((i) => Math.max(0, i - 1))} disabled={si === 0}>Back</button>
            <span>{si + 1} of {last + 1}</span>
            <button type="button" className="primary" onClick={() => setSi((i) => Math.min(last, i + 1))} disabled={si >= last}>Next</button>
          </div>
        </>
      )}
    </div>
  );
}
