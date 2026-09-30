// The details of whatever is selected: what it is, where it lives, who uses it, what it does next.
import { JIcon, ELEMENT_ICON, KIND_ICON, resourceIcon } from "./icons";
import { neighbours, type ElementRow, type Journey, type JNode } from "./model";
import { ELEMENT_WORDS, warningText } from "./text";
import { useUi } from "../state/ui";

export interface DrawerProps {
  journey: Journey;
  selected: string | null;
  onSelect(id: string): void;
  onOpenTemplate(file: string, line?: number): void;
  onOpenRoute(n: JNode): void;
  onOpenFlow(n: JNode): void;
  onOpenConnection(n: JNode): void;
  onFocus(id: string): void;
  onClose(): void;
}

const TONE: Record<string, string> = {
  page: "flow", shared: "compute", route: "plug", intent: "process", resource: "data", external: "end-neutral", unresolved: "end-failed",
};

const SENTENCE = (n: JNode): string => {
  const d = n.flow?.data ?? {};
  switch (n.kind) {
    case "page": return "A page visitors see. It lists what they can click or send, and where each thing leads.";
    case "shared": return "Links and buttons that come from layouts and shared parts, so they show up on many pages.";
    case "route": return `When a button, link or another app sends ${String(d.method ?? "").toUpperCase()} ${String(d.path ?? n.title)}, the app answers here.`;
    case "intent": return n.flow?.subkind === "process" ? "A workflow that waits for people and moves step by step." : "The steps the app runs to get the work done.";
    case "resource": return `Something the app connects to: ${n.sub.toLowerCase()}.`;
    case "external": return "A link that leaves the app for another website.";
    case "unresolved": return `A button or link goes to ${n.title}, but nothing in the app answers there.`;
  }
};

/** One sentence on where a link, form or button leads. */
export function rowLead(row: ElementRow): string {
  if (!row.url) return "This has no address of its own, so the app can’t tell where it leads.";
  const dest = `${row.method && row.method !== "GET" ? row.method + " " : ""}${row.url}`;
  return row.kind === "link" ? `Goes to ${dest}.` : `Sends ${dest}.`;
}

function Where({ file, line, dev, onOpen }: { file?: string; line?: number; dev: boolean; onOpen?(): void }) {
  if (!file) return null;
  return (
    <section className="jn-dsec">
      <h4>Where it lives</h4>
      <p className="jn-where">
        <code>{file}{line ? `:${line}` : ""}</code>
        {onOpen && <button type="button" className="link" onClick={onOpen}>{dev ? "Open" : line ? `Open at line ${line}` : "Open"}</button>}
      </p>
    </section>
  );
}

function Links({ title, items, onSelect, empty }: { title: string; items: { key: string; node: JNode; note: string; row?: ElementRow }[]; onSelect(id: string): void; empty?: string }) {
  if (!items.length && !empty) return null;
  return (
    <section className="jn-dsec">
      <h4>{title}</h4>
      {items.length === 0 ? <p className="jn-muted">{empty}</p> : (
        <ul className="jn-links">
          {items.map((i) => (
            <li key={i.key}>
              <button type="button" onClick={() => onSelect(i.row?.id ?? i.node.id)}>
                <span className={`jn-mini tone-${TONE[i.node.kind]}`}><JIcon name={i.row ? ELEMENT_ICON[i.row.kind] : KIND_ICON[i.node.kind]} size={11} /></span>
                <span className="t">{i.row ? `${i.row.label}` : i.node.title}</span>
                <span className="n">{i.row ? `on ${i.node.title} · ${i.note}` : i.note}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

export function Drawer(p: DrawerProps) {
  const dev = useUi((u) => u.devView);
  const { journey: j } = p;
  const hostId = p.selected ? j.rowHost.get(p.selected) : undefined;
  const host = p.selected ? j.byId.get(hostId ?? p.selected) : undefined;
  const row = hostId && host ? host.rows.find((r) => r.id === p.selected) : undefined;
  if (!p.selected || !host) {
    return (
      <div className="jn-drawer-empty">
        <h3>Nothing selected</h3>
        <p>Click a page, a request or a button to see what it does. The lines that touch it will light up.</p>
      </div>
    );
  }
  const nb = neighbours(j, p.selected);
  const problems = j.warnings.filter((w) => w.severity === "warning" && w.node === host.id && (!row || (w.line === row.line && (!w.file || w.file === row.file))));
  const incoming = nb.incoming.map((n) => ({ key: n.edge.id, node: n.node, row: n.row, note: n.edge.words.text }));
  const outgoing = nb.outgoing.map((n) => ({ key: n.edge.id, node: n.node, note: n.edge.words.text }));

  if (row) {
    const words = ELEMENT_WORDS[row.kind];
    return (
      <div className="jn-drawer">
        <header className="jn-dhead">
          <span className={`cv-ico tone-${TONE[host.kind]}`}><JIcon name={ELEMENT_ICON[row.kind]} size={16} /></span>
          <div className="cv-titles"><strong title={row.label}>{row.label}</strong><small>{words.one} on {host.title}</small></div>
          <button type="button" className="jn-x" aria-label="Close details" onClick={p.onClose}><JIcon name="x" size={14} /></button>
        </header>
        <div className="jn-dbody">
          <p className="jn-lead">{rowLead(row)}</p>
          {row.guessed && <p className="jn-note">The template only gives a placeholder for its text, so it is named after the address.</p>}
          <Where file={row.file} line={row.line} dev={dev} onOpen={row.file ? () => p.onOpenTemplate(row.file!, row.line) : undefined} />
          <Links title="What happens next" items={outgoing} onSelect={p.onSelect} empty="Nothing on this map. It may only change the page itself." />
          {problems.length > 0 && <Problems items={problems.map((w) => warningText(w).title)} />}
          <div className="jn-dact">
            <button type="button" onClick={() => p.onSelect(host.id)}>Go to {host.title}</button>
          </div>
        </div>
      </div>
    );
  }

  const d = host.flow?.data ?? {};
  return (
    <div className="jn-drawer">
      <header className="jn-dhead">
        <span className={`cv-ico tone-${TONE[host.kind]}`}><JIcon name={host.kind === "resource" ? resourceIcon(String(d.category ?? "")) : KIND_ICON[host.kind]} size={16} /></span>
        <div className="cv-titles"><strong title={host.title}>{host.title}</strong><small>{dev ? host.flow?.id : host.sub}</small></div>
        <button type="button" className="jn-x" aria-label="Close details" onClick={p.onClose}><JIcon name="x" size={14} /></button>
      </header>
      <div className="jn-dbody">
        <p className="jn-lead">{SENTENCE(host)}</p>
        {(host.chips.length > 0 || host.facts.length > 0 || host.routes.length > 0) && (
          <div className="jn-dchips">
            {host.routes.map((r) => <code key={r} className="jn-addr">{r}</code>)}
            {host.facts.map((f) => <span key={f.k} className="jn-fact"><span className="k">{f.k}</span>{f.code ? <code>{f.v}</code> : <span className="v">{f.v}</span>}</span>)}
            {host.chips.map((c) => <span key={c.text} className={`cv-chip t-${c.tone}`}>{c.text}</span>)}
          </div>
        )}
        {host.kind === "shared" && (
          <p className="jn-note">{host.rows.length} buttons and links. Open the card on the map to see each one.</p>
        )}
        <Where
          file={host.flow?.file} line={host.flow?.line} dev={dev}
          onOpen={
            host.kind === "page" && host.flow?.file ? () => p.onOpenTemplate(host.flow!.file!, host.flow!.line)
            : host.kind === "route" ? () => p.onOpenRoute(host)
            : host.kind === "intent" ? () => p.onOpenFlow(host)
            : host.kind === "resource" ? () => p.onOpenConnection(host)
            : undefined
          }
        />
        <Links title={host.kind === "page" ? "Shown by" : "Who uses this"} items={incoming} onSelect={p.onSelect} empty={host.kind === "page" ? "No address shows this page yet." : "Nothing on the map uses this."} />
        <Links title="What it does next" items={outgoing} onSelect={p.onSelect} />
        {problems.length > 0 && <Problems items={problems.map((w) => warningText(w).title)} />}
        <div className="jn-dact">
          {host.kind === "page" && host.flow?.file && <button type="button" onClick={() => p.onOpenTemplate(host.flow!.file!, undefined)}>Open the page design</button>}
          {host.kind === "route" && <button type="button" onClick={() => p.onOpenRoute(host)}>Open the request settings</button>}
          {host.kind === "intent" && <button type="button" onClick={() => p.onOpenFlow(host)}>Open the logic flow</button>}
          {host.kind === "resource" && <button type="button" onClick={() => p.onOpenConnection(host)}>Open the connection</button>}
          {host.kind === "unresolved" && incoming[0]?.row?.file && <button type="button" className="primary" onClick={() => p.onOpenTemplate(incoming[0]!.row!.file!, incoming[0]!.row!.line)}>Fix it in the page</button>}
          {host.kind !== "shared" && host.kind !== "external" && <button type="button" onClick={() => p.onFocus(host.id)}>Show only this journey</button>}
        </div>
      </div>
    </div>
  );
}

function Problems({ items }: { items: string[] }) {
  return (
    <section className="jn-dsec jn-problems" role="status">
      <h4>Needs attention</h4>
      <ul>{items.map((t, i) => <li key={i}>{t}</li>)}</ul>
    </section>
  );
}
