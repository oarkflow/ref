import { TriangleAlert } from "lucide-react";
import type { ReactNode } from "react";
import { Icon } from "./Icon";
import type { IconName } from "../labels";
import { Enter } from "./motion-components";

export type Tone = "ok" | "warn" | "bad" | "info" | "muted" | "accent";

export function Badge({ tone = "muted", children, title, className = "" }: { tone?: Tone; children: ReactNode; title?: string; className?: string }) {
  return <span className={`badge tone-${tone} ${className}`} title={title}>{children}</span>;
}

export function EmptyState({ icon = "sparkles", title, children, action }: { icon?: IconName; title: string; children?: ReactNode; action?: ReactNode }) {
  return (
    <Enter className="empty-state">
      <span className="empty-icon"><Icon name={icon} size={26} /></span>
      <h2>{title}</h2>
      {children && <p>{children}</p>}
      {action}
    </Enter>
  );
}

export function PageHeader({ title, subtitle, icon, actions, crumbs }: { title: ReactNode; subtitle?: ReactNode; icon?: IconName; actions?: ReactNode; crumbs?: ReactNode }) {
  return (
    <header className="page-head">
      <div className="page-head-main">
        {crumbs && <nav className="crumbs" aria-label="Breadcrumb">{crumbs}</nav>}
        <div className="page-title">
          {icon && <span className="page-icon"><Icon name={icon} size={20} /></span>}
          <h1>{title}</h1>
        </div>
        {subtitle && <p className="page-sub">{subtitle}</p>}
      </div>
      {actions && <div className="page-actions">{actions}</div>}
    </header>
  );
}

/** A collapsible-free titled card. */
export function Card({ title, children, actions, className = "" }: { title?: ReactNode; children: ReactNode; actions?: ReactNode; className?: string }) {
  return (
    <section className={`card-x ${className}`}>
      {(title || actions) && (
        <header className="card-x-head">
          {title && <h3>{title}</h3>}
          {actions}
        </header>
      )}
      {children}
    </section>
  );
}

export function Kbd({ children }: { children: ReactNode }) {
  return <kbd className="kbd">{children}</kbd>;
}

/** A calm, actionable error: what happened, in words, with a way out. */
export function ErrorState({ title = "Something went wrong", children, onRetry }: { title?: string; children?: ReactNode; onRetry?: () => void }) {
  return (
    <div className="error-state" role="alert">
      <span className="error-icon"><TriangleAlert size={18} /></span>
      <div className="error-text">
        <strong>{title}</strong>
        {children && <p>{children}</p>}
      </div>
      {onRetry && <button type="button" className="btn sm" onClick={onRetry}>Try again</button>}
    </div>
  );
}
