import "@xyflow/react/dist/style.css";
import "./canvas.css";
import {
  Background, BackgroundVariant, Controls, MiniMap, ReactFlow, ReactFlowProvider, ViewportPortal, applyEdgeChanges, applyNodeChanges, useNodesInitialized, useReactFlow,
  type Connection, type Edge, type EdgeChange, type Node, type NodeChange,
} from "@xyflow/react";
import { useCallback, useDeferredValue, useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { useShallow } from "zustand/react/shallow";
import type { BlockNode, Op } from "../api/types";
import { Inspector } from "../forms/Inspector";
import { childPath, findNode, isWithin } from "../lib/paths";
import { useStudio, useStudioStore } from "../state/context";
import { hasRole } from "../state/store";
import { shortcutFor } from "../Workspace";
import { CanvasActionsProvider, CollapsedProvider, FlowProvider, createCollapsedStore, createFlowStore, createLabelRegistry, type CanvasActions } from "./context";
import { SPEEDS, connectedEdges, loadFlowPrefs, saveFlowPrefs, type FlowPrefs, type Speed } from "./flow";
import { ConnectionLine, edgeTypes } from "./edges";
import { flowElements } from "./elements";
import { alignGuides, type Guides } from "./guides";
import { Icon } from "./icons";
import { clearPositions, layoutKey, loadPositions, savePositions, type Point } from "./layout";
import { count, friendlyProblem, problemWhere } from "../labels";
import { friendlyKind, TERMS } from "./labels";
import {
  addIntentNode, addStage, addStep, appendIntentNode, appendStep, buildGraph, connectIntent, connectProcess, deleteSteps,
  disconnectIntent, disconnectIntentMany, disconnectProcess, disconnectProcessMany, edgeShape, insertIntentNode, insertStep, INPUT_NODE,
  moveStage, removeNodes, renameIntentNode, renameStep, setStart, traceOrder, wouldCycle,
  type CanvasGraph, type IntentEdge, type NodeTypeInfo, type ProcessEdge,
} from "./model";
import { motionAllowed, viewportMs } from "./motion";
import { EdgePopover } from "./EdgePopover";
import { Legend } from "./Legend";
import { NodePanel } from "./NodePanel";
import { probe } from "./probe";
import { nodeTypes } from "./nodes";
import { DRAG_MIME, InsertPopover, TypeList } from "./Popover";
import { edgeLabel } from "./labels";

/** Structural equality for plain node/edge data, so an unchanged node keeps the very same `data` object and React skips it. */
function same(a: unknown, b: unknown): boolean {
  if (Object.is(a, b)) return true;
  if (typeof a !== "object" || typeof b !== "object" || !a || !b) return false;
  if (Array.isArray(a) !== Array.isArray(b)) return false;
  const ka = Object.keys(a);
  if (ka.length !== Object.keys(b).length) return false;
  for (const k of ka) if (!same((a as Record<string, unknown>)[k], (b as Record<string, unknown>)[k])) return false;
  return true;
}

const cx = (...a: (string | false | undefined | null)[]) => a.filter(Boolean).join(" ");
const GRID = 8;
/** below this a card's text is too small to read */
const READABLE_ZOOM = 0.9;
/** A flow that fits whole at or above this zoom opens whole; only a flow that would be too small to read opens zoomed in on its start. */
const MIN_FIT_ZOOM = 0.5;
const TWEEN_MS = 460;

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

export function CanvasPage() {
  return (
    <ReactFlowProvider>
      <CanvasInner />
    </ReactFlowProvider>
  );
}

interface Pop {
  anchor: DOMRect;
  title: string;
  pick(t: NodeTypeInfo): void;
}

function CanvasInner() {
  probe("canvas");
  const [params] = useSearchParams();
  const file = params.get("file") ?? "";
  const path = params.get("path") ?? "";
  const store = useStudioStore();
  const s = useStudio(
    useShallow((st) => ({
      draft: st.draft, trees: st.trees, catalog: st.catalog, selection: st.selection, diagnostics: st.diagnostics, meta: st.meta,
    })),
  );
  const canEdit = hasRole(s.meta, "editor");
  const colorMode = useColorMode();
  const rf = useReactFlow();
  const stageRef = useRef<HTMLDivElement>(null);
  const motion = useMemo(motionAllowed, []);
  const labels = useMemo(createLabelRegistry, []);

  // Make sure the file's tree is loaded and the block is the current selection.
  const draftId = s.draft?.id;
  useEffect(() => {
    if (draftId && file && path) void store.getState().select(file, path);
  }, [store, draftId, file, path]);

  const root = useMemo(() => (file ? findNode(s.trees[file], path) : undefined), [s.trees, file, path]);
  const graph: CanvasGraph | null = useMemo(() => (root ? buildGraph(root, s.catalog) : null), [root, s.catalog]);
  const graphRef = useRef(graph);
  graphRef.current = graph;

  // -- remembered positions -------------------------------------------------
  const key = layoutKey(draftId ?? "", file, path);
  const [saved, setSaved] = useState<Record<string, Point>>(() => loadPositions(key));
  useEffect(() => setSaved(loadPositions(key)), [key]);
  const persist = useCallback(
    (fn: (p: Record<string, Point>) => Record<string, Point>) =>
      setSaved((prev) => {
        const next = fn(prev);
        savePositions(key, next);
        return next;
      }),
    [key],
  );

  const elements = useMemo(
    () => (graph ? flowElements(graph, { catalog: s.catalog, diagnostics: s.diagnostics, file, saved }) : null),
    [graph, s.catalog, s.diagnostics, file, saved],
  );

  // Pin the first automatic layout: after that, adding or removing a node never
  // shuffles the ones already on the canvas ("Tidy up" clears the pins).
  useEffect(() => {
    if (elements && elements.nodes.length > 0 && Object.keys(saved).length === 0) persist(() => ({ ...elements.auto }));
  }, [elements, saved, persist]);

  // The child block the current selection lives in (drives highlight + side panel).
  const owner: BlockNode | undefined = useMemo(
    () => (root && s.selection && s.selection.file === file ? root.children?.find((c) => c.kind === "block" && isWithin(s.selection!.path, c.path)) : undefined),
    [root, s.selection, file],
  );

  // The details panel follows the selection a beat later: the highlight on the canvas must not wait for a form to build.
  const panelOwner = useDeferredValue(owner);
  const [railOpen, setRailOpen] = useState(false);
  const [drawerOpen, setDrawerOpen] = useState(false);

  // A different graph starts with the details closed.
  useEffect(() => setDrawerOpen(false), [file, path]);

  // Picking something to edit (from the canvas, a problem, a deep link) opens the details.
  const ownerPath = owner?.path;
  useEffect(() => {
    if (ownerPath) setDrawerOpen(true);
  }, [ownerPath]);

  // ...and slides the canvas so the step you picked is not hidden under them.
  useEffect(() => {
    if (!ownerPath || !drawerOpen) return;
    const t = setTimeout(() => {
      const el = stageRef.current;
      const n = rf.getNodes().find((x) => (x.data as { info?: { path: string } } | undefined)?.info?.path === ownerPath);
      if (!el || !n || !n.measured?.width) return;
      const vp = rf.getViewport();
      const drawerW = Math.min(430, el.clientWidth * 0.94);
      const right = (n.position.x + n.measured.width) * vp.zoom + vp.x;
      const limit = el.clientWidth - drawerW - 28;
      if (right > limit) void rf.setViewport({ x: vp.x - (right - limit), y: vp.y, zoom: vp.zoom }, { duration: viewportMs(motion, 260) });
    }, 60);
    return () => clearTimeout(t);
  }, [ownerPath, drawerOpen, rf, motion]);

  // -- React Flow state -----------------------------------------------------
  const [nodes, setNodes] = useState<Node[]>([]);
  const [edges, setEdges] = useState<Edge[]>([]);
  const flowStore = useMemo(createFlowStore, []);
  const collapsedStore = useMemo(createCollapsedStore, []);
  const [ready, setReady] = useState(false);
  const [tweening, setTweening] = useState(false);
  const [guides, setGuidesNow] = useState<Guides>({ x: [], y: [], dx: 0, dy: 0 });
  // Guides follow the pointer at most once per frame.
  const guideFrame = useRef(0);
  const setGuides = useCallback((g: Guides) => {
    cancelAnimationFrame(guideFrame.current);
    if (!g.x.length && !g.y.length) return setGuidesNow(g);
    guideFrame.current = requestAnimationFrame(() => setGuidesNow(g));
  }, []);
  useEffect(() => () => cancelAnimationFrame(guideFrame.current), []);
  const [fresh, setFresh] = useState<ReadonlySet<string>>(new Set());
  const [active, setActive] = useState<ReadonlySet<string>>(new Set());
  const prevEdgeIds = useRef<Set<string> | null>(null);

  useEffect(() => {
    if (!elements) return;
    setNodes((prev) => {
      const old = new Map(prev.map((n) => [n.id, n]));
      const next = elements.nodes.map((n) => {
        const info = (n.data as { info?: { path: string } }).info;
        const before = old.get(n.id);
        // Keep the previous `data` object when nothing in it changed, so memoised nodes do not re-render...
        const data = before && same(before.data, n.data) ? before.data : n.data;
        const selected = !!owner && info?.path === owner.path;
        // ...and the previous node object when nothing about it changed, so React Flow does not touch it either.
        if (before && data === before.data && before.selected === selected && before.position.x === n.position.x && before.position.y === n.position.y && before.type === n.type && !(before.data as { exiting?: boolean }).exiting) return before;
        // Keep React Flow's measurements, or every node is hidden for a beat after each edit.
        return { ...n, data, measured: before?.measured, selected };
      });
      const gone = motion ? prev.filter((p) => !(p.data as { exiting?: boolean }).exiting && !elements.nodes.some((n) => n.id === p.id)) : [];
      if (gone.length) {
        setTimeout(() => setNodes((ns) => ns.filter((n) => !(n.data as { exiting?: boolean }).exiting)), 240);
        return [...next, ...gone.map((g) => ({ ...g, selected: false, draggable: false, selectable: false, connectable: false, data: { ...g.data, exiting: true } }))];
      }
      return next;
    });
    setEdges((prev) => {
      const sel = new Set(prev.filter((e) => e.selected).map((e) => e.id));
      const blockSel = owner?.type === "edge" ? owner.path : undefined;
      const before = new Map(prev.map((e) => [e.id, e]));
      return elements.edges.map((e) => {
        const old = before.get(e.id);
        const data = old && same(old.data, e.data) ? old.data : e.data;
        const selected = sel.has(e.id) || (!!blockSel && (e.data as { blockPath?: string } | undefined)?.blockPath === blockSel);
        if (old && data === old.data && !!old.selected === selected && old.source === e.source && old.target === e.target && old.className === e.className && old.markerEnd === e.markerEnd) return old;
        return { ...e, data, selected };
      });
    });
    // new lines flow for a moment so the eye finds them
    const ids = new Set(elements.edges.map((e) => e.id));
    if (prevEdgeIds.current && motion) {
      const added = [...ids].filter((id) => !prevEdgeIds.current!.has(id));
      if (added.length) {
        setFresh(new Set(added));
        setTimeout(() => setFresh(new Set()), 2600);
      }
    }
    prevEdgeIds.current = ids;
  }, [elements, owner, motion]);

  useEffect(() => {
    if (!graph) return;
    const t = setTimeout(() => setReady(true), 400);
    return () => clearTimeout(t);
  }, [graph?.path]); // eslint-disable-line react-hooks/exhaustive-deps

  const shownEdges = useMemo(
    () =>
      edges.map((e) =>
        fresh.has(e.id) || active.has(e.id)
          ? { ...e, animated: true, className: cx(e.className, active.has(e.id) && "cv-edge-active", fresh.has(e.id) && "cv-edge-fresh") }
          : e,
      ),
    [edges, fresh, active],
  );

  const onNodesChange = useCallback(
    (changes: NodeChange[]) => {
      setNodes((ns) => {
        // alignment guides + snap while a node is dragged
        const adjusted = changes.map((c) => {
          if (c.type !== "position" || !c.dragging || !c.position) return c;
          const me = ns.find((n) => n.id === c.id);
          const w = me?.measured?.width ?? 0;
          const h = me?.measured?.height ?? 0;
          if (!w || !h) return c;
          const others = ns.filter((n) => n.id !== c.id && !(n.data as { exiting?: boolean }).exiting).map((n) => ({ x: n.position.x, y: n.position.y, w: n.measured?.width ?? 0, h: n.measured?.height ?? 0 })).filter((r) => r.w && r.h);
          const g = alignGuides({ x: c.position.x, y: c.position.y, w, h }, others);
          setGuides(g);
          return g.dx || g.dy ? { ...c, position: { x: c.position.x + g.dx, y: c.position.y + g.dy } } : c;
        });
        // a guide belongs to a drag in progress: once nothing is being dragged, it goes
        if (!changes.some((c) => c.type === "position" && c.dragging)) setGuides({ x: [], y: [], dx: 0, dy: 0 });
        return applyNodeChanges(adjusted, ns);
      });
    },
    [],
  );
  const onEdgesChange = useCallback((c: EdgeChange[]) => setEdges((es) => applyEdgeChanges(c, es)), []);

  const [edgeKind, setEdgeKind] = useState("simple");
  const [prefs, setPrefs] = useState<FlowPrefs>(loadFlowPrefs);
  const setFlow = useCallback((next: Partial<FlowPrefs>) => setPrefs((p) => { const n = { ...p, ...next }; saveFlowPrefs(n); return n; }), []);
  const [minimap, setMinimap] = useState(true);
  const [legend, setLegend] = useState(false);
  const [editing, setEditing] = useState<{ edgeId: string; anchor: DOMRect } | null>(null);
  // Nothing animates in a hidden tab: pause CSS animations and the SVG flow dots.
  const [hidden, setHidden] = useState(() => typeof document !== "undefined" && document.visibilityState === "hidden");
  useEffect(() => {
    const on = () => {
      const h = document.visibilityState === "hidden";
      setHidden(h);
      stageRef.current?.querySelectorAll("svg").forEach((svg) => {
        try { if (h) svg.pauseAnimations(); else svg.unpauseAnimations(); } catch { /* jsdom */ }
      });
    };
    document.addEventListener("visibilitychange", on);
    return () => document.removeEventListener("visibilitychange", on);
  }, []);
  const [pop, setPop] = useState<Pop | null>(null);

  // -- editing helpers ------------------------------------------------------
  const notify = (msg: string) => store.getState().notify("error", msg);
  const apply = useCallback(
    async (ops: Op[]) => {
      if (!ops.length) return;
      store.getState().edit(ops);
      await store.getState().flush();
    },
    [store],
  );
  const childType = (g: CanvasGraph) => (g.kind === "intent" ? "node" : g.kind === "process" ? "step" : "stage");
  const pathOf = (g: CanvasGraph, type: string, id: string) => childPath(childPath(g.path, type), id);
  const selectNew = useCallback(
    async (g: CanvasGraph, id: string) => {
      await store.getState().select(file, pathOf(g, childType(g), id));
      setDrawerOpen(true);
    },
    [store, file],
  );

  const addNode = useCallback(
    async (t: NodeTypeInfo, at?: Point) => {
      const g = graphRef.current;
      if (!g || g.kind === "pipeline") return;
      const r = g.kind === "intent" ? addIntentNode(g, file, t) : addStep(g, file, t);
      const pos = at ?? rf.screenToFlowPosition(centreOf(stageRef.current));
      persist((p) => ({ ...p, [r.id]: pos }));
      await apply(r.ops);
      await selectNew(g, r.id);
    },
    [file, rf, persist, apply, selectNew],
  );

  const addNewStage = useCallback(async () => {
    const g = graphRef.current;
    if (!g || g.kind !== "pipeline") return;
    const r = addStage(g, file);
    await apply(r.ops);
    await selectNew(g, r.id);
  }, [file, apply, selectNew]);

  const canConnect = useCallback<CanvasActions["canConnect"]>(
    (from, fromHandle, to, toHandle) => {
      const g = graphRef.current;
      if (!g || !canEdit || !from || !to) return false;
      if (g.kind === "intent") {
        const target = g.nodes.find((n) => n.id === to);
        const fact = (fromHandle ?? "").replace(/^out:/, "");
        return toHandle === "in:__new" && from !== to && !!target?.editable && !target.requires.includes(fact) && !wouldCycle(g, from, to);
      }
      return g.kind === "process" && g.steps.some((x) => x.id === from) && g.steps.some((x) => x.id === to);
    },
    [canEdit],
  );

  const onConnect = useCallback(
    (c: Connection) => {
      const g = graphRef.current;
      if (!g || !c.source || !c.target) return;
      if (g.kind === "intent") {
        const fact = (c.sourceHandle ?? "").replace(/^out:/, "");
        if (c.targetHandle !== "in:__new") return notify("Drop the line on the “+ needs” port of the step that should use it.");
        const r = connectIntent(g, file, c.source, fact, c.target);
        if (!r.ok) return notify(r.error);
        void apply(r.ops);
      } else if (g.kind === "process") {
        const r = connectProcess(g, s.catalog, file, c.source, c.target, edgeKind);
        if (!r.ok) return notify(r.error);
        void apply(r.ops).then(() => (r.id ? store.getState().select(file, pathOf(g, "edge", r.id)) : undefined));
      }
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [file, s.catalog, edgeKind, apply, store],
  );

  const isValidConnection = useCallback(
    (c: Connection | Edge) => canConnect(c.source ?? "", c.sourceHandle ?? null, c.target ?? "", c.targetHandle ?? null),
    [canConnect],
  );

  const removeEdgeById = useCallback(
    (edgeId: string) => {
      const g = graphRef.current;
      if (!g || !canEdit) return;
      if (g.kind === "intent") {
        const e = g.edges.find((x) => x.id === edgeId);
        if (!e) return;
        const r = disconnectIntent(g, file, e);
        return r.ok ? void apply(r.ops) : notify(r.error);
      }
      if (g.kind === "process") {
        const e = g.edges.find((x) => x.key === edgeId);
        if (e) void apply(disconnectProcess(g, file, e));
      }
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [file, canEdit, apply],
  );

  const deleteSelection = useCallback(
    (delNodes: Node[], delEdges: Edge[]) => {
      const g = graphRef.current;
      if (!g || !canEdit) return;
      const ids = delNodes.map((n) => n.id).filter((id) => id !== INPUT_NODE);
      const gone = new Set(ids);
      const picked = delEdges.filter((e) => e.selected && !gone.has(e.source) && !gone.has(e.target));
      let ops: Op[] = [];
      if (g.kind === "intent") {
        const lines = picked.map((e) => e.data as unknown as Pick<IntentEdge, "to" | "fact">);
        ops = [...disconnectIntentMany(g, file, lines), ...removeNodes(g, file, ids)];
      } else if (g.kind === "process") {
        const lines = picked.map((e) => g.edges.find((x) => x.key === e.id)).filter((x): x is ProcessEdge => !!x);
        ops = [...disconnectProcessMany(g, file, lines), ...(ids.length ? deleteSteps(g, file, ids) : [])];
      } else {
        ops = removeNodes({ nodes: g.stages }, file, ids);
      }
      void apply(ops);
    },
    [file, canEdit, apply],
  );

  const renameChild = useCallback(
    (child: BlockNode) => (newId: string): string | null => {
      const g = graphRef.current;
      if (!g) return null;
      let ops: Op[];
      const id = child.id ?? "";
      if (g.kind === "intent" && child.type === "node") {
        const r = renameIntentNode(g, file, id, newId);
        if (!r.ok) return r.error;
        ops = r.ops;
      } else if (g.kind === "process" && child.type === "step") {
        const r = renameStep(g, file, id, newId);
        if (!r.ok) return r.error;
        ops = r.ops;
      } else {
        const next = newId.trim();
        if (!next) return "A name is required.";
        if (next === id) return null;
        ops = [{ op: "renameBlock", file, path: child.path, newId: next }];
      }
      const next = newId.trim();
      persist((p) => {
        if (!(id in p)) return p;
        const { [id]: pos, ...rest } = p;
        return { ...rest, [next]: pos! };
      });
      void apply(ops).then(() => store.getState().select(file, childPath(childPath(g.path, child.type ?? ""), next)));
      return null;
    },
    [file, apply, persist, store],
  );

  const removeChild = useCallback(
    (child: BlockNode) => () => {
      const g = graphRef.current;
      if (!g) return;
      const id = child.id ?? "";
      let ops: Op[];
      if (g.kind === "process" && child.type === "step") ops = deleteSteps(g, file, [id]);
      else if (g.kind === "intent" && child.type === "node") ops = removeNodes(g, file, [id]);
      else ops = [{ op: "removeBlock", file, path: child.path }];
      void apply(ops).then(() => store.getState().select(file, g.path));
    },
    [file, apply, store],
  );

  // -- "+" popovers ---------------------------------------------------------
  const openInsert = useCallback(
    (edgeId: string, anchor: DOMRect) => {
      setPop({
        anchor,
        title: "Add a step here",
        pick: (t) => {
          const g = graphRef.current;
          setPop(null);
          if (!g) return;
          if (g.kind === "intent") {
            const e = g.edges.find((x) => x.id === edgeId);
            if (!e) return;
            const r = insertIntentNode(g, file, e, t);
            if (!r.ok) return notify(r.error);
            void apply(r.ops).then(() => selectNew(g, r.id));
          } else if (g.kind === "process") {
            const e = g.edges.find((x) => x.key === edgeId);
            if (!e) return;
            const r = insertStep(g, s.catalog, file, e, t);
            if (!r.ok) return notify(r.error);
            void apply(r.ops).then(() => selectNew(g, r.id));
          }
        },
      });
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [file, s.catalog, apply, selectNew],
  );

  const openAppend = useCallback(
    (nodeId: string, anchor: DOMRect) => {
      setPop({
        anchor,
        title: "What happens next?",
        pick: (t) => {
          const g = graphRef.current;
          setPop(null);
          if (!g) return;
          if (g.kind === "intent") {
            const r = appendIntentNode(g, file, nodeId, t);
            if (!r.ok) return notify(r.error);
            const from = elementsRef.current?.auto ?? {};
            void from;
            void apply(r.ops).then(() => selectNew(g, r.id));
          } else if (g.kind === "process") {
            const r = appendStep(g, file, nodeId, t);
            if (!r.ok) return notify(r.error);
            void apply(r.ops).then(() => selectNew(g, r.id));
          }
        },
      });
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [file, apply, selectNew],
  );
  const elementsRef = useRef(elements);
  elementsRef.current = elements;

  // -- toolbar actions ------------------------------------------------------
  const fitPadding = useCallback(
    () => ({ top: "72px", bottom: "32px", left: railOpen ? "336px" : "72px", right: drawerOpen ? "460px" : "48px" } as const),
    [railOpen, drawerOpen],
  );
  const fit = useCallback(() => void rf.fitView({ duration: viewportMs(motion), padding: fitPadding() }), [rf, motion, fitPadding]);
  // A long flow fitted whole is too small to read. Open at a readable zoom instead, with the start of the flow in view.
  const initialized = useNodesInitialized();
  const opened = useRef("");
  useEffect(() => {
    const id = `${file}|${path}`;
    if (!initialized || opened.current === id) return;
    let cancelled = false;
    const t = setTimeout(async () => {
      opened.current = id; // only once it has run: a re-run of this effect before then must reschedule, not skip
      // Fit once the page has settled (the shell animates in), then make sure the text is still readable.
      // fitView is asynchronous: wait for it, or it lands after (and undoes) the readable zoom below.
      await rf.fitView({ padding: fitPadding(), maxZoom: 1.1, duration: 0 });
      if (cancelled) return;
      const vp = rf.getViewport();
      if (vp.zoom >= MIN_FIT_ZOOM) return;
      const ns = rf.getNodes().filter((n) => n.measured?.width);
      if (!ns.length) return;
      const minX = Math.min(...ns.map((n) => n.position.x));
      const el = stageRef.current;
      const span = (el?.clientWidth ?? 1200) / READABLE_ZOOM;
      const first = ns.filter((n) => n.position.x < minX + span);
      const top = Math.min(...first.map((n) => n.position.y));
      const bottom = Math.max(...first.map((n) => n.position.y + (n.measured?.height ?? 0)));
      const h = el?.clientHeight ?? 700;
      void rf.setViewport({ x: 72 - minX * READABLE_ZOOM, y: h / 2 - ((top + bottom) / 2) * READABLE_ZOOM, zoom: READABLE_ZOOM }, { duration: 0 });
    }, 220);
    return () => { cancelled = true; clearTimeout(t); };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [initialized, file, path, rf]);

  const tidy = useCallback(() => {
    if (motion) {
      setTweening(true);
      setTimeout(() => setTweening(false), TWEEN_MS + 60);
    }
    clearPositions(key);
    setSaved({});
    setTimeout(fit, motion ? TWEEN_MS : 30);
  }, [key, motion, fit]);

  const traceTimers = useRef<ReturnType<typeof setTimeout>[]>([]);
  const trace = useCallback(() => {
    const g = graphRef.current;
    traceTimers.current.forEach(clearTimeout);
    traceTimers.current = [];
    if (!g) return;
    const groups = traceOrder(g);
    if (!motion) {
      setActive(new Set(groups.flat()));
      traceTimers.current.push(setTimeout(() => setActive(new Set()), 300));
      return;
    }
    groups.forEach((grp, i) => {
      traceTimers.current.push(setTimeout(() => setActive(new Set(grp)), i * 700));
    });
    traceTimers.current.push(setTimeout(() => setActive(new Set()), groups.length * 700 + 300));
  }, [motion]);
  useEffect(() => () => traceTimers.current.forEach(clearTimeout), []);

  const flowEdges = useMemo(
    () => (graph?.kind === "intent" ? graph.edges.map((e) => ({ id: e.id, from: e.from, to: e.to })) : graph?.kind === "process" ? graph.edges.map((e) => ({ id: e.key, from: e.from, to: e.to })) : []),
    [graph],
  );
  // Only selection drives the flow animation: the union of edges connected to every selected step.
  const selectedIds = useMemo(() => nodes.filter((n) => n.selected && !(n.data as { exiting?: boolean }).exiting).map((n) => n.id), [nodes]);
  const focus = useMemo(() => (prefs.on && selectedIds.length && flowEdges.length ? connectedEdges(flowEdges, selectedIds) : null), [prefs.on, selectedIds, flowEdges]);

  // The actions object never changes (each method reads the latest closure), so providing it re-renders nothing.
  const impl = {
    moveStage: (id: string, dir: -1 | 1) => { const g = graphRef.current; if (g?.kind === "pipeline") void apply(moveStage(g, file, id, dir)); },
    setStart: (id: string) => { const g = graphRef.current; if (g?.kind === "process") void apply(setStart(g, file, id)); },
    select: (p: string) => void store.getState().select(file, p, p),
    insertOnEdge: openInsert,
    appendAfter: openAppend,
    removeEdge: removeEdgeById,
    editEdge: (edgeId: string, anchor: DOMRect) => setEditing({ edgeId, anchor }),
    selectEdge: (id: string) => {
      const g = graphRef.current;
      const e = g?.kind === "process" ? g.edges.find((x) => x.key === id) : undefined;
      if (e) void store.getState().select(file, e.block.path);
    },
    canConnect,
  };
  const latest = useRef(impl);
  latest.current = impl;
  const actions = useMemo<CanvasActions>(
    () => ({
      readOnly: !canEdit,
      motion,
      labels,
      moveStage: (...a) => latest.current.moveStage(...a),
      setStart: (...a) => latest.current.setStart(...a),
      select: (...a) => latest.current.select(...a),
      insertOnEdge: (...a) => latest.current.insertOnEdge(...a),
      appendAfter: (...a) => latest.current.appendAfter(...a),
      removeEdge: (...a) => latest.current.removeEdge(...a),
      selectEdge: (...a) => latest.current.selectEdge(...a),
      editEdge: (...a) => latest.current.editEdge(...a),
      canConnect: (...a) => latest.current.canConnect(...a),
    }),
    [canEdit, motion, labels],
  );
  // The data-flow animation reads from its own store: a change of selection re-renders only the edges it touches.
  useEffect(() => {
    flowStore.set({ on: prefs.on, speed: prefs.speed, focus, total: flowEdges.length });
  }, [flowStore, prefs.on, prefs.speed, focus, flowEdges.length]);

  // Clicking empty canvas, or Esc, deselects everything: the flow animation stops.
  const deselect = useCallback(() => {
    setGuides({ x: [], y: [], dx: 0, dy: 0 });
    const g = graphRef.current;
    if (g) void store.getState().select(file, g.path);
    setPop(null);
    setNodes((ns) => (ns.some((n) => n.selected) ? ns.map((n) => (n.selected ? { ...n, selected: false } : n)) : ns));
    setEdges((es) => (es.some((e) => e.selected) ? es.map((e) => (e.selected ? { ...e, selected: false } : e)) : es));
  }, [store, file, setGuides]);

  // -- keyboard: undo / redo / validate, as in the editor -------------------
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        const t = e.target as HTMLElement | null;
        if (!t || !["INPUT", "TEXTAREA", "SELECT"].includes(t.tagName)) deselect();
        return;
      }
      const a = shortcutFor(e);
      if (!a) return;
      e.preventDefault();
      const st = store.getState();
      if (a === "undo") void st.undo();
      else if (a === "redo") void st.redo();
      else void st.validate();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [store, deselect]);

  const onDrop = useCallback(
    (e: React.DragEvent) => {
      const name = e.dataTransfer.getData(DRAG_MIME);
      if (!name || !canEdit) return;
      e.preventDefault();
      const t = s.catalog?.node_types.find((x) => x.name === name);
      if (t) void addNode(t, rf.screenToFlowPosition({ x: e.clientX, y: e.clientY }));
    },
    [s.catalog, canEdit, addNode, rf],
  );

  if (!s.draft) {
    return (
      <main className="page">
        <p className="empty">Open a draft first. <a href="#/">Back to the editor</a></p>
      </main>
    );
  }
  if (!file || !path) {
    return <main className="page"><p className="empty">Choose a flow, process or pipeline to open here. <a href="#/">Back to the editor</a></p></main>;
  }
  if (!root) {
    return <main className="page"><p className="empty" aria-busy="true">Loading {path}…</p></main>;
  }
  if (!graph || !elements) {
    return <main className="page"><p className="empty">“{root.type}” has no picture. <a href="#/">Back to the editor</a></p></main>;
  }

  const problems = s.diagnostics.filter((d) => d.path && isWithin(d.path, graph.path) && (!d.file || d.file === file));
  const unresolved = graph.kind === "intent" ? graph.unresolved : [];
  const palette = graph.kind === "intent" ? (s.catalog?.node_types ?? []).filter((t) => !t.durable) : graph.kind === "process" ? (s.catalog?.node_types ?? []) : [];
  const edgeKinds = (s.catalog?.edge_types ?? []).filter((e) => edgeShape(e) !== "none");
  const dur = viewportMs(motion);

  return (
    <CanvasActionsProvider value={actions}>
    <FlowProvider value={flowStore}>
    <CollapsedProvider value={collapsedStore}>
      <div data-flow-focus={focus ? [...focus].join(",") : ""} className={cx("canvas-page", !motion && "cv-static", hidden && "cv-paused", ready && "cv-ready", tweening && "cv-tween", drawerOpen && "drawer-open", railOpen && "rail-open", minimap && "has-minimap")}>
        <div
          className="cv-stage"
          ref={stageRef}
          onDragOver={(e) => { if (canEdit) { e.preventDefault(); e.dataTransfer.dropEffect = "copy"; } }}
          onDrop={onDrop}
        >
          <ReactFlow
            nodes={nodes}
            edges={shownEdges}
            nodeTypes={nodeTypes}
            edgeTypes={edgeTypes}
            connectionLineComponent={ConnectionLine}
            connectionRadius={36}
            onNodesChange={onNodesChange}
            onEdgesChange={onEdgesChange}
            onConnect={onConnect}
            isValidConnection={isValidConnection}
            onBeforeDelete={async ({ nodes: dn, edges: de }) => {
              deleteSelection(dn, de);
              return false; // the tree refresh redraws; the server is the source of truth
            }}
            onNodeDragStop={(_, n) => { setGuides({ x: [], y: [], dx: 0, dy: 0 }); persist((p) => ({ ...p, [n.id]: n.position })); }}
            // Open the details for a step or connection the user clicks. (Not for selection *reports*: React Flow
            // echoes back the previous selection while ours is catching up, and that must not undo a change of step.)
            onNodeClick={(e, n) => {
              if (e.shiftKey || e.metaKey || e.ctrlKey) return; // building a multi-selection
              const p = (n.data as { info?: { path: string } }).info?.path;
              if (!p) return;
              if (p !== owner?.path) void store.getState().select(file, p);
              setDrawerOpen(true);
            }}
            onEdgeClick={(e, edge) => {
              if (e.shiftKey || e.metaKey || e.ctrlKey) return;
              const bp = (edge.data as { blockPath?: string } | undefined)?.blockPath;
              if (!bp) return;
              if (bp !== owner?.path) void store.getState().select(file, bp);
              setDrawerOpen(true);
            }}
            onPaneClick={deselect}
            nodesConnectable={canEdit}
            deleteKeyCode={canEdit ? ["Delete", "Backspace"] : null}
            snapToGrid
            nodeDragThreshold={3}
            snapGrid={[GRID, GRID]}
            colorMode={colorMode}
            fitView
            fitViewOptions={{ padding: { top: "72px", bottom: "32px", left: "72px", right: "48px" } as const }}
            onlyRenderVisibleElements={nodes.length > 60}
            minZoom={0.15}
            maxZoom={1.8}
            zoomOnDoubleClick={false}
            multiSelectionKeyCode={["Shift", "Meta", "Control"]}
            proOptions={{ hideAttribution: false }}
            aria-label={`${friendlyKind(graph.kind)} diagram`}
          >
            <Background variant={BackgroundVariant.Dots} gap={24} size={1.4} />
            <Controls position="bottom-left" showInteractive={false} fitViewOptions={{ padding: fitPadding(), duration: dur }} aria-label="Zoom" />
            {minimap && <MiniMap pannable zoomable ariaLabel="Overview" nodeStrokeWidth={0} className="cv-minimap" nodeColor={minimapColor} maskColor="transparent" />}
            <ViewportPortal>
              {guides.x.map((x) => <div key={`gx${x}`} className="cv-guide v" style={{ transform: `translate(${x}px, -100000px)` }} />)}
              {guides.y.map((y) => <div key={`gy${y}`} className="cv-guide h" style={{ transform: `translate(-100000px, ${y}px)` }} />)}
            </ViewportPortal>
          </ReactFlow>

          {/* who / where */}
          <div className="cv-crumb">
            <a href="#/" className="cv-back" aria-label="Back to the editor" title="Back to the editor"><Icon name="chevron" size={12} /> Editor</a>
            <h1><span>{friendlyKind(graph.kind)}</span> <code>{graph.id}</code></h1>
            {problems.length > 0 && !drawerOpen && (
              <button type="button" className="cv-warnbtn" onClick={() => setDrawerOpen(true)} title="Show what needs attention">
                <Icon name="alert" size={12} /> {count(problems.length, "problem")}
              </button>
            )}
            {unresolved.length > 0 && (
              <button type="button" className="cv-warnbtn" onClick={() => void store.getState().select(file, pathOf(graph, "node", unresolved[0]!.node))} title="Some steps need something no step produces">
                <Icon name="alert" size={12} /> {unresolved.length} missing {unresolved.length === 1 ? "input" : "inputs"}
              </button>
            )}
          </div>

          {/* slim toolbar */}
          <div className="cv-toolbar" role="toolbar" aria-label="Canvas tools">
            <button type="button" onClick={() => void store.getState().undo()} disabled={!canEdit} aria-label="Undo" title="Undo (Ctrl+Z)"><Icon name="undo" /></button>
            <button type="button" onClick={() => void store.getState().redo()} disabled={!canEdit} aria-label="Redo" title="Redo (Ctrl+Shift+Z)"><Icon name="redo" /></button>
            <span className="sep" />
            <button type="button" onClick={tidy} aria-label="Tidy up" title="Arrange automatically (forgets hand-placed positions)"><Icon name="layout" /></button>
            <button type="button" onClick={fit} aria-label="Fit to screen" title="Fit everything on screen"><Icon name="fit" /></button>
            <span className="sep" />
            <button type="button" aria-pressed={minimap} onClick={() => setMinimap((m) => !m)} aria-label="Overview map" title="Show or hide the overview map"><Icon name="map" /></button>
            <button type="button" aria-pressed={legend} onClick={() => setLegend((l) => !l)} aria-label="What do the colours mean?" title="What do the colours mean?"><Icon name="info" /></button>
            <button type="button" onClick={trace} aria-label="Trace" title="Show the order a run visits the connections"><Icon name="trace" /></button>
            <span className="sep" />
            <button type="button" aria-pressed={prefs.on} onClick={() => setFlow({ on: !prefs.on })} aria-label="Show data flow" title={prefs.on ? "Data moves along the connections of the selected step. Click to keep everything still." : "Everything stays still. Click to animate the selected step's connections."}><Icon name="bolt" /></button>
            {prefs.on && (
              <label className="cv-speed">
                <span className="sr-only">Speed of the data flow</span>
                <select value={prefs.speed} onChange={(e) => setFlow({ speed: e.target.value as Speed })} aria-label="Speed of the data flow" title="How fast data moves along connections">
                  {SPEEDS.map((sp) => <option key={sp} value={sp}>{sp === "slow" ? "Slow" : sp === "fast" ? "Fast" : "Normal"}</option>)}
                </select>
              </label>
            )}
            {graph.kind === "process" && (
              <>
                <span className="sep" />
                <label className="cv-edgekind">
                  <span>New connection</span>
                  <select value={edgeKind} onChange={(e) => setEdgeKind(e.target.value)} aria-label="Kind of connection to create" disabled={!canEdit}>
                    {groupBy(edgeKinds, (e) => e.family).map(([fam, list]) => (
                      <optgroup key={fam} label={fam}>
                        {list.map((e) => <option key={e.name} value={e.name} title={e.summary}>{edgeLabel(e.name)}</option>)}
                      </optgroup>
                    ))}
                  </select>
                </label>
              </>
            )}
            {graph.kind === "pipeline" && canEdit && <button type="button" className="cv-textbtn" onClick={() => void addNewStage()}>+ Stage</button>}
          </div>

          {/* left rail: kinds of step */}
          {graph.kind !== "pipeline" && (
            <aside className="cv-rail" data-open={railOpen} aria-label={graph.kind === "intent" ? "Add a step" : "Add a step"}>
              <button type="button" className="cv-railtoggle" aria-expanded={railOpen} aria-label={railOpen ? "Hide the step list" : "Show the step list"} onClick={() => setRailOpen((o) => !o)}>
                <Icon name="palette" />
              </button>
              <div className="cv-railbody" aria-hidden={!railOpen}>
                <h3>Add a step</h3>
                <p className="cv-railhint">Click one, or drag it onto the canvas.</p>
                <TypeList types={palette} catalog={s.catalog} disabled={!canEdit} onPick={(t) => void addNode(t)} />
              </div>
            </aside>
          )}

          {/* right drawer: details */}
          <aside className="cv-drawer" data-open={drawerOpen} aria-label="Details" aria-hidden={!drawerOpen}>
            <div className="cv-drawerbody">
              {panelOwner ? (
                <NodePanel key={panelOwner.path} file={file} rootType={root.type ?? ""} node={panelOwner} graph={graph} onRename={renameChild(panelOwner)} onRemove={removeChild(panelOwner)} onSetStart={(id) => graph.kind === "process" && void apply(setStart(graph, file, id))} />
              ) : (
                <Inspector />
              )}
              <section className="cv-problems" aria-label="Problems in this diagram">
                <h3>Problems ({problems.length})</h3>
                {problems.length === 0 ? (
                  <p className="cv-none">None found.</p>
                ) : (
                  <ul>
                    {problems.map((d, i) => (
                      <li key={i}>
                        <button type="button" className={`problem ${d.severity}`} title={d.message} onClick={() => void store.getState().selectFromDiagnostic(d)}>
                          {friendlyProblem(d).text}
                          {d.path && <small> · {problemWhere({ ...d, path: d.path.replace(graph.path + "/", "node/") })}</small>}
                        </button>
                      </li>
                    ))}
                  </ul>
                )}
              </section>
            </div>
          </aside>
          <button type="button" className="cv-drawertoggle" aria-expanded={drawerOpen} aria-label={drawerOpen ? "Hide the details panel" : "Show the details panel"} onClick={() => setDrawerOpen((o) => !o)}>
            <Icon name="panel" />
          </button>

          {graph.kind === "intent" && graph.nodes.length === 0 && <p className="cv-hint">Nothing here yet. Pick a step from the list on the left.</p>}
          {graph.kind === "process" && graph.unlinked.length > 0 && (
            <div className="cv-unlinked" role="status">
              <strong>Not drawn:</strong>
              {graph.unlinked.map((u) => (
                <button key={u.block.path} type="button" className="link" onClick={() => void store.getState().select(file, u.block.path)}>
                  {u.block.id} {u.missing.length ? `(no step “${u.missing.join("”, “")}”)` : "(nothing to connect)"}
                </button>
              ))}
            </div>
          )}

          {legend && <Legend kind={graph.kind} onClose={() => setLegend(false)} />}
          {editing && graph.kind === "process" && (() => {
            const pe = graph.edges.find((x) => x.key === editing.edgeId);
            return pe ? <EdgePopover key={editing.edgeId} anchor={editing.anchor} file={file} block={pe.block} catalog={s.catalog} readOnly={!canEdit} siblings={graph.edgeBlocks} facts={[]} edit={(ops) => store.getState().edit(ops)} onMore={() => { setEditing(null); void store.getState().select(file, pe.block.path); setDrawerOpen(true); }} onClose={() => setEditing(null)} /> : null;
          })()}
          {pop && <InsertPopover anchor={pop.anchor} title={pop.title} types={palette} catalog={s.catalog} onPick={pop.pick} onClose={() => setPop(null)} />}
        </div>
      </div>
    </CollapsedProvider>
    </FlowProvider>
    </CanvasActionsProvider>
  );
}

/** Minimap colour of a card: its tone as a CSS variable, so it follows the theme. */
function minimapColor(n: Node): string {
  const d = n.data as { visual?: { tone?: string }; outcome?: string; info?: { terminal?: boolean } } | undefined;
  const tone = d?.outcome ? `end-${d.outcome}` : n.type === "request" ? "start" : n.type === "stage" ? "process" : (d?.visual?.tone ?? "compute");
  return `var(--t-${tone})`;
}

function centreOf(el: HTMLElement | null): Point {
  const r = el?.getBoundingClientRect();
  return r ? { x: r.left + r.width / 2, y: r.top + r.height / 2 } : { x: 300, y: 200 };
}

function groupBy<T>(xs: T[], key: (x: T) => string): [string, T[]][] {
  const m = new Map<string, T[]>();
  for (const x of xs) m.set(key(x), [...(m.get(key(x)) ?? []), x]);
  return [...m.entries()];
}

export { TERMS, addStep };
export default CanvasPage;
