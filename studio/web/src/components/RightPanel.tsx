import { HelpCircle, ListChecks, OctagonAlert, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import type { DraftDiff } from "../api/types";
import { DiagnosticsPanel } from "../diag/DiagnosticsPanel";
import { blockInfo, fieldHelp, fieldLabel } from "../labels";
import { findNode } from "../lib/paths";
import { useStudio, useStudioStore } from "../state/context";
import { DEFAULT_PREFS, LIMITS, uiStore, useUi, type RightTab } from "../state/ui";
import { Icon } from "../ui/Icon";
import { motionOff, SPRING } from "../ui/motion";
import { AnimatedNumber, Enter, Presence, Skeleton, motion } from "../ui/motion-components";
import { useNarrow } from "../ui/useNarrow";
import { useResize } from "../ui/useResize";
import { ChangeList } from "./ChangeList";

const TABS: { id: RightTab; label: string }[] = [
  { id: "problems", label: "Problems" },
  { id: "help", label: "Help" },
  { id: "changes", label: "Changes" },
];

export function RightPanel({ hidden = false }: { hidden?: boolean }) {
  const narrow = useNarrow();
  const overlay = useUi((s) => s.rightOverlay);
  const pref = useUi((s) => s.rightOpen);
  const open = (narrow ? overlay : pref) && !hidden;
  const width = useUi((s) => s.rightWidth);
  const tab = useUi((s) => s.rightTab);
  const diagnostics = useStudio((s) => s.diagnostics);
  const navigate = useNavigate();
  const { dragging, handleProps } = useResize(
    () => uiStore.getState().rightWidth,
    (n) => uiStore.getState().resizeRight(n),
    -1,
    () => uiStore.getState().set({ rightWidth: DEFAULT_PREFS.rightWidth }),
  );
  const off = motionOff();
  const Aside = off ? "aside" : motion.aside;
  const w = open ? width : 0;
  const anim = off ? { style: { width: w, display: open ? undefined : "none" } } : { initial: false, animate: { width: w, opacity: open ? 1 : 0 }, transition: dragging ? { duration: 0 } : SPRING.panel, style: { pointerEvents: open ? undefined : ("none" as const) } };

  return (
    <Aside className={`right-panel${open ? "" : " closed"}${narrow ? " floating" : ""}`} aria-label="Side panel" aria-hidden={!open} {...(anim as object)}>
      <div className="right-inner" style={{ width }}>
        <header className="right-head">
          <div className="tab-bar" role="tablist" aria-label="Side panel sections">
            {TABS.map((t) => (
              <button key={t.id} type="button" role="tab" aria-selected={tab === t.id} className={tab === t.id ? "on" : ""} onClick={() => uiStore.getState().set({ rightTab: t.id })}>
                {t.id === "problems" && <OctagonAlert size={15} />}
                {t.id === "help" && <HelpCircle size={15} />}
                {t.id === "changes" && <ListChecks size={15} />}
                {t.label}
                {t.id === "problems" && diagnostics.length > 0 && <span className="count-badge sm"><AnimatedNumber value={diagnostics.length} /></span>}
              </button>
            ))}
          </div>
          <button type="button" className="icon-btn" aria-label="Close side panel" data-tip="Close" onClick={() => (narrow ? uiStore.getState().setRightOverlay(false) : uiStore.getState().toggleRight(false))}><X size={16} /></button>
        </header>
        <div className="right-body" role="tabpanel">
          {tab === "problems" && <DiagnosticsPanel onOpen={() => navigate("/edit")} />}
          {tab === "help" && <HelpTab />}
          {tab === "changes" && <ChangesTab />}
        </div>
      </div>
      {open && (
        <div
          className={`resize-edge left${dragging ? " active" : ""}`}
          role="separator"
          aria-orientation="vertical"
          aria-label="Resize side panel"
          aria-valuenow={width}
          aria-valuemin={LIMITS.right.min}
          aria-valuemax={LIMITS.right.max}
          tabIndex={0}
          {...handleProps}
        />
      )}
    </Aside>
  );
}

function HelpTab() {
  const selection = useStudio((s) => s.selection);
  const trees = useStudio((s) => s.trees);
  const schemas = useStudio((s) => s.schemas);
  const dev = useUi((s) => s.devView);
  const node = useMemo(() => (selection ? findNode(trees[selection.file], selection.path) : undefined), [selection, trees]);
  if (!node || node.kind !== "block") {
    return (
      <Enter className="help-empty">
        <HelpCircle size={26} />
        <strong>Pick something to see how it works</strong>
        <span className="muted">Choose an item from the left and its explanation will appear here.</span>
      </Enter>
    );
  }
  const info = blockInfo(node.type);
  const schema = schemas?.[node.type ?? ""];
  const rows = (schema?.fields ?? []).map((f) => ({ name: f.name, help: fieldHelp(f.name) ?? (f.doc ? oneLine(f.doc) : "") })).filter((r) => r.help).slice(0, 14);
  return (
    <Enter className="help-body">
      <div className="help-hero">
        <span className="hero-icon"><Icon name={info.icon} size={22} /></span>
        <div>
          <h3>{info.label}</h3>
          <p>{info.description || (schema?.doc ? oneLine(schema.doc) : "")}</p>
        </div>
      </div>
      {rows.length > 0 && (
        <>
          <h4 className="help-sub">What the settings mean</h4>
          <dl className="help-list">
            {rows.map((r) => (
              <div key={r.name}>
                <dt>{fieldLabel(r.name)}{dev && <code className="raw-name">{r.name}</code>}</dt>
                <dd>{r.help}</dd>
              </div>
            ))}
          </dl>
        </>
      )}
    </Enter>
  );
}

const oneLine = (s: string) => {
  const flat = s.replace(/\s+/g, " ").trim();
  return flat.length > 200 ? flat.slice(0, 197) + "…" : flat;
};

function ChangesTab() {
  const store = useStudioStore();
  const draft = useStudio((s) => s.draft);
  const dev = useUi((s) => s.devView);
  const [diff, setDiff] = useState<DraftDiff | null>(null);
  useEffect(() => {
    if (!draft) return;
    let live = true;
    const t = setTimeout(() => {
      store.getState().api.diff(draft.id).then((d) => live && setDiff(d)).catch(() => live && setDiff({ files: [], changes: [] }));
    }, 300);
    return () => { live = false; clearTimeout(t); };
  }, [store, draft?.id, draft?.version]);
  if (!draft) return <p className="panel-pad muted">No working copy is open.</p>;
  if (!diff) return <div className="panel-pad"><Skeleton lines={4} /></div>;
  return (
    <div className="panel-pad">
      <h4 className="help-sub">Compared with the live version</h4>
      <ChangeList changes={diff.changes} />
      {dev && diff.files.length > 0 && (
        <>
          <h4 className="help-sub">Files</h4>
          <ul className="file-changes">
            <Presence>{diff.files.map((f) => <li key={f.path}><span className={`change-verb ${f.status}`}>{f.status}</span> <code>{f.path}</code></li>)}</Presence>
          </ul>
        </>
      )}
    </div>
  );
}
