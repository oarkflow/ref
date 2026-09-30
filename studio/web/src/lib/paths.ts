import type { BlockNode } from "../api/types";

/** Joins a child segment onto a model path ("route/x" + "authz" -> "route/x/authz"). */
export function childPath(parent: string, seg: string): string {
  const s = seg.replace(/\//g, "\\/");
  return parent ? `${parent}/${s}` : s;
}

/** Splits a model path into segments, honouring "\/" escapes. */
export function splitPath(p: string): string[] {
  const out: string[] = [];
  let cur = "";
  for (let i = 0; i < p.length; i++) {
    if (p[i] === "\\" && p[i + 1] === "/") {
      cur += "/";
      i++;
    } else if (p[i] === "/") {
      out.push(cur);
      cur = "";
    } else cur += p[i];
  }
  out.push(cur);
  return out;
}

/** The last segment of a path without its "[n]" index. */
export function leafName(p: string): string {
  const s = splitPath(p);
  return (s[s.length - 1] ?? "").replace(/\[\d+\]$/, "");
}

/** True when `path` is `prefix` or lies beneath it. */
export function isWithin(path: string, prefix: string): boolean {
  return path === prefix || path.startsWith(prefix + "/");
}

/** Finds the node at `path` in a tree. */
export function findNode(nodes: BlockNode[] | undefined, path: string): BlockNode | undefined {
  for (const n of nodes ?? []) {
    if (n.path === path) return n;
    if (n.children && isWithin(path, n.path)) {
      const f = findNode(n.children, path);
      if (f) return f;
    }
  }
  return undefined;
}

/** A human label for a block node: `route web.todos_list`. */
export function blockLabel(n: BlockNode): string {
  return n.kind === "block" ? (n.id ? `${n.type} ${n.id}` : (n.type ?? "")) : (n.name ?? "");
}

/** Child field by name. */
export function fieldOf(node: BlockNode | undefined, name: string): BlockNode | undefined {
  return node?.children?.find((c) => c.kind === "field" && c.name === name);
}

/** Child blocks of the given type. */
export function blocksOf(node: BlockNode | undefined, type: string): BlockNode[] {
  return (node?.children ?? []).filter((c) => c.kind === "block" && c.type === type);
}
