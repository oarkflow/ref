// Reads and writes BCL lists of inline objects, the shape of `cases`, `rules`
// and `branches` in node config:
//
//   cases [
//     { name "large" condition "input.amount > 500" intent "orders.tier_large" },
//     { name "small" condition "input.amount <= 500" intent "orders.tier_small" }
//   ]
//
// Values are kept as raw BCL text, so strings, numbers, booleans, env() calls
// and expressions all round-trip untouched. Anything that cannot be read
// exactly (comments inside the list, nested objects with odd syntax) returns
// null and the caller falls back to a raw text editor: no content is guessed.
import { emitList, parseList, quote } from "../lib/bcl";

export interface Pair {
  key: string;
  /** raw BCL text of the value */
  raw: string;
}

export interface ObjItem {
  pairs: Pair[];
}

const KEY = /[A-Za-z_][A-Za-z0-9_-]*/y;

function readValue(s: string, i: number): [string, number] | null {
  const c = s[i];
  if (c === '"' || c === "`") {
    let j = i + 1;
    while (j < s.length) {
      if (c === '"' && s[j] === "\\") j += 2;
      else if (s[j] === c) return [s.slice(i, j + 1), j + 1];
      else j++;
    }
    return null;
  }
  if (c === "[" || c === "{" || c === "(") {
    const close = c === "[" ? "]" : c === "{" ? "}" : ")";
    let depth = 0;
    let q: string | null = null;
    for (let j = i; j < s.length; j++) {
      const ch = s[j]!;
      if (q) {
        if (ch === "\\" && q === '"') j++;
        else if (ch === q) q = null;
        continue;
      }
      if (ch === '"' || ch === "`") q = ch;
      else if (ch === "[" || ch === "{" || ch === "(") depth++;
      else if (ch === "]" || ch === "}" || ch === ")") {
        depth--;
        if (depth === 0) return ch === close ? [s.slice(i, j + 1), j + 1] : null;
      }
    }
    return null;
  }
  // bare token: number, boolean, identifier, env("X") call (has its own parens)
  let j = i;
  while (j < s.length && !/[\s,}]/.test(s[j]!)) {
    if (s[j] === "(") {
      const r = readValue(s, j);
      if (!r) return null;
      j = r[1];
    } else j++;
  }
  return j > i ? [s.slice(i, j), j] : null;
}

function parseItem(raw: string): ObjItem | null {
  const t = raw.trim();
  if (!t.startsWith("{") || !t.endsWith("}")) return null;
  const s = t.slice(1, -1);
  const pairs: Pair[] = [];
  let i = 0;
  while (i < s.length) {
    while (i < s.length && /[\s,]/.test(s[i]!)) i++;
    if (i >= s.length) break;
    KEY.lastIndex = i;
    const m = KEY.exec(s);
    if (!m) return null;
    i = KEY.lastIndex;
    while (i < s.length && /\s/.test(s[i]!)) i++;
    if (s[i] === "=" || s[i] === ":") {
      i++;
      while (i < s.length && /\s/.test(s[i]!)) i++;
    }
    const v = readValue(s, i);
    if (!v) return null;
    pairs.push({ key: m[0], raw: v[0] });
    i = v[1];
  }
  return { pairs };
}

/** null when the list is absent-safe to edit as text only. */
export function parseObjectList(raw: string): ObjItem[] | null {
  const items = parseList(raw);
  if (!items) return null;
  const out: ObjItem[] = [];
  for (const it of items) {
    const o = parseItem(it);
    if (!o) return null;
    out.push(o);
  }
  return out;
}

export function emitItem(item: ObjItem): string {
  return "{ " + item.pairs.map((p) => `${p.key} ${p.raw}`).join(" ") + " }";
}

/** One item per line, so a long list stays readable and diffs stay small. */
export function emitObjectList(items: ObjItem[]): string {
  if (items.length === 0) return "[]";
  return "[\n" + items.map((i) => emitItem(i)).join(",\n") + "\n]";
}

export const getPair = (item: ObjItem, key: string): string | undefined => item.pairs.find((p) => p.key === key)?.raw;

/** Sets (or removes, when raw is undefined) one pair, keeping the others in order. */
export function withPair(item: ObjItem, key: string, raw: string | undefined): ObjItem {
  const has = item.pairs.some((p) => p.key === key);
  if (raw === undefined) return { pairs: item.pairs.filter((p) => p.key !== key) };
  if (has) return { pairs: item.pairs.map((p) => (p.key === key ? { key, raw } : p)) };
  return { pairs: [...item.pairs, { key, raw }] };
}

export const strPair = (key: string, text: string): Pair => ({ key, raw: quote(text) });

/** Suggests a case name that is not used yet. */
export function freshName(items: ObjItem[], base: string): string {
  const used = new Set(items.map((i) => getPair(i, "name") ?? getPair(i, "label")));
  for (let n = items.length + 1; ; n++) if (!used.has(quote(`${base}${n}`))) return `${base}${n}`;
}

export { emitList };
