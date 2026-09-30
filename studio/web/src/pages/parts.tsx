// Small shared pieces of the Page designs screens.
import { FilePlus2 } from "lucide-react";
import { useMemo, useState, type ReactNode } from "react";
import type { TemplateInfo, TemplateKind } from "../api/pageTypes";
import { Dialog } from "../components/Dialog";
import { Icon } from "../ui/Icon";
import "./picker.css";
import { Badge, type Tone } from "../ui/primitives";
import {
  KIND_HELP, KIND_LABEL, STATUS_HELP, STATUS_LABEL, newStaticPath, newTemplateName, starterSource, templatePath,
  type Status,
} from "./lib";

const STATUS_TONE: Record<Status, Tone> = { original: "muted", customized: "accent", new: "ok" };
const KIND_TONE: Record<TemplateKind, Tone> = { page: "info", layout: "accent", component: "muted" };

export function StatusPill({ status }: { status: Status }) {
  return <Badge tone={STATUS_TONE[status]} className="dot" title={STATUS_HELP[status]}>{STATUS_LABEL[status]}</Badge>;
}

export function KindPill({ kind }: { kind: TemplateKind }) {
  return <Badge tone={KIND_TONE[kind]} title={KIND_HELP[kind]}>{KIND_LABEL[kind]}</Badge>;
}

export function StaticKindPill({ path }: { path: string }) {
  const ext = path.slice(path.lastIndexOf(".") + 1).toUpperCase();
  return <Badge tone="muted" className="mono">{ext}</Badge>;
}

const KINDS: TemplateKind[] = ["page", "layout", "component"];
const KIND_ICON = { page: "globe", layout: "layout-template", component: "layers" } as const;

/** "New design": pick what it is, name it, and land in the editor with a working starting point. */
export function NewDesignDialog({
  existing, initialName = "", initialKind = "page", onCreate, onClose,
}: {
  existing: Set<string>;
  initialName?: string;
  initialKind?: TemplateKind;
  onCreate(path: string, content: string): Promise<boolean> | boolean;
  onClose(): void;
}) {
  const [kind, setKind] = useState<TemplateKind>(initialKind);
  const [text, setText] = useState(initialName.replace(/^(pages|layouts|components)\//, ""));
  const [busy, setBusy] = useState(false);
  const name = useMemo(() => newTemplateName(kind, text), [kind, text]);
  const clash = !!name && existing.has(name);
  const error = text.trim() && !name ? "Use letters, numbers and dashes." : clash ? "A design with that name already exists." : null;
  const submit = async () => {
    if (!name || clash || busy) return;
    setBusy(true);
    const ok = await onCreate(templatePath(name), starterSource(kind, name));
    setBusy(false);
    if (ok) onClose();
  };
  return (
    <Dialog title="New page design" subtitle="Start from a working template and change it." onClose={onClose}>
      <form onSubmit={(e) => { e.preventDefault(); void submit(); }} className="form-stack">
        <div className="kind-cards" role="radiogroup" aria-label="What are you making?">
          {KINDS.map((k) => (
            <button key={k} type="button" role="radio" aria-checked={kind === k} className={`kind-card${kind === k ? " on" : ""}`} onClick={() => setKind(k)}>
              <span className="kind-card-icon"><Icon name={KIND_ICON[k]} size={18} /></span>
              <strong>{KIND_LABEL[k]}</strong>
              <span>{KIND_HELP[k]}</span>
            </button>
          ))}
        </div>
        <label className="fld">
          <span className="fld-label">Name</span>
          <input type="text" value={text} onChange={(e) => setText(e.target.value)} placeholder={kind === "page" ? "e.g. About us" : kind === "layout" ? "e.g. Wide" : "e.g. Pricing table"} autoFocus data-autofocus spellCheck={false} />
        </label>
        <p className={`hint${error ? " bad" : ""}`} role={error ? "alert" : undefined}>
          {error ?? (name ? <>Saved as <code>{name}</code></> : "Give it a short name.")}
        </p>
        <div className="dialog-actions">
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button type="submit" className="btn primary" disabled={!name || clash || busy}><FilePlus2 size={16} /> Create design</button>
        </div>
      </form>
    </Dialog>
  );
}

/** "New file" / "Customize an existing file" for static assets. */
export function StaticFileDialog({
  mode, existing, onSubmit, onClose,
}: {
  mode: "new" | "customize";
  existing: Set<string>;
  onSubmit(path: string): Promise<boolean> | boolean;
  onClose(): void;
}) {
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const path = useMemo(() => newStaticPath(text), [text]);
  const clash = !!path && existing.has(path);
  const error = text.trim() && !path ? "Use a path like css/site.css. Allowed types: .css .js .json .svg .txt .html" : clash ? "That file is already in this version." : null;
  return (
    <Dialog
      title={mode === "new" ? "New static file" : "Customize an existing file"}
      subtitle={mode === "new" ? "Styles, scripts and images your pages load." : "Copy one of the app’s own files here to change it. The original stays untouched."}
      onClose={onClose}
    >
      <form
        className="form-stack"
        onSubmit={async (e) => {
          e.preventDefault();
          if (!path || clash || busy) return;
          setBusy(true);
          const ok = await onSubmit(path);
          setBusy(false);
          if (ok) onClose();
        }}
      >
        <label className="fld">
          <span className="fld-label">File path</span>
          <input type="text" value={text} onChange={(e) => setText(e.target.value)} placeholder="css/app.css" autoFocus data-autofocus spellCheck={false} />
        </label>
        <p className={`hint${error ? " bad" : ""}`} role={error ? "alert" : undefined}>{error ?? (path ? <>Saved as <code>{path}</code></> : "Relative to the app’s static folder.")}</p>
        <div className="dialog-actions">
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button type="submit" className="btn primary" disabled={!path || clash || busy}>{mode === "new" ? "Create file" : "Customize file"}</button>
        </div>
      </form>
    </Dialog>
  );
}

/** A small titled block in a side pane. */
export function Pane({ title, hint, actions, children, className = "" }: { title: string; hint?: ReactNode; actions?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={`pane ${className}`}>
      <header className="pane-head">
        <h3>{title}</h3>
        {actions}
      </header>
      {hint && <p className="pane-hint">{hint}</p>}
      {children}
    </section>
  );
}

export function templateWarnings(t: Pick<TemplateInfo, "diagnostics" | "missing">): number {
  return (t.diagnostics?.length ?? 0) + (t.missing?.length ?? 0);
}
