import {
  BaseEdge, EdgeLabelRenderer, getBezierPath, getSmoothStepPath, useStore, useStoreApi, type ConnectionLineComponentProps, type EdgeProps,
} from "@xyflow/react";
import { memo, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useCanvasActions, useEdgeFlow } from "./context";
import { probe } from "./probe";
import { Icon } from "./icons";
import { labelSize } from "./layout";
import { pickLabelT, type Rect } from "./labelPlacement";
import { FADE_MS, MIN_DOT_ZOOM, flowStyleFor, planDots, type FlowStyle, type Speed } from "./flow";
import { INPUT_NODE } from "./model";
import { COMPACT_ZOOM } from "./nodes";
import { useUi } from "../state/ui";

/** Colours for one look of the flow edge. A host (a different view) can bring its own. */
export interface FlowPalette {
  line?: string;
  dot?: string;
  error?: string;
  park?: string;
}

/** Reusable colour sets. `journey` is for the Journeys view. */
export const FLOW_PALETTES: Record<string, FlowPalette> = {
  default: {},
  journey: { dot: "#0f9d8a", line: "#7fb8b0", error: "#d1453b", park: "#8a6fd1" },
};

export interface EdgeData {
  label?: string;
  /** the expression as written, for the Developer view */
  raw?: string;
  /** hover text: the whole story, including the expression */
  tip?: string;
  tone?: "normal" | "quiet" | "error" | "wait" | "todo";
  icon?: string;
  /** set on a connection that means nothing yet: the prompt that invites filling it in */
  needs?: string;
  insertable?: boolean;
  /** the request feeding a step: drawn faintly until the step is selected */
  quiet?: boolean;
  plain?: boolean;
  blockPath?: string;
  errorPath?: boolean;
  parks?: boolean;
  from?: string;
  /** every fact/field this line carries, for the hover text */
  carries?: string[];
  /** how many things travel together (shown in the pill when more than one) */
  count?: number;
  palette?: FlowPalette | string;
  /** "bezier": forward lines are smooth curves (the Journeys map). Loops back keep their routing. */
  curve?: "bezier";
  /** How far a line that loops back stands off the card it returns to (default 34). */
  backOffset?: number;
  [k: string]: unknown;
}

const paletteVars = (p: FlowPalette | string | undefined): React.CSSProperties => {
  const pal = typeof p === "string" ? FLOW_PALETTES[p] : p;
  if (!pal) return {};
  return {
    ...(pal.line ? { ["--cv-line" as string]: pal.line } : {}),
    ...(pal.dot ? { ["--cv-dot" as string]: pal.dot } : {}),
    ...(pal.error ? { ["--cv-dot-error" as string]: pal.error } : {}),
    ...(pal.park ? { ["--cv-dot-park" as string]: pal.park } : {}),
  };
};

/** Class names for an edge from its state. Pure, so the reduced-motion fallback and the highlight states are tested. */
export function edgeClass(o: { kind: string; hover?: boolean; focused?: boolean; dimmed?: boolean; quiet?: boolean; motion: boolean; flowOn: boolean }): string {
  return [
    "cv-edge",
    `kind-${o.kind}`,
    o.hover && "hover",
    o.focused && "focus",
    o.dimmed && "dim",
    o.quiet && "quiet",
    // reduced motion (or animations off): no travelling dots, a subtle dashed line and the arrowhead instead
    !o.motion && "cv-edge-static",
    !o.flowOn && "flow-off",
  ].filter(Boolean).join(" ");
}

/**
 * The travelling dots. Pure SVG: each dot is a circle moved along the edge by
 * <animateMotion>, which the browser runs itself. The dots start part-way through
 * their crossing (negative begin) so no two edges pulse together.
 */
export function FlowDots({ id, d, length, speed, style, boosted, on, fading }: { id: string; d: string; length: number; speed: Speed; style: FlowStyle; boosted?: boolean; on: boolean; fading?: boolean }) {
  if (!on) return null;
  const plan = planDots(id, { length, speed, style, boosted });
  return (
    <g className={`cv-dots ${style.kind}${fading ? " fading" : ""}`} aria-hidden data-dots={plan.count} style={{ opacity: fading ? 0 : style.opacity }}>
      {plan.begins.map((begin, i) => (
        <circle key={i} className={`cv-dot${style.hollow ? " hollow" : ""}`} r={style.dotRadius}>
          <animateMotion dur={`${plan.dur.toFixed(2)}s`} begin={`${begin.toFixed(2)}s`} repeatCount="indefinite" path={d} calcMode="linear" />
        </circle>
      ))}
    </g>
  );
}

const HIDE_DELAY = 140;

function FlowEdgeView(props: EdgeProps) {
  probe("edge");
  const { id, sourceX, sourceY, targetX, targetY, sourcePosition, targetPosition, markerEnd, style: edgeStyle, selected, interactionWidth } = props;
  const data = (props.data ?? {}) as EdgeData;
  const actions = useCanvasActions();
  // A line that runs back against the flow (a loop) goes around instead of crossing the forward line.
  const back = targetX < sourceX + 24;
  const [path, midX, midY] = back
    ? getSmoothStepPath({ sourceX, sourceY, sourcePosition, targetX, targetY, targetPosition, borderRadius: 18, offset: data.backOffset ?? 34, centerY: Math.min(sourceY, targetY) - 84 })
    : data.curve === "bezier"
      ? getBezierPath({ sourceX, sourceY, sourcePosition, targetX, targetY, targetPosition, curvature: 0.32 })
      : getSmoothStepPath({ sourceX, sourceY, sourcePosition, targetX, targetY, targetPosition, borderRadius: 12, offset: 24 });
  const group = useRef<SVGGElement>(null);
  const [hover, setHover] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [pos, setPos] = useState({ x: midX, y: midY });
  const [len, setLen] = useState(() => Math.hypot(targetX - sourceX, targetY - sourceY) * 1.15);

  // Every step's box, so the label can slide off them. Read when the label is placed, not subscribed to:
  // subscribing would re-render every edge whenever any step changes.
  const api = useStoreApi();
  const compact = useStore((s) => s.transform[2] < COMPACT_ZOOM);
  // Plain words by default; the Developer view shows the expression as written.
  const dev = useUi((u) => u.devView);
  const text = (dev && typeof data.raw === "string" ? data.raw : data.label) ?? "";
  const sizeText = text || (data.count && data.count > 1 ? `${data.count} things` : "");
  const size = useMemo(() => ({ w: sizeText ? labelSize(sizeText).width : 16, h: 24 }), [sizeText]);

  useLayoutEffect(() => {
    if (compact) return;
    const el = group.current?.querySelector<SVGPathElement>("path.react-flow__edge-path");
    let at: (t: number) => { x: number; y: number } = () => ({ x: midX, y: midY });
    if (el && typeof el.getTotalLength === "function") {
      try {
        const len = el.getTotalLength();
        if (len > 0) at = (t) => { const p = el.getPointAtLength(len * t); return { x: p.x, y: p.y }; };
      } catch {
        /* jsdom: fall back to the middle */
      }
    }
    if (el && typeof el.getTotalLength === "function") {
      try {
        const l = el.getTotalLength();
        if (l > 0) setLen((prev) => (Math.abs(prev - l) < 1 ? prev : l));
      } catch {
        /* jsdom */
      }
    }
    const boxes: Rect[] = [];
    for (const n of api.getState().nodeLookup.values()) {
      const w = n.measured?.width ?? n.width ?? 0;
      const h = n.measured?.height ?? n.height ?? 0;
      if (w && h) boxes.push({ x: n.internals.positionAbsolute.x, y: n.internals.positionAbsolute.y, w, h });
    }
    const placed = pickLabelT(at, size, [...boxes, ...actions.labels.others(id)]);
    setPos((p) => (Math.abs(p.x - placed.x) < 0.5 && Math.abs(p.y - placed.y) < 0.5 ? p : { x: placed.x, y: placed.y }));
    actions.labels.set(id, { x: placed.x - size.w / 2, y: placed.y - size.h / 2, w: size.w, h: size.h });
  }, [path, midX, midY, size, id, actions.labels, api, compact]);

  useEffect(() => () => void (timer.current && clearTimeout(timer.current)), []);
  const enter = () => {
    if (timer.current) clearTimeout(timer.current);
    setHover(true);
  };
  const leave = () => {
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(() => setHover(false), HIDE_DELAY);
  };
  const open = (hover || !!selected) && !data.plain;
  const canEdit = !actions.readOnly && !data.plain;

  // -- data flow ------------------------------------------------------------
  const flow = useEdgeFlow(id);
  const zoomOk = useStore((s) => s.transform[2] >= MIN_DOT_ZOOM);
  // Zoomed far out, labels and tools cannot be read: draw the line alone.
  const focused = flow.mark === "focus";
  const dimmed = flow.mark === "dim";
  const style = useMemo(() => flowStyleFor({ errorPath: data.errorPath, parks: data.parks, fromRequest: data.from === INPUT_NODE }), [data.errorPath, data.parks, data.from]);
  // Selection drives everything: an edge animates only when it is connected to a selected step.
  // Hovering an edge (or a step) never starts it; hovering an edge only shows what it carries.
  const animate = actions.motion && flow.on && zoomOk && focused && !data.plain;
  const [showDots, setShowDots] = useState(animate);
  const [fading, setFading] = useState(false);
  useEffect(() => {
    if (animate) {
      setShowDots(true);
      setFading(false);
      return;
    }
    // deselected: fade the dots out instead of cutting them
    setFading(true);
    const t = setTimeout(() => { setShowDots(false); setFading(false); }, FADE_MS);
    return () => clearTimeout(t);
  }, [animate]);
  const carries = data.carries?.length ? `Carries: ${data.carries.join(", ")}` : "";
  const pill = text || (data.count && data.count > 1 ? `${data.count} things` : "");

  return (
    <>
      <g
        ref={group}
        className={edgeClass({ kind: style.kind, hover, focused, dimmed, quiet: data.quiet, motion: actions.motion, flowOn: flow.on })}
        style={paletteVars(data.palette)}
        onMouseEnter={enter}
        onMouseLeave={leave}
      >
        {carries && <title>{carries}</title>}
        <BaseEdge id={id} path={path} markerEnd={markerEnd} style={edgeStyle} interactionWidth={interactionWidth ?? 22} />
        <FlowDots id={id} d={path} length={len} speed={flow.speed} style={style} boosted={false} on={showDots} fading={fading} />
      </g>
      {!compact && <EdgeLabelRenderer>
        <div
          className={`cv-elabel nodrag nopan tone-${data.tone ?? "normal"}${data.needs ? " needs" : ""}${pill ? " has-text" : ""}${open ? " open" : ""}${selected ? " sel" : ""}${data.plain ? " plain" : ""}${dimmed ? " dim" : ""}`}
          style={{ transform: `translate(-50%, -50%) translate(${pos.x}px, ${pos.y}px)` }}
          data-edge-id={id}
          onMouseEnter={enter}
          onMouseLeave={leave}
        >
          {pill ? (
            <button
              type="button" className="cv-elabel-text"
              onClick={(e) => (data.blockPath ? actions.editEdge(id, e.currentTarget.getBoundingClientRect()) : actions.selectEdge(id))}
              title={data.tip ?? (carries ? `${text ? text + " · " : ""}${carries}` : text)}
            >
              {data.icon && <Icon name={data.icon} size={11} />}
              <span>{pill}</span>
            </button>
          ) : (
            !data.plain && <span className="cv-elabel-dot" aria-hidden />
          )}
          {canEdit && (
            <span className="cv-elabel-tools">
              {data.insertable && (
                <button type="button" className="ins" aria-label="Add a step here" title="Add a step here" onClick={(e) => actions.insertOnEdge(id, e.currentTarget.getBoundingClientRect())}>
                  <Icon name="plus" size={12} />
                </button>
              )}
              <button type="button" className="del" aria-label="Remove this connection" title="Remove this connection" onClick={() => actions.removeEdge(id)}>
                <Icon name="x" size={12} />
              </button>
            </span>
          )}
        </div>
      </EdgeLabelRenderer>}
    </>
  );
}

/** The connection line with data flowing along it. Reusable: hand it a palette through `data.palette`. */
export const FlowEdge = memo(FlowEdgeView);
export const LabeledEdge = FlowEdge;
export const flowEdgeTypes = { labeled: FlowEdge, flow: FlowEdge };
export const edgeTypes = flowEdgeTypes;

/**
 * The line you drag out of a port. It trails the pointer with a little spring,
 * turns green over a port it can join and red over one it cannot.
 */
export function ConnectionLine({ fromX, fromY, toX, toY, connectionStatus }: ConnectionLineComponentProps) {
  const actions = useCanvasActions();
  const [end, setEnd] = useState({ x: toX, y: toY });
  const target = useRef({ x: toX, y: toY });
  target.current = { x: toX, y: toY };
  useEffect(() => {
    if (!actions.motion) return;
    let raf = 0;
    const v = { x: 0, y: 0 };
    const cur = { x: target.current.x, y: target.current.y };
    const step = () => {
      // critically damped spring toward the pointer
      v.x += (target.current.x - cur.x) * 0.22;
      v.y += (target.current.y - cur.y) * 0.22;
      v.x *= 0.62;
      v.y *= 0.62;
      cur.x += v.x;
      cur.y += v.y;
      setEnd({ x: cur.x, y: cur.y });
      raf = requestAnimationFrame(step);
    };
    raf = requestAnimationFrame(step);
    return () => cancelAnimationFrame(raf);
  }, [actions.motion]);
  const ex = actions.motion ? end.x : toX;
  const ey = actions.motion ? end.y : toY;
  const [d] = getBezierPath({ sourceX: fromX, sourceY: fromY, targetX: ex, targetY: ey, curvature: 0.34 });
  return (
    <g className={`cv-connline ${connectionStatus ?? "pending"}`}>
      <path d={d} fill="none" />
      <circle cx={ex} cy={ey} r={5} />
    </g>
  );
}
