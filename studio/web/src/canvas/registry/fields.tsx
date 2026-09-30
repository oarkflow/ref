// One renderer per field kind. Each reads and writes through the step's BlockIO,
// so every edit is the same kind of op as anywhere else in the studio.
import { useEffect, useId, useMemo, useRef, useState } from "react";
import { Problems, useDiagnostics } from "../../forms/ctx";
import { emitList, quote, unquote } from "../../lib/bcl";
import { humanize } from "../../labels";
import { ObjectListEditor } from "../panels/ObjectListEditor";
import { AddName, Choice, NameChips, Setting, Toggle, useIntentIds, useStringList, type FieldIO } from "../panels/kit";
import { itemRaw } from "../model";
import { typeLabel } from "../labels";
import { ConditionBuilder } from "./ConditionBuilder";
import { useProcessIds, useResourceIds, useRouteIds, useSecretIds, useSettings, useShapeIds } from "./context";
import { KeyValueEditor } from "./KeyValueEditor";
import type { FieldDef, Option, OptionSource } from "./types";

/** The read/write handle for a field's place on the step. */
export function useFieldIO(field: FieldDef): FieldIO {
  const { io } = useSettings();
  return field.at.length === 1 ? io.field(field.at[0]) : io.inner(field.at[0], field.at[1]);
}

/** The choices behind an OptionSource, or the static list. */
export function useOptions(src: readonly Option[] | OptionSource): Option[] {
  const { catalog } = useSettings();
  const intents = useIntentIds();
  const processes = useProcessIds();
  const shapes = useShapeIds();
  const isStatic = Array.isArray(src);
  const source = isStatic ? undefined : (src as OptionSource);
  const resources = useResourceIds(source?.from === "resources" ? source.kinds : undefined);
  return useMemo(() => {
    if (isStatic) return src as Option[];
    switch (source!.from) {
      case "nodeTypes": return (catalog?.node_types ?? []).map((t) => ({ value: t.name, label: typeLabel(t.name), hint: t.summary }));
      case "actions": return (catalog?.actions ?? []).map((a) => ({ value: a.name, label: a.name, hint: a.summary }));
      case "edgeTypes": return (catalog?.edge_types ?? []).map((e) => ({ value: e.name, label: humanize(e.name), hint: e.summary }));
      case "intents": return intents.map((i) => ({ value: i, label: i }));
      case "processes": return processes.map((p) => ({ value: p, label: p }));
      case "shapes": return shapes.map((p) => ({ value: p, label: p }));
      case "resources": return resources.map((r) => ({ value: r.id, label: r.id, hint: r.kind }));
    }
  }, [isStatic, src, source, catalog, intents, processes, shapes, resources]);
}

// ---------------------------------------------------------------------------

/** A multi-line text box that commits on blur; `facts` are offered to insert at the caret. */
function TextArea({ label, raw, path, onCommit, rows, mono, placeholder, help, facts, readOnly, required }: {
  label: string; raw: string | undefined; path: string; onCommit(raw: string | undefined): void; rows: number; mono: boolean;
  placeholder?: string; help?: string; facts?: readonly string[]; readOnly?: boolean; required?: boolean;
}) {
  const id = useId();
  const cur = raw === undefined ? "" : (unquote(raw) ?? raw.trim());
  const [text, setText] = useState(cur);
  const last = useRef(cur);
  const ref = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    if (cur !== last.current) {
      last.current = cur;
      setText(cur);
    }
  }, [cur]);
  const { here } = useDiagnostics(path);
  const commit = (t: string) => {
    if (t === last.current) return;
    last.current = t;
    onCommit(t === "" ? undefined : quote(t));
  };
  const insert = (f: string) => {
    const el = ref.current;
    const at = el ? el.selectionStart : text.length;
    const next = text.slice(0, at) + f + text.slice(el ? el.selectionEnd : at);
    setText(next);
    commit(next);
  };
  return (
    <div className="cv-field" data-path={path}>
      <label htmlFor={id} className={required ? "is-required" : undefined}>{label}</label>
      <textarea
        id={id} ref={ref} className={mono ? "mono" : undefined} rows={rows} value={text} placeholder={placeholder} disabled={readOnly} spellCheck={false}
        aria-invalid={here.length > 0 || undefined} onChange={(e) => setText(e.target.value)} onBlur={() => commit(text)}
      />
      {facts && facts.length > 0 && !readOnly && (
        <div className="cv-insert" aria-label="Insert a value">
          <span>Insert</span>
          {facts.slice(0, 8).map((f) => <button type="button" key={f} className="cv-chipbtn" onClick={() => insert(f)}>{f}</button>)}
        </div>
      )}
      <Problems items={here} />
      {help && <small>{help}</small>}
    </div>
  );
}

function ListField({ field, io, item }: { field: FieldDef; io: FieldIO; item: "text" | "number" }) {
  const { readOnly } = useSettings();
  const list = useStringList(io);
  if (!list.editable) return <Setting io={io} label={field.label} kind="any" doc="Written as an expression; edit it as text." />;
  const options = field.kind === "stringList" ? field.options : undefined;
  const bad = item === "number";
  const add = (n: string) => {
    if (bad && !/^-?\d+(\.\d+)?$/.test(n)) return;
    // numbers are bare; text is always quoted (unlike fact names, which are bare words)
    list.write([...list.raws, bad ? n : quote(n)]);
  };
  return (
    <div className="cv-field" data-path={io.path}>
      <label>{field.label}</label>
      <NameChips names={list.names} readOnly={readOnly} empty="Nothing yet" onRemove={(n) => list.write(list.raws.filter((_, i) => list.names[i] !== n))} />
      {options ? (
        <AddName label={`Add to ${field.label}`} placeholder={`+ ${field.label}…`} readOnly={readOnly} options={options.map((o) => o.value).filter((v) => !list.names.includes(v))} onAdd={add} />
      ) : (
        <AddName free label={`Add to ${field.label}`} placeholder={`+ ${bad ? "Number" : "Add"}…`} readOnly={readOnly} onAdd={add} />
      )}
      {field.help && <small>{field.help}</small>}
    </div>
  );
}

function FactPicker({ field, io }: { field: Extract<FieldDef, { kind: "factPicker" }>; io: FieldIO }) {
  const { readOnly, available, needs, produces, graph } = useSettings();
  const multi = !!field.multiple;
  const role = field.role ?? "needs";
  const pool = role === "produces" ? produces : role === "needs" ? (graph?.kind === "intent" ? [...new Set([...needs, ...available])] : needs) : available;
  const list = useStringList(io);
  const cur = io.raw === undefined ? "" : (unquote(io.raw) ?? io.raw.trim());
  if (!multi) {
    return <Choice io={io} label={field.label} bare={false} readOnly={readOnly} doc={field.help} options={pool.map((n) => ({ value: n, label: n }))} />;
  }
  if (!list.editable) return <Setting io={io} label={field.label} kind="any" />;
  void cur;
  return (
    <div className="cv-field" data-path={io.path}>
      <label>{field.label}</label>
      <NameChips names={list.names} readOnly={readOnly} empty="None" onRemove={(n) => list.write(list.raws.filter((_, i) => list.names[i] !== n))} />
      <AddName label={`Add to ${field.label}`} placeholder="+ Add a fact…" readOnly={readOnly} options={pool.filter((n) => !list.names.includes(n))} onAdd={(n) => list.write([...list.raws, itemRaw(n)])} />
      {field.help && <small>{field.help}</small>}
    </div>
  );
}

/** Renders one field of a descriptor. */
export function FieldView({ field }: { field: FieldDef }) {
  const s = useSettings();
  const io = useFieldIO(field);
  const label = field.label;
  const help = field.help;
  const ro = s.readOnly;
  const req = field.required;
  switch (field.kind) {
    case "text":
      return <Setting io={io} label={label} kind="string" doc={help} placeholder={field.placeholder} readOnly={ro} />;
    case "number":
      return <Setting io={io} label={label} kind={field.integer ? "int" : "number"} doc={help} placeholder={field.placeholder} readOnly={ro} />;
    case "duration":
      return <Setting io={io} label={label} kind="duration" doc={help} placeholder={field.placeholder} readOnly={ro} />;
    case "boolean":
      return <Toggle io={io} label={label} doc={help} readOnly={ro} />;
    case "longText":
      return <TextArea label={label} raw={io.raw} path={io.path} onCommit={io.set} rows={field.rows ?? 3} mono={false} placeholder={field.placeholder} help={help} readOnly={ro} required={req} facts={s.available} />;
    case "expression":
      return <TextArea label={label} raw={io.raw} path={io.path} onCommit={io.set} rows={field.rows ?? 3} mono placeholder={field.placeholder} help={help} readOnly={ro} required={req} facts={[...s.needs]} />;
    case "code":
      return <TextArea label={label} raw={io.raw} path={io.path} onCommit={io.set} rows={field.rows ?? 5} mono placeholder={field.placeholder} help={help} readOnly={ro} required={req} />;
    case "select":
      return <SelectField field={field} io={io} />;
    case "keyValue":
      return <KeyValueEditor at={field.at} label={label} help={help} keyLabel={field.keyLabel} valueLabel={field.valueLabel} />;
    case "list":
      return <ListField field={field} io={io} item={field.item ?? "text"} />;
    case "stringList":
      return <ListField field={field} io={io} item="text" />;
    case "factPicker":
      return <FactPicker field={field} io={io} />;
    case "resourcePicker":
      return <ResourceField field={field} io={io} />;
    case "intentPicker":
      return field.multiple ? <IntentList field={field} io={io} /> : <IntentField field={field} io={io} />;
    case "routePicker":
      return <RouteField field={field} io={io} />;
    case "secretRef":
      return <SecretField field={field} io={io} />;
    case "cases":
      return (
        <ObjectListEditor
          io={io} rowLabel={field.rowLabel} addLabel={field.addLabel} seed={field.seed} readOnly={ro}
          empty={field.empty ?? "Nothing here yet."}
          cols={field.cols.map((c) => ({ key: c.key, label: c.label, kind: c.kind, placeholder: c.placeholder, title: c.title }))}
        />
      );
    case "conditions": {
      const cur = io.raw === undefined ? undefined : (unquote(io.raw) ?? io.raw.trim());
      return <ConditionBuilder label={label} value={cur} path={io.path} onChange={(f) => io.set(f === undefined ? undefined : quote(f))} suggestions={[...s.needs, ...s.available]} readOnly={ro} help={help} placeholder={field.placeholder} lead={field.lead} />;
    }
    case "custom":
      return <>{field.render({ field, values: valuesOf(s.node, [field]), node: s.node, readOnly: ro, set: io.set })}</>;
  }
}

function SelectField({ field, io }: { field: Extract<FieldDef, { kind: "select" }>; io: FieldIO }) {
  const { readOnly } = useSettings();
  const options = useOptions(field.options);
  return <Choice io={io} label={field.label} options={options} bare={field.bare ?? true} readOnly={readOnly} doc={field.help} allowNone={field.allowNone ?? !field.required} />;
}

function ResourceField({ field, io }: { field: Extract<FieldDef, { kind: "resourcePicker" }>; io: FieldIO }) {
  const { readOnly, catalog, node } = useSettings();
  const own = field.kinds === "fromType" ? typeKinds(catalog, node) : field.kinds;
  const all = useResourceIds(own);
  const ids = all.length ? all : [];
  return <Choice io={io} label={field.label} bare={false} readOnly={readOnly} doc={field.help ?? (all.length ? undefined : "No connection of this kind exists yet. Add one under Connections.")} options={ids.map((r) => ({ value: r.id, label: r.id, hint: r.kind }))} />;
}

function typeKinds(catalog: ReturnType<typeof useSettings>["catalog"], node: ReturnType<typeof useSettings>["node"]): readonly string[] | undefined {
  const fam = node.children?.find((c) => c.name === "family")?.raw;
  const name = fam ? (unquote(fam) ?? fam.trim()) : undefined;
  return catalog?.node_types.find((t) => t.name === name)?.resource_kinds;
}

function IntentField({ field, io }: { field: Extract<FieldDef, { kind: "intentPicker" }>; io: FieldIO }) {
  const { readOnly } = useSettings();
  const ids = useIntentIds();
  return <Choice io={io} label={field.label} bare={false} readOnly={readOnly} doc={field.help} options={ids.map((i) => ({ value: i, label: i }))} />;
}

function IntentList({ field, io }: { field: Extract<FieldDef, { kind: "intentPicker" }>; io: FieldIO }) {
  const { readOnly } = useSettings();
  const ids = useIntentIds();
  const list = useStringList(io);
  if (!list.editable) return <Setting io={io} label={field.label} kind="any" />;
  return (
    <div className="cv-field" data-path={io.path}>
      <label>{field.label}</label>
      <NameChips names={list.names} readOnly={readOnly} empty="No flows yet" onRemove={(n) => list.write(list.raws.filter((_, i) => list.names[i] !== n))} />
      <AddName label={`Add to ${field.label}`} placeholder="+ Add a flow…" readOnly={readOnly} options={ids.filter((i) => !list.names.includes(i))} onAdd={(n) => list.write([...list.raws, itemRaw(n)])} />
      {field.help && <small>{field.help}</small>}
    </div>
  );
}

function RouteField({ field, io }: { field: Extract<FieldDef, { kind: "routePicker" }>; io: FieldIO }) {
  const { readOnly } = useSettings();
  const ids = useRouteIds();
  return <Choice io={io} label={field.label} bare={false} readOnly={readOnly} doc={field.help} options={ids.map((i) => ({ value: i, label: i }))} />;
}

function SecretField({ field, io }: { field: Extract<FieldDef, { kind: "secretRef" }>; io: FieldIO }) {
  const { readOnly } = useSettings();
  const ids = useSecretIds();
  return <Choice io={io} label={field.label} bare={false} readOnly={readOnly} doc={field.help ?? "Pick a secret; its value is never shown here."} options={ids.map((i) => ({ value: i, label: i }))} />;
}

/** The values of a descriptor's fields, read off the step (for `visibleWhen`, `validate` and custom fields). */
export function valuesOf(node: import("../../api/types").BlockNode, fields: readonly FieldDef[]): import("./types").Values {
  const at = (a: readonly string[]): string | undefined => {
    if (a.length === 1) return node.children?.find((c) => c.kind === "field" && c.name === a[0])?.raw;
    const b = node.children?.find((c) => c.kind === "block" && c.type === a[0]);
    return b?.children?.find((c) => c.kind === "field" && c.name === a[1])?.raw;
  };
  const byId = new Map(fields.map((f) => [f.id, f]));
  const rawAt = (a: import("./types").At) => at(a);
  return {
    node,
    rawAt,
    raw: (id) => (byId.get(id) ? at(byId.get(id)!.at) : undefined),
    text: (id) => {
      const raw = byId.get(id) ? at(byId.get(id)!.at) : undefined;
      return raw === undefined ? undefined : (unquote(raw) ?? raw.trim());
    },
  };
}

export { emitList };
