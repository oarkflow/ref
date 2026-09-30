import { Copy, MoreHorizontal, Pencil, Route, Trash2, Workflow } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { useInRouterContext, useNavigate } from "react-router-dom";
import { useShallow } from "zustand/react/shallow";
import type { BlockNode } from "../api/types";
import { canvasHref } from "../canvas/link";
import { isCanvasType } from "../canvas/model";
import { focusHref } from "../journeys/useFlows";
import { Dialog } from "../components/Dialog";
import { blockInfo, categoryInfo, lc } from "../labels";
import { childPath, findNode, splitPath } from "../lib/paths";
import { useStudio, useStudioStore } from "../state/context";
import { hasRole } from "../state/store";
import { useUi } from "../state/ui";
import { Icon } from "../ui/Icon";
import { Menu, MenuItem } from "../ui/Menu";
import { motionOff } from "../ui/motion";
import { Enter, Presence, Skeleton } from "../ui/motion-components";
import { Badge } from "../ui/primitives";
import { BlockForm } from "./BlockForm";
import { FormProvider, type FormContext } from "./ctx";

/** Scrolls to and highlights the element for `path`, or its nearest ancestor that has one. */
export function focusField(root: HTMLElement, path: string): HTMLElement | null {
  const els = Array.from(root.querySelectorAll<HTMLElement>("[data-path]"));
  const segs = splitPath(path);
  for (let n = segs.length; n > 0; n--) {
    const want = segs.slice(0, n).join("/");
    const el = els.find((e) => e.dataset.path === want || e.dataset.path?.replace(/\\\//g, "/") === want.replace(/\\\//g, "/"));
    if (el) {
      el.scrollIntoView?.({ block: "center", behavior: "smooth" });
      el.classList.remove("flash");
      void el.offsetWidth; // restart the animation
      el.classList.add("flash");
      el.querySelector<HTMLElement>("input, select, textarea, button")?.focus?.({ preventScroll: true });
      return el;
    }
  }
  return null;
}

function useMaybeNavigate(): (to: string) => void {
  const inRouter = useInRouterContext();
  // The router context never changes for the life of a mounted component.
  // eslint-disable-next-line react-hooks/rules-of-hooks
  return inRouter ? useNavigate() : () => {};
}

export function Inspector() {
  const s = useStudio(
    useShallow((st) => ({
      selection: st.selection, trees: st.trees, schemas: st.schemas, catalog: st.catalog, diagnostics: st.diagnostics,
      focusPath: st.focusPath, overlay: st.overlay, meta: st.meta, draft: st.draft,
    })),
  );
  const store = useStudioStore();
  const rootRef = useRef<HTMLDivElement>(null);
  const tree = s.selection ? s.trees[s.selection.file] : undefined;
  const node = useMemo(() => (s.selection && tree ? findNode(tree, s.selection.path) : undefined), [s.selection, tree]);
  const readOnly = !hasRole(s.meta, "editor");

  useEffect(() => {
    if (!s.focusPath || !rootRef.current) return;
    const root = rootRef.current;
    focusField(root, s.focusPath);
    if (motionOff()) return;
    // A collapsed section may still be opening: centre again once it has.
    const t = setTimeout(() => root.querySelector<HTMLElement>(".flash")?.scrollIntoView?.({ block: "center", behavior: "smooth" }), 260);
    return () => clearTimeout(t);
  }, [s.focusPath, node?.path, tree]);

  if (!s.draft) return <p className="empty">Create or open a working copy to start editing.</p>;
  if (!s.selection) return <p className="empty">Pick something from the list to edit it.</p>;
  if (!tree) return <div className="inspector"><Skeleton title lines={5} /></div>;
  if (!node) return <p className="empty">This item no longer exists. Pick another.</p>;

  const ctx: FormContext = {
    file: s.selection.file,
    schemas: s.schemas,
    catalog: s.catalog,
    diagnostics: s.diagnostics,
    focusPath: s.focusPath,
    overlay: s.overlay,
    readOnly,
    edit: (ops) => store.getState().edit(ops),
  };
  const type = node.type ?? "";
  const schema = s.schemas?.[type];

  return (
    <Enter key={node.path} className="inspector" ref={rootRef}>
      <InspectorHeader file={s.selection.file} node={node} hasId={!!schema?.has_id || !!node.id} readOnly={readOnly} />
      {!schema && <p className="hint">There’s no guided form for “{type}” yet, so its settings are shown as written.</p>}
      <FormProvider value={ctx}>
        <BlockForm node={node} schema={schema} blockType={type} />
      </FormProvider>
    </Enter>
  );
}

/** UTF-8 byte offsets (from the server) -> the source text of a node. */
export function sliceBytes(content: string, start: number, end: number): string {
  const bytes = new TextEncoder().encode(content);
  return new TextDecoder().decode(bytes.slice(start, end));
}

/** The text between a block's outer braces. */
export function innerBody(blockSource: string): string {
  const i = blockSource.indexOf("{");
  const j = blockSource.lastIndexOf("}");
  return i >= 0 && j > i ? blockSource.slice(i + 1, j).trim() : "";
}

function InspectorHeader({ file, node, hasId, readOnly }: { file: string; node: BlockNode; hasId: boolean; readOnly: boolean }) {
  const store = useStudioStore();
  const navigate = useMaybeNavigate();
  const dev = useUi((s) => s.devView);
  const [dialog, setDialog] = useState<null | "rename" | "duplicate" | "delete">(null);
  const info = blockInfo(node.type);
  const cat = categoryInfo(info.category);
  const top = splitPath(node.path).length <= 2;
  const inCanvas = typeof window !== "undefined" && window.location.hash.startsWith("#/canvas");

  return (
    <header className="insp-head">
      <div className="insp-titles">
        {top && (
          <nav className="crumbs" aria-label="Breadcrumb">
            <a href={`#/c/${cat.id}`}>{cat.label}</a><span className="sep">/</span><span>{info.label}</span>
          </nav>
        )}
        {!top && <p className="crumb">{info.label}</p>}
        <div className="insp-title">
          <span className="insp-icon"><Icon name={info.icon} size={20} /></span>
          <h2>{node.id || info.label}</h2>
          <Badge tone="accent">{info.label}</Badge>
          {dev && <code className="raw-name">{node.type} · {file}</code>}
        </div>
        {info.description && top && <p className="block-doc">{info.description}</p>}
      </div>
      <div className="insp-actions">
        {top && hasId && (node.type === "route" || node.type === "intent" || node.type === "process") && (
          <a className="btn" href={`#${focusHref(`${node.type}:${node.id}`)}`} title="See where this sits in how visitors move through the app"><Route size={16} /> See journey</a>
        )}
        {!readOnly && isCanvasType(node.type) && !inCanvas && (
          <a className="btn" href={canvasHref(file, node.path)}><Workflow size={16} /> Open flow editor</a>
        )}
        {!readOnly && (
          <Menu label="More actions" align="end" className="icon-btn" trigger={<MoreHorizontal size={18} />}>
            {() => (
              <>
                {hasId && <MenuItem onClick={() => setDialog("rename")}><Pencil size={16} /> Rename…</MenuItem>}
                {top && hasId && <MenuItem onClick={() => setDialog("duplicate")}><Copy size={16} /> Duplicate…</MenuItem>}
                <MenuItem danger onClick={() => setDialog("delete")}><Trash2 size={16} /> Delete…</MenuItem>
              </>
            )}
          </Menu>
        )}
      </div>

      <Presence mode="sync">
        {dialog === "rename" && <RenameDialog key="r" file={file} node={node} onClose={() => setDialog(null)} />}
        {dialog === "duplicate" && <DuplicateDialog key="d" file={file} node={node} onClose={() => setDialog(null)} />}
        {dialog === "delete" && (
          <Dialog key="x" title={`Delete ${node.id || lc(info.label)}?`} onClose={() => setDialog(null)}>
            <p className="dialog-body">This removes it from your working copy. You can undo it right after, and nothing changes for users until a version is approved.</p>
            <div className="dialog-actions">
              <button type="button" className="btn" onClick={() => setDialog(null)}>Keep it</button>
              <button
                type="button"
                className="btn danger-solid"
                onClick={async () => {
                  setDialog(null);
                  store.getState().edit([{ op: "removeBlock", file, path: node.path }]);
                  await store.getState().flush();
                  store.setState({ selection: null });
                  if (top) navigate(`/c/${cat.id}`);
                }}
              >
                Delete
              </button>
            </div>
          </Dialog>
        )}
      </Presence>
    </header>
  );
}

function RenameDialog({ file, node, onClose }: { file: string; node: BlockNode; onClose(): void }) {
  const store = useStudioStore();
  const [id, setId] = useState(node.id ?? "");
  const info = blockInfo(node.type);
  const submit = async () => {
    const next = id.trim();
    if (!next || next === node.id) return onClose();
    onClose();
    store.getState().edit([{ op: "renameBlock", file, path: node.path, newId: next }]);
    await store.getState().flush();
    // The block keeps its type and parent; only the last path segment changes.
    const segs = splitPath(node.path);
    segs[segs.length - 1] = next;
    await store.getState().select(file, segs.reduce((acc, seg) => (acc ? childPath(acc, seg) : childPath("", seg)), ""));
  };
  return (
    <Dialog title={`Rename ${lc(info.label)}`} subtitle="Anything that refers to it by name will need updating too." onClose={onClose}>
      <form className="form-stack" onSubmit={(e) => { e.preventDefault(); void submit(); }}>
        <label className="fld">
          <span className="fld-label">New name</span>
          <input aria-label="New block id" value={id} onChange={(e) => setId(e.target.value)} autoFocus spellCheck={false} />
        </label>
        <div className="dialog-actions">
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button type="submit" className="btn primary" disabled={!id.trim()}>Rename</button>
        </div>
      </form>
    </Dialog>
  );
}

function DuplicateDialog({ file, node, onClose }: { file: string; node: BlockNode; onClose(): void }) {
  const store = useStudioStore();
  const navigate = useMaybeNavigate();
  const [id, setId] = useState(`${node.id ?? node.type}-copy`);
  const [busy, setBusy] = useState(false);
  const info = blockInfo(node.type);
  const submit = async () => {
    const d = store.getState().draft;
    if (!d || !id.trim()) return;
    setBusy(true);
    try {
      await store.getState().flush();
      const f = await store.getState().api.getFile(d.id, file);
      const body = innerBody(sliceBytes(f.content, node.start, node.end));
      store.getState().edit([{ op: "addBlock", file, type: node.type ?? "", id: id.trim(), body }]);
      await store.getState().flush();
      onClose();
      await store.getState().select(file, `${node.type}/${id.trim()}`);
      navigate("/edit");
    } catch (e) {
      store.getState().notify("error", `Could not duplicate: ${(e as Error).message}`);
      setBusy(false);
    }
  };
  return (
    <Dialog title={`Duplicate ${lc(info.label)}`} subtitle="Makes a copy with the same settings under a new name." onClose={onClose}>
      <form className="form-stack" onSubmit={(e) => { e.preventDefault(); void submit(); }}>
        <label className="fld">
          <span className="fld-label">Name of the copy</span>
          <input value={id} onChange={(e) => setId(e.target.value)} autoFocus spellCheck={false} />
        </label>
        <div className="dialog-actions">
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button type="submit" className="btn primary" disabled={!id.trim() || busy}>Duplicate</button>
        </div>
      </form>
    </Dialog>
  );
}
