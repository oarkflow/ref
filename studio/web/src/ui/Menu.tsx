import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { Pop, Presence } from "./motion-components";

/** Closes on outside click and Escape. */
export function useDismiss(open: boolean, close: () => void, ref: React.RefObject<HTMLElement | null>) {
  useEffect(() => {
    if (!open) return;
    const down = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) close();
    };
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") close();
    };
    document.addEventListener("mousedown", down);
    document.addEventListener("keydown", key);
    return () => {
      document.removeEventListener("mousedown", down);
      document.removeEventListener("keydown", key);
    };
  }, [open, close, ref]);
}

export interface MenuProps {
  /** The button contents. */
  trigger: ReactNode;
  label: string;
  className?: string;
  align?: "start" | "end";
  /** Open above the trigger (for menus at the bottom of the screen). */
  up?: boolean;
  children: (close: () => void) => ReactNode;
}

/** A button that opens a small floating menu. */
export function Menu({ trigger, label, className = "", align = "start", up = false, children }: MenuProps) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const id = useId();
  const close = () => setOpen(false);
  useDismiss(open, close, ref);
  return (
    <div className="menu-wrap" ref={ref}>
      <button type="button" className={`btn ${className}`} aria-haspopup="menu" aria-expanded={open} aria-controls={open ? id : undefined} aria-label={label} onClick={() => setOpen((o) => !o)}>
        {trigger}
      </button>
      <Presence mode="sync">
        {open && (
          <Pop key="m" id={id} role="menu" className={`menu menu-${align}${up ? " menu-up" : ""}`} onClick={(e) => { if ((e.target as HTMLElement).closest("[role=menuitem]")) close(); }}>
            {children(close)}
          </Pop>
        )}
      </Presence>
    </div>
  );
}

export function MenuItem({ children, onClick, danger, disabled, hint }: { children: ReactNode; onClick(): void; danger?: boolean; disabled?: boolean; hint?: ReactNode }) {
  return (
    <button type="button" role="menuitem" className={`menu-item${danger ? " danger" : ""}`} disabled={disabled} onClick={onClick}>
      <span className="menu-item-main">{children}</span>
      {hint && <span className="menu-hint">{hint}</span>}
    </button>
  );
}

export function MenuLabel({ children }: { children: ReactNode }) {
  return <div className="menu-label">{children}</div>;
}

export function MenuSep() {
  return <div className="menu-sep" role="separator" />;
}
