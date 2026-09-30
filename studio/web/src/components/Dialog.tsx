import { X } from "lucide-react";
import { useEffect, useId, useRef, type ReactNode } from "react";
import { Pop, Presence, motion } from "../ui/motion-components";
import { motionOff } from "../ui/motion";

export function Dialog({ title, onClose, children, wide = false, subtitle }: { title: string; onClose(): void; children: ReactNode; wide?: boolean; subtitle?: string }) {
  const id = useId();
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const prev = document.activeElement as HTMLElement | null;
    ref.current?.querySelector<HTMLElement>("input, select, textarea, [data-autofocus], button:not(.dialog-close)")?.focus();
    return () => prev?.focus?.();
  }, []);
  const backdrop = motionOff()
    ? {}
    : { initial: { opacity: 0 }, animate: { opacity: 1 }, exit: { opacity: 0 }, transition: { duration: 0.16 } };
  return (
    <motion.div className="backdrop" onMouseDown={(e) => { if (e.target === e.currentTarget) onClose(); }} {...backdrop}>
      <Pop
        ref={ref}
        className={`dialog${wide ? " wide" : ""}`}
        role="dialog"
        aria-modal="true"
        aria-labelledby={id}
        onKeyDown={(e) => { if (e.key === "Escape") onClose(); }}
      >
        <header className="dialog-head">
          <div>
            <h2 id={id}>{title}</h2>
            {subtitle && <p className="muted">{subtitle}</p>}
          </div>
          <button type="button" className="icon-btn dialog-close" aria-label="Close" onClick={onClose}><X size={18} /></button>
        </header>
        {children}
      </Pop>
    </motion.div>
  );
}

/** Renders a dialog with an exit animation. */
export function DialogHost({ open, children }: { open: boolean; children: ReactNode }) {
  return <Presence mode="sync">{open ? children : null}</Presence>;
}
