import { ChevronDown, Eye, OctagonAlert, Plus, Redo2, Search, Trash2, Undo2, Send, TriangleAlert } from "lucide-react";
import { useState } from "react";
import { useStudio, useStudioStore } from "../state/context";
import { hasRole } from "../state/store";
import { useUi, uiStore } from "../state/ui";
import { Menu, MenuItem, MenuLabel, MenuSep } from "../ui/Menu";
import { Kbd } from "../ui/primitives";
import { AnimatedNumber, CheckDraw, Presence } from "../ui/motion-components";
import { useNarrow } from "../ui/useNarrow";
import { Dialog } from "./Dialog";
import { SaveState } from "./SaveState";
import { ProposeDialog } from "./ProposeDialog";

export const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform ?? "");

export function TopBar(_props: { theme?: string; onTheme?(): void }) {
  const store = useStudioStore();
  const meta = useStudio((s) => s.meta);
  const draft = useStudio((s) => s.draft);
  const drafts = useStudio((s) => s.drafts);
  const undoDepth = useStudio((s) => s.undoDepth);
  const redoDepth = useStudio((s) => s.redoDepth);
  const diagnostics = useStudio((s) => s.diagnostics);
  const previewOn = useStudio((s) => s.previewOn);
  const narrow = useNarrow();
  const rightOpenPref = useUi((s) => s.rightOpen);
  const overlay = useUi((s) => s.rightOverlay);
  const rightOpen = narrow ? overlay : rightOpenPref;
  const [creating, setCreating] = useState(false);
  const [proposing, setProposing] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const canEdit = hasRole(meta, "editor");

  const errors = diagnostics.filter((d) => d.severity === "error").length;
  const problems = diagnostics.length;
  const canUndo = !!draft && (undoDepth > 0 || !!draft.dirty);
  const canRedo = !!draft && redoDepth > 0;
  const previewAvailable = !!meta?.features.preview;
  const submitBlocked = !draft ? "Open a working copy first" : errors > 0 ? "Fix the problems first" : !draft.dirty ? "No changes to send yet" : "";

  return (
    <header className="topbar">
      {canEdit && (
        <Menu label="Switch working copy" className="wc-btn" trigger={<><span className="wc-name">{draft?.name ?? "No working copy"}</span>{draft?.dirty && <span className="dirty-dot" title="Has unsent changes" />}<ChevronDown size={16} /></>}>
          {() => (
            <>
              <MenuLabel>Your working copies</MenuLabel>
              {drafts.length === 0 && <div className="menu-empty">None yet</div>}
              {drafts.map((d) => (
                <MenuItem key={d.id} onClick={() => void store.getState().openDraft(d.id)} hint={d.id === draft?.id ? <CheckDraw /> : undefined}>
                  {d.name}{d.dirty ? " •" : ""}
                </MenuItem>
              ))}
              <MenuSep />
              <MenuItem onClick={() => setCreating(true)}><Plus size={16} /> New working copy…</MenuItem>
              {draft && <MenuItem danger onClick={() => setConfirmDelete(true)}><Trash2 size={16} /> Discard this working copy…</MenuItem>}
            </>
          )}
        </Menu>
      )}

      <button type="button" className="search-btn" onClick={() => uiStore.getState().setPalette(true)} aria-label="Search or jump to anything">
        <Search size={16} />
        <span>Search or jump to…</span>
        <Kbd>{isMac ? "⌘" : "Ctrl"} K</Kbd>
      </button>

      <div className="topbar-right">
        {canEdit && draft && (
          <>
            <SaveState />
            <div className="btn-group" role="group" aria-label="History">
              <button type="button" className="icon-btn" data-tip="Undo" onClick={() => void store.getState().undo()} disabled={!canUndo} aria-label="Undo"><Undo2 size={17} /></button>
              <button type="button" className="icon-btn" data-tip="Redo" onClick={() => void store.getState().redo()} disabled={!canRedo} aria-label="Redo"><Redo2 size={17} /></button>
            </div>
            <label className={`toggle-btn${previewOn ? " on" : ""}`} data-tip={previewAvailable ? "See your changes live" : "Live preview isn’t available on this server yet"}>
              <input type="checkbox" role="switch" checked={previewOn} disabled={!previewAvailable} onChange={(e) => store.getState().setPreview(e.target.checked)} aria-label="Live preview" />
              <Eye size={16} /> <span>Preview</span>
              <span className="switch-track" aria-hidden="true"><span className="switch-thumb" /></span>
            </label>
          </>
        )}
        {draft && (
          <button
            type="button"
            className={`btn ghost problems-toggle${problems ? (errors ? " has-errors" : " has-warnings") : ""}`}
            data-tip={rightOpen ? "Hide side panel" : "Show problems, help and changes"}
            aria-label={`Side panel. ${problems} problems`}
            aria-pressed={rightOpen}
            onClick={() => (narrow ? uiStore.getState().setRightOverlay(!overlay) : uiStore.getState().toggleRight())}
          >
            {errors ? <OctagonAlert size={16} /> : <TriangleAlert size={16} />}
            {problems > 0 ? <span className="problems-count"><AnimatedNumber value={problems} /></span> : <span className="problems-count ok">0</span>}
          </button>
        )}
        {canEdit && draft && (
          <button type="button" className="btn primary" onClick={() => setProposing(true)} disabled={!!submitBlocked} data-tip={submitBlocked || "Send these changes to be reviewed"}>
            <Send size={15} /> <span className="btn-label">Submit for review</span>
          </button>
        )}
      </div>

      <Presence mode="sync">
        {creating && <NewDraftDialog key="new" onClose={() => setCreating(false)} />}
        {proposing && <ProposeDialog key="propose" onClose={() => setProposing(false)} />}
        {confirmDelete && draft && (
          <Dialog key="del" title="Discard this working copy?" onClose={() => setConfirmDelete(false)}>
            <p className="dialog-body">“{draft.name}” and every change in it will be deleted. Versions that were already submitted stay as they are.</p>
            <div className="dialog-actions">
              <button type="button" className="btn" onClick={() => setConfirmDelete(false)}>Keep it</button>
              <button type="button" className="btn danger-solid" onClick={() => { setConfirmDelete(false); void store.getState().deleteDraft(draft.id); }}>Discard</button>
            </div>
          </Dialog>
        )}
      </Presence>
    </header>
  );
}

function NewDraftDialog({ onClose }: { onClose(): void }) {
  const store = useStudioStore();
  const devView = useUi((s) => s.devView);
  const [from, setFrom] = useState("active");
  const [name, setName] = useState("");
  return (
    <Dialog title="New working copy" subtitle="A private space to change things. Nothing goes live until it is reviewed." onClose={onClose}>
      <form className="form-stack" onSubmit={(e) => { e.preventDefault(); void store.getState().createDraft(from, name.trim() || undefined); onClose(); }}>
        <label className="fld">
          <span className="fld-label">Start from</span>
          <select value={from} onChange={(e) => setFrom(e.target.value)}>
            <option value="active">The version that is live now</option>
            {devView && <option value="dir">The config directory on the server</option>}
          </select>
        </label>
        <label className="fld">
          <span className="fld-label">Name (optional)</span>
          <input value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. Add a reports page" />
        </label>
        <div className="dialog-actions">
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button type="submit" className="btn primary">Create working copy</button>
        </div>
      </form>
    </Dialog>
  );
}

