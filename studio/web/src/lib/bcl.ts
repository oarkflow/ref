// Small, pure helpers for the raw BCL text the studio sends in ops. The server
// (studio/model) is the authority on syntax; these only produce values it will
// accept and read back the common ones so a form can show them.

export type ScalarKind = "string" | "ident" | "int" | "number" | "bool" | "duration" | "any";
export type Mode = "literal" | "env" | "expr";

const DURATION = /^(?:\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h))+$/;
const IDENT = /^[A-Za-z_][A-Za-z0-9_.-]*$/;
const INT = /^[+-]?\d+$/;
const NUMBER = /^[+-]?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?$/;

export const isDuration = (s: string) => DURATION.test(s);
export const isIdent = (s: string) => IDENT.test(s);

/** quote renders s as a BCL string literal (mirrors model.Quote). */
export function quote(s: string): string {
  let raw = false;
  for (const ch of s) {
    const c = ch.codePointAt(0)!;
    if ((c < 0x20 && c !== 0x0a && c !== 0x09) || c === 0x7f) {
      raw = true;
      break;
    }
  }
  if (raw) {
    if (s.includes("`")) throw new Error("string has control characters and a backtick");
    return "`" + s + "`";
  }
  let out = '"';
  for (const ch of s) {
    if (ch === '"') out += '\\"';
    else if (ch === "\\") out += "\\\\";
    else if (ch === "\n") out += "\\n";
    else if (ch === "\t") out += "\\t";
    else out += ch;
  }
  return out + '"';
}

/** unquote reads a complete string literal; null when raw is not exactly one. */
export function unquote(raw: string): string | null {
  const s = raw.trim();
  if (s.length >= 2 && s.startsWith("`") && s.endsWith("`") && !s.slice(1, -1).includes("`")) return s.slice(1, -1);
  if (s.length < 2 || !s.startsWith('"') || !s.endsWith('"')) return null;
  let out = "";
  for (let i = 1; i < s.length - 1; i++) {
    const ch = s[i]!;
    if (ch === '"') return null; // an unescaped quote ends the literal early
    if (ch !== "\\") {
      out += ch;
      continue;
    }
    const n = s[++i];
    if (n === undefined || i >= s.length - 1) return null;
    switch (n) {
      case "n": out += "\n"; break;
      case "t": out += "\t"; break;
      case "r": out += "\r"; break;
      case '"': out += '"'; break;
      case "\\": out += "\\"; break;
      default: return null; // unknown escape: leave it to the expression editor
    }
  }
  return out;
}

/** Splits the top level of `[a, b, c]` into raw items; null if raw is not a list. */
export function parseList(raw: string): string[] | null {
  const s = raw.trim();
  if (!s.startsWith("[") || !s.endsWith("]")) return null;
  const body = s.slice(1, -1);
  const items: string[] = [];
  let depth = 0;
  let cur = "";
  let q: string | null = null;
  for (let i = 0; i < body.length; i++) {
    const ch = body[i]!;
    if (q) {
      cur += ch;
      if (ch === "\\" && q === '"') cur += body[++i] ?? "";
      else if (ch === q) q = null;
      continue;
    }
    if (ch === '"' || ch === "`") { q = ch; cur += ch; continue; }
    if (ch === "[" || ch === "(" || ch === "{") depth++;
    if (ch === "]" || ch === ")" || ch === "}") depth--;
    if (depth < 0) return null;
    if ((ch === "," || ch === "\n") && depth === 0) {
      if (cur.trim()) items.push(cur.trim());
      cur = "";
      continue;
    }
    cur += ch;
  }
  if (q || depth !== 0) return null;
  if (cur.trim()) items.push(cur.trim());
  // "a b" without a comma is two values BCL may accept; we cannot split it safely.
  for (const it of items) if (/^"[^"]*"\s+\S/.test(it)) return null;
  if (body.includes("#") || body.includes("//") || body.includes("/*")) {
    // comments inside a list would be lost on rewrite
    const stripped = body.replace(/"(?:[^"\\]|\\.)*"|`[^`]*`/g, "");
    if (/#|\/\/|\/\*/.test(stripped)) return null;
  }
  return items;
}

export function emitList(items: string[]): string {
  return "[" + items.join(", ") + "]";
}

export interface Env {
  name: string;
  def?: string;
}

/** Reads `env("NAME")` / `env("NAME", "default")`. */
export function parseEnv(raw: string): Env | null {
  const m = /^env\s*\(([\s\S]*)\)$/.exec(raw.trim());
  if (!m) return null;
  const args = splitArgs(m[1]!);
  if (!args || args.length < 1 || args.length > 2) return null;
  const name = unquote(args[0]!);
  if (name === null) return null;
  if (args.length === 1) return { name };
  const def = unquote(args[1]!);
  if (def === null) return null;
  return { name, def };
}

export function emitEnv(name: string, def?: string): string {
  return def === undefined || def === "" ? `env(${quote(name)})` : `env(${quote(name)},${quote(def)})`;
}

function splitArgs(s: string): string[] | null {
  const out: string[] = [];
  let cur = "";
  let q: string | null = null;
  let depth = 0;
  for (let i = 0; i < s.length; i++) {
    const ch = s[i]!;
    if (q) {
      cur += ch;
      if (ch === "\\" && q === '"') cur += s[++i] ?? "";
      else if (ch === q) q = null;
      continue;
    }
    if (ch === '"' || ch === "`") { q = ch; cur += ch; continue; }
    if ("([{".includes(ch)) depth++;
    if (")]}".includes(ch)) depth--;
    if (ch === "," && depth === 0) { out.push(cur.trim()); cur = ""; continue; }
    cur += ch;
  }
  if (q || depth !== 0) return null;
  if (cur.trim() || out.length) out.push(cur.trim());
  return out;
}

/** Decides how a form should present raw for a scalar of the given kind. */
export function detectMode(raw: string | undefined, kind: ScalarKind): Mode {
  if (raw === undefined || raw.trim() === "") return "literal";
  if (parseEnv(raw)) return "env";
  return parseLiteral(raw, kind) !== null ? "literal" : "expr";
}

/** The text a literal editor shows for raw, or null when raw is not a literal of this kind. */
export function parseLiteral(raw: string, kind: ScalarKind): string | null {
  const s = raw.trim();
  switch (kind) {
    case "string": return unquote(s);
    case "ident": return IDENT.test(s) ? s : null;
    case "int": return INT.test(s) ? s : null;
    case "number": return NUMBER.test(s) ? s : null;
    case "bool": return s === "true" || s === "false" ? s : null;
    case "duration": return DURATION.test(s) ? s : null;
    case "any": {
      const u = unquote(s);
      if (u !== null) return u;
      return INT.test(s) || NUMBER.test(s) || s === "true" || s === "false" ? s : null;
    }
  }
}

export interface EmitResult {
  raw?: string;
  /** Set when the typed text is not valid for the kind (nothing is emitted). */
  error?: string;
}

/** The raw BCL for text typed into a literal editor of the given kind. */
export function emitLiteral(text: string, kind: ScalarKind): EmitResult {
  switch (kind) {
    case "string":
    case "any":
      try {
        // "any" keeps numbers and booleans bare when they look like them.
        if (kind === "any" && (INT.test(text) || NUMBER.test(text) || text === "true" || text === "false")) return { raw: text };
        return { raw: quote(text) };
      } catch (e) {
        return { error: (e as Error).message };
      }
    case "ident":
      return IDENT.test(text) ? { raw: text } : { error: "expected a bare identifier (letters, digits, _ . -)" };
    case "int":
      return INT.test(text) ? { raw: text.replace(/^\+/, "") } : { error: "expected a whole number" };
    case "number":
      return NUMBER.test(text) ? { raw: text } : { error: "expected a number" };
    case "bool":
      return text === "true" || text === "false" ? { raw: text } : { error: "expected true or false" };
    case "duration":
      return DURATION.test(text) ? { raw: text } : { error: "expected a duration such as 500ms, 30m or 1h30m" };
  }
}

/** Maps a schema/catalog type name onto a scalar kind; null when it is not a scalar. */
export function scalarKindOf(t: string | undefined): ScalarKind | null {
  switch ((t ?? "").toLowerCase()) {
    case "string": case "str": case "text": return "string";
    case "ident": return "ident";
    case "int": case "int32": case "int64": case "integer": case "uint": case "uint32": case "uint64": return "int";
    case "float": case "float64": case "number": return "number";
    case "bool": case "boolean": return "bool";
    case "duration": case "time.duration": return "duration";
    case "any": case "interface{}": case "": return "any";
    default: return null;
  }
}
