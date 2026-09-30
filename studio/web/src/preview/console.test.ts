import { describe, expect, it } from "vitest";
import type { BlockNode, RecordedRequest } from "../api/types";
import { filterRequests, formatDuration, pathMatches, resolveRoute, routesOf, statusClass, truncate, type ConsoleFilter } from "./console";

const req = (over: Partial<RecordedRequest> & { at: string }): RecordedRequest => ({ kind: "request", method: "GET", url: "/", status: 200, ...over });
const rows: RecordedRequest[] = [
  req({ at: "2026-01-01T10:00:01Z", url: "/todos", detail: { route: "web.todos_list", intent: "todo.list" } }),
  req({ at: "2026-01-01T10:00:02Z", method: "POST", url: "/todos", status: 422, detail: { route: "web.todos_create", intent: "todo.create" } }),
  req({ at: "2026-01-01T10:00:03Z", url: "/missing", status: 404 }),
  { at: "2026-01-01T10:00:04Z", kind: "outbound", method: "POST", url: "smtp://mail.internal/send", detail: { channel: "smtp", preview: "Welcome aboard" } },
  req({ at: "2026-01-01T10:00:05Z", url: "/boom", status: 500 }),
];
const all: ConsoleFilter = { text: "", kind: "all", errorsOnly: false };
const urls = (f: Partial<ConsoleFilter>) => filterRequests(rows, { ...all, ...f }).map((r) => r.url);

describe("filterRequests", () => {
  it("lists newest first", () => {
    expect(urls({})).toEqual(["/boom", "smtp://mail.internal/send", "/missing", "/todos", "/todos"]);
  });

  it("searches method, url, status, route, intent and outbound payload; all terms must match", () => {
    expect(urls({ text: "post todos" })).toEqual(["/todos"]);
    expect(urls({ text: "422" })).toEqual(["/todos"]);
    expect(urls({ text: "todo.list" })).toEqual(["/todos"]);
    expect(urls({ text: "web.todos_create" })).toEqual(["/todos"]);
    expect(urls({ text: "welcome" })).toEqual(["smtp://mail.internal/send"]);
    expect(urls({ text: "SMTP" })).toEqual(["smtp://mail.internal/send"]);
    expect(urls({ text: "get nothing" })).toEqual([]);
  });

  it("filters by kind", () => {
    expect(urls({ kind: "outbound" })).toEqual(["smtp://mail.internal/send"]);
    expect(urls({ kind: "request" })).toHaveLength(4);
  });

  it("errorsOnly keeps HTTP failures and drops outbound calls", () => {
    expect(urls({ errorsOnly: true })).toEqual(["/boom", "/missing", "/todos"]);
  });

  it("'since' hides what was recorded before Clear", () => {
    expect(urls({ since: Date.parse("2026-01-01T10:00:03Z") })).toEqual(["/boom", "smtp://mail.internal/send"]);
  });
});

describe("small formatters", () => {
  it("truncates with an ellipsis and reports whether it cut", () => {
    expect(truncate("short", 10)).toEqual({ text: "short", cut: false });
    expect(truncate("abcdefghij", 4)).toEqual({ text: "abcd…", cut: true });
  });
  it("classifies statuses and formats durations", () => {
    expect([200, 302, 404, 503, undefined].map(statusClass)).toEqual(["ok", "redirect", "client", "server", "none"]);
    expect([0.2, 12.4, 1500, undefined].map(formatDuration)).toEqual(["<1 ms", "12 ms", "1.50 s", ""]);
  });
});

describe("pathMatches", () => {
  it("matches literals, {params}, :params and trailing wildcards", () => {
    expect(pathMatches("/todos", "/todos")).toBe(true);
    expect(pathMatches("/todos", "/todos/")).toBe(true);
    expect(pathMatches("/todos", "/todos?x=1")).toBe(true);
    expect(pathMatches("/todos/{id}", "/todos/42")).toBe(true);
    expect(pathMatches("/todos/:id/edit", "/todos/42/edit")).toBe(true);
    expect(pathMatches("/todos/{id}", "/todos")).toBe(false);
    expect(pathMatches("/todos", "/todos/42")).toBe(false);
    expect(pathMatches("/static/*", "/static/css/app.css")).toBe(true);
    expect(pathMatches("/", "/")).toBe(true);
  });
});

const field = (parent: string, name: string, raw: string): BlockNode => ({ kind: "field", path: `${parent}/${name}`, name, raw, line: 1, start: 0, end: 0 });
const route = (id: string, method: string, path: string): BlockNode => ({
  kind: "block", type: "route", id, path: `route/${id}`, line: 1, start: 0, end: 0,
  children: [field(`route/${id}`, "method", method), field(`route/${id}`, "path", `"${path}"`)],
});

describe("resolveRoute", () => {
  const tree: BlockNode[] = [
    route("web.todos_list", "GET", "/todos"),
    route("web.todos_show", "GET", "/todos/{id}"),
    route("web.todos_create", "POST", "/todos"),
    { kind: "block", type: "intent", id: "todo.list", path: "intent/todo.list", line: 1, start: 0, end: 0 },
  ];
  const routes = routesOf("04_routes.bcl", tree);

  it("reads routes with their method and path from a deep tree", () => {
    expect(routes.map((r) => [r.id, r.method, r.routePath])).toEqual([
      ["web.todos_list", "GET", "/todos"], ["web.todos_show", "GET", "/todos/{id}"], ["web.todos_create", "POST", "/todos"],
    ]);
  });

  it("uses the route name the recorder gave", () => {
    const hit = resolveRoute(req({ at: "x", url: "/anything", detail: { route: "web.todos_show" } }), routes);
    expect(hit).toMatchObject({ file: "04_routes.bcl", path: "route/web.todos_show" });
  });

  it("falls back to method + path, preferring a literal pattern over a parameterised one", () => {
    expect(resolveRoute(req({ at: "x", url: "/todos" }), routes)?.id).toBe("web.todos_list");
    expect(resolveRoute(req({ at: "x", method: "POST", url: "/todos" }), routes)?.id).toBe("web.todos_create");
    expect(resolveRoute(req({ at: "x", url: "/todos/9?tab=2" }), routes)?.id).toBe("web.todos_show");
  });

  it("strips the preview prefix from the recorded URL", () => {
    expect(resolveRoute(req({ at: "x", url: "/studio/preview/d1/todos" }), routes, "/studio/preview/d1/")?.id).toBe("web.todos_list");
  });

  it("finds nothing for an unknown path, an unnamed route or an outbound call", () => {
    expect(resolveRoute(req({ at: "x", url: "/nope" }), routes)).toBeNull();
    expect(resolveRoute(req({ at: "x", detail: { route: "gone" }, url: "/nope" }), routes)).toBeNull();
    expect(resolveRoute({ at: "x", kind: "outbound", url: "/todos" }, routes)).toBeNull();
  });

  it("works from top-level nodes alone when the recorder names the route", () => {
    const shallow = routesOf("04_routes.bcl", tree.map((n) => ({ ...n, children: undefined })));
    expect(resolveRoute(req({ at: "x", detail: { route: "web.todos_list" } }), shallow)?.path).toBe("route/web.todos_list");
    expect(resolveRoute(req({ at: "x", url: "/todos" }), shallow)).toBeNull(); // needs the fields
  });
});
