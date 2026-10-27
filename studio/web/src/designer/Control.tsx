import type { DesignInput } from "./model";
import { sampleLabel } from "./model";

/** One field as the user will see it (design shows a muted, inert control). */
export function Control({ input: i, design }: { input: DesignInput; design: boolean }) {
  const common = { disabled: design, "aria-label": sampleLabel(i), placeholder: i.placeholder };
  let el;
  switch (i.kind) {
    case "textarea": el = <textarea rows={3} {...common} />; break;
    case "select": el = <select {...common}><option>{i.lookup ? `— from lookup “${i.lookup}” —` : "— select —"}</option>{i.options.map((o) => <option key={o}>{o}</option>)}</select>; break;
    case "multiselect": el = <select multiple {...common}>{i.options.map((o) => <option key={o}>{o}</option>)}</select>; break;
    case "radio": el = <div className="dz-choices">{i.options.map((o) => <label key={o}><input type="radio" name={i.path} disabled={design} /> {o}</label>)}</div>; break;
    case "boolean": el = <input type="checkbox" disabled={design} aria-label={sampleLabel(i)} />; break;
    case "number": case "integer": el = <input type="number" {...common} />; break;
    case "date": el = <input type="date" {...common} />; break;
    case "datetime": el = <input type="datetime-local" {...common} />; break;
    case "email": el = <input type="email" {...common} />; break;
    case "file": el = <input type="file" disabled={design} aria-label={sampleLabel(i)} />; break;
    default: el = <input type="text" {...common} />;
  }
  return (
    <label className="dz-field">
      <span className="dz-label">{sampleLabel(i)}{i.required && <b className="dz-req" title="Required"> *</b>}{design && <em className="dz-kind">{i.kind}</em>}</span>
      {el}
      {i.help && <span className="dz-help">{i.help}</span>}
      {design && <Badges input={i} />}
    </label>
  );
}


/** What else the field does, at a glance: where its choices come from, what it depends on, which rules and roles apply. */
export function Badges({ input: i }: { input: DesignInput }) {
  const b: [string, string][] = [];
  if (i.shared) b.push(["shared", "A shared catalog field"]);
  if (i.lookup) b.push([`lookup · ${i.lookup}`, "Choices come from this workspace lookup"]);
  if (i.dependsOn.length) b.push([`depends on ${i.dependsOn.join(", ")}`, "Choices are narrowed by these fields"]);
  if (i.visibleIf) b.push(["visible if", i.visibleIf]);
  if (i.requiredIf) b.push(["required if", i.requiredIf]);
  if (i.disabledIf) b.push(["disabled if", i.disabledIf]);
  if (i.viewRoles.length || i.editRoles.length) b.push(["roles", `view: ${i.viewRoles.join(", ") || "all"}; edit: ${i.editRoles.join(", ") || "all"}`]);
  if (i.pii || i.sensitive || i.sealed) b.push([[i.pii && "PII", i.sensitive && "sensitive", i.sealed && "sealed"].filter(Boolean).join(" · "), "Protected data"]);
  if (i.compute) b.push(["computed", i.compute]);
  if (!b.length) return null;
  return <span className="dz-badges">{b.map(([t, why]) => <em key={t} title={why}>{t}</em>)}</span>;
}
