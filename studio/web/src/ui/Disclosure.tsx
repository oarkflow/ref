import { ChevronRight } from "lucide-react";
import { useEffect, useId, useState, type ReactNode } from "react";
import { Collapse } from "./motion-components";

/**
 * A titled section that expands with a height animation. `forceOpen` opens it
 * without the user's click (a problem points inside it); the user's own
 * toggling wins afterwards.
 */
export function Disclosure({
  title, meta, actions, defaultOpen = true, forceOpen = false, children, className = "", icon, dataPath,
}: {
  title: ReactNode; meta?: ReactNode; actions?: ReactNode; defaultOpen?: boolean; forceOpen?: boolean; children: ReactNode; className?: string; icon?: ReactNode; dataPath?: string;
}) {
  const [manual, setManual] = useState<boolean | null>(null);
  const shown = manual ?? (defaultOpen || forceOpen);
  const id = useId();
  // A problem or a newly added setting opens the section once; the user can close it again.
  useEffect(() => { if (forceOpen) setManual(true); }, [forceOpen]);
  return (
    <section className={`disclosure${shown ? " open" : ""} ${className}`} data-path={dataPath}>
      <header className="disclosure-head">
        <button type="button" className="disclosure-toggle" aria-expanded={shown} aria-controls={id} onClick={() => setManual(!shown)}>
          <ChevronRight size={16} className="chev" />
          {icon && <span className="disclosure-icon">{icon}</span>}
          <span className="disclosure-title">{title}</span>
          {meta && <span className="disclosure-meta">{meta}</span>}
        </button>
        {actions && <div className="disclosure-actions">{actions}</div>}
      </header>
      <div id={id}>
        <Collapse open={shown}>
          <div className="disclosure-body">{children}</div>
        </Collapse>
      </div>
    </section>
  );
}
