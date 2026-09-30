import { useEffect, useMemo, useRef, useState } from "react";
import { Icon } from "./icons";
import { visualFor } from "./kinds";
import { FAMILIES, familyLabel, typeLabel } from "./labels";
import type { NodeTypeInfo } from "./model";
import type { Catalog } from "../api/types";

/** Ordered so the useful, common kinds come first. */
const COMMON = ["action", "transform", "script", "branch", "decision", "http", "email", "approval", "delay", "foreach", "response"];

export function TypeList({ types, catalog, onPick, autoFocus, disabled }: { types: NodeTypeInfo[]; catalog?: Catalog | null; onPick(t: NodeTypeInfo): void; autoFocus?: boolean; disabled?: boolean }) {
  const [q, setQ] = useState("");
  const groups = useMemo(() => {
    const needle = q.trim().toLowerCase();
    const match = (t: NodeTypeInfo) => !needle || `${t.name} ${typeLabel(t.name)} ${t.summary} ${familyLabel(t.family)}`.toLowerCase().includes(needle);
    const common = types.filter((t) => COMMON.includes(t.name) && match(t)).sort((a, b) => COMMON.indexOf(a.name) - COMMON.indexOf(b.name));
    const byFam = new Map<string, NodeTypeInfo[]>();
    for (const t of types) if (match(t)) byFam.set(t.family, [...(byFam.get(t.family) ?? []), t]);
    return { common: needle ? [] : common, byFam: [...byFam.entries()] };
  }, [types, q]);

  const item = (t: NodeTypeInfo) => {
    const v = visualFor(t.name, catalog);
    return (
      <li key={t.name}>
        <button type="button" className={`cv-typebtn tone-${v.tone}`} disabled={disabled} draggable={!disabled}
          onDragStart={(e) => { e.dataTransfer.setData(DRAG_MIME, t.name); e.dataTransfer.effectAllowed = "copy"; }}
          onClick={() => onPick(t)} title={t.summary}>
          <span className="cv-ico"><Icon name={v.icon} size={14} /></span>
          <span className="cv-typetext">
            <strong>{typeLabel(t.name)}</strong>
            <small>{t.summary}</small>
          </span>
          {t.durable && <span className="cv-chip dur" title="Pauses the run until something happens">waits</span>}
        </button>
      </li>
    );
  };

  return (
    <div className="cv-typelist">
      <input type="search" aria-label="Search kinds of step" placeholder="Search…" value={q} autoFocus={autoFocus} onChange={(e) => setQ(e.target.value)} />
      {groups.common.length > 0 && (
        <section>
          <h4>Popular</h4>
          <ul>{groups.common.map(item)}</ul>
        </section>
      )}
      {groups.byFam.map(([fam, list]) => (
        <details key={fam} open={!!q}>
          <summary>{FAMILIES[fam]?.label ?? fam}</summary>
          <ul>{list.map(item)}</ul>
        </details>
      ))}
      {groups.byFam.length === 0 && groups.common.length === 0 && <p className="cv-none">Nothing matches.</p>}
    </div>
  );
}

export const DRAG_MIME = "application/x-studio-node-type";

/** A small panel that opens beside the "+" that was clicked. */
export function InsertPopover({ anchor, types, catalog, onPick, onClose, title }: { anchor: DOMRect; types: NodeTypeInfo[]; catalog?: Catalog | null; onPick(t: NodeTypeInfo): void; onClose(): void; title: string }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const away = (e: PointerEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) onClose();
    };
    const key = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    document.addEventListener("pointerdown", away, true);
    document.addEventListener("keydown", key);
    return () => {
      document.removeEventListener("pointerdown", away, true);
      document.removeEventListener("keydown", key);
    };
  }, [onClose]);
  const W = 320;
  const H = 420;
  const left = Math.max(8, Math.min(window.innerWidth - W - 8, anchor.left + anchor.width / 2 - W / 2));
  const below = anchor.bottom + 10 + H < window.innerHeight;
  const top = below ? anchor.bottom + 10 : Math.max(8, anchor.top - H - 10);
  return (
    <div ref={ref} className={`cv-popover ${below ? "below" : "above"}`} role="dialog" aria-label={title} style={{ left, top, width: W, maxHeight: H }}>
      <h3>{title}</h3>
      <TypeList types={types} catalog={catalog} onPick={onPick} autoFocus />
    </div>
  );
}
