// Under a route's Template and Layout fields: what the value points at, a way
// to pick a real design instead of typing a name, and a link to edit it.
import { ArrowUpRight, FilePlus2, ListFilter } from "lucide-react";
import { useMemo, useState } from "react";
import { useInRouterContext, useNavigate } from "react-router-dom";
import { Dialog } from "../components/Dialog";
import { unquote } from "../lib/bcl";
import { useOptionalStudioStore, useStudio } from "../state/context";
import { KindPill, NewDesignDialog, StatusPill } from "./parts";
import { editorUrl, friendlyName, statusOf, templatePath } from "./lib";
import { usePages, usePagesStore, usePagesSync } from "./store";
import "./picker.css";

interface Props { field: "template" | "layout"; raw: string | undefined; path: string; readOnly?: boolean; onPick(raw: string): void }

/** Renders nothing where there is no app around it (the form is reused in isolation, e.g. in tests). */
export function TemplateExtras(props: Props) {
  const store = useOptionalStudioStore();
  const inRouter = useInRouterContext();
  return store && inRouter ? <Extras {...props} /> : null;
}

function Extras({ field, raw, path, readOnly, onPick }: Props) {
  usePagesSync();
  const navigate = useNavigate();
  const catalog = usePages((s) => s.catalog);
  const byName = usePages((s) => s.byName);
  const diagnostics = useStudio((s) => s.diagnostics);
  const pages = usePagesStore();
  const [open, setOpen] = useState(false);
  const [creating, setCreating] = useState(false);
  const value = raw === undefined ? "" : (unquote(raw) ?? raw.trim());
  const name = value.replace(/\.html$/, "");
  const info = name ? byName[name] : undefined;
  const wanted = field === "layout" ? "layout" : "page";
  const options = useMemo(() => (catalog?.templates ?? []).filter((t) => t.kind === wanted), [catalog, wanted]);
  // The field itself already shows the server's finding; only add our own line while validation catches up.
  const reported = diagnostics.some((d) => d.code?.startsWith("studio.pages.") && d.path === path);
  const missing = !!name && !!catalog && !info;

  return (
    <div className="tpl-extra">
      {info ? (
        <>
          <KindPill kind={info.kind} />
          <StatusPill status={statusOf(info)} />
          <button type="button" className="link" onClick={() => navigate(editorUrl(templatePath(info.name)))}>Open in Page designs <ArrowUpRight size={12} /></button>
        </>
      ) : missing && !reported ? (
        <span className="bad">“{name}” isn’t one of your page designs.</span>
      ) : null}
      {missing && !readOnly && (
        <button type="button" className="link" onClick={() => setCreating(true)}><FilePlus2 size={12} /> Create “{name}”…</button>
      )}
      {!readOnly && (
        <button type="button" className="link" onClick={() => setOpen(true)}><ListFilter size={12} /> Choose {field === "layout" ? "a layout" : "a page design"}…</button>
      )}
      {creating && (
        <NewDesignDialog
          existing={new Set((catalog?.templates ?? []).map((t) => t.name))}
          initialName={name}
          initialKind={wanted}
          onClose={() => setCreating(false)}
          onCreate={(p, c) => pages.getState().create(p, c)}
        />
      )}
      {open && (
        <Dialog title={field === "layout" ? "Choose a layout" : "Choose a page design"} subtitle={field === "layout" ? "The frame around the page." : "The design this address shows."} onClose={() => setOpen(false)}>
          <ul className="pick-list" role="listbox" aria-label="Designs">
            {options.map((t) => (
              <li key={t.name}>
                <button type="button" role="option" aria-selected={t.name === name} className={`pick-row${t.name === name ? " on" : ""}`} data-autofocus={t.name === name || undefined} onClick={() => { onPick(JSON.stringify(t.name)); setOpen(false); }}>
                  <span><strong>{friendlyName(t.name).title}</strong><small>{t.name}</small></span>
                  <KindPill kind={t.kind} />
                </button>
              </li>
            ))}
            {options.length === 0 && <li className="muted pick-empty">There are no {field === "layout" ? "layouts" : "page designs"} yet. Create one in Page designs.</li>}
          </ul>
          <div className="dialog-actions"><button type="button" className="btn" onClick={() => setOpen(false)}>Close</button></div>
        </Dialog>
      )}
    </div>
  );
}
