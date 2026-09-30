// The side panels of the template editor.
import { ArrowUpRight, CircleAlert, CircleCheck, FilePlus2, Monitor, RotateCcw, Smartphone, Tablet } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import type { PreviewData, TemplateInfo, TemplateRoute } from "../api/pageTypes";
import type { Diagnostic } from "../api/types";
import { usePreviewSession } from "../preview/usePreviewSession";
import { useStudio, useStudioStore } from "../state/context";
import { Badge } from "../ui/primitives";
import { describeValue, friendlyName } from "./lib";
import { Pane } from "./parts";

// ---- Structure ---------------------------------------------------------------

export function OutlinePanel({
  t, byName, cycles, canEdit, onOpen, onCreate,
}: {
  t: TemplateInfo;
  byName: Record<string, TemplateInfo>;
  cycles: Diagnostic[];
  canEdit: boolean;
  onOpen(path: string): void;
  onCreate(name: string): void;
}) {
  const chain = [...t.layouts].reverse(); // outermost first
  const includes = t.includes.filter((n) => !t.layouts.includes(n));
  const missing = new Set(t.missing ?? []);
  const item = (name: string, role: string) => {
    const info = byName[name];
    const gone = !info || missing.has(name);
    return (
      <li key={`${role}:${name}`} className={`outline-item${gone ? " bad" : ""}`}>
        <div className="outline-main">
          <strong>{friendlyName(name).title}</strong>
          <small>{gone ? "Doesn’t exist" : role}</small>
        </div>
        {gone
          ? canEdit && <button type="button" className="btn sm" onClick={() => onCreate(name)}><FilePlus2 size={13} /> Create</button>
          : <button type="button" className="btn sm ghost" onClick={() => onOpen(info.path)} aria-label={`Open ${friendlyName(name).title}`}>Open <ArrowUpRight size={13} /></button>}
      </li>
    );
  };
  return (
    <Pane title="Structure" hint={t.kind === "page" ? "What this page is built from." : "Where this design fits."}>
      {cycles.length > 0 && (
        <div className="callout bad" role="alert">
          <CircleAlert size={15} aria-hidden="true" />
          <span>This design ends up including itself, so it can never finish drawing.</span>
        </div>
      )}
      <ol className="outline">
        {chain.map((n) => item(n, "Layout"))}
        <li className="outline-item self" aria-current="true">
          <div className="outline-main"><strong>{friendlyName(t.name).title}</strong><small>You are here</small></div>
        </li>
        {includes.map((n) => item(n, "Piece"))}
      </ol>
      {(t.blocks?.length ?? 0) > 0 && (
        <>
          <h4 className="pane-sub">Places to fill in</h4>
          <ul className="slot-list">
            {t.blocks!.map((b) => {
              const empty = (t.unfilled ?? []).includes(b);
              return <li key={b}><code>{b}</code>{empty ? <Badge tone="muted">Empty</Badge> : <Badge tone="ok" className="dot">Filled</Badge>}</li>;
            })}
          </ul>
        </>
      )}
      {(t.unknown?.length ?? 0) > 0 && (
        <p className="hint bad">Fills {t.unknown!.map((u) => `“${u}”`).join(", ")}, which the layout doesn’t offer.</p>
      )}
      {t.layouts.length === 0 && t.includes.length === 0 && (t.blocks?.length ?? 0) === 0 && <p className="muted">Nothing else is pulled in.</p>}
    </Pane>
  );
}

// ---- Needs + used by -----------------------------------------------------------

/** "reads a, b, c, which intent…" -> ["a", "b", "c"] */
export function unprovidedVars(diags: Diagnostic[], templateName: string): Set<string> {
  const out = new Set<string>();
  for (const d of diags) {
    if (d.code !== "studio.pages.vars_unprovided" || !d.message.includes(`"${templateName}"`)) continue;
    const m = d.message.match(/reads (.+?), which/);
    if (m) m[1]!.split(/,\s*/).forEach((v) => out.add(v.replace(/[\s"]/g, "")));
  }
  return out;
}

export function NeedsPanel({ t, globals, sample, unprovided }: { t: TemplateInfo; globals: string[]; sample: PreviewData | null; unprovided: Set<string> }) {
  const g = new Set(globals);
  const own = t.vars.filter((v) => !g.has(v));
  const app = t.vars.filter((v) => g.has(v));
  const [showApp, setShowApp] = useState(false);
  return (
    <Pane title={t.kind === "page" ? "This page needs" : "This design reads"} hint="Information the design can show. The page’s logic supplies most of it; the app fills in the rest on every page.">
      {t.vars.length === 0 && <p className="muted">It shows nothing that changes.</p>}
      {own.length > 0 && (
        <ul className="need-list">
          {own.map((v) => {
            const missing = unprovided.has(v);
            return (
              <li key={v} className={missing ? "warn" : ""}>
                <code>{v}</code>
                <span className="need-type">{sample ? describeValue(sample.data[v]) : ""}</span>
                {missing ? <Badge tone="warn" className="dot" title="The linked logic flow never mentions this, so it may come out blank.">Not provided</Badge>
                  : t.routes.length ? <Badge tone="ok" className="dot" title="The linked logic flow mentions it.">Provided</Badge>
                  : <Badge tone="muted" title="No page uses this design yet, so there is nothing to check it against.">Unchecked</Badge>}
              </li>
            );
          })}
        </ul>
      )}
      {app.length > 0 && (
        <div className="need-app">
          <button type="button" className="link" aria-expanded={showApp} onClick={() => setShowApp(!showApp)}>
            {showApp ? "Hide" : "Also"} {app.length} filled in by the app
          </button>
          {showApp && <p className="need-app-list">{app.map((v) => <code key={v}>{v}</code>)}</p>}
        </div>
      )}
    </Pane>
  );
}

export function UsedByPanel({ t, onOpenRoute }: { t: TemplateInfo; onOpenRoute(r: TemplateRoute): void }) {
  return (
    <Pane title="Used by" hint={t.routes.length ? undefined : "Nothing shows this design directly."}>
      {t.routes.length > 0 && (
        <ul className="used-list">
          {t.routes.map((r) => (
            <li key={`${r.route}:${r.as}`}>
              <button type="button" className="route-chip wide" onClick={() => onOpenRoute(r)}>
                <b>{r.method}</b> {r.url}
                <small>{r.as === "layout" ? "uses it as its layout" : r.route}</small>
              </button>
            </li>
          ))}
        </ul>
      )}
    </Pane>
  );
}

// ---- Sample data ----------------------------------------------------------------

function flatten(v: unknown, prefix = "", depth = 0, out: { path: string; type: string }[] = []) {
  if (v && typeof v === "object" && !Array.isArray(v) && depth < 2) {
    for (const [k, x] of Object.entries(v as Record<string, unknown>)) {
      const p = prefix ? `${prefix}.${k}` : k;
      out.push({ path: p, type: describeValue(x) });
      if (x && typeof x === "object" && !Array.isArray(x)) flatten(x, p, depth + 1, out);
    }
  }
  return out;
}

export function SampleDataPanel({ sample, onInsert, disabled }: { sample: PreviewData | null; onInsert(text: string): void; disabled?: boolean }) {
  const initial = useMemo(() => (sample ? JSON.stringify(sample.data, null, 2) : "{}"), [sample]);
  const [text, setText] = useState(initial);
  useEffect(() => setText(initial), [initial]);
  let parsed: unknown = null;
  let error: string | null = null;
  try {
    parsed = JSON.parse(text || "{}");
  } catch (e) {
    error = (e as Error).message;
  }
  const fields = useMemo(() => (error ? [] : flatten(parsed)), [parsed, error]);
  return (
    <Pane
      title="Sample data"
      hint="Made-up values so you can see what the design can use. Only kept on this screen. Click a name to put it in the design."
      actions={text !== initial ? <button type="button" className="btn sm ghost" onClick={() => setText(initial)}><RotateCcw size={13} /> Reset</button> : undefined}
    >
      <ul className="pick-list chips" aria-label="Values">
        {fields.map((f) => (
          <li key={f.path}>
            <button type="button" className="pick-chip" disabled={disabled} title={`Insert \${${f.path}}`} onClick={() => onInsert(`\${${f.path}}`)}>
              {f.path}<small>{f.type}</small>
            </button>
          </li>
        ))}
        {fields.length === 0 && !error && <li className="muted pick-empty">No sample values were guessed.</li>}
      </ul>
      <label className="fld">
        <span className="fld-label">Values (JSON)</span>
        <textarea className="json-area" rows={10} value={text} onChange={(e) => setText(e.target.value)} spellCheck={false} aria-invalid={!!error} />
      </label>
      {error && <p className="hint bad" role="alert">That isn’t valid JSON yet: {error}</p>}
    </Pane>
  );
}

// ---- Preview ----------------------------------------------------------------------

const DEVICES = { desktop: { label: "Desktop", w: 0, icon: Monitor }, tablet: { label: "Tablet", w: 820, icon: Tablet }, mobile: { label: "Phone", w: 390, icon: Smartphone } } as const;

/** Picks a route that shows this design and fills any {id} with 1. */
export function previewPathFor(routes: TemplateRoute[]): string | null {
  const gets = routes.filter((r) => r.as === "template" && r.method === "GET");
  const plain = gets.find((r) => !/[{:]/.test(r.url));
  const r = plain ?? gets[0];
  if (!r) return null;
  return r.url.replace(/\{[^}]+\}/g, "1").replace(/:[A-Za-z_]\w*/g, "1");
}

export function PreviewTab({ t }: { t: TemplateInfo }) {
  const draftId = useStudio((s) => s.draft?.id);
  const version = useStudio((s) => s.draft?.version);
  const { state } = usePreviewSession(draftId, version);
  const store = useStudioStore();
  const [device, setDevice] = useState<keyof typeof DEVICES>("desktop");
  const path = previewPathFor(t.routes);
  const src = state.url && path ? state.url.replace(/\/$/, "") + path : null;

  if (t.kind !== "page" || !path) {
    return (
      <Pane title="Preview">
        <p className="muted">
          {t.kind !== "page"
            ? "Layouts and components are drawn inside pages. Open a page that uses this design to see it."
            : "No page address shows this design yet. Point a page at it in Pages & APIs to preview it."}
        </p>
        {t.routes.filter((r) => r.as === "layout").length > 0 && <p className="hint">Tip: open one of the pages that uses this layout.</p>}
      </Pane>
    );
  }
  return (
    <Pane
      title="Preview"
      hint={<>Showing <code>{path}</code> from this version, in a safe copy of the app.</>}
      actions={
        <span className="seg tight" role="group" aria-label="Screen size">
          {(Object.keys(DEVICES) as (keyof typeof DEVICES)[]).map((k) => {
            const I = DEVICES[k].icon;
            return <button key={k} type="button" aria-pressed={device === k} className={device === k ? "on" : ""} title={DEVICES[k].label} aria-label={DEVICES[k].label} onClick={() => setDevice(k)}><I size={14} /></button>;
          })}
        </span>
      }
      className="preview-pane"
    >
      {state.phase === "unavailable" && <p className="callout warn" role="alert">{state.message ?? "Live preview isn’t available on this server."}</p>}
      {(state.phase === "idle" || state.phase === "starting") && <p className="muted" aria-busy="true">Starting the preview…</p>}
      {state.phase === "failed" && (
        <div className="callout bad" role="alert">
          <CircleAlert size={15} aria-hidden="true" />
          <div>
            <strong>The preview couldn’t be built{state.showingLastGood ? ", so this shows the last good one" : ""}.</strong>
            <ul>{state.errors.slice(0, 3).map((e, i) => <li key={i}><button type="button" className="link" onClick={() => void store.getState().selectFromDiagnostic(e)}>{e.message}</button></li>)}</ul>
          </div>
        </div>
      )}
      {src && (
        <div className="frame-wrap" data-device={device}>
          <div className="frame" style={DEVICES[device].w ? { width: DEVICES[device].w, maxWidth: "100%" } : undefined}>
            <iframe key={state.reloadKey} title={`Preview of ${friendlyName(t.name).title}`} src={src} sandbox="allow-scripts allow-forms allow-same-origin allow-popups" />
          </div>
        </div>
      )}
      {src && state.stale && <p className="hint"><CircleCheck size={13} /> Updating with your latest change…</p>}
      {src && <p className="hint">Pages behind sign-in show the sign-in page first. Use the demo accounts on it to look further in.</p>}
      {src && <a className="link" href={src} target="_blank" rel="noreferrer">Open in a new tab <ArrowUpRight size={12} /></a>}
    </Pane>
  );
}

