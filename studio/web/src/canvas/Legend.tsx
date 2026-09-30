import { Icon } from "./icons";

const STEP_KINDS: [string, string][] = [
  ["compute", "Work with data"], ["plug", "Other systems"], ["data", "Stored data"], ["decision", "Decisions"],
  ["flow", "Control flow"], ["process", "People & waiting"], ["message", "Messages"], ["identity", "Who is asking"],
];

const ENDINGS: [string, string][] = [["end-success", "Ends well"], ["end-cancelled", "Cancelled"], ["end-failed", "Ends badly"]];

/** A small key to the colours and lines on the canvas. */
export function Legend({ kind, onClose }: { kind: "intent" | "process" | "pipeline"; onClose(): void }) {
  return (
    <div className="cv-legend" role="dialog" aria-label="What the colours mean">
      <h3>What do the colours mean?</h3>
      <ul>
        {STEP_KINDS.map(([tone, label]) => (
          <li key={tone} className={`tone-${tone}`}><span className="sw" />{label}</li>
        ))}
        {kind === "process" && ENDINGS.map(([tone, label]) => (
          <li key={tone} className={`tone-${tone}`}><span className="sw" />{label}</li>
        ))}
      </ul>
      <ul>
        <li><span className="line" />Then</li>
        <li><span className="line err" />If it fails</li>
      </ul>
      <p>Select a step to see data move along its connections. Hover a connection to add a step in the middle.</p>
      <button type="button" className="ghost" onClick={onClose} aria-label="Close the key" style={{ position: "absolute", top: 8, right: 8, width: 24, height: 24, padding: 0 }}>
        <Icon name="x" size={12} />
      </button>
    </div>
  );
}
