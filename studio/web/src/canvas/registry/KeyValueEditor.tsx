import { useState } from "react";
import type { Op } from "../../api/types";
import { quote } from "../../lib/bcl";
import { blocksOf, childPath, fieldOf } from "../../lib/paths";
import { useForm } from "../../forms/ctx";
import { ScalarField } from "../../forms/ScalarField";
import { Icon } from "../icons";
import { useSettings } from "./context";
import type { At } from "./types";

const KEY = /^[A-Za-z_][\w-]*$/;

/**
 * A map (`headers { Accept "text/plain" }`): one row per entry. Each value is a
 * full scalar field, so it can be fixed text, an environment value or a formula.
 * A map that is written as a single value instead of a block is shown as text.
 */
export function KeyValueEditor({ at, label, help, keyLabel = "Name", valueLabel = "Value" }: { at: At; label: string; help?: string; keyLabel?: string; valueLabel?: string }) {
  const { node, io, readOnly } = useSettings();
  const { file } = useForm();
  const [key, setKey] = useState("");
  const [error, setError] = useState<string | null>(null);
  const parent = at.length === 2 ? blocksOf(node, at[0])[0] : node;
  const name = at.length === 2 ? at[1] : at[0];
  const block = blocksOf(parent, name)[0];
  const asValue = fieldOf(parent, name);
  const parentPath = at.length === 2 ? childPath(node.path, at[0]) : node.path;

  if (asValue) {
    // Written as one value: keep it as it is, editable as text.
    return (
      <div className="cv-field" data-path={asValue.path}>
        <label>{label}</label>
        <ScalarField label={label} kind="any" raw={asValue.raw} path={asValue.path} onCommit={(raw) => io.edit([raw === undefined ? { op: "removeField", file: file, path: asValue.path } : { op: "setField", file: file, path: asValue.path, value: raw }])} readOnly={readOnly} />
        {help && <small>{help}</small>}
      </div>
    );
  }

  const entries = (block?.children ?? []).filter((c) => c.kind === "field");
  const add = () => {
    const k = key.trim();
    if (!k) return;
    if (!KEY.test(k)) return setError("Use letters, digits, _ and - only, starting with a letter.");
    if (entries.some((e) => e.name === k)) return setError(`“${k}” is already there.`);
    setError(null);
    const ops: Op[] = [];
    if (!block) ops.push({ op: "addBlock", file, parent: parentPath, type: name });
    ops.push({ op: "setField", file, path: childPath(childPath(parentPath, name), k), value: quote("") });
    io.edit(ops);
    setKey("");
  };

  return (
    <div className="cv-field cv-kv" data-path={childPath(parentPath, name)}>
      <label>{label}</label>
      {entries.length === 0 && <p className="cv-none">Nothing yet.</p>}
      {entries.map((e) => (
        <div className="cv-kvrow" key={e.name}>
          <span className="cv-kvkey" title={keyLabel}>{e.name}</span>
          <div className="cv-kvval">
            <ScalarField
              label={`${label}: ${e.name}`}
              display={valueLabel}
              kind="string"
              raw={e.raw}
              path={e.path}
              compact
              readOnly={readOnly}
              onCommit={(raw) => io.edit([raw === undefined ? { op: "removeField", file: file, path: e.path } : { op: "setField", file: file, path: e.path, value: raw }])}
            />
          </div>
          {!readOnly && (
            <button type="button" className="cv-x" aria-label={`Remove ${e.name}`} onClick={() => io.edit([{ op: "removeField", file: file, path: e.path }])}>
              <Icon name="x" size={12} />
            </button>
          )}
        </div>
      ))}
      {!readOnly && (
        <form className="cv-add" onSubmit={(ev) => { ev.preventDefault(); add(); }}>
          <input aria-label={`Add to ${label}`} value={key} placeholder={`+ ${keyLabel}…`} spellCheck={false} onChange={(e) => { setKey(e.target.value); setError(null); }} />
          <button type="submit" disabled={!key.trim()}>Add</button>
        </form>
      )}
      {error && <small className="bad" role="alert">{error}</small>}
      {help && <small>{help}</small>}
    </div>
  );
}
