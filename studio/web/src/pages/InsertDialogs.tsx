// The Insert menu: each entry opens a small dialog that builds valid markup
// for the person (no template syntax to remember) and hands it to the editor.
import { Code2, Component, Link2, MousePointerClick, Send, Variable } from "lucide-react";
import { useMemo, useState } from "react";
import type { TemplateInfo } from "../api/pageTypes";
import { Dialog } from "../components/Dialog";
import { Menu, MenuItem } from "../ui/Menu";
import { buttonSnippet, formSnippet, friendlyName, includeSnippet, linkSnippet, variableSnippet, type RouteChoice } from "./lib";
import { KindPill } from "./parts";

export type InsertKind = "component" | "variable" | "button" | "link" | "form";

export function InsertMenu({ onPick, disabled }: { onPick(kind: InsertKind): void; disabled?: boolean }) {
  return (
    <span className={disabled ? "insert-disabled" : undefined}>
      <Menu label="Insert something" trigger={<><Code2 size={15} /> Insert</>} className="sm">
        {() => (
          <>
            <MenuItem onClick={() => onPick("component")}><Component size={16} /> Reusable piece…</MenuItem>
            <MenuItem onClick={() => onPick("variable")}><Variable size={16} /> Information from the page…</MenuItem>
            <MenuItem onClick={() => onPick("button")}><MousePointerClick size={16} /> Button</MenuItem>
            <MenuItem onClick={() => onPick("link")}><Link2 size={16} /> Link to a page…</MenuItem>
            <MenuItem onClick={() => onPick("form")}><Send size={16} /> Form that calls an endpoint…</MenuItem>
          </>
        )}
      </Menu>
    </span>
  );
}

interface Props {
  kind: InsertKind;
  /** Templates the current design could include (never itself or a layout). */
  templates: TemplateInfo[];
  /** Names of the variables this design reads, plus the app's own. */
  variables: string[];
  routes: RouteChoice[];
  /** The address of a page that uses the design being edited (for "come back here"). */
  ownUrl?: string;
  onInsert(text: string): void;
  onClose(): void;
}

export function InsertDialog({ kind, ...p }: Props) {
  const done = (text: string) => {
    p.onInsert(text);
    p.onClose();
  };
  switch (kind) {
    case "component": return <ComponentPicker {...p} onDone={done} />;
    case "variable": return <VariablePicker {...p} onDone={done} />;
    case "button": return <ButtonBuilder {...p} onDone={done} />;
    case "link": return <LinkBuilder {...p} onDone={done} />;
    case "form": return <FormBuilder {...p} onDone={done} />;
  }
}

type Inner = Omit<Props, "kind" | "onInsert"> & { onDone(text: string): void };

function Footer({ onClose, disabled, label = "Insert" }: { onClose(): void; disabled?: boolean; label?: string }) {
  return (
    <div className="dialog-actions">
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="submit" className="btn primary" disabled={disabled}>{label}</button>
    </div>
  );
}

function ComponentPicker({ templates, onDone, onClose }: Inner) {
  const [q, setQ] = useState("");
  const [sel, setSel] = useState<string | null>(null);
  const list = useMemo(
    () => templates.filter((t) => t.kind !== "layout").filter((t) => !q.trim() || t.name.toLowerCase().includes(q.trim().toLowerCase())),
    [templates, q],
  );
  return (
    <Dialog title="Insert a reusable piece" subtitle="It’s drawn here and stays in sync with the original." onClose={onClose}>
      <form className="form-stack" onSubmit={(e) => { e.preventDefault(); if (sel) onDone(includeSnippet(sel)); }}>
        <input type="search" placeholder="Search…" value={q} onChange={(e) => setQ(e.target.value)} aria-label="Search reusable pieces" autoFocus data-autofocus />
        <ul className="pick-list" role="listbox" aria-label="Reusable pieces">
          {list.map((t) => (
            <li key={t.name}>
              <button type="button" role="option" aria-selected={sel === t.name} className={`pick-row${sel === t.name ? " on" : ""}`} onClick={() => setSel(t.name)} onDoubleClick={() => onDone(includeSnippet(t.name))}>
                <span><strong>{friendlyName(t.name).title}</strong><small>{t.name}</small></span>
                <KindPill kind={t.kind} />
              </button>
            </li>
          ))}
          {list.length === 0 && <li className="muted pick-empty">Nothing matches.</li>}
        </ul>
        <Footer onClose={onClose} disabled={!sel} />
      </form>
    </Dialog>
  );
}

function VariablePicker({ variables, onDone, onClose }: Inner) {
  const [q, setQ] = useState("");
  const clean = q.trim();
  const list = useMemo(() => variables.filter((v) => !clean || v.toLowerCase().includes(clean.toLowerCase())), [variables, clean]);
  const valid = /^[A-Za-z_][\w.]*$/.test(clean);
  return (
    <Dialog title="Show information from the page" subtitle="Pick a value the page can draw, or type its name." onClose={onClose}>
      <form className="form-stack" onSubmit={(e) => { e.preventDefault(); if (valid) onDone(variableSnippet(clean)); }}>
        <input type="text" placeholder="e.g. title or user.email" value={q} onChange={(e) => setQ(e.target.value)} aria-label="Name of the value" spellCheck={false} autoFocus data-autofocus />
        <ul className="pick-list chips" role="listbox" aria-label="Values">
          {list.slice(0, 60).map((v) => (
            <li key={v}><button type="button" role="option" aria-selected={clean === v} className={`pick-chip${clean === v ? " on" : ""}`} onClick={() => onDone(variableSnippet(v))}>{v}</button></li>
          ))}
          {list.length === 0 && <li className="muted pick-empty">No known value matches. You can still use the name you typed.</li>}
        </ul>
        {clean && !valid && <p className="hint bad" role="alert">Use letters, numbers and _ only.</p>}
        <Footer onClose={onClose} disabled={!valid} />
      </form>
    </Dialog>
  );
}

function ButtonBuilder({ onDone, onClose }: Inner) {
  const [label, setLabel] = useState("Save");
  return (
    <Dialog title="Insert a button" subtitle="A plain button. Wrap it in a form or link to make it do something." onClose={onClose}>
      <form className="form-stack" onSubmit={(e) => { e.preventDefault(); onDone(buttonSnippet(label)); }}>
        <label className="fld"><span className="fld-label">Button text</span><input type="text" value={label} onChange={(e) => setLabel(e.target.value)} autoFocus data-autofocus /></label>
        <Footer onClose={onClose} />
      </form>
    </Dialog>
  );
}

function RoutePicker({ routes, value, onChange, label }: { routes: RouteChoice[]; value: string; onChange(v: string): void; label: string }) {
  return (
    <label className="fld">
      <span className="fld-label">{label}</span>
      <select value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">Choose…</option>
        {routes.map((r) => <option key={r.name} value={r.name}>{r.method} {r.path}{r.hasTemplate ? "" : " · no page"}</option>)}
      </select>
    </label>
  );
}

function LinkBuilder({ routes, onDone, onClose }: Inner) {
  const gets = useMemo(() => routes.filter((r) => r.method === "GET" && r.hasTemplate), [routes]);
  const [name, setName] = useState("");
  const [label, setLabel] = useState("Open");
  const route = gets.find((r) => r.name === name);
  return (
    <Dialog title="Link to a page" subtitle="A button-style link that takes people to another page." onClose={onClose}>
      <form className="form-stack" onSubmit={(e) => { e.preventDefault(); if (route) onDone(linkSnippet(route.path, label)); }}>
        <RoutePicker routes={gets} value={name} onChange={setName} label="Where should it go?" />
        <label className="fld"><span className="fld-label">Link text</span><input type="text" value={label} onChange={(e) => setLabel(e.target.value)} /></label>
        {route && <p className="hint">Goes to <code>{route.path}</code></p>}
        <Footer onClose={onClose} disabled={!route} />
      </form>
    </Dialog>
  );
}

function FormBuilder({ routes, ownUrl, onDone, onClose }: Inner) {
  const posts = useMemo(() => routes.filter((r) => r.method !== "GET"), [routes]);
  const [name, setName] = useState("");
  const [fields, setFields] = useState("title");
  const [submit, setSubmit] = useState("Save");
  const [back, setBack] = useState(!!ownUrl);
  const route = posts.find((r) => r.name === name);
  const list = fields.split(",").map((f) => f.trim()).filter(Boolean);
  return (
    <Dialog title="Form that calls an endpoint" subtitle="Sends what people type to one of your endpoints." onClose={onClose}>
      <form
        className="form-stack"
        onSubmit={(e) => {
          e.preventDefault();
          if (route) onDone(formSnippet(route.method, route.path, { redirect: back ? ownUrl : undefined, submit, fields: list }));
        }}
      >
        <RoutePicker routes={posts} value={name} onChange={setName} label="Which endpoint?" />
        <label className="fld"><span className="fld-label">Fields (comma separated)</span><input type="text" value={fields} onChange={(e) => setFields(e.target.value)} placeholder="title, description" spellCheck={false} /></label>
        <label className="fld"><span className="fld-label">Button text</span><input type="text" value={submit} onChange={(e) => setSubmit(e.target.value)} /></label>
        {ownUrl && (
          <label className="check"><input type="checkbox" checked={back} onChange={(e) => setBack(e.target.checked)} /> Come back to this page afterwards <code>{ownUrl}</code></label>
        )}
        {route && route.method !== "POST" && <p className="hint">Browsers only send forms as POST, so this form posts to <code>{route.path}</code>.</p>}
        <Footer onClose={onClose} disabled={!route || list.length === 0} />
      </form>
    </Dialog>
  );
}

