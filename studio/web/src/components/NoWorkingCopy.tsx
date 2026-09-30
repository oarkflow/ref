import { Plus } from "lucide-react";
import { useStudio, useStudioStore } from "../state/context";
import { EmptyState } from "../ui/primitives";

/** Shown instead of a misleading "nothing here yet" when there is no working copy to look at. */
export function NoWorkingCopy() {
  const store = useStudioStore();
  const meta = useStudio((s) => s.meta);
  const loading = useStudio((s) => s.loading);
  const from = meta?.activeRevision ? "active" : "dir";
  return (
    <EmptyState
      icon="sparkles"
      title={loading ? "Getting things ready…" : "Start a working copy"}
      action={!loading ? (
        <button type="button" className="btn primary" onClick={() => void store.getState().createDraft(from)}>
          <Plus size={16} /> {from === "active" ? "Start from the live version" : "Start from the current configuration"}
        </button>
      ) : undefined}
    >
      {loading ? "Loading your configuration." : "A working copy is your private space to change things. Nothing goes live until someone else reviews it."}
    </EmptyState>
  );
}
