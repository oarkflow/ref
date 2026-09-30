// The real responses of GET /flows on the starter, for tests and stories.
import full from "./fixtures/flows.full.json";
import focus from "./fixtures/flows.focus.json";
import type { FlowGraph, FlowNode } from "./types";

export const FULL = full as unknown as FlowGraph;
export const FOCUS = focus as unknown as FlowGraph;

/** A deep copy, so a test can change it. */
export const clone = <T,>(g: T): T => JSON.parse(JSON.stringify(g)) as T;

/** The starter graph with one button that calls an address nothing answers. */
export function withUnresolved(): FlowGraph {
  const g = clone(FULL);
  const page = g.nodes.find((n) => n.id === "page:pages/todos/list")!;
  const el: FlowNode = {
    id: "element:page:pages/todos/list#dead0001", kind: "element", subkind: "button", label: "Archive all", group: page.id,
    file: "templates/pages/todos/list.html", line: 42, path: "templates/pages/todos/list.html", data: { method: "POST", url: "/todos/archive", rawUrl: "/todos/archive" },
  };
  const gone: FlowNode = { id: "unresolved:dead0001", kind: "unresolved", label: "POST /todos/archive", file: el.file, line: 42, path: el.path, data: { method: "POST", url: "/todos/archive" } };
  g.nodes.push(el, gone);
  g.edges.push(
    { id: "contains:page->dead", kind: "contains", from: page.id, to: el.id },
    { id: "calls:dead", kind: "calls", from: el.id, to: gone.id, label: "POST" },
  );
  g.warnings.push({ code: "flows.unresolved", severity: "warning", message: "POST /todos/archive is not served by any route", node: el.id, file: el.file, line: 42 });
  g.stats.unresolved += 1;
  return g;
}
