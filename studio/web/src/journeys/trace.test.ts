import { describe, expect, it } from "vitest";
import { FULL, withUnresolved } from "./fixtures";
import { DEFAULT_FILTERS, buildJourney } from "./model";
import { pagesToFollow, tracesFrom } from "./trace";

const j = buildJourney(FULL, { ...DEFAULT_FILTERS, showOrphans: true });

describe("follow the user", () => {
  it("lists the pages that have something to click", () => {
    const pages = pagesToFollow(j).map((p) => p.id);
    expect(pages).toContain("page:pages/todos/new");
    expect(pages).not.toContain("page:pages/dashboard/admin"); // no buttons of its own
    expect(pages).not.toContain("page:pages/errors/maintenance");
  });

  it("walks Save draft: page, button, request, flow, connection, then the page you land on", () => {
    const traces = tracesFrom(j, "page:pages/todos/new");
    expect(traces).toHaveLength(1);
    const t = traces[0]!;
    expect(t.title).toBe("Save draft");
    expect(t.dead).toBe(false);
    const ids = t.steps.map((s) => s.id);
    expect(ids[0]).toBe("page:pages/todos/new");
    expect(ids[1]).toMatch(/^element:page:pages\/todos\/new#/);
    expect(ids.slice(2, 4)).toEqual(["route:web.todos_create", "intent:todo.create"]);
    expect(t.steps.map((s) => s.kind).slice(0, 4)).toEqual(["page", "element", "route", "intent"]);
    expect(t.steps.some((s) => s.kind === "resource")).toBe(true);
    expect(ids.at(-1)).toBe("page:pages/todos/list");
    expect(t.steps.at(-1)!.kind).toBe("result");
    expect(t.steps.at(-1)!.note).toBe("When it works, you land on “Todos”.");
  });

  it("makes each hop's line the one that animates, from the request on", () => {
    const t = tracesFrom(j, "page:pages/todos/new")[0]!;
    expect(t.steps[0]!.via).toBeUndefined();
    expect(t.steps[1]!.via).toBeUndefined();
    for (const s of t.steps.slice(2)) {
      expect(s.via, s.id).toBeTruthy();
      expect(j.edges.some((e) => e.id === s.via)).toBe(true);
    }
  });

  it("follows a link to the page its address shows", () => {
    const t = tracesFrom(j, "page:pages/todos/list")[0]!;
    expect(t.title).toBe("New Todo");
    expect(t.steps.map((s) => s.id)).toEqual([
      "page:pages/todos/list", t.steps[1]!.id, "route:web.todos_new", "page:pages/todos/new",
    ]);
    expect(t.steps.at(-1)!.note).toBe("It answers with “New todo”.");
  });

  it("puts the rows in page order", () => {
    const t = tracesFrom(j, "page:pages/auth/login");
    expect(t.length).toBe(7);
    expect(t.map((x) => x.title)).toContain("Forgot password?");
  });

  it("can include the shared parts", () => {
    const withShared = tracesFrom(j, "page:pages/todos/list", { includeShared: true });
    expect(withShared.length).toBeGreaterThan(1);
    expect(withShared.some((t) => t.title === "Sign out")).toBe(true);
  });

  it("ends at the problem when a click goes somewhere nothing answers", () => {
    const bad = buildJourney(withUnresolved(), { ...DEFAULT_FILTERS, showOrphans: true });
    const t = tracesFrom(bad, "page:pages/todos/list").find((x) => x.title === "Archive all")!;
    expect(t.steps.map((s) => s.kind)).toEqual(["page", "element", "problem"]);
    expect(t.steps.at(-1)!.note).toContain("nothing answers there");
  });

  it("says so when a button leads nowhere on the map", () => {
    const t = tracesFrom(j, "page:pages/todos/show").find((x) => x.dead);
    expect(t).toBeTruthy();
    expect(t!.steps).toHaveLength(2);
  });

  it("returns nothing for a card that is not a page", () => {
    expect(tracesFrom(j, "route:web.todos_list")).toEqual([]);
    expect(tracesFrom(j, "page:nope")).toEqual([]);
  });
});
