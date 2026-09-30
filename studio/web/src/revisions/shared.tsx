import type { RevisionStatus } from "../api/types";
import { count, statusInfo } from "../labels";
import { Badge } from "../ui/primitives";

export function StatusPill({ status }: { status: RevisionStatus }) {
  const s = statusInfo(status);
  return <Badge tone={s.tone} title={s.help} className="dot">{s.label}</Badge>;
}

export function fmtTime(t?: string | null): string {
  if (!t) return "";
  const d = new Date(t);
  return Number.isNaN(d.getTime()) ? t : d.toLocaleString();
}

/** "3 minutes ago" for recent times, a date otherwise. */
export function ago(t?: string | null, now = Date.now()): string {
  if (!t) return "";
  const d = new Date(t).getTime();
  if (Number.isNaN(d)) return t;
  const s = Math.round((now - d) / 1000);
  if (s < 45) return "just now";
  if (s < 3600) return count(Math.round(s / 60), "minute") + " ago";
  if (s < 86400) return count(Math.round(s / 3600), "hour") + " ago";
  if (s < 86400 * 7) return count(Math.round(s / 86400), "day") + " ago";
  return new Date(d).toLocaleDateString();
}

