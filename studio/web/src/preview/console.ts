import type { BlockNode, RecordedRequest } from "../api/types";

export type KindFilter = "all" | "request" | "outbound";

export interface ConsoleFilter {
  text: string;
  kind: KindFilter;
  /** Only failures: HTTP status >= 400. Outbound calls are kept out when set. */
  errorsOnly: boolean;
  /** Ignore everything recorded at or before this time (the "Clear" button). */
  since?: number;
}

/** The single line a row is searched and sorted by. */
export function haystack(r: RecordedRequest): string {
  const d = r.detail ?? {};
  return [r.method, r.url, r.status, d.route, d.intent, d.channel, d.preview].filter((x) => x !== undefined && x !== "").join(" ").toLowerCase();
}

/** Filters and orders newest first. All search terms must match (AND). */
export function filterRequests(list: RecordedRequest[], f: ConsoleFilter): RecordedRequest[] {
  const terms = f.text.toLowerCase().split(/\s+/).filter(Boolean);
  const out = list.filter((r) => {
    if (f.kind !== "all" && r.kind !== f.kind) return false;
    if (f.errorsOnly && !(r.kind === "request" && (r.status ?? 0) >= 400)) return false;
    if (f.since !== undefined && Date.parse(r.at) <= f.since) return false;
    if (!terms.length) return true;
    const h = haystack(r);
    return terms.every((t) => h.includes(t));
  });
  return out.sort((a, b) => Date.parse(b.at) - Date.parse(a.at));
}

/** A stable key for a row (the wire format has no id). */
export function rowKey(r: RecordedRequest, i: number): string {
  return `${r.at}|${r.kind}|${r.method ?? ""}|${r.url ?? ""}|${i}`;
}

export function truncate(s: string, n = 100): { text: string; cut: boolean } {
  if (s.length <= n) return { text: s, cut: false };
  return { text: s.slice(0, n).trimEnd() + "…", cut: true };
}

export function statusClass(status?: number): "ok" | "redirect" | "client" | "server" | "none" {
  if (!status) return "none";
  if (status >= 500) return "server";
  if (status >= 400) return "client";
  if (status >= 300) return "redirect";
  return "ok";
}

export function formatDuration(ms?: number): string {
  if (ms === undefined || ms === null) return "";
  if (ms < 1) return "<1 ms";
  if (ms < 1000) return `${Math.round(ms)} ms`;
  return `${(ms / 1000).toFixed(2)} s`;
}

// ---- jumping to the route block ------------------------------------------------

export interface RouteRef {
  file: string;
  /** The model path of the route block, "route/<id>". */
  path: string;
  id: string;
  method?: string;
  routePath?: string;
}

/** Strips quotes from a raw BCL string. */
function unquote(raw?: string): string | undefined {
  if (raw === undefined) return undefined;
  const m = /^"(.*)"$/s.exec(raw.trim());
  return m ? m[1] : raw.trim();
}

/** Route blocks of a tree, with their method and path fields when the tree is deep. */
export function routesOf(file: string, tree: BlockNode[] | undefined): RouteRef[] {
  const out: RouteRef[] = [];
  for (const n of tree ?? []) {
    if (n.kind !== "block" || n.type !== "route" || !n.id) continue;
    const f = (name: string) => unquote(n.children?.find((c) => c.kind === "field" && c.name === name)?.raw);
    out.push({ file, path: n.path, id: n.id, method: f("method")?.toUpperCase(), routePath: f("path") });
  }
  return out;
}

/** True when a route pattern ("/todos/{id}", "/todos/:id", "/static/*") matches a URL path. */
export function pathMatches(pattern: string, path: string): boolean {
  const p = path.split(/[?#]/)[0]!;
  const norm = (s: string) => s.replace(/\/+$/, "") || "/";
  const pat = norm(pattern).split("/");
  const got = norm(p).split("/");
  for (let i = 0; i < pat.length; i++) {
    const seg = pat[i]!;
    if (seg === "*" || seg.startsWith("*")) return true;
    if (i >= got.length) return false;
    if (seg.startsWith(":") || (seg.startsWith("{") && seg.endsWith("}"))) continue;
    if (seg !== got[i]) return false;
  }
  return pat.length === got.length;
}

/**
 * Picks the route block a request was served by. The recorder names the
 * route when it knows it; otherwise the method and URL path are matched
 * against the routes' own fields (exact patterns before parameterised ones).
 */
export function resolveRoute(req: RecordedRequest, routes: RouteRef[], prefix?: string): RouteRef | null {
  if (req.kind !== "request") return null;
  const named = req.detail?.route;
  if (named) {
    const hit = routes.find((r) => r.id === named);
    if (hit) return hit;
  }
  let url = req.url ?? "";
  if (prefix && url.startsWith(prefix)) url = "/" + url.slice(prefix.length);
  const method = req.method?.toUpperCase();
  const candidates = routes.filter((r) => r.routePath && (!r.method || !method || r.method === method) && pathMatches(r.routePath, url));
  if (!candidates.length) return null;
  const literal = (r: RouteRef) => !/[:{*]/.test(r.routePath!);
  return candidates.find(literal) ?? candidates[0]!;
}
