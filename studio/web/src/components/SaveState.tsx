import { useEffect, useRef, useState } from "react";
import { useStudio } from "../state/context";
import { CheckDraw } from "../ui/motion-components";

export type SaveKind = "saving" | "problem" | "saved";

export function saveKind(pending: boolean, errors: number): SaveKind {
  return pending ? "saving" : errors > 0 ? "problem" : "saved";
}

/** "All changes saved" / "Saving…" / "N problems" — quiet, but always answers "is my work safe?". */
export function SaveState() {
  const overlay = useStudio((s) => s.overlay);
  const diagnostics = useStudio((s) => s.diagnostics);
  const validating = useStudio((s) => s.validating);
  const pending = Object.keys(overlay).length > 0;
  const errors = diagnostics.filter((d) => d.severity === "error").length;
  const kind = saveKind(pending, errors);
  const prev = useRef<SaveKind>(kind);
  const [pulse, setPulse] = useState(0);
  useEffect(() => {
    if (prev.current === "saving" && kind !== "saving") setPulse((n) => n + 1);
    prev.current = kind;
  }, [kind]);

  return (
    <span className={`save-state ${kind}`} role="status" aria-live="polite">
      {kind === "saving" && (<><span className="dot-pulse" aria-hidden="true" /> Saving…</>)}
      {kind === "problem" && (<>{errors === 1 ? "1 problem to fix" : `${errors} problems to fix`}</>)}
      {kind === "saved" && (
        <span key={pulse} className={pulse ? "pulse-once saved-inner" : "saved-inner"}>
          <CheckDraw /> {validating ? "Checking…" : "All changes saved"}
        </span>
      )}
    </span>
  );
}
