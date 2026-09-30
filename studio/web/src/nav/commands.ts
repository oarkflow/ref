// Pure search over the command palette's entries (unit-tested).
import type { IconName } from "../labels";

export interface Command {
  id: string;
  title: string;
  subtitle?: string;
  group: string;
  icon: IconName | "action" | "history" | "audit" | "dash" | "files";
  /** Extra words that should also match (e.g. the raw BCL keyword). */
  keywords?: string;
  run(): void;
}

const norm = (s: string) => s.toLowerCase().normalize("NFKD").replace(/[̀-ͯ]/g, "");

/** Higher is better; 0 = no match. Every query word must match somewhere. */
export function score(c: Pick<Command, "title" | "subtitle" | "keywords" | "group">, query: string): number {
  const words = norm(query).split(/\s+/).filter(Boolean);
  if (!words.length) return 1;
  const title = norm(c.title);
  const rest = norm(`${c.subtitle ?? ""} ${c.keywords ?? ""} ${c.group}`);
  let total = 0;
  for (const w of words) {
    let s = 0;
    if (title === w) s = 100;
    else if (title.startsWith(w)) s = 80;
    else if (title.split(/[\s._\-/]+/).some((t) => t.startsWith(w))) s = 60;
    else if (title.includes(w)) s = 40;
    else if (rest.split(/[\s._\-/]+/).some((t) => t.startsWith(w))) s = 20;
    else if (rest.includes(w)) s = 10;
    if (!s) return 0;
    total += s;
  }
  return total;
}

export function searchCommands(commands: Command[], query: string, limit = 60): Command[] {
  const q = query.trim();
  if (!q) return commands.slice(0, limit);
  return commands
    .map((c, i) => ({ c, s: score(c, q), i }))
    .filter((x) => x.s > 0)
    .sort((a, b) => b.s - a.s || a.i - b.i)
    .slice(0, limit)
    .map((x) => x.c);
}

/** Groups in first-seen order. */
export function groupCommands(commands: Command[]): [string, Command[]][] {
  const m = new Map<string, Command[]>();
  for (const c of commands) m.set(c.group, [...(m.get(c.group) ?? []), c]);
  return [...m.entries()];
}
