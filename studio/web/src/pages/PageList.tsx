// "Page designs": every template (pages, layouts, components) and the static
// files, with where each is used and whether it has been changed.
import { ChevronRight, Plus, Search, TriangleAlert, Upload } from "lucide-react";
import { useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import type { AssetInfo, TemplateInfo, TemplateKind } from "../api/pageTypes";
import { NoWorkingCopy } from "../components/NoWorkingCopy";
import { count } from "../labels";
import { useStudio, useStudioStore } from "../state/context";
import { hasRole } from "../state/store";
import { useUi } from "../state/ui";
import { Skeleton } from "../ui/motion-components";
import { Badge, EmptyState, ErrorState, PageHeader } from "../ui/primitives";
import {
  KIND_LABEL, MAX_UPLOAD_BYTES, assetStatus, bytesLabel, editorUrl, friendlyName, statusOf, templatePath,
} from "./lib";
import { KindPill, NewDesignDialog, StaticFileDialog, StaticKindPill, StatusPill, templateWarnings } from "./parts";
import { usePages, usePagesStore, usePagesSync } from "./store";
import "./pages.css";

type Tab = "designs" | "static";
/** Pages first: they are what people open; layouts and components support them. */
const KIND_ORDER: Record<TemplateKind, number> = { page: 0, layout: 1, component: 2 };
type KindFilter = "all" | TemplateKind;
const FILTERS: { id: KindFilter; label: string }[] = [
  { id: "all", label: "All" },
  { id: "page", label: "Pages" },
  { id: "layout", label: "Layouts" },
  { id: "component", label: "Components" },
];

export function PageList() {
  usePagesSync();
  const studio = useStudioStore();
  const navigate = useNavigate();
  const draft = useStudio((s) => s.draft);
  const meta = useStudio((s) => s.meta);
  const dev = useUi((s) => s.devView);
  const catalog = usePages((s) => s.catalog);
  const assets = usePages((s) => s.assets);
  const loading = usePages((s) => s.loading);
  const error = usePages((s) => s.error);
  const pages = usePagesStore();
  const canEdit = hasRole(meta, "editor");

  const [tab, setTab] = useState<Tab>("designs");
  const [kind, setKind] = useState<KindFilter>("all");
  const [q, setQ] = useState("");
  const [dialog, setDialog] = useState<null | { type: "design"; name?: string } | { type: "static"; mode: "new" | "customize" }>(null);
  const upload = useRef<HTMLInputElement>(null);

  const templates = catalog?.templates ?? [];
  const needle = q.trim().toLowerCase();
  const shown = useMemo(
    () =>
      [...templates]
        .sort((a, b) => KIND_ORDER[a.kind] - KIND_ORDER[b.kind] || a.name.localeCompare(b.name))
        .filter((t) => kind === "all" || t.kind === kind)
        .filter((t) => !needle || `${t.name} ${friendlyName(t.name).title} ${t.routes.map((r) => `${r.method} ${r.url} ${r.route}`).join(" ")}`.toLowerCase().includes(needle)),
    [templates, kind, needle],
  );
  const statics = useMemo(
    () => assets.filter((a) => (a.kind === "static" || a.kind === "other") && a.status !== "removed").filter((a) => !needle || a.path.toLowerCase().includes(needle)),
    [assets, needle],
  );

  if (!canEdit) return <div className="view"><EmptyState icon="lock" title="Page designs are for editors">Ask an editor if you want to see or change how a screen looks.</EmptyState></div>;
  if (!draft) return <div className="view"><NoWorkingCopy /></div>;

  const openRoute = (file: string, path: string) => {
    void studio.getState().select(file, path);
    navigate("/edit");
  };
  const existing = new Set(templates.map((t) => t.name));
  const missing = catalog?.missing ?? [];
  const first = !catalog && loading;

  const onUpload = async (file: File | undefined) => {
    if (!file) return;
    if (file.size > MAX_UPLOAD_BYTES) {
      studio.getState().notify("error", `That file is ${bytesLabel(file.size)}. Upload text files up to ${bytesLabel(MAX_UPLOAD_BYTES)}.`);
      return;
    }
    const text = await file.text();
    const path = `static/${file.name.replace(/[^\w.-]+/g, "-")}`;
    if (await pages.getState().create(path, text)) navigate(editorUrl(path));
  };

  return (
    <div className="view pages-view">
      <PageHeader
        icon="layout-template"
        title="Page designs"
        subtitle="How every screen looks: full pages, the layouts around them and the reusable pieces."
        actions={canEdit && (
          <>
            {tab === "static" && (
              <>
                <input ref={upload} type="file" hidden accept=".css,.js,.json,.svg,.txt,.html" onChange={(e) => { void onUpload(e.target.files?.[0]); e.target.value = ""; }} />
                <button type="button" className="btn" onClick={() => upload.current?.click()}><Upload size={15} /> Upload a text file</button>
                <button type="button" className="btn" onClick={() => setDialog({ type: "static", mode: "customize" })}>Customize an existing file</button>
              </>
            )}
            <button type="button" className="btn primary" onClick={() => setDialog(tab === "designs" ? { type: "design" } : { type: "static", mode: "new" })}>
              <Plus size={16} /> {tab === "designs" ? "New design" : "New file"}
            </button>
          </>
        )}
      />

      <div className="list-toolbar pages-toolbar">
        <div className="seg" role="tablist" aria-label="What to show">
          <button role="tab" type="button" aria-selected={tab === "designs"} className={tab === "designs" ? "on" : ""} onClick={() => setTab("designs")}>
            Pages &amp; layouts <span className="seg-count">{templates.length}</span>
          </button>
          <button role="tab" type="button" aria-selected={tab === "static"} className={tab === "static" ? "on" : ""} onClick={() => setTab("static")}>
            Static files <span className="seg-count">{assets.filter((a) => a.kind === "static" || a.kind === "other").length}</span>
          </button>
        </div>
        <label className="search-field"><Search size={15} /><input type="search" aria-label="Search page designs" placeholder={tab === "designs" ? "Search designs or routes…" : "Search files…"} value={q} onChange={(e) => setQ(e.target.value)} /></label>
        {tab === "designs" && (
          <div className="seg" role="group" aria-label="Filter by type">
            {FILTERS.map((f) => (
              <button key={f.id} type="button" aria-pressed={kind === f.id} className={kind === f.id ? "on" : ""} onClick={() => setKind(f.id)}>{f.label}</button>
            ))}
          </div>
        )}
        <span className="muted list-count">{tab === "designs" ? count(shown.length, "design") : count(statics.length, "file")}</span>
      </div>

      {error && <ErrorState title="Couldn’t load the page designs" onRetry={() => void pages.getState().load()}>{error}</ErrorState>}

      {tab === "designs" && missing.length > 0 && (
        <div className="callout warn" role="status">
          <TriangleAlert size={16} aria-hidden="true" />
          <div>
            <strong>{count(missing.length, "page")} point{missing.length === 1 ? "s" : ""} at a design that doesn’t exist.</strong>
            <ul>
              {missing.slice(0, 4).map((m, i) => (
                <li key={i}>
                  <code>{m.route}</code> wants <code>{m.template}</code>
                  {canEdit && <> · <button type="button" className="link" onClick={() => setDialog({ type: "design", name: m.template })}>Create it</button></>}
                </li>
              ))}
            </ul>
          </div>
        </div>
      )}

      {first ? (
        <div className="table-skel"><Skeleton lines={7} /></div>
      ) : tab === "designs" ? (
        shown.length === 0 ? (
          <EmptyState icon="layout-template" title={needle || kind !== "all" ? "Nothing matches" : "No page designs yet"} action={canEdit && !needle && kind === "all" ? <button type="button" className="btn primary" onClick={() => setDialog({ type: "design" })}><Plus size={16} /> New design</button> : undefined}>
            {needle || kind !== "all" ? "Try different words or another type." : "Designs are the screens people see. Create one to get started."}
          </EmptyState>
        ) : (
          <div className="table-wrap">
            <table className="table designs-table">
              <thead>
                <tr><th>Design</th><th>Type</th><th>Status</th><th>Used by</th><th className="num">Problems</th><th aria-label="Open" /></tr>
              </thead>
              <tbody>
                {shown.map((t) => (
                  <tr key={t.name} className="row-link" onClick={() => navigate(editorUrl(t.path))}>
                    <DesignRow t={t} dev={dev} onOpenRoute={openRoute} />
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )
      ) : statics.length === 0 ? (
        <EmptyState icon="file-code" title={needle ? "Nothing matches" : "No changed static files"} action={canEdit && !needle ? <button type="button" className="btn primary" onClick={() => setDialog({ type: "static", mode: "new" })}><Plus size={16} /> New file</button> : undefined}>
          {needle ? "Try different words." : "The app’s own styles and scripts stay as they are. Files you add or customize in this version show up here."}
        </EmptyState>
      ) : (
        <div className="table-wrap">
          <table className="table designs-table">
            <thead><tr><th>File</th><th>Type</th><th>Status</th><th className="num">Size</th><th aria-label="Open" /></tr></thead>
            <tbody>
              {statics.map((a) => <StaticRow key={a.path} a={a} onOpen={() => navigate(editorUrl(a.path))} />)}
            </tbody>
          </table>
        </div>
      )}

      {dialog?.type === "design" && (
        <NewDesignDialog
          existing={existing}
          initialName={dialog.name}
          onClose={() => setDialog(null)}
          onCreate={async (path, content) => {
            const ok = await pages.getState().create(path, content);
            if (ok) navigate(editorUrl(path));
            return ok;
          }}
        />
      )}
      {dialog?.type === "static" && (
        <StaticFileDialog
          mode={dialog.mode}
          existing={new Set(assets.map((a) => a.path))}
          onClose={() => setDialog(null)}
          onSubmit={async (path) => {
            const ok = dialog.mode === "new" ? await pages.getState().create(path, "") : await pages.getState().customize([path]);
            if (ok) navigate(editorUrl(path));
            return ok;
          }}
        />
      )}
    </div>
  );
}

function DesignRow({ t, dev, onOpenRoute }: { t: TemplateInfo; dev: boolean; onOpenRoute(file: string, path: string): void }) {
  const { title } = friendlyName(t.name);
  const problems = templateWarnings(t);
  const routes = t.routes.filter((r) => r.as === "template");
  const asLayout = t.routes.filter((r) => r.as === "layout");
  return (
    <>
      <td>
        <div className="cell-title">
          <strong>{title}</strong>
          <span className="cell-sub" title={KIND_LABEL[t.kind]}>{dev ? templatePath(t.name) : t.name}</span>
        </div>
      </td>
      <td><KindPill kind={t.kind} /></td>
      <td><StatusPill status={statusOf(t)} /></td>
      <td onClick={(e) => e.stopPropagation()}>
        <div className="route-chips">
          {routes.slice(0, 3).map((r) => (
            <button key={r.route + r.as} type="button" className="route-chip" title={`Open ${r.route}`} onClick={() => onOpenRoute(r.file, r.path)}>
              <b>{r.method}</b> {r.url}
            </button>
          ))}
          {routes.length > 3 && <span className="muted">+{routes.length - 3} more</span>}
          {routes.length === 0 && asLayout.length > 0 && <span className="muted" title={asLayout.map((r) => r.route).join(", ")}>Layout for {count(asLayout.length, "page")}</span>}
          {routes.length === 0 && asLayout.length === 0 && (
            t.unused
              ? <Badge tone="muted" title="No page uses this, and no design includes it. The app’s own code may still draw it (for example error pages).">Not used</Badge>
              : <span className="muted">Included by other designs</span>
          )}
        </div>
      </td>
      <td className="num">{problems > 0 ? <Badge tone="warn" className="dot">{problems}</Badge> : <span className="muted">—</span>}</td>
      <td className="go"><ChevronRight size={16} aria-hidden="true" /></td>
    </>
  );
}

function StaticRow({ a, onOpen }: { a: AssetInfo; onOpen(): void }) {
  return (
    <tr className="row-link" onClick={onOpen}>
      <td><div className="cell-title"><strong>{a.path.replace(/^static\//, "")}</strong><span className="cell-sub">static</span></div></td>
      <td><StaticKindPill path={a.path} /></td>
      <td><StatusPill status={assetStatus(a)} /></td>
      <td className="num">{bytesLabel(a.size)}</td>
      <td className="go"><ChevronRight size={16} aria-hidden="true" /></td>
    </tr>
  );
}

