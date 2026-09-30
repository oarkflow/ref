// The cards of the journey map. The same rectangular card as the canvas (8px corners, a
// coloured icon tile, a title and a quiet type line); what a card *is* shows in its colour
// and icon and in what it lists: a page lists the things you can do on it, a request shows
// its address and who may use it, a logic flow its steps.
import { Handle, Position, type Node, type NodeProps } from "@xyflow/react";
import { memo, type ReactNode } from "react";
import { Icon } from "../canvas/icons";
import { useUi } from "../state/ui";
import { ELEMENT_ICON, JIcon, KIND_ICON, resourceIcon } from "./icons";
import { HEAD_H, type Chip, type ElementRow, type JNode } from "./model";
import { ELEMENT_WORDS } from "./text";
import { useCardSelected, useDimmed, useJourneyActions, useMatched, useSelectedRow } from "./view";

export type CardData = { node: JNode } & Record<string, unknown>;

const cx = (...a: (string | false | undefined | null)[]) => a.filter(Boolean).join(" ");

/** Card colour families (defined with the canvas palette in canvas.css). */
const TONE: Record<string, string> = {
  page: "flow", shared: "compute", route: "plug", intent: "process", resource: "data", external: "end-neutral", unresolved: "end-failed",
};

const HANDLE_Y = HEAD_H / 2 + 2;
const handleAt = { top: HANDLE_Y, transform: "translate(0, -50%)" } as const;

function Problems({ n }: { n: number }) {
  if (!n) return null;
  return <span className="cv-badge warn" title={`${n} ${n === 1 ? "problem" : "problems"}`} aria-label={`${n} problems`}>{n}</span>;
}

function ChipView({ c }: { c: Chip }) {
  return (
    <span className={cx("cv-chip", c.tone === "warn" && "t-warn", c.tone === "danger" && "t-danger", c.tone === "ok" && "t-ok", c.tone === "info" && "t-info")} title={c.title}>
      {c.icon && <JIcon name={c.icon} size={11} />} {c.text}
    </span>
  );
}

function Card({ node, tone, children, className }: { node: JNode; tone?: string; children: ReactNode; className?: string }) {
  const selected = useCardSelected(node.id);
  const dim = useDimmed(node.id);
  const hit = useMatched(node.id);
  return (
    <div
      className={cx("cv-node jn-card", `tone-${tone ?? TONE[node.kind]}`, `jn-${node.kind}`, selected && "selected", dim && "jn-dim", hit && "jn-hit", node.problems > 0 && "has-error", className)}
      style={{ width: node.width }}
      data-card={node.id}
    >
      {children}
    </div>
  );
}

function Head({ node, icon, tools }: { node: JNode; icon: string; tools?: ReactNode }) {
  const dev = useUi((u) => u.devView);
  return (
    <header className="cv-head">
      <span className="cv-ico"><JIcon name={icon} size={16} /></span>
      <div className="cv-titles">
        <strong title={node.title}>{node.title}</strong>
        <small title={dev ? node.flow?.id : node.sub}>{dev ? node.flow?.id ?? node.sub : node.sub}</small>
      </div>
      <div className="cv-headtools">
        {tools}
        <Problems n={node.problems} />
      </div>
    </header>
  );
}

// ---- a page, with the things you can do on it -------------------------------------------------

function Row({ row, selected, problems }: { row: ElementRow; selected: boolean; problems: number }) {
  const actions = useJourneyActions();
  const words = ELEMENT_WORDS[row.kind];
  return (
    <li
      className={cx("jn-row", `kind-${row.kind}`, selected && "selected", row.guessed && "guessed", problems > 0 && "bad")}
      data-row={row.id}
      title={`${words.one}: ${row.label}${row.url ? ` → ${row.method && row.method !== "GET" ? row.method + " " : ""}${row.url}` : ""}${row.line ? ` (line ${row.line})` : ""}`}
    >
      <button type="button" className="jn-rowbtn nodrag" aria-pressed={selected} aria-label={`${words.one}: ${row.label}`} onClick={(e) => { e.stopPropagation(); actions.selectRow(row.id); }}>
        <span className="jn-rico"><JIcon name={ELEMENT_ICON[row.kind]} size={12} /></span>
        <span className="jn-label">{row.label}</span>
        {row.method && row.method !== "GET" && <span className="jn-method">{row.method}</span>}
        {problems > 0 && <span className="jn-dot" aria-label="Has a problem" />}
      </button>
      <Handle type="source" position={Position.Right} id={row.id} className="jn-rowhandle" />
    </li>
  );
}

function PageCard({ data }: NodeProps<Node<CardData, "page">>) {
  const node = data.node;
  const sel = useSelectedRow(node.id);
  return (
    <Card node={node}>
      <Handle type="target" position={Position.Left} id="in" style={handleAt} />
      <Head node={node} icon={KIND_ICON.page} />
      <div className="jn-meta">
        {node.routes.slice(0, 2).map((r) => <code key={r} className="jn-addr" title={r}>{r}</code>)}
        {node.routes.length > 2 && <span className="cv-more-inline">+{node.routes.length - 2}</span>}
        {node.routes.length === 0 && <span className="jn-muted">Not shown by any address</span>}
        {node.chips.map((c) => <ChipView key={c.text} c={c} />)}
      </div>
      {node.rows.length > 0 && (
        <ul className="jn-rows" aria-label={`What you can do on ${node.title}`}>
          {node.rows.map((r) => <Row key={r.id} row={r} selected={sel === r.id} problems={node.rowProblems[r.id] ?? 0} />)}
        </ul>
      )}
    </Card>
  );
}

function SharedCard({ data }: NodeProps<Node<CardData, "shared">>) {
  const node = data.node;
  const actions = useJourneyActions();
  const sel = useSelectedRow(node.id);
  const open = !!node.open;
  const groups = new Map<string, ElementRow[]>();
  for (const r of node.rows) (groups.get(r.component ?? "") ?? groups.set(r.component ?? "", []).get(r.component ?? "")!).push(r);
  return (
    <Card node={node}>
      <Head node={node} icon={KIND_ICON.shared} tools={
        <button type="button" className="jn-toggle nodrag" aria-expanded={open} onClick={(e) => { e.stopPropagation(); actions.toggleShared(); }}>
          {open ? "Hide" : "Show"}
        </button>
      } />
      {open ? (
        <div className="jn-rows grouped">
          {[...groups.entries()].map(([name, rows]) => (
            <section key={name}>
              <h4>{name || "Layouts"}</h4>
              <ul>{rows.map((r) => <Row key={r.id} row={r} selected={sel === r.id} problems={node.rowProblems[r.id] ?? 0} />)}</ul>
            </section>
          ))}
        </div>
      ) : (
        <div className="jn-summary">{[...groups.keys()].map((g) => g || "layouts").join(", ")}</div>
      )}
      {!open && <Handle type="source" position={Position.Right} id="out" style={handleAt} />}
    </Card>
  );
}

// ---- requests, logic flows, connections -----------------------------------------------------------

const METHOD_TONE: Record<string, string> = { GET: "get", POST: "post", PUT: "put", PATCH: "put", DELETE: "delete" };

function Facts({ node }: { node: JNode }) {
  if (!node.facts.length) return <div className="jn-meta" />;
  return (
    <div className="jn-meta">
      {node.facts.map((f) => (
        <span key={f.k + f.v} className="jn-fact" title={`${f.k}: ${f.v}`}>
          <span className="k">{f.k}</span>
          {f.code ? <code>{f.v}</code> : <span className="v">{f.v}</span>}
        </span>
      ))}
    </div>
  );
}

/** One row of small chips under the title: who may use it, how big it is. */
function ChipRow({ node }: { node: JNode }) {
  const dev = useUi((u) => u.devView);
  return (
    <div className="jn-meta">
      {node.chips.map((c) => <ChipView key={c.text} c={c} />)}
      {dev && node.facts.map((f) => <span key={f.k} className="jn-fact" title={`${f.k}: ${f.v}`}><code>{f.v}</code></span>)}
    </div>
  );
}

function RouteCard({ data }: NodeProps<Node<CardData, "route">>) {
  const node = data.node;
  const method = String(node.flow?.data?.method ?? "").toUpperCase();
  return (
    <Card node={node}>
      <Handle type="target" position={Position.Left} id="in" style={handleAt} />
      <Head node={node} icon={KIND_ICON.route} tools={method ? <span className={cx("jn-verb", METHOD_TONE[method] ?? "get")}>{method}</span> : null} />
      <ChipRow node={node} />
      <Handle type="source" position={Position.Right} id="out" style={handleAt} />
    </Card>
  );
}

function IntentCard({ data }: NodeProps<Node<CardData, "intent">>) {
  const node = data.node;
  return (
    <Card node={node} tone={node.flow?.subkind === "process" ? "watch" : undefined}>
      <Handle type="target" position={Position.Left} id="in" style={handleAt} />
      <Head node={node} icon={node.flow?.subkind === "process" ? "user" : KIND_ICON.intent} />
      <ChipRow node={node} />
      <Handle type="source" position={Position.Right} id="out" style={handleAt} />
    </Card>
  );
}

function ResourceCard({ data }: NodeProps<Node<CardData, "resource">>) {
  const node = data.node;
  return (
    <Card node={node}>
      <Handle type="target" position={Position.Left} id="in" style={handleAt} />
      <Head node={node} icon={resourceIcon(String(node.flow?.data?.category ?? ""))} />
    </Card>
  );
}

function ExternalCard({ data }: NodeProps<Node<CardData, "external">>) {
  const node = data.node;
  return (
    <Card node={node}>
      <Handle type="target" position={Position.Left} id="in" style={handleAt} />
      <Head node={node} icon={KIND_ICON.external} />
    </Card>
  );
}

function UnresolvedCard({ data }: NodeProps<Node<CardData, "unresolved">>) {
  const node = data.node;
  const actions = useJourneyActions();
  return (
    <Card node={node}>
      <Handle type="target" position={Position.Left} id="in" style={handleAt} />
      <Head node={node} icon={KIND_ICON.unresolved} />
      <Facts node={node} />
      <div className="cv-foot jn-foot">
        {node.chips.map((c) => <ChipView key={c.text} c={c} />)}
        <button type="button" className="jn-fix nodrag" onClick={(e) => { e.stopPropagation(); actions.fix(node); }}>
          <Icon name="wrench" size={12} /> Fix
        </button>
      </div>
    </Card>
  );
}

export const nodeTypes = {
  page: memo(PageCard),
  shared: memo(SharedCard),
  route: memo(RouteCard),
  intent: memo(IntentCard),
  resource: memo(ResourceCard),
  external: memo(ExternalCard),
  unresolved: memo(UnresolvedCard),
};
