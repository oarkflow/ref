import { describe, expect, it } from "vitest";
import { FULL, FOCUS, withUnresolved } from "./fixtures";
import { DEFAULT_FILTERS, SHARED_ID, buildJourney, edgesTouching, neighbours, searchJourney, type Filters } from "./model";

const build = (g = FULL, f: Partial<Filters> = {}) => buildJourney(g, { ...DEFAULT_FILTERS, ...f });

describe("cards from the real starter", () => {
  const j = build();
  it("puts links, forms and buttons inside their page, in page order", () => {
    const list = j.byId.get("page:pages/todos/list")!;
    expect(list.kind).toBe("page");
    expect(list.rows.map((r) => r.label)).toEqual(["New Todo"]);
    const show = j.byId.get("page:pages/todos/show")!;
    expect(show.rows.length).toBe(5);
    expect(show.rows.map((r) => r.kind)).toContain("button");
    const lines = show.rows.filter((r) => r.file === "templates/pages/todos/show.html").map((r) => r.line ?? 0);
    expect(lines).toEqual([...lines].sort((a, b) => a - b));
  });

  it("collects the navbar and other shared parts into one card", () => {
    const shared = j.byId.get(SHARED_ID)!;
    expect(shared.kind).toBe("shared");
    expect(shared.rows.length).toBe(9);
    expect(shared.rows.map((r) => r.component)).toContain("navbar");
    expect(j.nodes.filter((n) => n.kind === "element" as never)).toEqual([]);
  });

  it("gives every line its own leaving point on the page that holds the button", () => {
    const save = j.edges.find((e) => e.kind === "calls" && e.target === "route:web.todos_create" && e.source === "page:pages/todos/new")!;
    expect(save).toBeTruthy();
    expect(save.sourceHandle).toMatch(/^element:page:pages\/todos\/new#/);
    expect(j.byId.get("page:pages/todos/new")!.rows.some((r) => r.id === save.sourceHandle)).toBe(true);
    expect(save.words.text).toBe("sends POST");
  });

  it("never draws a contains line: that is what the rows are", () => {
    expect(j.edges.some((e) => e.kind === ("contains" as never))).toBe(false);
  });

  it("draws the shared logout form as one line however many pages include it", () => {
    const logout = j.edges.filter((e) => e.target === "route:web.logout_action");
    expect(logout.length).toBe(1);
    expect(logout[0]!.source).toBe(SHARED_ID);
  });

  it("names a request by its address, and says what kind it is", () => {
    const r = j.byId.get("route:web.todos_list")!;
    expect(r.title).toBe("/todos");
    expect(r.sub).toBe("Shows a page");
    expect(j.byId.get("route:web.todos_create")!.sub).toBe("Changes data");
    const intent = j.byId.get("intent:todo.create")!;
    expect(intent.sub).toBe("Logic flow");
    expect(intent.chips[0]?.text).toMatch(/^\d+ steps?$/);
  });

  it("gives a page its addresses and who may open it", () => {
    const login = j.byId.get("page:pages/auth/login")!;
    expect(login.routes).toContain("GET /login");
    expect(login.chips.some((c) => c.text === "Public")).toBe(true);
    expect(j.byId.get("page:pages/todos/list")!.chips.some((c) => c.text === "Sign-in")).toBe(true);
  });

  it("draws renders as a faint line that runs back to its page, and redirects as a normal one", () => {
    const shows = j.edges.find((e) => e.kind === "renders")!;
    expect(shows.back).toBe(true);
    expect(shows.quiet).toBe(true);
    expect(shows.words.text).toBe("shows");
    const goes = j.edges.find((e) => e.kind === "redirects")!;
    expect(goes.back).toBe(true);
    expect(goes.quiet).toBe(false);
    expect(goes.words.text).toBe("on success, goes to");
  });

  it("replaces placeholder labels with the address", () => {
    const row = j.byId.get(SHARED_ID)!.rows.find((r) => r.guessed)!;
    expect(row).toBeTruthy();
    expect(row.label).toMatch(/^(Link|Form|Button|Script call)( to .+)?$/);
  });
});

describe("titles", () => {
  it("names a page whose heading is computed after its template", () => {
    expect(build().byId.get("page:pages/dashboard/index")!.title).toBe("Dashboard");
  });
  it("keeps a real heading", () => {
    expect(build().byId.get("page:pages/todos/new")!.title).toBe("New todo");
  });
});

describe("what is shown", () => {
  it("draws the shared card's lines faintly: it links to half the app", () => {
    const shared = build().edges.filter((e) => e.source === SHARED_ID);
    expect(shared.length).toBeGreaterThan(3);
    expect(shared.every((e) => e.quiet)).toBe(true);
    expect(build().edges.filter((e) => e.source.startsWith("page:") && e.kind === "calls").every((e) => !e.quiet)).toBe(true);
  });

  it("keeps only what connects to the thing in focus", () => {
    const j = buildJourney(FOCUS, DEFAULT_FILTERS);
    // the navbar's destinations (logout, register, ...) are not part of this journey
    expect(j.byId.has("route:web.logout_action")).toBe(false);
    for (const n of j.nodes) {
      if (n.id === "page:pages/todos/new") continue;
      expect(j.edges.some((e) => e.source === n.id || e.target === n.id), n.id).toBe(true);
    }
  });

  it("honours how far to reach: a page's buttons count as inside it", () => {
    const one = buildJourney(FOCUS, DEFAULT_FILTERS, { reach: 1 });
    expect(one.byId.has("route:web.todos_create")).toBe(true);
    expect(one.byId.has("intent:todo.create")).toBe(false);
    const two = buildJourney(FOCUS, DEFAULT_FILTERS, { reach: 2 });
    expect(two.byId.has("intent:todo.create")).toBe(true);
    expect(two.byId.has("page:pages/todos/list")).toBe(true); // where it lands on success
    expect(two.byId.has("page:pages/dashboard/admin")).toBe(false);
    const three = buildJourney(FOCUS, DEFAULT_FILTERS, { reach: 3 });
    expect(three.nodes.length).toBeGreaterThanOrEqual(two.nodes.length);
    expect(buildJourney(FOCUS, DEFAULT_FILTERS).nodes.length).toBeGreaterThanOrEqual(three.nodes.length);
  });

  it("leaves the shared navbar out of a focused journey unless asked", () => {
    expect(buildJourney(FOCUS, DEFAULT_FILTERS).byId.has(SHARED_ID)).toBe(false);
    expect(buildJourney(FOCUS, { ...DEFAULT_FILTERS, sharedInFocus: true }).byId.has(SHARED_ID)).toBe(true);
  });

  it("hides endpoints no page reaches until asked, and says how many", () => {
    const base = build();
    const all = build(FULL, { showOrphans: true });
    expect(base.hidden.endpoints).toBeGreaterThan(0);
    expect(all.hidden.endpoints).toBe(0);
    expect(all.nodes.length).toBeGreaterThan(base.nodes.length);
    // everything shown is reachable from a page
    expect(base.nodes.every((n) => n.kind !== "route" || base.edges.some((e) => e.target === n.id || e.source === n.id))).toBe(true);
    expect(base.byId.has("route:web.todos_list")).toBe(true);
    expect(base.byId.has("route:orders.create")).toBe(false);
    expect(all.byId.has("route:orders.create")).toBe(true);
  });

  it("can hide the shared card and the connections", () => {
    const noShared = build(FULL, { hideShared: true });
    expect(noShared.byId.has(SHARED_ID)).toBe(false);
    expect(noShared.edges.some((e) => e.target === "route:web.logout_action")).toBe(false);
    const noConn = build(FULL, { hideConnections: true });
    expect(noConn.nodes.some((n) => n.kind === "resource")).toBe(false);
    expect(noConn.edges.some((e) => e.kind === "uses")).toBe(false);
    expect(noConn.hidden.connections).toBeGreaterThan(0);
  });

  it("shows everything in a focused journey", () => {
    const j = buildJourney(FOCUS, DEFAULT_FILTERS);
    expect(j.hidden.endpoints).toBe(0);
    expect(j.byId.has("page:pages/todos/new")).toBe(true);
    expect(j.byId.has("route:web.todos_create")).toBe(true);
  });

  it("assigns lanes: pages, requests, logic flows, connections", () => {
    const j = build();
    const lane = (id: string) => j.byId.get(id)!.lane;
    expect(lane("page:pages/todos/list")).toBe(0);
    expect(lane(SHARED_ID)).toBe(0);
    expect(lane("route:web.todos_create")).toBe(1);
    expect(lane("intent:todo.create")).toBe(2);
    const res = j.nodes.filter((n) => n.kind === "resource");
    expect(res.length).toBeGreaterThan(0);
    const maxIntent = Math.max(...j.nodes.filter((n) => n.kind === "intent").map((n) => n.lane));
    expect(res.every((n) => n.lane === maxIntent + 1)).toBe(true);
  });
});

describe("problems", () => {
  const g = withUnresolved();
  const j = build(g);

  it("shows a call nothing answers as a red card, with who calls it", () => {
    const gone = j.byId.get("unresolved:dead0001")!;
    expect(gone.kind).toBe("unresolved");
    expect(gone.problems).toBe(1);
    expect(gone.facts[0]).toEqual({ k: "Called by", v: "Todos · line 42" });
    expect(gone.chips[0]!.tone).toBe("danger");
  });

  it("counts the problem on the page row and the page", () => {
    const page = j.byId.get("page:pages/todos/list")!;
    expect(page.problems).toBe(1);
    expect(page.rowProblems["element:page:pages/todos/list#dead0001"]).toBe(1);
    expect(page.rows.map((r) => r.label)).toContain("Archive all");
  });

  it("points warnings at the card that holds the element", () => {
    const w = j.warnings.find((x) => x.code === "flows.unresolved")!;
    expect(w.node).toBe("page:pages/todos/list");
  });

  it("'only problems' keeps the problem, what holds it and what is next to it", () => {
    const only = build(g, { onlyProblems: true });
    expect(only.byId.has("unresolved:dead0001")).toBe(true);
    expect(only.byId.has("page:pages/todos/list")).toBe(true);
    expect(only.nodes.length).toBeLessThan(6);
    expect(only.byId.has("page:pages/auth/login")).toBe(false);
  });

  it("'only problems' on a healthy app shows nothing", () => {
    expect(build(FULL, { onlyProblems: true }).nodes).toEqual([]);
  });
});

describe("reading the map", () => {
  const j = build();
  it("finds who calls a request and what it calls", () => {
    const { incoming, outgoing } = neighbours(j, "route:web.todos_create");
    expect(incoming.map((n) => n.node.id)).toContain("page:pages/todos/new");
    expect(incoming.find((n) => n.node.id === "page:pages/todos/new")!.row?.label).toBe("Save draft");
    expect(outgoing.map((n) => n.node.id)).toContain("intent:todo.create");
  });

  it("a row's lines are the ones it sends, plus the redirects it asks for", () => {
    const row = j.byId.get("page:pages/todos/new")!.rows[0]!;
    const ids = edgesTouching(j, row.id);
    expect(ids.length).toBeGreaterThanOrEqual(1);
    const kinds = ids.map((id) => j.edges.find((e) => e.id === id)!.kind);
    expect(kinds).toContain("calls");
  });

  it("a card's lines are every line that touches it", () => {
    const ids = edgesTouching(j, "route:web.todos_create");
    expect(ids.length).toBeGreaterThanOrEqual(2);
    expect(edgesTouching(j, null)).toEqual([]);
  });

  it("searches names, addresses and button labels, and brings the page a button is on", () => {
    expect(searchJourney(j, "todos/new").has("route:web.todos_new")).toBe(true);
    expect(searchJourney(j, "Save draft").has("page:pages/todos/new")).toBe(true);
    expect(searchJourney(j, "todo.create").has("intent:todo.create")).toBe(true);
    expect(searchJourney(j, "  ").size).toBe(0);
    expect(searchJourney(j, "zzzz-nothing").size).toBe(0);
  });
});
