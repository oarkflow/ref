// The cards on the canvas. Every step, whatever its type, is the same rectangular
// card (264px wide, 8px corners): a header with a solid icon tile, a few labelled
// fact rows, and a footer of small chips. What a step *is* shows in its colour, its
// icon and its badges - never in a different silhouette - so a diagram reads as
// one system. Sizes come from layout.cardSize; keep the two in step.
import { Handle, Position, useConnection, useStore, type Node, type NodeProps } from "@xyflow/react";
import { memo, useEffect, useRef, useState, type ReactNode } from "react";
import { useCanvasActions, useCollapsed, useToggleCollapse } from "./context";
import { probe } from "./probe";
import { Icon } from "./icons";
import { INPUT_NODE, INPUT_FACT, type IntentNodeInfo, type StageInfo, type StepInfo } from "./model";
import { HANDLE_Y, INTENT_ROW, SEC_PAD } from "./layout";
import type { Visual } from "./kinds";
import { OUTCOME_TEXT, chipFor, visibleRows, type Outcome, type Row } from "./look";
import { TERMS, typeLabel } from "./labels";

export interface DiagBadge {
  errors: number;
  warnings: number;
}

export type IntentData = { info: IntentNodeInfo; rows: Row[]; unresolved: string[]; diag: DiagBadge; visual: Visual; typeText: string; outcome?: Outcome; canAppend?: boolean; exiting?: boolean } & Record<string, unknown>;
export type InputData = { label: string; exiting?: boolean } & Record<string, unknown>;
export type StepData = { info: StepInfo; rows: Row[]; diag: DiagBadge; visual: Visual; typeText: string; outcome?: Outcome; hasOutgoing: boolean; canAppend?: boolean; exiting?: boolean } & Record<string, unknown>;
export type StageData = { info: StageInfo; first: boolean; last: boolean; diag: DiagBadge; exiting?: boolean } & Record<string, unknown>;

const cx = (...a: (string | false | undefined | null)[]) => a.filter(Boolean).join(" ");
const unique = (xs: string[]) => [...new Set(xs)];
/** Below this zoom the text cannot be read; cards draw compactly (a name and a colour). */
export const COMPACT_ZOOM = 0.5;
/** True only while zoomed out that far: a boolean selector, so nodes re-render when crossing the threshold, not on every zoom step. */
export const useCompact = () => useStore((s) => s.transform[2] < COMPACT_ZOOM);

const handleAt = { top: HANDLE_Y, transform: "translate(0, -50%)" } as const;

/** Ending steps say how they end in their icon. */
const OUTCOME_ICON: Record<Outcome, string> = { success: "check", failed: "x", cancelled: "ban", neutral: "flag" };

/** The colour family of a card: an ending takes the colour of how it ends, everything else its type's. */
export const toneOf = (visual: Visual, outcome?: Outcome): string => (outcome ? `end-${outcome}` : visual.tone);

/** One-shot effects when the number of problems changes: a shake on new errors, a tick when they clear. */
function useStatusFx(errors: number): "shake" | "ok" | "" {
  const prev = useRef(errors);
  const [fx, setFx] = useState<"shake" | "ok" | "">("");
  useEffect(() => {
    let next: "shake" | "ok" | "" = "";
    if (errors > prev.current) next = "shake";
    else if (prev.current > 0 && errors === 0) next = "ok";
    prev.current = errors;
    if (!next) return;
    setFx(next);
    const t = setTimeout(() => setFx(""), 1300);
    return () => clearTimeout(t);
  }, [errors]);
  return fx;
}

function Badges({ diag, fx }: { diag: DiagBadge; fx: string }) {
  if (fx === "ok") return <span className="cv-diag ok" title="Fixed"><Icon name="check" size={12} /></span>;
  if (!diag.errors && !diag.warnings) return null;
  return (
    <span className="cv-diag" title={`${diag.errors} problem(s), ${diag.warnings} warning(s)`}>
      {diag.errors > 0 && <span className="cv-badge error" aria-label={`${diag.errors} errors`}>{diag.errors}</span>}
      {diag.warnings > 0 && <span className="cv-badge warn" aria-label={`${diag.warnings} warnings`}>{diag.warnings}</span>}
    </span>
  );
}

function Header({ id, visual, title, typeText, diag, fx, collapsible, start, tools, compact }: { id: string; visual: Visual; title: string; typeText: string; diag: DiagBadge; fx: string; collapsible: boolean; start?: boolean; tools?: ReactNode; compact?: boolean }) {
  const collapsed = useCollapsed(id);
  const toggle = useToggleCollapse();
  return (
    <header className="cv-head">
      <span className="cv-ico">{!compact && <Icon name={visual.icon} size={16} />}</span>
      <div className="cv-titles">
        <strong title={title}>{title}</strong>
        {!compact && <small title={typeText}>{typeText}</small>}
      </div>
      {compact ? <Badges diag={diag} fx={fx} /> : <div className="cv-headtools">
        {start && <span className="cv-start" title={TERMS.start}><Icon name="play" size={9} /> Start</span>}
        {tools}
        <Badges diag={diag} fx={fx} />
        {collapsible && (
          <button type="button" className="cv-collapse nodrag" aria-label={collapsed ? `Show details of ${title}` : `Hide details of ${title}`} aria-expanded={!collapsed} onClick={() => toggle(id)}>
            <Icon name="chevron" size={12} />
          </button>
        )}
      </div>}
    </header>
  );
}

function Shell({ id, visual, tone, selected, hasError, fx, exiting, children, className, canAppend, compact }: { id: string; visual: Visual; tone: string; selected: boolean; hasError: boolean; fx: string; exiting?: boolean; children: ReactNode; className?: string; canAppend?: boolean; compact?: boolean }) {
  const actions = useCanvasActions();
  const collapsed = useCollapsed(id);
  return (
    <div
      className={cx("cv-node", `shape-${visual.shape}`, `tone-${tone}`, selected && "selected", hasError && "has-error", visual.parks && "parks", fx && `fx-${fx}`, exiting && "exiting", collapsed && "collapsed", compact && "compact", className)}
    >
      {children}
      {canAppend && !actions.readOnly && !compact && (
        <button type="button" className="cv-append nodrag" aria-label="Add a step after this one" title="Add a step after this one" onClick={(e) => actions.appendAfter(id, e.currentTarget.getBoundingClientRect())}>
          <Icon name="plus" size={12} />
        </button>
      )}
    </div>
  );
}

/** Everything under the header: fact rows, the "ends the run" band, chips. Collapses as one. */
function Details({ id, rows, chips, end, empty }: { id: string; rows: Row[]; chips: string[]; end?: Outcome; empty?: boolean }) {
  const collapsed = useCollapsed(id);
  if (empty) return null;
  const { shown, more } = visibleRows(rows);
  const facts = shown.filter((r) => !r.dot);
  const cases = shown.filter((r) => r.dot);
  return (
    <div className="cv-details" data-open={!collapsed}>
      <div className="cv-details-inner">
        {(shown.length > 0 || more > 0) && (
          <div className="cv-body">
            {facts.length > 0 && (
              <ul className="cv-rows">
                {facts.map((r, i) => (
                  <li key={`${r.label}-${i}`} className={cx("cv-row", r.label === "Waits for" && "wait")}>
                    <span className="k">{r.label}</span>
                    <span className="v" title={r.value}>
                      {r.label === "Waits for" && <Icon name="user" size={12} />}
                      {r.code ? <code>{r.value}</code> : r.value}
                    </span>
                  </li>
                ))}
              </ul>
            )}
            {cases.length > 0 && (
              <ul className="cv-cases">
                {cases.map((c, i) => (
                  <li key={`${c.label}-${i}`}>
                    <i aria-hidden />
                    <span>{c.label}</span>
                    {c.value && <em title={c.value}>→ {c.value}</em>}
                  </li>
                ))}
              </ul>
            )}
            {more > 0 && <div className="cv-more">+ {more} more</div>}
          </div>
        )}
        {(chips.length > 0 || end) && (
          <div className="cv-foot">
            {end && (
              <span className={cx("cv-end", `outcome-${end}`)}>
                <Icon name="flag" size={12} /> {OUTCOME_TEXT[end]}
              </span>
            )}
            {chips.map((raw) => {
              const c = chipFor(raw);
              return (
                <span key={c.key} className={cx("cv-chip", c.tone && `t-${c.tone}`)} title={c.title}>
                  <Icon name={c.icon} size={11} /> {c.label}
                </span>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}

function useDim(nodeId: string) {
  const actions = useCanvasActions();
  const conn = useConnection();
  const dragging = conn.inProgress && conn.fromHandle?.type === "source";
  return (handleId: string, type: "target" | "source") =>
    dragging && type === "target" && !actions.canConnect(conn.fromNode?.id ?? "", conn.fromHandle?.id ?? null, nodeId, handleId);
}

function IntentNodeView({ id, data, selected }: NodeProps<Node<IntentData, "intent">>) {
  probe("node");
  const { info, unresolved, diag, visual, typeText, outcome } = data;
  const fx = useStatusFx(diag.errors);
  const dim = useDim(id);
  const reqs = unique(info.requires);
  const provs = unique(info.provides);
  const portRows = Math.max(reqs.length + 1, provs.length, 1);
  const rows = data.rows;
  const hasDetails = rows.length > 0 || info.chips.length > 0 || !!outcome;
  const compact = useCompact();
  return (
    <Shell id={id} visual={visual} tone={toneOf(visual, outcome)} selected={selected} hasError={diag.errors > 0 || unresolved.length > 0} fx={fx} exiting={data.exiting} canAppend={data.canAppend} compact={compact} className={cx(outcome && "is-terminal", info.typeName && `type-${info.typeName}`)}>
      <Header id={id} visual={outcome ? { ...visual, icon: OUTCOME_ICON[outcome] } : visual} title={info.id} typeText={typeText} diag={diag} fx={fx} collapsible={hasDetails && !compact} compact={compact} />
      <div className="cv-ports" style={{ height: portRows * INTENT_ROW + SEC_PAD }}>
        {reqs.map((f, i) => {
          const bad = unresolved.includes(f);
          return (
            <div key={`in-${f}`} className={cx("cv-port in", bad && "unresolved")} style={{ top: SEC_PAD / 2 + i * INTENT_ROW, height: INTENT_ROW }}>
              {!compact && <span title={bad ? `No step produces “${f}”` : f}>{f === INPUT_FACT ? "request" : f}</span>}
              <Handle type="target" position={Position.Left} id={`in:${f}`} className={cx(bad && "unresolved", dim(`in:${f}`, "target") && "dim")} />
            </div>
          );
        })}
        <div className="cv-port in new" style={{ top: SEC_PAD / 2 + reqs.length * INTENT_ROW, height: INTENT_ROW }}>
          {!compact && <span>+ needs</span>}
          <Handle type="target" position={Position.Left} id="in:__new" className={cx("new", dim("in:__new", "target") && "dim")} />
        </div>
        {provs.map((f, i) => (
          <div key={`out-${f}`} className="cv-port out" style={{ top: SEC_PAD / 2 + i * INTENT_ROW, height: INTENT_ROW }}>
            {!compact && <span title={f}>{f}</span>}
            <Handle type="source" position={Position.Right} id={`out:${f}`} />
          </div>
        ))}
      </div>
      <Details id={id} rows={rows} chips={info.chips} end={outcome} empty={!hasDetails || compact} />
    </Shell>
  );
}

function InputNodeView({ id, data }: NodeProps<Node<InputData, "request">>) {
  return (
    <div className={cx("cv-node tone-start request", data.exiting && "exiting")} data-node-id={id}>
      <header className="cv-head">
        <span className="cv-ico"><Icon name="play" size={16} /></span>
        <div className="cv-titles">
          <strong>{data.label}</strong>
          <small>Starts the flow</small>
        </div>
      </header>
      <Handle type="source" position={Position.Right} id={`out:${INPUT_FACT}`} style={handleAt} />
    </div>
  );
}

function StepNodeView({ id, data, selected }: NodeProps<Node<StepData, "step">>) {
  probe("node");
  const { info, diag, visual, typeText, outcome } = data;
  const actions = useCanvasActions();
  const fx = useStatusFx(diag.errors);
  const conn = useConnection();
  const dimIn = conn.inProgress && conn.fromHandle?.type === "source" && !actions.canConnect(conn.fromNode?.id ?? "", conn.fromHandle?.id ?? null, id, "in");
  const rows = data.rows;
  const hasDetails = rows.length > 0 || info.chips.length > 0 || !!info.terminal;
  const compact = useCompact();
  const first = !info.isStart && !actions.readOnly && (
    <button type="button" className="cv-mini nodrag" onClick={() => actions.setStart(info.id)} title="Make this the first step">Make first</button>
  );
  return (
    <Shell
      id={id} visual={visual} tone={toneOf(visual, info.terminal ? (outcome ?? "neutral") : undefined)} selected={selected} hasError={diag.errors > 0} fx={fx} exiting={data.exiting} canAppend={data.canAppend} compact={compact}
      className={cx(info.isStart && "is-start", info.terminal && "is-terminal", info.human && "role-wait", info.terminal && `outcome-${outcome ?? "neutral"}`)}
    >
      <Handle type="target" position={Position.Left} id="in" className={cx(dimIn && "dim")} style={handleAt} />
      <Header id={id} visual={info.terminal ? { ...visual, icon: OUTCOME_ICON[outcome ?? "neutral"] } : visual} title={info.id} typeText={typeText} diag={diag} fx={fx} collapsible={hasDetails && !compact} start={info.isStart && !compact} compact={compact} />
      {first && !compact && <div className="cv-hovertools nodrag">{first}</div>}
      <Details id={id} rows={rows} chips={info.chips} end={info.terminal ? (outcome ?? "neutral") : undefined} empty={!hasDetails || compact} />
      <Handle type="source" position={Position.Right} id="out" style={handleAt} />
    </Shell>
  );
}

function StageNodeView({ id, data, selected }: NodeProps<Node<StageData, "stage">>) {
  probe("node");
  const { info, first, last, diag } = data;
  const actions = useCanvasActions();
  const fx = useStatusFx(diag.errors);
  const visual: Visual = { shape: "card", tone: "process", icon: "list" };
  const shown = info.reviews.slice(0, 4);
  const move = !actions.readOnly && (
    <span className="cv-move nodrag">
      <button type="button" disabled={first} onClick={() => actions.moveStage(info.id, -1)} aria-label={`Move ${info.id} earlier`} title="Move earlier"><Icon name="chevron" size={12} /></button>
      <button type="button" disabled={last} onClick={() => actions.moveStage(info.id, 1)} aria-label={`Move ${info.id} later`} title="Move later"><Icon name="chevron" size={12} /></button>
    </span>
  );
  return (
    <Shell id={id} visual={visual} tone="process" selected={selected} hasError={diag.errors > 0} fx={fx} exiting={data.exiting}>
      <Handle type="target" position={Position.Left} id="in" style={handleAt} />
      <Header id={id} visual={visual} title={info.id} typeText={info.title && info.title !== info.id ? info.title : "Stage"} diag={diag} fx={fx} collapsible={false} tools={move || undefined} />
      {info.reviews.length > 0 && (
        <div className="cv-body">
          <ul className="cv-rows">
            {shown.map((r) => (
              <li key={r.path} className="cv-row">
                <span className="k">Review</span>
                <span className="v"><button type="button" className="link nodrag" onClick={() => actions.select(r.path)}>{r.label}</button></span>
              </li>
            ))}
          </ul>
          {info.reviews.length > 4 && <div className="cv-more">+ {info.reviews.length - 4} more</div>}
        </div>
      )}
      {info.chips.length > 0 && (
        <div className="cv-foot">
          {info.chips.map((raw) => {
            const c = chipFor(raw);
            return <span key={c.key} className="cv-chip" title={c.title}><Icon name={c.icon} size={11} /> {c.label}</span>;
          })}
        </div>
      )}
      <Handle type="source" position={Position.Right} id="out" style={handleAt} />
    </Shell>
  );
}

export const nodeTypes = {
  intent: memo(IntentNodeView),
  request: memo(InputNodeView),
  step: memo(StepNodeView),
  stage: memo(StageNodeView),
};

export { INPUT_NODE, typeLabel };
