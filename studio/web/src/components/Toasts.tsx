import { AlertTriangle, CheckCircle2, Info, X } from "lucide-react";
import { useStudio, useStudioStore } from "../state/context";
import { Pop, Presence } from "../ui/motion-components";

const ICON = { error: AlertTriangle, success: CheckCircle2, info: Info } as const;

export function Toasts() {
  const notices = useStudio((s) => s.notices);
  const store = useStudioStore();
  return (
    <div className="toasts" role="status" aria-live="polite">
      <Presence mode="popLayout">
        {notices.map((n) => {
          const I = ICON[n.level];
          return (
            <Pop key={n.id} className={`toast ${n.level}`}>
              <I size={18} className="toast-icon" aria-hidden="true" />
              <span className="toast-text">{n.text}</span>
              <button type="button" className="icon-btn" aria-label="Dismiss" onClick={() => store.getState().dismiss(n.id)}><X size={16} /></button>
            </Pop>
          );
        })}
      </Presence>
    </div>
  );
}
