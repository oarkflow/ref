import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import type { DraftDiff } from "../api/types";
import { useStudio, useStudioStore } from "../state/context";
import { Skeleton } from "../ui/motion-components";
import { ChangeList } from "./ChangeList";
import { Dialog } from "./Dialog";

export function ProposeDialog({ onClose }: { onClose(): void }) {
  const store = useStudioStore();
  const navigate = useNavigate();
  const draft = useStudio((s) => s.draft);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [diff, setDiff] = useState<DraftDiff | null>(null);

  useEffect(() => {
    if (!draft) return;
    let live = true;
    void store.getState().flush().then(() => store.getState().api.diff(draft.id)).then((d) => live && setDiff(d)).catch(() => live && setDiff({ files: [], changes: [] }));
    return () => { live = false; };
  }, [store, draft?.id]);

  return (
    <Dialog title="Submit for review" subtitle="Someone else has to approve this before it goes live." onClose={onClose} wide>
      <form
        className="form-stack"
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          const rev = await store.getState().propose(message.trim());
          setBusy(false);
          if (rev) {
            onClose();
            navigate(`/revisions/${rev.id}`);
          }
        }}
      >
        <div className="propose-summary">
          <h3>What will change</h3>
          {diff ? <ChangeList changes={diff.changes} /> : <Skeleton lines={3} />}
        </div>
        <label className="fld">
          <span className="fld-label">Describe the change</span>
          <textarea value={message} onChange={(e) => setMessage(e.target.value)} rows={3} required placeholder="e.g. Adds a reports page for managers" />
          <span className="fld-help">This is what your reviewer will read first.</span>
        </label>
        <div className="progress" hidden={!busy} aria-hidden="true" />
        <div className="dialog-actions">
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button type="submit" className="btn primary" disabled={busy || !message.trim()}>{busy ? "Sending…" : "Submit for review"}</button>
        </div>
      </form>
    </Dialog>
  );
}
