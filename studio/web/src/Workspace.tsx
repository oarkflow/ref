import { useEffect } from "react";
import { useStudioStore } from "./state/context";
import { uiStore } from "./state/ui";

export type ShortcutAction = "undo" | "redo" | "validate" | "palette";

/** Maps a key event to a studio action. Text inputs keep their own undo. */
export function shortcutFor(e: Pick<KeyboardEvent, "key" | "ctrlKey" | "metaKey" | "shiftKey" | "target">): ShortcutAction | null {
  const mod = e.ctrlKey || e.metaKey;
  if (!mod) return null;
  const key = e.key.toLowerCase();
  if (key === "s") return "validate"; // always ours: never let the browser save the page
  if (key === "k") return "palette"; // works from inside inputs too
  const t = e.target as HTMLElement | null;
  const editable = !!t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.tagName === "SELECT" || t.isContentEditable);
  if (editable) return null;
  if (key === "z") return e.shiftKey ? "redo" : "undo";
  if (key === "y") return "redo";
  return null;
}

/** Global keyboard shortcuts for the whole app. */
export function useShortcuts() {
  const store = useStudioStore();
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const a = shortcutFor(e);
      if (!a) return;
      e.preventDefault();
      const s = store.getState();
      if (a === "undo") void s.undo();
      else if (a === "redo") void s.redo();
      else if (a === "palette") uiStore.getState().setPalette(!uiStore.getState().paletteOpen);
      else void s.validate();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [store]);
}
