import type { DiffLine } from "../lib/diff";
import { stats } from "../lib/diff";
import { Disclosure } from "../ui/Disclosure";

export function DiffView({ name, lines, status }: { name: string; lines: DiffLine[]; status?: string }) {
  const { added, removed } = stats(lines);
  return (
    <Disclosure
      className="diff"
      dataPath={undefined}
      title={<><span className="mono">{name}</span> {status && <span className={`tag ${status}`}>{status}</span>}</>}
      meta={<><span className="added">+{added}</span> <span className="removed">−{removed}</span></>}
    >
      <div data-file={name}>
        {lines.length === 0 ? (
          <p className="empty">No textual changes.</p>
        ) : (
          <pre className="mono" aria-label={`Diff of ${name}`}>
            {lines.map((l, i) => (
              <div key={i} className={`dl ${l.t === "+" ? "add" : l.t === "-" ? "del" : l.t === "@" ? "gap" : ""}`}>
                <span className="mark" aria-hidden="true">{l.t === "@" ? "…" : l.t}</span>
                <span>{l.t === "@" ? l.text : l.text || " "}</span>
              </div>
            ))}
          </pre>
        )}
      </div>
    </Disclosure>
  );
}
