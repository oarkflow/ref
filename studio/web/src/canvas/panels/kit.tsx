// Small building blocks the per-type panels share. Every edit goes through the
// form context's `edit`, i.e. the same op queue as the rest of the studio.
import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import type { BlockNode, Op } from "../../api/types";
import { useForm, useDiagnostics, Problems } from "../../forms/ctx";
import { ScalarField } from "../../forms/ScalarField";
import type { ScalarKind } from "../../lib/bcl";
import { emitList, parseList, quote, unquote } from "../../lib/bcl";
import { blocksOf, childPath, fieldOf } from "../../lib/paths";
import { useStudio } from "../../state/context";

// ---------------------------------------------------------------------------
// reading and writing fields of one block

export interface FieldIO {
  path: string;
  raw: string | undefined;
  set(raw: string | undefined): void;
  /** Field node, when it exists. */
  node?: BlockNode;
}

export interface BlockIO {
  node: BlockNode;
  readOnly: boolean;
  /** a field directly on the block */
  field(name: string): FieldIO;
  /** a field inside a child block (config, retry, task ...), created on demand */
  inner(block: string, name: string): FieldIO;
  /** does the child block exist */
  has(block: string): boolean;
  addBlock(block: string): void;
  removeBlock(block: string): void;
  edit(ops: Op[]): void;
}

export function useBlockIO(node: BlockNode): BlockIO {
  const { file, edit, readOnly, overlay } = useForm();
  return useMemo(() => {
    const rawAt = (path: string, n: BlockNode | undefined) => {
      const o = overlay[`${file}|${path}`];
      return o ? (o.raw ?? undefined) : n?.raw;
    };
    const write = (path: string, raw: string | undefined, pre: Op[] = []) =>
      edit([...pre, raw === undefined ? { op: "removeField", file, path } : { op: "setField", file, path, value: raw }]);
    const io: BlockIO = {
      node,
      readOnly: !!readOnly,
      field(name) {
        const n = fieldOf(node, name);
        const path = childPath(node.path, name);
        return { path, node: n, raw: rawAt(path, n), set: (raw) => write(path, raw) };
      },
      inner(block, name) {
        const b = blocksOf(node, block)[0];
        const bpath = childPath(node.path, block);
        const n = fieldOf(b, name);
        const path = childPath(bpath, name);
        return {
          path,
          node: n,
          raw: rawAt(path, n),
          set: (raw) => {
            if (raw === undefined && !b) return;
            write(path, raw, b ? [] : [{ op: "addBlock", file, parent: node.path, type: block }]);
          },
        };
      },
      has: (block) => blocksOf(node, block).length > 0,
      addBlock: (block) => edit([{ op: "addBlock", file, parent: node.path, type: block }]),
      removeBlock: (block) => {
        const b = blocksOf(node, block)[0];
        if (b) edit([{ op: "removeBlock", file, path: b.path }]);
      },
      edit,
    };
    return io;
  }, [node, file, edit, readOnly, overlay]);
}

// ---------------------------------------------------------------------------
// layout

export function Section({ title, hint, open = true, id, children }: { title: string; hint?: string; open?: boolean; id?: string; children: ReactNode }) {
  return (
    <details className="cv-sec" open={open} data-section={id ?? title}>
      <summary>
        <span>{title}</span>
        {hint && <small>{hint}</small>}
      </summary>
      <div className="cv-sec-body">{children}</div>
    </details>
  );
}

export function Row({ children }: { children: ReactNode }) {
  return <div className="cv-row">{children}</div>;
}

// ---------------------------------------------------------------------------
// controls

interface TextProps {
  io: FieldIO;
  label: string;
  kind?: ScalarKind;
  doc?: string;
  placeholder?: string;
  options?: string[];
  suggestions?: string[];
  readOnly?: boolean;
}

/** One setting, with the literal / env() / expression switch. */
export function Setting({ io, label, kind = "string", doc, placeholder, options, suggestions, readOnly }: TextProps) {
  return (
    <ScalarField
      label={label}
      kind={kind}
      raw={io.raw}
      path={io.path}
      onCommit={io.set}
      doc={doc}
      placeholder={placeholder}
      options={options}
      suggestions={suggestions}
      readOnly={readOnly}
    />
  );
}

export function Toggle({ io, label, doc, readOnly }: { io: FieldIO; label: string; doc?: string; readOnly?: boolean }) {
  const id = useId();
  const on = io.raw?.trim() === "true";
  return (
    <div className="cv-toggle" data-path={io.path}>
      <label htmlFor={id}>
        <input id={id} type="checkbox" checked={on} disabled={readOnly} onChange={(e) => io.set(e.target.checked ? "true" : undefined)} />
        <span>{label}</span>
      </label>
      {doc && <small>{doc}</small>}
    </div>
  );
}

/** A choice from a fixed list, written as a bare word (ident) or a string. */
export function Choice({ io, label, options, bare = true, doc, readOnly, allowNone = true }: {
  io: FieldIO; label: string; options: { value: string; label: string; hint?: string }[]; bare?: boolean; doc?: string; readOnly?: boolean; allowNone?: boolean;
}) {
  const id = useId();
  const cur = io.raw === undefined ? "" : (unquote(io.raw) ?? io.raw.trim());
  const known = options.some((o) => o.value === cur);
  return (
    <div className="cv-field" data-path={io.path}>
      <label htmlFor={id}>{label}</label>
      <select id={id} value={cur} disabled={readOnly} onChange={(e) => io.set(e.target.value ? (bare ? e.target.value : quote(e.target.value)) : undefined)}>
        {allowNone && <option value="">—</option>}
        {!known && cur && <option value={cur}>{cur}</option>}
        {options.map((o) => (
          <option key={o.value} value={o.value}>{o.label}</option>
        ))}
      </select>
      {(doc || options.find((o) => o.value === cur)?.hint) && <small>{options.find((o) => o.value === cur)?.hint ?? doc}</small>}
    </div>
  );
}

/** A free-text expression (condition, script). Commits when focus leaves. */
export function ExprBox({ io, label, placeholder, doc, rows = 3, readOnly }: { io: FieldIO; label: string; placeholder?: string; doc?: string; rows?: number; readOnly?: boolean }) {
  const id = useId();
  const cur = io.raw === undefined ? "" : (unquote(io.raw) ?? io.raw.trim());
  const [text, setText] = useState(cur);
  const last = useRef(cur);
  useEffect(() => {
    if (cur !== last.current) {
      last.current = cur;
      setText(cur);
    }
  }, [cur]);
  const { here } = useDiagnostics(io.path);
  const commit = () => {
    if (text === last.current) return;
    last.current = text;
    io.set(text === "" ? undefined : quote(text));
  };
  return (
    <div className="cv-field" data-path={io.path}>
      <label htmlFor={id}>{label}</label>
      <textarea
        id={id}
        className="mono"
        rows={rows}
        value={text}
        placeholder={placeholder}
        disabled={readOnly}
        spellCheck={false}
        aria-invalid={here.length > 0 || undefined}
        onChange={(e) => setText(e.target.value)}
        onBlur={commit}
      />
      <Problems items={here} />
      {doc && <small>{doc}</small>}
    </div>
  );
}

/** Small tag list of names (for facts, roles, statuses). */
export function NameChips({ names, onRemove, tone, readOnly, empty = "None yet" }: { names: string[]; onRemove(name: string): void; tone?: (n: string) => "bad" | undefined; readOnly?: boolean; empty?: string }) {
  if (names.length === 0) return <p className="cv-none">{empty}</p>;
  return (
    <ul className="cv-chips">
      {names.map((n) => (
        <li key={n} className={tone?.(n)}>
          <span>{n}</span>
          {!readOnly && (
            <button type="button" aria-label={`Remove ${n}`} onClick={() => onRemove(n)}>×</button>
          )}
        </li>
      ))}
    </ul>
  );
}

/** Adds a name from a list (or typed, when `free`). */
export function AddName({ options = [], onAdd, placeholder, free = false, label, readOnly }: { options?: string[]; onAdd(name: string): void; placeholder: string; free?: boolean; label: string; readOnly?: boolean }) {
  const [text, setText] = useState("");
  if (readOnly) return null;
  const add = (v: string) => {
    const t = v.trim();
    if (!t) return;
    onAdd(t);
    setText("");
  };
  if (!free) {
    if (options.length === 0) return null;
    return (
      <select aria-label={label} value="" onChange={(e) => e.target.value && add(e.target.value)}>
        <option value="">{placeholder}</option>
        {options.map((o) => (
          <option key={o} value={o}>{o}</option>
        ))}
      </select>
    );
  }
  return (
    <form className="cv-add" onSubmit={(e) => { e.preventDefault(); add(text); }}>
      <input aria-label={label} value={text} placeholder={placeholder} onChange={(e) => setText(e.target.value)} list={undefined} spellCheck={false} />
      <button type="submit" disabled={!text.trim()}>Add</button>
    </form>
  );
}

/** Lists of plain strings: `[a, "b"]`, edited as chips. */
export function useStringList(io: FieldIO): { names: string[]; raws: string[]; editable: boolean; write(raws: string[]): void } {
  return useMemo(() => {
    if (io.raw === undefined) return { names: [], raws: [], editable: true, write: (r: string[]) => io.set(r.length ? emitList(r) : undefined) };
    const items = parseList(io.raw);
    if (!items) return { names: [], raws: [], editable: false, write: () => {} };
    return { names: items.map((i) => unquote(i) ?? i.trim()), raws: items, editable: true, write: (r: string[]) => io.set(r.length ? emitList(r) : undefined) };
  }, [io]);
}

/** Every intent (flow) defined in the draft, for pickers. */
export function useIntentIds(): string[] {
  const trees = useStudio((s) => s.trees);
  return useMemo(() => {
    const out: string[] = [];
    for (const roots of Object.values(trees)) for (const b of roots) if (b.kind === "block" && b.type === "intent" && b.id) out.push(b.id);
    return out.sort();
  }, [trees]);
}
