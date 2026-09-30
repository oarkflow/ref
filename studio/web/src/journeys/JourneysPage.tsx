// "User journeys": how the app behaves for a visitor, as a map. Pages (with the links, forms
// and buttons on them) lead to requests, which run logic flows, which use connections - and
// to the page you land on. Selecting anything lights up the lines that touch it.
import {
  Background, BackgroundVariant, Controls, MiniMap, ReactFlow, ReactFlowProvider, useNodesInitialized, useNodesState, useReactFlow, type Node,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import "../canvas/canvas.css";
import "./journeys.css";
import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { CanvasActionsProvider, FlowProvider, createFlowStore, readOnlyActions } from "../canvas/context";
import { flowEdgeTypes } from "../canvas/edges";
import { DEFAULT_PREFS, SPEEDS, loadFlowPrefs, saveFlowPrefs, type FlowPrefs, type Speed } from "../canvas/flow";
import { Icon } from "../canvas/icons";
import { motionAllowed, viewportMs } from "../canvas/motion";
import { useStudio, useStudioStore } from "../state/context";
import { hasRole } from "../state/store";
import { useUi } from "../state/ui";
import { Drawer } from "./Drawer";
import { journeyElements, withHiddenLoops } from "./elements";
import { JIcon } from "./icons";
import { layoutJourney } from "./layout";
import { SHARED_ID, buildJourney, edgesTouching, neighbours, searchJourney, type JNode } from "./model";
import { nodeTypes, type CardData } from "./nodes";
import { FiltersPanel, Legend, TracePanel, WarningsPanel } from "./Panels";
import { entityCount } from "./text";
import type { TraceStep } from "./trace";
import { focusHref, parseDepth, useFilters, useFlows } from "./useFlows";
import { ActionsProvider, ViewProvider, createViewStore, type JourneyActions } from "./view";
import type { FlowWarning } from "./types";

/** A map that fits whole at or above this zoom opens whole; a bigger one opens readable, from the top left. */
const MIN_FIT_ZOOM = 0.4;
const READABLE_ZOOM = 0.8;

/** The page "Follow the user" opens on: the selected page (or the page of the selected button), else the focused page. */
export function traceStart(j: { byId: Map<string, JNode>; rowHost: Map<string, string> }, selected: string | null, focus: string | undefined): string | null {
  const host = selected ? (j.rowHost.get(selected) ?? selected) : null;
  if (host && j.byId.get(host)?.kind === "page") return host;
  return focus && j.byId.get(focus)?.kind === "page" ? focus : null;
}

const cx = (...a: (string | false | undefined | null)[]) => a.filter(Boolean).join(" ");

function useColorMode(): "light" | "dark" {
  const read = () => (document.documentElement.dataset.theme === "dark" ? "dark" : "light");
  const [mode, setMode] = useState<"light" | "dark">(read);
  useEffect(() => {
    const mo = new MutationObserver(() => setMode(read()));
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    return () => mo.disconnect();
  }, []);
  return mode;
}

const MINIMAP: Record<string, [string, string]> = {
  page: ["#4f46e5", "#818cf8"], shared: ["#64748b", "#94a3b8"], route: ["#2563eb", "#60a5fa"], intent: ["#7c3aed", "#a78bfa"],
  resource: ["#0d9488", "#2dd4bf"], external: ["#64748b", "#94a3b8"], unresolved: ["#dc2626", "#f87171"],
};

export function JourneysPage() {
  return (
    <ReactFlowProvider>
      <JourneysInner />
    </ReactFlowProvider>
  );
}

function JourneysInner() {
  const store = useStudioStore();
  const navigate = useNavigate();
  const rf = useReactFlow();
  const [params, setParams] = useSearchParams();
  const focus = params.get("focus") ?? undefined;
  const depth = parseDepth(params.get("depth"));
  const draft = useStudio((s) => s.draft);
  const meta = useStudio((s) => s.meta);
  const dev = useUi((u) => u.devView);
  const canEdit = hasRole(meta, "editor");
  const colorMode = useColorMode();
  const motion = useMemo(motionAllowed, []);

  const { graph, loading, refreshing, error, reload } = useFlows(focus, depth);
  const [filters, setFilters] = useFilters();
  const journey = useMemo(() => (graph ? buildJourney(graph, filters, { reach: focus ? depth : undefined }) : null), [graph, filters, focus, depth]);
  const layout = useMemo(() => (journey ? layoutJourney(journey) : null), [journey]);
  const els = useMemo(() => (journey && layout ? journeyElements(journey, layout, { dev }) : null), [journey, layout, dev]);

  // ---- selection, search ------------------------------------------------------------------------
  const view = useMemo(() => createViewStore(), []);
  const selected = useSyncExternalStore(view.subscribe, () => view.get().selected);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [search, setSearch] = useState("");
  const [panel, setPanel] = useState<null | "legend" | "filters" | "warnings">(null);
  const [minimap, setMinimap] = useState(true);
  const [trace, setTrace] = useState<{ start: string | null } | null>(null);
  const [traceStep, setTraceStep] = useState<TraceStep | null>(null);
  const [prefs, setPrefs] = useState<FlowPrefs>(() => { try { return loadFlowPrefs(); } catch { return DEFAULT_PREFS; } });
  const setFlow = useCallback((next: Partial<FlowPrefs>) => setPrefs((p) => { const n = { ...p, ...next }; saveFlowPrefs(n); return n; }), []);

  const journeyRef = useRef(journey);
  journeyRef.current = journey;

  const select = useCallback((id: string | null, open = true) => {
    view.set({ selected: id });
    if (id && open) setDrawerOpen(true);
  }, [view]);

  const matches = useMemo(() => (journey && search.trim() ? searchJourney(journey, search) : null), [journey, search]);
  useEffect(() => { view.set({ matches }); }, [view, matches]);

  // Leaving a focused view or changing the map drops a selection that is no longer there.
  useEffect(() => {
    const sel = view.get().selected;
    if (sel && journey && !journey.byId.has(sel) && !journey.rowHost.has(sel)) view.set({ selected: null });
  }, [journey, view]);

  // ---- opening things --------------------------------------------------------------------------------
  const openTemplate = useCallback((file: string, line?: number) => {
    navigate(`/pages/edit?path=${encodeURIComponent(file)}${line ? `&line=${line}` : ""}`);
  }, [navigate]);
  const openBlock = useCallback((n: JNode, to: string) => {
    const f = n.flow;
    if (f?.file && f.path) void store.getState().select(f.file, f.path);
    navigate(to);
  }, [navigate, store]);

  const actions = useMemo<JourneyActions>(() => ({
    readOnly: !canEdit,
    hostOf: (id) => journeyRef.current?.rowHost.get(id),
    selectCard: (id) => { setTrace(null); select(id); },
    selectRow: (id) => { setTrace(null); select(id); },
    toggleShared: () => setFilters({ sharedOpen: !filtersRef.current.sharedOpen }),
    fix: (n) => {
      const j = journeyRef.current;
      const row = j ? neighbours(j, n.id).incoming.find((i) => i.row?.file)?.row : undefined;
      if (row?.file) openTemplate(row.file, row.line);
    },
  }), [canEdit, select, setFilters, openTemplate]);
  const filtersRef = useRef(filters);
  filtersRef.current = filters;

  // ---- data flow: only the lines that touch the selection move --------------------------------------
  const flowStore = useMemo(() => createFlowStore(), []);
  const flowActions = useMemo(() => readOnlyActions({ motion }), [motion]);
  const focusEdges = useMemo(() => {
    if (!journey) return null;
    if (trace && traceStep) return new Set(traceStep.via ? [traceStep.via] : []);
    if (!selected || !prefs.on) return null;
    return new Set(edgesTouching(journey, selected));
  }, [journey, selected, prefs.on, trace, traceStep]);
  useEffect(() => {
    flowStore.set({ on: prefs.on || !!trace, speed: prefs.speed, focus: focusEdges, total: els?.edges.length ?? 0 });
  }, [flowStore, prefs.on, prefs.speed, focusEdges, els?.edges.length, trace]);

  // The lines that only say "this request shows that page" would crowd the map with loops;
  // they show when something they touch is selected, and always in a focused journey.
  const revealed = useMemo(() => {
    const ids = new Set(journey && selected ? edgesTouching(journey, selected) : []);
    if (trace && traceStep?.via) ids.add(traceStep.via);
    return ids;
  }, [journey, selected, trace, traceStep]);
  const edges = useMemo(() => {
    if (!els) return [];
    return withHiddenLoops(els.edges, { focused: !!focus, revealed });
  }, [els, revealed, focus]);

  // ---- React Flow state -------------------------------------------------------------------------------
  const [nodes, setNodes, onNodesChange] = useNodesState<Node<CardData>>([]);
  useEffect(() => { setNodes(els?.nodes ?? []); }, [els?.nodes, setNodes]);

  // A trace walks the selection along: show whatever step it is on.
  useEffect(() => {
    if (trace) { view.set({ selected: traceStep?.id ?? null }); setDrawerOpen(false); }
  }, [trace, traceStep, view]);

  // ---- opening view -----------------------------------------------------------------------------------
  const initialized = useNodesInitialized();
  const opened = useRef("");
  const sig = useMemo(() => (journey ? `${focus ?? ""}|${depth}|${journey.nodes.map((n) => n.id).join(",")}` : ""), [journey, focus, depth]);
  // room on the left for the line that loops back round to a page (its label sits there)
  const fitPadding = useMemo(() => ({ top: "96px", bottom: "40px", left: "130px", right: drawerOpen ? "460px" : "56px" } as const), [drawerOpen]);
  useEffect(() => {
    if (!initialized || !sig || opened.current === sig) return;
    let cancelled = false;
    const t = setTimeout(async () => {
      opened.current = sig;
      await rf.fitView({ padding: fitPadding, maxZoom: 1.05, duration: 0 });
      if (cancelled) return;
      if (rf.getViewport().zoom >= MIN_FIT_ZOOM) return;
      const ns = rf.getNodes();
      if (!ns.length) return;
      const minX = Math.min(...ns.map((n) => n.position.x));
      const minY = Math.min(...ns.map((n) => n.position.y));
      void rf.setViewport({ x: 130 - minX * READABLE_ZOOM, y: 96 - minY * READABLE_ZOOM, zoom: READABLE_ZOOM }, { duration: 0 });
    }, 200);
    return () => { cancelled = true; clearTimeout(t); };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [initialized, sig, rf]);

  const fit = useCallback(() => void rf.fitView({ padding: fitPadding, duration: viewportMs(motion), maxZoom: 1.05 }), [rf, fitPadding, motion]);
  // Show the first search hits.
  useEffect(() => {
    if (!matches || matches.size === 0) return;
    const t = setTimeout(() => void rf.fitView({ nodes: [...matches].map((id) => ({ id })), padding: 0.4, duration: viewportMs(motion), maxZoom: 1 }), 250);
    return () => clearTimeout(t);
  }, [matches, rf, motion]);

  // Esc clears the selection.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      const t = e.target as HTMLElement | null;
      if (t && ["INPUT", "TEXTAREA", "SELECT"].includes(t.tagName)) return;
      if (panel) setPanel(null);
      else if (trace) setTrace(null);
      else view.set({ selected: null });
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [view, panel, trace]);

  // A hidden tab should not animate.
  const [hidden, setHidden] = useState(() => typeof document !== "undefined" && document.visibilityState === "hidden");
  useEffect(() => {
    const on = () => setHidden(document.visibilityState === "hidden");
    document.addEventListener("visibilitychange", on);
    return () => document.removeEventListener("visibilitychange", on);
  }, []);

  const setFocus = useCallback((id: string | null, d = depth) => {
    setParams(id ? { focus: id, depth: String(d) } : {}, { replace: false });
    view.set({ selected: null });
  }, [setParams, depth, view]);

  // ---- states without a map -----------------------------------------------------------------------------
  if (!draft) {
    return (
      <main className="page">
        <div className="view"><p className="empty">Start a working copy to see how your app behaves. <a href="#/">Back to the start</a></p></div>
      </main>
    );
  }
  if (loading || (!journey && !error)) {
    return (
      <main className="page" aria-busy="true" aria-label="Loading the map">
        <div className="jn-skeleton"><i /><i /><i /><i /><i /></div>
      </main>
    );
  }
  if (error && !journey) {
    return (
      <main className="page">
        <div className="view">
          <p className="empty" role="alert">Couldn’t draw the map: {error}. <button type="button" className="link" onClick={reload}>Try again</button></p>
        </div>
      </main>
    );
  }
  if (!journey || !els) return null;

  const warnings = journey.warnings;
  const realProblems = warnings.filter((w) => w.severity === "warning").length;
  const pageCount = journey.nodes.filter((n) => n.kind === "page").length;
  const empty = journey.nodes.length === 0;
  const focusCard = focus ? journey.byId.get(focus) : undefined;
  const entities = entityCount(warnings);
  const openWarning = (w: FlowWarning) => { if (w.file) { setPanel(null); openTemplate(w.file, w.line); } };

  return (
    <CanvasActionsProvider value={flowActions}>
    <FlowProvider value={flowStore}>
    <ViewProvider value={view}>
    <ActionsProvider value={actions}>
      <div className={cx("canvas-page journeys-page", !motion && "cv-static", hidden && "cv-paused", drawerOpen && "drawer-open", minimap && "has-minimap")} data-flow-focus={focusEdges ? [...focusEdges].join(",") : ""}>
        <div className="cv-stage">
          <ReactFlow
            nodes={nodes}
            edges={edges}
            nodeTypes={nodeTypes}
            edgeTypes={flowEdgeTypes}
            onNodesChange={onNodesChange}
            onNodeClick={(e, n) => { if (!e.shiftKey && !e.metaKey && !e.ctrlKey) { setTrace(null); select(n.id); } }}
            onPaneClick={() => { view.set({ selected: null }); setPanel(null); }}
            nodesConnectable={false}
            elementsSelectable
            deleteKeyCode={null}
            nodeDragThreshold={3}
            colorMode={colorMode}
            fitView
            fitViewOptions={{ padding: fitPadding }}
            onlyRenderVisibleElements={nodes.length > 60}
            minZoom={0.12}
            maxZoom={1.6}
            zoomOnDoubleClick={false}
            proOptions={{ hideAttribution: false }}
            aria-label="Map of how the app behaves for a visitor"
          >
            <Background variant={BackgroundVariant.Dots} gap={24} size={1.4} />
            <Controls position="bottom-left" showInteractive={false} fitViewOptions={{ padding: fitPadding, duration: viewportMs(motion) }} aria-label="Zoom" />
            {minimap && (
              <MiniMap
                pannable zoomable ariaLabel="Overview" nodeStrokeWidth={0} className="cv-minimap" maskColor="transparent"
                nodeColor={(n) => MINIMAP[n.type ?? "page"]?.[colorMode === "dark" ? 1 : 0] ?? "#94a3b8"}
              />
            )}
          </ReactFlow>

          <div className="cv-crumb jn-crumb">
            <a href="#/" className="cv-back" aria-label="Back to the start" title="Back to the start"><Icon name="chevron" size={12} /> Editor</a>
            <h1>
              {focus ? (
                <>
                  <button type="button" className="jn-crumb-link" onClick={() => setFocus(null)}>User journeys</button>
                  <span className="jn-sep" aria-hidden>›</span>
                  <span className="jn-focus" title={focusCard?.title ?? focus}>{focusCard?.title ?? focus.replace(/^[a-z]+:/, "")}</span>
                </>
              ) : (
                <>User journeys</>
              )}
            </h1>
            {focus && (
              <label className="jn-depth" title="How far from it to follow the lines">
                <span>Reach</span>
                <select value={depth} onChange={(e) => setParams({ focus, depth: e.target.value })} aria-label="How far to follow">
                  <option value={1}>1 step</option>
                  <option value={2}>2 steps</option>
                  <option value={3}>3 steps</option>
                </select>
              </label>
            )}
            {refreshing && <span className="jn-updating" role="status"><i /> Updating</span>}
            {realProblems > 0 && !drawerOpen && (
              <button type="button" className="cv-warnbtn" onClick={() => setFilters({ onlyProblems: !filters.onlyProblems })} title="Show only what needs fixing">
                <Icon name="alert" size={12} /> {realProblems} {realProblems === 1 ? "problem" : "problems"}
              </button>
            )}
          </div>

          <div className="cv-toolbar jn-toolbar" role="toolbar" aria-label="Map tools">
            <label className="jn-search">
              <JIcon name="search" size={13} />
              <input
                type="search" placeholder="Find on the map" value={search} aria-label="Find on the map"
                onChange={(e) => setSearch(e.target.value)} onKeyDown={(e) => { if (e.key === "Escape") { setSearch(""); (e.target as HTMLInputElement).blur(); } }}
              />
              {search && matches && <span className="jn-count" aria-live="polite">{matches.size}</span>}
            </label>
            <span className="sep" />
            <button type="button" aria-pressed={panel === "filters"} onClick={() => setPanel(panel === "filters" ? null : "filters")} aria-label="What to show" title="What to show">
              <JIcon name="filter" />{(filters.hideShared || filters.hideConnections || filters.showOrphans || filters.onlyProblems) && <i className="jn-pip" />}
            </button>
            <button type="button" className="cv-textbtn" aria-pressed={!!trace} onClick={() => { if (trace) setTrace(null); else { setTrace({ start: traceStart(journey, selected, focus) }); setPanel(null); } }} title="Step through what happens when someone clicks">
              <JIcon name="route" size={13} /> Follow the user
            </button>
            <span className="sep" />
            <button type="button" onClick={fit} aria-label="Fit to screen" title="Fit everything on screen"><Icon name="fit" /></button>
            <button type="button" aria-pressed={minimap} onClick={() => setMinimap((m) => !m)} aria-label="Overview map" title="Show or hide the overview map"><Icon name="map" /></button>
            <button type="button" aria-pressed={panel === "legend"} onClick={() => setPanel(panel === "legend" ? null : "legend")} aria-label="How to read this map" title="How to read this map"><Icon name="info" /></button>
            <span className="sep" />
            <button type="button" aria-pressed={prefs.on} onClick={() => setFlow({ on: !prefs.on })} aria-label="Show data flow" title={prefs.on ? "Data moves along the lines of what you select. Click to keep everything still." : "Everything stays still. Click to animate the lines of what you select."}><Icon name="bolt" /></button>
            {prefs.on && (
              <label className="cv-speed">
                <span className="sr-only">Speed of the data flow</span>
                <select value={prefs.speed} onChange={(e) => setFlow({ speed: e.target.value as Speed })} aria-label="Speed of the data flow">
                  {SPEEDS.map((sp) => <option key={sp} value={sp}>{sp === "slow" ? "Slow" : sp === "fast" ? "Fast" : "Normal"}</option>)}
                </select>
              </label>
            )}
            <button type="button" className={cx("jn-notes", warnings.length > 0 && "has")} aria-pressed={panel === "warnings"} onClick={() => setPanel(panel === "warnings" ? null : "warnings")} aria-label={`Notes about this map (${warnings.length})`} title="Notes about this map">
              <Icon name="list" />{warnings.length > 0 && <b>{warnings.length}</b>}
            </button>
          </div>

          <button type="button" className="cv-drawertoggle" aria-label={drawerOpen ? "Hide details" : "Show details"} aria-expanded={drawerOpen} onClick={() => setDrawerOpen((o) => !o)}>
            <Icon name="panel" size={16} />
          </button>

          {panel === "legend" && <Legend onClose={() => setPanel(null)} />}
          {panel === "filters" && <FiltersPanel filters={filters} set={setFilters} hiddenEndpoints={journey.hidden.endpoints} onClose={() => setPanel(null)} />}
          {panel === "warnings" && <WarningsPanel warnings={warnings} onOpen={openWarning} onClose={() => setPanel(null)} />}
          {trace && <TracePanel journey={journey} startPage={trace.start} onStep={setTraceStep} onClose={() => { setTrace(null); setTraceStep(null); }} />}

          {empty && (
            <div className="jn-empty" role="status">
              {filters.onlyProblems ? (
                <><h2>No problems</h2><p>Every button, link and form on the map leads somewhere.</p><button type="button" onClick={() => setFilters({ onlyProblems: false })}>Show the whole map</button></>
              ) : (
                <><h2>No pages yet</h2><p>Add a page and a request that shows it, and they appear here with everything you can do on them.</p><button type="button" onClick={() => navigate("/pages")}>Go to page designs</button></>
              )}
            </div>
          )}

          {!empty && !focus && (journey.hidden.endpoints > 0 || entities > 0) && (
            <div className="jn-hiddenbar" role="note">
              {journey.hidden.endpoints > 0 && (
                <span>{pageCount} pages. <button type="button" className="link" onClick={() => setFilters({ showOrphans: true })}>Also show {journey.hidden.endpoints} endpoints no page uses</button></span>
              )}
              {entities > 0 && <span className="jn-muted">{entities} data tables create endpoints of their own that aren’t drawn.</span>}
            </div>
          )}
          {focus && journey.hidden.shared > 0 && !filters.sharedInFocus && (
            <div className="jn-hiddenbar" role="note">
              <span className="jn-muted">The shared navbar and layouts are left out of this journey.</span>
              <button type="button" className="link" onClick={() => setFilters({ sharedInFocus: true })}>Show them</button>
            </div>
          )}
          {focus && filters.sharedInFocus && (
            <div className="jn-hiddenbar" role="note"><button type="button" className="link" onClick={() => setFilters({ sharedInFocus: false })}>Hide the shared navbar and layouts</button></div>
          )}
          {filters.showOrphans && !focus && (
            <div className="jn-hiddenbar" role="note"><button type="button" className="link" onClick={() => setFilters({ showOrphans: false })}>Hide endpoints no page uses</button></div>
          )}
        </div>

        <aside className="cv-drawer jn-drawerwrap" data-open={drawerOpen} aria-label="Details" aria-hidden={!drawerOpen}>
          <div className="cv-drawerbody">
            <Drawer
              journey={journey} selected={selected}
              onSelect={(id) => { setTrace(null); select(id); }}
              onOpenTemplate={openTemplate}
              onOpenRoute={(n) => openBlock(n, "/edit")}
              onOpenFlow={(n) => { const f = n.flow; if (f?.file && f.path) navigate(`/canvas?file=${encodeURIComponent(f.file)}&path=${encodeURIComponent(f.path)}`); }}
              onOpenConnection={(n) => openBlock(n, "/edit")}
              onFocus={(id) => { setDrawerOpen(true); navigate(focusHref(id, depth)); view.set({ selected: null }); }}
              onClose={() => { setDrawerOpen(false); }}
            />
          </div>
        </aside>
      </div>
    </ActionsProvider>
    </ViewProvider>
    </FlowProvider>
    </CanvasActionsProvider>
  );
}

export { SHARED_ID };
