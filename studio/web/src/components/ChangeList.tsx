import { useMemo, useState } from "react";
import type { DocumentChange } from "../api/types";
import { blockInfo, count, lc } from "../labels";
import { Icon } from "../ui/Icon";
import { useUi } from "../state/ui";

const VERB = { added: "Added", removed: "Removed", changed: "Changed" } as const;

/** "14 connections · 10 data shapes": what a big change is made of, before the details. */
export function ChangeSummary({ changes }: { changes: DocumentChange[] }) {
  const parts = useMemo(() => {
    const by = new Map<string, { kind: string; n: number }>();
    for (const c of changes) {
      const cur = by.get(c.kind) ?? { kind: c.kind, n: 0 };
      cur.n++;
      by.set(c.kind, cur);
    }
    return [...by.values()].sort((a, b) => b.n - a.n);
  }, [changes]);
  return (
    <ul className="change-summary" aria-label="Summary of changes">
      {parts.map((p) => {
        const info = blockInfo(p.kind);
        return <li key={p.kind}><Icon name={info.icon} size={14} /> <strong>{p.n}</strong> {lc(p.n === 1 ? info.label : info.plural ?? `${info.label}s`)}</li>;
      })}
    </ul>
  );
}

/** Plain-language list of what changed: "Added Page or API endpoint reports". Long lists start collapsed. */
export function ChangeList({ changes, limit }: { changes: DocumentChange[]; limit?: number }) {
  const dev = useUi((s) => s.devView);
  const [all, setAll] = useState(false);
  if (!changes.length) return <p className="muted">No changes yet.</p>;
  const shown = limit && !all ? changes.slice(0, limit) : changes;
  return (
    <>
      <ul className="change-list">
        {shown.map((c, i) => {
          const info = blockInfo(c.kind);
          return (
            <li key={i} className={`change ${c.change}`}>
              <span className="change-icon"><Icon name={info.icon} size={16} /></span>
              <span className="change-text">
                <span className={`change-verb ${c.change}`}>{VERB[c.change] ?? c.change}</span> {lc(info.label)} <strong>{c.name}</strong>
                {dev && <code className="raw-name">{c.kind}</code>}
              </span>
            </li>
          );
        })}
      </ul>
      {limit && changes.length > limit && (
        <button type="button" className="link-btn change-more" onClick={() => setAll((v) => !v)}>
          {all ? "Show fewer" : `Show all ${count(changes.length, "change")}`}
        </button>
      )}
    </>
  );
}
