import { ArrowDown, ArrowUp, Plus, Trash2 } from "lucide-react";
import { useMemo, useState, type ReactNode } from "react";
import type { BlockNode, BlockSchema, ConfigField, FieldSchema, Op } from "../api/types";
import { SECTIONS, fieldHelp, fieldLabel, humanize, sectionOf, type SectionId } from "../labels";
import { emitList, parseList, scalarKindOf, unquote, type ScalarKind } from "../lib/bcl";
import { blocksOf, childPath, fieldOf, isWithin } from "../lib/paths";
import { useUi } from "../state/ui";
import { Disclosure } from "../ui/Disclosure";
import { Icon } from "../ui/Icon";
import { Presence, Row } from "../ui/motion-components";
import { Problems, useDiagnostics, useForm, useRaw } from "./ctx";
import { IDENT_OPTIONS } from "./hints";
import { ScalarField } from "./ScalarField";
import { TemplateExtras } from "../pages/TemplateExtras";

// ---------------------------------------------------------------------------
// BlockForm: renders one block from its schema. Top-level blocks are grouped
// into friendly sections; nested blocks are a flat grid. Fields the schema does
// not know are shown as raw rows: nothing present in the file is ever hidden.
// ---------------------------------------------------------------------------

export interface BlockFormProps {
  node: BlockNode;
  schema: BlockSchema | undefined;
  /** The BCL keyword of this block ("resource", "node", "authz"...). */
  blockType: string;
  /** Flat grid instead of sections (used inside cards). */
  nested?: boolean;
}

const SCALAR_KINDS = new Set(["string", "ident", "int", "number", "bool", "duration"]);
const WIDE = new Set(["list", "map", "block", "blocks"]);
const WIDE_NAMES = new Set(["description", "config"]);

export function BlockForm({ node, schema, blockType, nested = false }: BlockFormProps) {
  const { file, catalog, readOnly, edit, focusPath, expandAll } = useForm();
  const dev = useUi((s) => s.devView);
  const [revealed, setRevealed] = useState<string[]>(() => defaultRevealed(blockType));
  const [revealedSection, setRevealedSection] = useState<SectionId | null>(null);
  const { here } = useDiagnostics(node.path);
  const fields = schema?.fields ?? [];
  const known = new Set(fields.map((f) => f.name));

  const isSet = (f: FieldSchema) => {
    switch (f.kind) {
      case "block":
      case "blocks":
        return blocksOf(node, f.name).length > 0 || !!fieldOf(node, f.name);
      case "map":
        return !!fieldOf(node, f.name) || blocksOf(node, f.name).length > 0;
      default:
        return !!fieldOf(node, f.name);
    }
  };
  const shown = fields.filter((f) => isSet(f) || revealed.includes(f.name));
  const hidden = fields.filter((f) => !shown.includes(f));

  const extraFields = (node.children ?? []).filter((c) => c.kind === "field" && !known.has(c.name ?? ""));
  const extraBlocks = (node.children ?? []).filter((c) => c.kind === "block" && !known.has(c.type ?? ""));

  const config = configFieldsFor(blockType, node, catalog);
  const row = (f: FieldSchema) => <FieldRow key={f.name} field={f} node={node} blockType={blockType} configFields={config} />;

  let body: ReactNode;
  if (nested) {
    body = <div className="field-grid">{shown.map(row)}</div>;
  } else {
    const bySection = new Map<SectionId, FieldSchema[]>();
    for (const f of shown) {
      const s = sectionOf(blockType, f.name);
      bySection.set(s, [...(bySection.get(s) ?? []), f]);
    }
    const focusSection = (() => {
      if (!focusPath || !isWithin(focusPath, node.path) || focusPath === node.path) return null;
      const seg = focusPath.slice(node.path.length + 1).split("/")[0]!.replace(/\\$/, "");
      return sectionOf(blockType, seg);
    })();
    const multi = [...bySection.keys()].length > 1 || bySection.has("advanced");
    body = (
      <div className="sections">
        {SECTIONS.filter((s) => bySection.has(s.id)).map((s) => {
          const list = bySection.get(s.id)!;
          const setCount = list.filter(isSet).length;
          const problems = list.some((f) => { const p = childPath(node.path, f.name); return here.some((d) => d.path === p); });
          return multi ? (
            <Disclosure
              key={s.id}
              className={`section section-${s.id}`}
              icon={<Icon name={s.icon} size={16} />}
              title={s.label}
              meta={s.id === "advanced" ? `${setCount} set` : undefined}
              defaultOpen={s.id !== "advanced" || expandAll === true || problems}
              forceOpen={focusSection === s.id || revealedSection === s.id}
            >
              <div className="field-grid">{list.map(row)}</div>
            </Disclosure>
          ) : (
            <div key={s.id} className="section section-flat"><div className="field-grid">{list.map(row)}</div></div>
          );
        })}
      </div>
    );
  }

  return (
    <div className="block-form" data-path={node.path}>
      <Problems items={here} />
      {body}
      {extraFields.length + extraBlocks.length > 0 && (
        <section className="extras" aria-label="Other settings">
          <h4 className="extras-title">Other settings</h4>
          <p className="extras-note">These aren’t part of the standard form, so they’re shown as they’re written.</p>
          <div className="field-grid">
            {extraFields.map((c) => (
              <RawField key={c.path} path={c.path} name={c.name ?? ""} node={c} />
            ))}
          </div>
          {extraBlocks.map((b) => (
            <Disclosure key={b.path} className="card" dataPath={b.path} title={b.id ? `${humanize(b.type)} ${b.id}` : humanize(b.type)} actions={
              <button type="button" className="icon-btn sm danger" aria-label={`Remove ${b.type}`} disabled={readOnly} data-tip="Remove" onClick={() => edit([{ op: "removeBlock", file, path: b.path }])}><Trash2 size={15} /></button>
            }>
              <BlockForm node={b} schema={undefined} blockType={b.type ?? ""} nested />
            </Disclosure>
          ))}
        </section>
      )}
      {hidden.length > 0 && !readOnly && <AddFieldMenu fields={hidden} blockType={blockType} dev={dev} onPick={(name) => { setRevealed((r) => [...r, name]); setRevealedSection(sectionOf(blockType, name)); }} />}
    </div>
  );
}

function defaultRevealed(blockType: string): string[] {
  if (blockType === "resource") return ["kind", "config"];
  if (blockType === "node") return ["uses", "config"];
  return [];
}

function AddFieldMenu({ fields, blockType, dev, onPick }: { fields: FieldSchema[]; blockType: string; dev: boolean; onPick(name: string): void }) {
  const groups = SECTIONS.map((s) => ({ s, list: fields.filter((f) => sectionOf(blockType, f.name) === s.id) })).filter((g) => g.list.length);
  return (
    <label className="add-field">
      <Plus size={15} />
      <select
        aria-label="Add a setting"
        value=""
        onChange={(e) => {
          if (e.target.value) onPick(e.target.value);
        }}
      >
        <option value="">Add a setting…</option>
        {groups.map((g) => (
          <optgroup key={g.s.id} label={g.s.label}>
            {g.list.map((f) => (
              <option key={f.name} value={f.name}>
                {fieldLabel(f.name)}{dev ? ` (${f.name})` : ""}
              </option>
            ))}
          </optgroup>
        ))}
      </select>
    </label>
  );
}

// ---------------------------------------------------------------------------

interface RowProps {
  field: FieldSchema;
  node: BlockNode;
  blockType: string;
  configFields: ConfigField[] | null;
}

function FieldRow({ field, node, blockType, configFields }: RowProps) {
  const ctx = useForm();
  const path = childPath(node.path, field.name);
  const child = fieldOf(node, field.name);
  const raw = useRaw(child, path);

  const setRaw = (value: string | undefined) => {
    if (value === undefined) {
      if (child) ctx.edit([{ op: "removeField", file: ctx.file, path }]);
      return;
    }
    ctx.edit([{ op: "setField", file: ctx.file, path, value }]);
  };

  const wide = WIDE.has(field.kind) || WIDE_NAMES.has(field.name);
  const wrap = (el: ReactNode) => <div className={`cell${wide ? " wide" : ""}`}>{el}</div>;

  if (SCALAR_KINDS.has(field.kind)) {
    const kind = field.kind as ScalarKind;
    const tplField = blockType === "route" && (field.name === "template" || field.name === "layout") ? (field.name as "template" | "layout") : null;
    return wrap(
      <>
      <ScalarField
        label={field.name}
        display={fieldLabel(field.name, blockType)}
        kind={kind}
        raw={raw}
        path={path}
        doc={field.doc}
        help={fieldHelp(field.name, blockType)}
        readOnly={ctx.readOnly}
        options={kind === "ident" ? IDENT_OPTIONS[field.name] : undefined}
        suggestions={suggestionsFor(blockType, field.name, ctx.catalog)}
        onCommit={setRaw}
      />
      {tplField && <TemplateExtras field={tplField} raw={raw} path={path} readOnly={ctx.readOnly} onPick={setRaw} />}
      </>,
    );
  }

  switch (field.kind) {
    case "list":
      return wrap(<ListField field={field} path={path} raw={raw} onCommit={setRaw} />);
    case "map":
      if (field.name === "config" && configFields) {
        return wrap(<ConfigForm node={node} path={path} fields={configFields} />);
      }
      return wrap(<MapField field={field} node={node} path={path} />);
    case "block":
      return wrap(<NestedBlock field={field} node={node} />);
    case "blocks":
      return wrap(<RepeatedBlocks field={field} node={node} />);
    default:
      return wrap(<RawField path={path} name={field.name} node={child} doc={field.doc} />);
  }
}

/** Free-text suggestions: registered resource kinds and action names. */
function suggestionsFor(blockType: string, field: string, catalog: ReturnType<typeof useForm>["catalog"]): string[] | undefined {
  if (!catalog) return undefined;
  if (blockType === "resource" && field === "kind") return catalog.resource_kinds.map((k) => k.name);
  if (blockType === "node" && field === "uses") return catalog.actions.map((a) => a.name);
  return undefined;
}

function configFieldsFor(blockType: string, node: BlockNode, catalog: ReturnType<typeof useForm>["catalog"]): ConfigField[] | null {
  if (!catalog) return null;
  if (blockType === "resource") {
    const kind = unquote(fieldOf(node, "kind")?.raw ?? "");
    return catalog.resource_kinds.find((k) => k.name === kind)?.config ?? null;
  }
  if (blockType === "node") {
    const uses = unquote(fieldOf(node, "uses")?.raw ?? "");
    return catalog.actions.find((a) => a.name === uses)?.config ?? null;
  }
  return null;
}

// ---------------------------------------------------------------------------
// Raw text field: the escape hatch for anything without a dedicated widget.
// ---------------------------------------------------------------------------

export function RawField({ path, name, node, doc }: { path: string; name: string; node: BlockNode | undefined; doc?: string }) {
  const ctx = useForm();
  const dev = useUi((s) => s.devView);
  const raw = useRaw(node, path) ?? "";
  const [text, setText] = useState(raw);
  const [seen, setSeen] = useState(raw);
  if (raw !== seen) {
    setSeen(raw);
    setText(raw);
  }
  const { here } = useDiagnostics(path);
  return (
    <div className={`field${here.length ? " has-problem" : ""}`} data-path={path} data-kind="raw">
      <div className="field-head">
        <label className="field-label" htmlFor={`raw-${path}`} title={doc ?? name}>
          {name}
        </label>
        {dev && <span className="raw-name">as written</span>}
        {node && (
          <button type="button" className="icon-btn sm field-remove" aria-label={`Remove ${name}`} data-tip="Remove" disabled={ctx.readOnly}
            onClick={() => ctx.edit([{ op: "removeField", file: ctx.file, path }])}>
            <Trash2 size={15} />
          </button>
        )}
      </div>
      <textarea
        id={`raw-${path}`}
        className="mono"
        rows={Math.min(8, Math.max(1, text.split("\n").length))}
        value={text}
        spellCheck={false}
        disabled={ctx.readOnly}
        onChange={(e) => {
          setText(e.target.value);
          if (e.target.value.trim()) ctx.edit([{ op: "setField", file: ctx.file, path, value: e.target.value }]);
        }}
      />
      <Problems items={here} />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Lists
// ---------------------------------------------------------------------------

function defaultFor(kind: ScalarKind): string {
  return kind === "int" || kind === "number" ? "0" : kind === "bool" ? "false" : kind === "duration" ? "30s" : '""';
}

export function ListField({ field, path, raw, onCommit, display }: { field: FieldSchema; path: string; raw: string | undefined; onCommit(v: string | undefined): void; display?: string }) {
  const ctx = useForm();
  const itemKind = scalarKindOf(field.items) ?? "string";
  const items = raw === undefined ? [] : parseList(raw);
  const { here } = useDiagnostics(path);
  const label = display ?? fieldLabel(field.name);

  if (items === null) {
    // Not a plain list (an expression, or comments inside): edit as text.
    return <RawField path={path} name={label} node={{ kind: "field", path, name: field.name, raw, line: 0, start: 0, end: 0 }} doc={field.doc} />;
  }

  const commit = (next: string[]) => onCommit(emitList(next));
  const move = (i: number, d: number) => {
    const j = i + d;
    if (j < 0 || j >= items.length) return;
    const next = [...items];
    [next[i], next[j]] = [next[j]!, next[i]!];
    commit(next);
  };

  return (
    <fieldset className={`field list${here.length ? " has-problem" : ""}`} data-path={path} data-kind="list">
      <legend title={field.name}>
        {label} <span className="count-chip">{items.length}</span>
      </legend>
      {fieldHelp(field.name) && <p className="field-doc">{fieldHelp(field.name)}</p>}
      <div className="rows">
        <Presence mode="popLayout">
          {items.map((it, i) => (
            <Row key={`${i}`} className="list-item">
              <ScalarField
                compact
                label={`${field.name} item ${i + 1}`}
                kind={itemKind === "any" ? "any" : itemKind}
                raw={it}
                path={`${path}[${i}]`}
                readOnly={ctx.readOnly}
                onCommit={(v) => {
                  if (v === undefined) return;
                  const next = [...items];
                  next[i] = v;
                  commit(next);
                }}
                onRemove={() => commit(items.filter((_, k) => k !== i))}
              />
              <span className="reorder">
                <button type="button" className="icon-btn sm" aria-label={`Move ${field.name} item ${i + 1} up`} disabled={i === 0 || ctx.readOnly} onClick={() => move(i, -1)}><ArrowUp size={15} /></button>
                <button type="button" className="icon-btn sm" aria-label={`Move ${field.name} item ${i + 1} down`} disabled={i === items.length - 1 || ctx.readOnly} onClick={() => move(i, 1)}><ArrowDown size={15} /></button>
              </span>
            </Row>
          ))}
        </Presence>
      </div>
      <div className="field-foot">
        <button type="button" className="btn sm" aria-label={`Add ${field.name} item`} disabled={ctx.readOnly} onClick={() => commit([...items, defaultFor(itemKind)])}>
          <Plus size={14} /> Add item
        </button>
        {raw !== undefined && (
          <button type="button" className="link-btn danger" aria-label={`Remove ${field.name}`} disabled={ctx.readOnly} onClick={() => onCommit(undefined)}>
            Clear list
          </button>
        )}
      </div>
      <Problems items={here} />
    </fieldset>
  );
}

// ---------------------------------------------------------------------------
// Maps: free-form key/value blocks (headers, config, extract, ...)
// ---------------------------------------------------------------------------

function MapField({ field, node, path }: { field: FieldSchema; node: BlockNode; path: string }) {
  const ctx = useForm();
  const block = blocksOf(node, field.name)[0];
  const asField = fieldOf(node, field.name);
  const valueKind = scalarKindOf(field.items) ?? "any";
  const [key, setKey] = useState("");
  const { here } = useDiagnostics(path);
  const label = fieldLabel(field.name);

  if (asField && !block) {
    // Written inline (`name = {...}` or an expression): edit as text.
    return <RawField path={path} name={label} node={asField} doc={field.doc} />;
  }

  const add = () => {
    const k = key.trim();
    if (!k) return;
    const ops: Op[] = [];
    if (!block) ops.push({ op: "addBlock", file: ctx.file, parent: node.path, type: field.name });
    ops.push({ op: "setField", file: ctx.file, path: childPath(path, k), value: defaultFor(valueKind) });
    ctx.edit(ops);
    setKey("");
  };

  const entries = (block?.children ?? []).filter((c) => c.kind === "field");
  const nested = (block?.children ?? []).filter((c) => c.kind === "block");

  return (
    <fieldset className={`field map${here.length ? " has-problem" : ""}`} data-path={path} data-kind="map">
      <legend title={field.name}>
        {label} <span className="count-chip">{entries.length + nested.length}</span>
      </legend>
      {fieldHelp(field.name) && <p className="field-doc">{fieldHelp(field.name)}</p>}
      <div className="rows">
        <Presence mode="popLayout">
          {entries.map((e) => (
            <Row key={e.path} className="map-row"><MapEntry entry={e} kind={valueKind} /></Row>
          ))}
        </Presence>
      </div>
      {nested.map((b) => (
        <Disclosure key={b.path} className="card" dataPath={b.path} title={b.id ? `${humanize(b.type)} ${b.id}` : humanize(b.type)}>
          <BlockForm node={b} schema={undefined} blockType={b.type ?? ""} nested />
        </Disclosure>
      ))}
      {!ctx.readOnly && (
        <div className="add-row">
          <input
            type="text"
            aria-label={`New ${field.name} key`}
            placeholder="Name"
            value={key}
            spellCheck={false}
            onChange={(e) => setKey(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                add();
              }
            }}
          />
          <button type="button" className="btn sm" aria-label={`Add ${field.name} entry`} onClick={add} disabled={!key.trim()}>
            <Plus size={14} /> Add entry
          </button>
          {block && (
            <button type="button" className="link-btn danger" aria-label={`Remove ${field.name}`} onClick={() => ctx.edit([{ op: "removeBlock", file: ctx.file, path: block.path }])}>
              Clear all
            </button>
          )}
        </div>
      )}
      <Problems items={here} />
    </fieldset>
  );
}

function MapEntry({ entry, kind }: { entry: BlockNode; kind: ScalarKind }) {
  const ctx = useForm();
  const raw = useRaw(entry, entry.path);
  return (
    <ScalarField
      label={entry.name ?? ""}
      kind={kind}
      raw={raw}
      path={entry.path}
      readOnly={ctx.readOnly}
      onCommit={(v) =>
        ctx.edit([v === undefined ? { op: "removeField", file: ctx.file, path: entry.path } : { op: "setField", file: ctx.file, path: entry.path, value: v }])
      }
    />
  );
}

// ---------------------------------------------------------------------------
// config { } forms driven by the catalog (resource kind / action)
// ---------------------------------------------------------------------------

function ConfigForm({ node, path, fields }: { node: BlockNode; path: string; fields: ConfigField[] }) {
  const ctx = useForm();
  const dev = useUi((s) => s.devView);
  const block = blocksOf(node, "config")[0];
  const [revealed, setRevealed] = useState<string[]>([]);
  const known = new Set(fields.map((f) => f.name));
  const isSet = (f: ConfigField) => !!fieldOf(block, f.name);
  const shown = fields.filter((f) => isSet(f) || f.required || revealed.includes(f.name));
  const hidden = fields.filter((f) => !shown.includes(f));
  const extras = (block?.children ?? []).filter((c) => !known.has(c.kind === "field" ? (c.name ?? "") : (c.type ?? "")));

  const pre = (): Op[] => (block ? [] : [{ op: "addBlock", file: ctx.file, parent: node.path, type: "config" }]);

  return (
    <fieldset className="field config" data-path={path} data-kind="config">
      <legend>Settings <span className="count-chip">{shown.length}</span></legend>
      <div className="field-grid">
        {shown.map((cf) => (
          <div className="cell" key={cf.name}><ConfigRow cf={cf} block={block} path={childPath(path, cf.name)} pre={pre} /></div>
        ))}
      </div>
      {extras.length > 0 && (
        <section className="extras" aria-label="Other config">
          <h4 className="extras-title">Other settings</h4>
          <div className="field-grid">
            {extras.map((c) =>
              c.kind === "field" ? (
                <RawField key={c.path} path={c.path} name={c.name ?? ""} node={c} />
              ) : (
                <Disclosure key={c.path} className="card cell wide" dataPath={c.path} title={humanize(c.type)}>
                  <BlockForm node={c} schema={undefined} blockType={c.type ?? ""} nested />
                </Disclosure>
              ),
            )}
          </div>
        </section>
      )}
      {hidden.length > 0 && !ctx.readOnly && (
        <label className="add-field">
          <Plus size={15} />
          <select aria-label="Add a setting" value="" onChange={(e) => { if (e.target.value) setRevealed((r) => [...r, e.target.value]); }}>
            <option value="">Add a setting…</option>
            {hidden.map((f) => <option key={f.name} value={f.name}>{humanize(f.name)}{dev ? ` (${f.name})` : ""}</option>)}
          </select>
        </label>
      )}
    </fieldset>
  );
}

function ConfigRow({ cf, block, path, pre }: { cf: ConfigField; block: BlockNode | undefined; path: string; pre: () => Op[] }) {
  const ctx = useForm();
  const child = fieldOf(block, cf.name);
  const raw = useRaw(child, path);
  const commit = (v: string | undefined) => {
    if (v === undefined) {
      if (child) ctx.edit([{ op: "removeField", file: ctx.file, path }]);
      return;
    }
    ctx.edit([...pre(), { op: "setField", file: ctx.file, path, value: v }]);
  };
  const doc = [cf.summary, cf.default ? `Default: ${cf.default}` : ""].filter(Boolean).join(" ");
  const t = cf.type;
  const listOf = t.startsWith("[]") ? scalarKindOf(t.slice(2)) : null;
  const scalar = scalarKindOf(t);
  const shownName = humanize(cf.name);

  if (listOf) {
    return <ListField field={{ name: cf.name, kind: "list", items: t.slice(2), doc }} display={shownName} path={path} raw={raw} onCommit={commit} />;
  }
  if (scalar && scalar !== "any") {
    return <ScalarField label={cf.name} display={shownName} kind={scalar} raw={raw} path={path} doc={doc} help={cf.summary} required={cf.required} placeholder={cf.default} readOnly={ctx.readOnly} onCommit={commit} />;
  }
  if (scalar === "any" && (t === "" || t === "any")) {
    return <ScalarField label={cf.name} display={shownName} kind="any" raw={raw} path={path} doc={doc} help={cf.summary} required={cf.required} placeholder={cf.default} readOnly={ctx.readOnly} onCommit={commit} />;
  }
  return <RawField path={path} name={shownName} node={child} doc={doc} />;
}

// ---------------------------------------------------------------------------
// Nested blocks
// ---------------------------------------------------------------------------

function NestedBlock({ field, node }: { field: FieldSchema; node: BlockNode }) {
  const ctx = useForm();
  const child = blocksOf(node, field.name)[0];
  const path = childPath(node.path, field.name);
  const label = fieldLabel(field.name);
  const { below } = useDiagnostics(path);
  if (!child) {
    return (
      <button type="button" className="add-card" disabled={ctx.readOnly} aria-label={`Add ${field.name}`}
        onClick={() => ctx.edit([{ op: "addBlock", file: ctx.file, parent: node.path, type: field.name }])}>
        <Plus size={16} />
        <span className="add-card-text"><strong>{label}</strong><span>{fieldHelp(field.name) ?? "Not set up yet. Click to add."}</span></span>
      </button>
    );
  }
  const schema = field.recursive ? undefined : field.block;
  return (
    <Disclosure
      className="card"
      dataPath={path}
      title={label}
      meta={fieldHelp(field.name)}
      forceOpen={below.length > 0 || ctx.focusPath === path}
      actions={
        <button type="button" className="icon-btn sm danger" aria-label={`Remove ${field.name}`} disabled={ctx.readOnly} data-tip="Remove" onClick={() => ctx.edit([{ op: "removeBlock", file: ctx.file, path: child.path }])}>
          <Trash2 size={15} />
        </button>
      }
    >
      <BlockForm node={child} schema={schema} blockType={field.name} nested />
    </Disclosure>
  );
}

function RepeatedBlocks({ field, node }: { field: FieldSchema; node: BlockNode }) {
  const ctx = useForm();
  const items = blocksOf(node, field.name);
  const all = node.children ?? [];
  const hasId = !!field.block?.has_id;
  const [id, setId] = useState("");
  const label = fieldLabel(field.name);
  const singular = label.replace(/s$/, "");

  const position = (b: BlockNode) => all.findIndex((c) => c.path === b.path);
  const move = (b: BlockNode, dir: -1 | 1) => {
    const i = items.indexOf(b);
    const other = items[i + dir];
    if (!other) return;
    const target = dir < 0 ? position(other) : position(other) + 1;
    ctx.edit([{ op: "moveBlock", file: ctx.file, path: b.path, index: target }]);
  };

  const add = () => {
    if (hasId && !id.trim()) return;
    ctx.edit([{ op: "addBlock", file: ctx.file, parent: node.path, type: field.name, ...(hasId ? { id: id.trim() } : {}) }]);
    setId("");
  };

  return (
    <fieldset className="field blocks" data-path={childPath(node.path, field.name)} data-kind="blocks">
      <legend title={field.name}>
        {label} <span className="count-chip">{items.length}</span>
      </legend>
      {fieldHelp(field.name) && <p className="field-doc">{fieldHelp(field.name)}</p>}
      <div className="rows">
        <Presence mode="popLayout">
          {items.map((b, i) => (
            <Row key={b.path} className="block-row">
              <Disclosure
                className="card"
                dataPath={b.path}
                defaultOpen={items.length <= 4}
                forceOpen={!!ctx.focusPath && isWithin(ctx.focusPath, b.path)}
                title={b.id ? `${singular} ${b.id}` : `${singular} ${i + 1}`}
                actions={
                  <>
                    <button type="button" className="icon-btn sm" aria-label={`Move ${field.name} ${b.id ?? i + 1} up`} disabled={i === 0 || ctx.readOnly} onClick={() => move(b, -1)}><ArrowUp size={15} /></button>
                    <button type="button" className="icon-btn sm" aria-label={`Move ${field.name} ${b.id ?? i + 1} down`} disabled={i === items.length - 1 || ctx.readOnly} onClick={() => move(b, 1)}><ArrowDown size={15} /></button>
                    <button type="button" className="icon-btn sm danger" aria-label={`Remove ${field.name} ${b.id ?? i + 1}`} disabled={ctx.readOnly} data-tip="Remove"
                      onClick={() => ctx.edit([{ op: "removeBlock", file: ctx.file, path: b.path }])}><Trash2 size={15} /></button>
                  </>
                }
              >
                <BlockForm node={b} schema={field.recursive ? undefined : field.block} blockType={field.name} nested />
              </Disclosure>
            </Row>
          ))}
        </Presence>
      </div>
      {!ctx.readOnly && (
        <div className="add-row">
          {hasId && (
            <input type="text" aria-label={`New ${field.name} id`} placeholder="Name" value={id} spellCheck={false}
              onChange={(e) => setId(e.target.value)}
              onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); add(); } }} />
          )}
          <button type="button" className="btn sm" aria-label={`Add ${field.name}`} onClick={add} disabled={hasId && !id.trim()}>
            <Plus size={14} /> Add {singular.toLowerCase()}
          </button>
        </div>
      )}
    </fieldset>
  );
}

export function useBlockSchema(type: string | undefined): BlockSchema | undefined {
  const { schemas } = useForm();
  return useMemo(() => (type ? schemas?.[type] : undefined), [schemas, type]);
}
