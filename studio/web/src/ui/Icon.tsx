import {
  Box, Building2, CircleHelp, ClipboardCheck, Clock, Coins, Cog, Database, FileText, Flag, Folder, GitBranch, Globe,
  History, KeyRound, Layers, LayoutDashboard, ListChecks, Lock, Plug, Route, Settings2, Shapes, ShieldCheck, Sparkles,
  Table2, Users, Webhook, Workflow, Zap, LayoutTemplate, FileCode2, type LucideIcon,
} from "lucide-react";
import type { IconName } from "../labels";

const ICONS: Record<IconName, LucideIcon> = {
  globe: Globe, workflow: Workflow, plug: Plug, database: Database, table: Table2, shapes: Shapes, cog: Cog, clock: Clock,
  webhook: Webhook, flag: Flag, folder: Folder, shield: ShieldCheck, users: Users, key: KeyRound, "git-branch": GitBranch,
  "clipboard-check": ClipboardCheck, settings: Settings2, layers: Layers, "layout-dashboard": LayoutDashboard, history: History,
  coins: Coins, "file-text": FileText, "circle-help": CircleHelp, sparkles: Sparkles, route: Route, building: Building2,
  "list-checks": ListChecks, lock: Lock, zap: Zap, box: Box, "layout-template": LayoutTemplate, "file-code": FileCode2,
};

export function Icon({ name, size = 18, className, strokeWidth = 1.5 }: { name: IconName; size?: number; className?: string; strokeWidth?: number }) {
  const C = ICONS[name] ?? Box;
  return <C size={size} strokeWidth={strokeWidth} className={className} aria-hidden="true" focusable="false" />;
}

export { ICONS };
