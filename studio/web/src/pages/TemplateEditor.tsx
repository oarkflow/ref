// The design editor: structure on the left, the code in the middle, what the
// design needs / sample data / a live preview on the right. Changes save by
// themselves (debounced); every save is one undo step of the working copy.
import { Check, ChevronLeft, LoaderCircle, PanelLeft, PanelRight, Pencil, RotateCcw, Route, Trash2, TriangleAlert, Wand2 } from "lucide-react";
import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import type { AssetContent, PreviewData, TemplateInfo } from "../api/pageTypes";
import type { Diagnostic } from "../api/types";
import { Dialog } from "../components/Dialog";
import { NoWorkingCopy } from "../components/NoWorkingCopy";
import { useStudio, useStudioStore } from "../state/context";
import { hasRole } from "../state/store";
import { Menu, MenuItem, MenuSep } from "../ui/Menu";
import { focusHref } from "../journeys/useFlows";
import { Skeleton } from "../ui/motion-components";
import { EmptyState, ErrorState } from "../ui/primitives";
import type { CodeEditorHandle } from "./CodeEditor";
import { InsertDialog, InsertMenu, type InsertKind } from "./InsertDialogs";
import {
  assetStatus, bytesLabel, editorUrl, friendlyName, isTemplatePath, newStaticPath, newTemplateName, statusOf,
  templateName, templatePath, type Status,
} from "./lib";
import { KindPill, NewDesignDialog, StaticKindPill, StatusPill } from "./parts";
import { NeedsPanel, OutlinePanel, PreviewTab, SampleDataPanel, UsedByPanel, unprovidedVars } from "./panels";
import { useRouteChoices } from "./routes";
import { usePages, usePagesStore, usePagesSync } from "./store";
import "./pages.css";

const CodeEditor = lazy(() => import("./CodeEditor"));

type SaveState = "idle" | "editing" | "saving" | "saved" | "invalid" | "error";
type SideTab = "details" | "data" | "preview";
const AUTOSAVE_MS = 800;

function read(key: string, fallback: boolean): boolean {
  try {
    const v = localStorage.getItem(key);
    return v === null ? fallback : v === "1";
  } catch {
    return fallback;
  }
}
function write(key: string, v: boolean) {
  try { localStorage.setItem(key, v ? "1" : "0"); } catch { /* private mode */ }
}

export function TemplateEditor() {
  usePagesSync();
  const [params] = useSearchParams();
  const path = params.get("path") ?? "";
  const navigate = useNavigate();
  const studio = useStudioStore();
  const pages = usePagesStore();
  const draft = useStudio((s) => s.draft);
  const meta = useStudio((s) => s.meta);
  const diagnostics = useStudio((s) => s.diagnostics);
  const canEditRole = hasRole(meta, "editor");
  const catalog = usePages((s) => s.catalog);
  const assets = usePages((s) => s.assets);
  const byName = usePages((s) => s.byName);
  const catalogError = usePages((s) => s.error);
  const routes = useRouteChoices();

  const isTpl = isTemplatePath(path);
  const info: TemplateInfo | undefined = isTpl ? byName[templateName(path)] : undefined;
  const asset = assets.find((a) => a.path === path);

  const [remote, setRemote] = useState<AssetContent | null>(null);
  const [text, setText] = useState("");
  const [loadError, setLoadError] = useState<string | null>(null);
  const [saveState, setSaveState] = useState<SaveState>("idle");
  const [saveDiags, setSaveDiags] = useState<Diagnostic[]>([]);
  const [saveMessage, setSaveMessage] = useState<string | null>(null);
  const [dialog, setDialog] = useState<null | "revert" | "delete" | "rename" | "create-missing">(null);
  const [missingName, setMissingName] = useState<string | undefined>();
  const [insert, setInsert] = useState<InsertKind | null>(null);
  const [tab, setTab] = useState<SideTab>("details");
  const [showLeft, setShowLeft] = useState(() => read("studio.pages.left", true));
  const [showRight, setShowRight] = useState(() => read("studio.pages.right", true));
  const [sample, setSample] = useState<PreviewData | null>(null);
  const editor = useRef<CodeEditorHandle>(null);
  const latest = useRef({ text: "", remote: null as AssetContent | null });
  latest.current = { text, remote };
  const blocked = useRef<string | null>(null); // text the server refused; don't resend it
  const ownVersion = useRef<number>(-1);

  const editable = canEditRole && remote?.source === "draft";
  const dirty = !!remote && text !== remote.content;
  const version = draft?.version ?? -1;

  // ---- load the file ------------------------------------------------------------
  const load = useCallback(async () => {
    if (!path || !draft) return;
    try {
      const c = await pages.getState().read(path);
      setRemote(c);
      setText(c.content);
      setLoadError(null);
      setSaveState("idle");
      setSaveDiags([]);
      blocked.current = null;
    } catch (e) {
      setLoadError(e instanceof Error ? e.message : "Could not open that file.");
      setRemote(null);
    }
  }, [pages, path, draft?.id]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => { setRemote(null); setText(""); setSample(null); void load(); }, [load]);

  // Arriving from another screen with ?line=N (for example from a journey): show that line once the editor is up.
  const wantLine = Number(params.get("line") ?? 0) || 0;
  const revealed = useRef("");
  useEffect(() => {
    if (!wantLine || !remote) return;
    const key = `${path}:${wantLine}`;
    if (revealed.current === key) return;
    let tries = 0;
    const t = setInterval(() => {
      tries++;
      if (editor.current) { editor.current.reveal(wantLine); revealed.current = key; clearInterval(t); }
      else if (tries > 30) clearInterval(t);
    }, 100);
    return () => clearInterval(t);
  }, [wantLine, path, remote]);

  // A change we did not make (undo, redo, another editor): show the new content unless there are unsaved edits.
  useEffect(() => {
    if (!remote || version === ownVersion.current || version === remote.version) return;
    if (latest.current.text !== latest.current.remote?.content) return;
    let live = true;
    void pages.getState().read(path).then((c) => {
      if (!live) return;
      if (c.content !== latest.current.remote?.content || c.source !== latest.current.remote?.source) {
        setRemote(c);
        setText(c.content);
        setSaveState("idle");
        setSaveDiags([]);
      } else if (c.version !== latest.current.remote?.version) {
        setRemote(c);
      }
    }).catch(() => {});
    return () => { live = false; };
  }, [version]); // eslint-disable-line react-hooks/exhaustive-deps

  // ---- sample data ------------------------------------------------------------------
  useEffect(() => {
    if (!draft || !info) return;
    let live = true;
    void studio.getState().api.templateData(draft.id, info.name).then((d) => live && setSample(d)).catch(() => live && setSample(null));
    return () => { live = false; };
  }, [studio, draft?.id, info?.name, info?.vars.join(",")]); // eslint-disable-line react-hooks/exhaustive-deps

  // ---- saving ---------------------------------------------------------------------------
  const doSave = useCallback(async (force = false) => {
    const { text: body, remote: base } = latest.current;
    if (!base || body === base.content) return;
    setSaveState("saving");
    setSaveMessage(null);
    const out = await pages.getState().save(path, body, { force });
    if (out.ok) {
      ownVersion.current = out.version;
      blocked.current = null;
      setRemote((r) => (r ? { ...r, content: body, version: out.version } : r));
      setSaveDiags(out.diagnostics);
      setSaveState(latest.current.text === body ? "saved" : "editing");
    } else {
      blocked.current = body;
      setSaveDiags(out.diagnostics);
      setSaveMessage(out.message);
      setSaveState(out.reason === "invalid" ? "invalid" : "error");
    }
  }, [pages, path]);

  useEffect(() => {
    if (!editable || !dirty || text === blocked.current) return;
    setSaveState((s) => (s === "saving" ? s : "editing"));
    const t = setTimeout(() => void doSave(), AUTOSAVE_MS);
    return () => clearTimeout(t);
  }, [text, editable, dirty, doSave]);

  useEffect(() => {
    if (saveState !== "saved") return;
    const t = setTimeout(() => setSaveState("idle"), 2500);
    return () => clearTimeout(t);
  }, [saveState]);

  // ---- actions ----------------------------------------------------------------------------
  const status: Status | null = isTpl ? (info ? statusOf(info) : null) : remote ? (remote.source === "disk" ? "original" : asset ? assetStatus(asset) : "new") : null;

  const customize = async () => {
    if (await pages.getState().customize([path])) await load();
  };
  const revert = async () => {
    setDialog(null);
    if (await pages.getState().remove(path)) {
      if (status === "new") navigate("/pages");
      else await load();
    }
  };
  const rename = async (to: string) => {
    if (await pages.getState().rename(path, to)) {
      navigate(editorUrl(to), { replace: true });
      return true;
    }
    return false;
  };

  const shownDiags: Diagnostic[] = useMemo(() => {
    if (saveState === "invalid" || saveState === "error") return saveDiags;
    const fromCatalog = (info?.diagnostics ?? []).map((d) => ({ ...d, file: path }) as Diagnostic);
    return dirty ? [] : fromCatalog;
  }, [saveState, saveDiags, info?.diagnostics, dirty, path]);

  const cycles = useMemo(() => diagnostics.filter((d) => d.code === "studio.pages.include_cycle" && info && d.message.includes(`"${info.name}"`)), [diagnostics, info]);
  const unprovided = useMemo(() => (info ? unprovidedVars(diagnostics, info.name) : new Set<string>()), [diagnostics, info]);
  const variables = useMemo(() => [...new Set([...(info?.vars ?? []), ...(catalog?.globals ?? []), ...Object.keys(sample?.data ?? {})])].sort(), [info, catalog, sample]);
  const includable = useMemo(() => (catalog?.templates ?? []).filter((t) => t.name !== info?.name), [catalog, info]);
  const ownUrl = info?.routes.find((r) => r.as === "template" && r.method === "GET" && !/[{:]/.test(r.url))?.url;

  // ---- guards -------------------------------------------------------------------------------
  if (!canEditRole) return <div className="view"><EmptyState icon="lock" title="Page designs are for editors">Ask an editor if you want to see or change how a screen looks.</EmptyState></div>;
  if (!draft) return <div className="view"><NoWorkingCopy /></div>;
  if (!path) return <div className="view"><EmptyState icon="layout-template" title="Choose a design to edit"><button type="button" className="btn primary" onClick={() => navigate("/pages")}>Back to Page designs</button></EmptyState></div>;
  if (loadError) {
    return (
      <div className="view">
        <ErrorState title="That file isn’t here" onRetry={() => void load()}>{loadError}</ErrorState>
        <p><button type="button" className="btn" onClick={() => navigate("/pages")}><ChevronLeft size={15} /> Back to Page designs</button></p>
      </div>
    );
  }

  const title = isTpl ? friendlyName(templateName(path)).title : path.replace(/^static\//, "");
  const crumbKind = isTpl ? (info ? (info.kind === "page" ? "Pages" : info.kind === "layout" ? "Layouts" : "Components") : "Designs") : "Static files";
  const canRevert = canEditRole && status === "customized";
  const canDelete = canEditRole && status === "new";
  const gridCols = `${isTpl && showLeft ? "minmax(200px, 256px) " : ""}minmax(0, 1fr)${isTpl && showRight ? " minmax(280px, 360px)" : ""}`;

  return (
    <div className="view pages-editor">
      <header className="editor-head">
        <div className="editor-head-main">
          <nav className="crumbs" aria-label="Breadcrumb">
            <button type="button" className="link" onClick={() => navigate("/pages")}>Page designs</button>
            <span aria-hidden="true">/</span>
            <span>{crumbKind}</span>
          </nav>
          <div className="editor-title">
            <h1>{title}</h1>
            {isTpl && info && <KindPill kind={info.kind} />}
            {!isTpl && <StaticKindPill path={path} />}
            {status && <StatusPill status={status} />}
            <code className="editor-path">{path}</code>
          </div>
        </div>
        <div className="editor-actions">
          {info?.kind === "page" && (
            <a className="btn" href={`#${focusHref(`page:${info.name}`)}`} title="See the buttons on this page and where each one leads"><Route size={15} /> See journey</a>
          )}
          {canEditRole && remote?.source === "disk" && (
            <button type="button" className="btn primary" onClick={() => void customize()}><Wand2 size={15} /> Customize</button>
          )}
          {(canRevert || canDelete || status === "new") && canEditRole && (
            <Menu label="More actions" trigger={<>More</>} align="end">
              {() => (
                <>
                  {status === "new" && <MenuItem onClick={() => setDialog("rename")}><Pencil size={16} /> Rename</MenuItem>}
                  {canRevert && <MenuItem onClick={() => setDialog("revert")}><RotateCcw size={16} /> Revert to original</MenuItem>}
                  {(canRevert || canDelete) && <MenuSep />}
                  {canDelete && <MenuItem onClick={() => setDialog("delete")}><Trash2 size={16} /> Delete this design</MenuItem>}
                </>
              )}
            </Menu>
          )}
        </div>
      </header>

      {remote?.source === "disk" && (
        <div className="callout info" role="status">
          <Wand2 size={16} aria-hidden="true" />
          <div>
            <strong>This is the app’s own {isTpl ? "design" : "file"}.</strong> {canEditRole ? "Customize it to make changes here. Your changes are stored with this version; the original stays untouched." : "You can read it, but not change it."}
          </div>
        </div>
      )}

      {(catalogError) && <ErrorState title="Couldn’t load the page designs">{catalogError}</ErrorState>}
      {isTpl && catalog && !info && !remote && !loadError && <p className="muted">Looking for that design…</p>}

      <div className="editor-grid" style={{ gridTemplateColumns: gridCols }}>
        {isTpl && showLeft && (
          <aside className="editor-left" aria-label="Structure">
            {info ? (
              <OutlinePanel
                t={info}
                byName={byName}
                cycles={cycles}
                canEdit={canEditRole}
                onOpen={(p) => navigate(editorUrl(p))}
                onCreate={(n) => { setMissingName(n); setDialog("create-missing"); }}
              />
            ) : <div className="pane"><Skeleton lines={5} /></div>}
          </aside>
        )}

        <section className="editor-center" aria-label="Editor">
          <div className="editor-toolbar">
            {isTpl && (
              <button type="button" className={`icon-btn${showLeft ? " on" : ""}`} aria-pressed={showLeft} aria-label="Show structure" data-tip="Structure" onClick={() => { setShowLeft(!showLeft); write("studio.pages.left", !showLeft); }}>
                <PanelLeft size={16} />
              </button>
            )}
            {isTpl && <InsertMenu disabled={!editable} onPick={(k) => editable && setInsert(k)} />}
            <SaveIndicator state={saveState} message={saveMessage} onForce={() => void doSave(true)} onRetry={() => void doSave()} canEdit={editable} />
            <span className="spacer" />
            {remote && <span className="muted editor-size">{bytesLabel(new Blob([text]).size)}</span>}
            {isTpl && (
              <button type="button" className={`icon-btn${showRight ? " on" : ""}`} aria-pressed={showRight} aria-label="Show details" data-tip="Details" onClick={() => { setShowRight(!showRight); write("studio.pages.right", !showRight); }}>
                <PanelRight size={16} />
              </button>
            )}
          </div>
          <div className="editor-surface">
            {!remote ? (
              <div className="editor-loading" aria-busy="true"><Skeleton lines={12} /></div>
            ) : (
              <Suspense fallback={<div className="editor-loading" aria-busy="true"><Skeleton lines={12} /></div>}>
                <CodeEditor ref={editor} path={path} value={text} readOnly={!editable} diagnostics={shownDiags} onChange={setText} onSave={() => void doSave()} />
              </Suspense>
            )}
          </div>
          {shownDiags.length > 0 && (
            <ul className="editor-problems" aria-label="Problems in this design">
              {shownDiags.slice(0, 4).map((d, i) => (
                <li key={i}>
                  <TriangleAlert size={14} aria-hidden="true" />
                  <button type="button" className="link" onClick={() => d.line && editor.current?.reveal(d.line)}>
                    {d.line ? `Line ${d.line}: ` : ""}{d.message}
                  </button>
                </li>
              ))}
            </ul>
          )}
          {!isTpl && remote && (
            <p className="static-note muted">Files in the static folder are served as they are, so pages can load them (for example <code>/{path}</code>).</p>
          )}
        </section>

        {isTpl && showRight && (
          <aside className="editor-right" aria-label="Details">
            <div className="seg full" role="tablist" aria-label="Details">
              {(["details", "data", "preview"] as SideTab[]).map((k) => (
                <button key={k} role="tab" type="button" aria-selected={tab === k} className={tab === k ? "on" : ""} onClick={() => setTab(k)}>
                  {k === "details" ? "Details" : k === "data" ? "Sample data" : "Preview"}
                </button>
              ))}
            </div>
            <div className="editor-right-body">
              {!info ? <Skeleton lines={6} /> : tab === "details" ? (
                <>
                  <NeedsPanel t={info} globals={catalog?.globals ?? []} sample={sample} unprovided={unprovided} />
                  <UsedByPanel t={info} onOpenRoute={(r) => { void studio.getState().select(r.file, r.path); navigate("/edit"); }} />
                </>
              ) : tab === "data" ? (
                <SampleDataPanel sample={sample} disabled={!editable} onInsert={(s) => editor.current?.insert(s)} />
              ) : (
                <PreviewTab t={info} />
              )}
            </div>
          </aside>
        )}
      </div>

      {insert && (
        <InsertDialog kind={insert} templates={includable} variables={variables} routes={routes} ownUrl={ownUrl} onInsert={(s) => editor.current?.insert(s)} onClose={() => setInsert(null)} />
      )}

      {dialog === "revert" && (
        <ConfirmDialog
          title="Revert to the original?"
          confirm="Revert to original"
          onClose={() => setDialog(null)}
          onConfirm={() => void revert()}
        >
          Your changes to this file are dropped and the app’s own {isTpl ? "design" : "file"} is used again. You can undo this from the top bar.
        </ConfirmDialog>
      )}
      {dialog === "delete" && (
        <ConfirmDialog title="Delete this design?" confirm="Delete" danger onClose={() => setDialog(null)} onConfirm={() => void revert()}>
          It only exists in this version, so it’s removed from it. Pages that use it will show a warning. You can undo this from the top bar.
        </ConfirmDialog>
      )}
      {dialog === "rename" && (
        <RenameDialog path={path} taken={new Set([...(catalog?.templates ?? []).map((t) => t.path), ...assets.map((a) => a.path)])} onClose={() => setDialog(null)} onRename={rename} />
      )}
      {dialog === "create-missing" && (
        <NewDesignDialog
          existing={new Set((catalog?.templates ?? []).map((t) => t.name))}
          initialName={missingName}
          initialKind={missingName?.startsWith("layouts/") ? "layout" : missingName?.startsWith("components/") ? "component" : "page"}
          onClose={() => setDialog(null)}
          onCreate={(p, c) => pages.getState().create(p, c)}
        />
      )}
    </div>
  );
}

function SaveIndicator({ state, message, canEdit, onForce, onRetry }: { state: SaveState; message: string | null; canEdit: boolean; onForce(): void; onRetry(): void }) {
  if (!canEdit) return <span className="ed-save muted">Read only</span>;
  return (
    <span className={`ed-save ${state}`} role="status" aria-live="polite">
      {state === "saving" && <><LoaderCircle size={14} className="spin" aria-hidden="true" /> Saving…</>}
      {state === "editing" && <>Unsaved changes…</>}
      {state === "saved" && <><Check size={14} aria-hidden="true" /> All changes saved</>}
      {state === "invalid" && (
        <>
          <TriangleAlert size={14} aria-hidden="true" /> Not saved: it has a mistake
          <button type="button" className="link" onClick={onForce} title="Save it even though it has a mistake">Save anyway</button>
        </>
      )}
      {state === "error" && (
        <>
          <TriangleAlert size={14} aria-hidden="true" /> {message ?? "Couldn’t save."}
          <button type="button" className="link" onClick={onRetry}>Try again</button>
        </>
      )}
    </span>
  );
}

function ConfirmDialog({ title, confirm, danger, children, onConfirm, onClose }: { title: string; confirm: string; danger?: boolean; children: React.ReactNode; onConfirm(): void; onClose(): void }) {
  return (
    <Dialog title={title} onClose={onClose}>
      <p className="dialog-body">{children}</p>
      <div className="dialog-actions">
        <button type="button" className="btn" onClick={onClose} data-autofocus>Cancel</button>
        <button type="button" className={`btn ${danger ? "danger-solid" : "primary"}`} onClick={onConfirm}>{confirm}</button>
      </div>
    </Dialog>
  );
}

function RenameDialog({ path, taken, onRename, onClose }: { path: string; taken: Set<string>; onRename(to: string): Promise<boolean>; onClose(): void }) {
  const isTpl = isTemplatePath(path);
  const currentName = isTpl ? templateName(path) : path;
  const kind = currentName.startsWith("layouts/") ? "layout" : currentName.startsWith("components/") ? "component" : "page";
  const [text, setText] = useState(isTpl ? currentName.replace(/^(pages|layouts|components)\//, "") : path.replace(/^static\//, ""));
  const [busy, setBusy] = useState(false);
  const target = isTpl ? (() => { const n = newTemplateName(kind, text); return n ? templatePath(n) : null; })() : newStaticPath(text);
  const clash = !!target && target !== path && taken.has(target);
  return (
    <Dialog title="Rename" subtitle="Pages that use this design keep pointing at the old name until you change them." onClose={onClose}>
      <form
        className="form-stack"
        onSubmit={async (e) => {
          e.preventDefault();
          if (!target || clash || busy || target === path) return;
          setBusy(true);
          const ok = await onRename(target);
          setBusy(false);
          if (ok) onClose();
        }}
      >
        <label className="fld"><span className="fld-label">New name</span><input type="text" value={text} onChange={(e) => setText(e.target.value)} autoFocus data-autofocus spellCheck={false} /></label>
        <p className={`hint${!target || clash ? " bad" : ""}`}>{!target ? "Use letters, numbers and dashes." : clash ? "That name is taken." : <>Saved as <code>{target}</code></>}</p>
        <div className="dialog-actions">
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button type="submit" className="btn primary" disabled={!target || clash || busy || target === path}>Rename</button>
        </div>
      </form>
    </Dialog>
  );
}

