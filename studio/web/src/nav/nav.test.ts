import { describe, expect, it, vi } from "vitest";
import type { BlockNode } from "../api/types";
import trees from "../fixtures/trees.json";
import { groupCommands, score, searchCommands, type Command } from "./commands";
import { categoryCounts, collectItems, groupByCategory, summarize, suggestFile } from "./model";
import { buildBody, defaultAnswers } from "./quickstart";

const all = trees as unknown as Record<string, BlockNode[]>;
const tops = Object.fromEntries(Object.entries(all).map(([f, ns]) => [f, ns.map((n) => ({ ...n, children: undefined }))]));

describe("navigation model", () => {
  it("collects top-level blocks and groups them by friendly category", () => {
    const items = collectItems(tops, all);
    expect(items.length).toBeGreaterThan(5);
    const g = groupByCategory(items);
    expect(g.flows.every((i) => ["intent", "process", "pipeline"].includes(i.type))).toBe(true);
    expect(g.flows.length).toBeGreaterThan(0);
    const counts = categoryCounts(items);
    expect(Object.values(counts).reduce((a, b) => a + b, 0)).toBe(items.length);
    expect(counts.flows).toBe(g.flows.length);
  });

  it("uses the block id as the name and falls back to the type", () => {
    const items = collectItems({ "a.bcl": [{ kind: "block", path: "tenant", type: "tenant", line: 1, start: 0, end: 1 }] });
    expect(items[0]).toMatchObject({ name: "tenant", id: "", category: "access" });
  });

  it("summarises a route from its fields", () => {
    const route: BlockNode = {
      kind: "block", path: "route/r", type: "route", id: "r", line: 1, start: 0, end: 1,
      children: [
        { kind: "field", path: "route/r/method", name: "method", raw: "POST", line: 2, start: 0, end: 0 },
        { kind: "field", path: "route/r/path", name: "path", raw: '"/login"', line: 3, start: 0, end: 0 },
        { kind: "field", path: "route/r/allow_anonymous", name: "allow_anonymous", raw: "true", line: 4, start: 0, end: 0 },
        { kind: "field", path: "route/r/intent", name: "intent", raw: '"auth.login"', line: 5, start: 0, end: 0 },
      ],
    };
    const [item] = collectItems({ "r.bcl": [route] }, { "r.bcl": [route] });
    const s = summarize(item!);
    expect(s.chips.map((c) => c.text)).toEqual(["POST", "/login", "Open to everyone"]);
    expect(s.line).toBe("Runs flow auth.login");
  });

  it("suggests the file that already holds that kind of thing", () => {
    const nav = { "a.bcl": [{ kind: "block", path: "route/x", type: "route", line: 1, start: 0, end: 0 }], "b.bcl": [] } as Record<string, BlockNode[]>;
    expect(suggestFile("route", nav, ["a.bcl", "b.bcl"])).toBe("a.bcl");
    expect(suggestFile("resource", nav, ["b.bcl", "a.bcl"])).toBe("a.bcl");
  });
});

describe("add flow answers", () => {
  it("turns answers into a block body, quoting strings but not identifiers", () => {
    expect(buildBody("route", { method: "POST", path: "/reports", intent: 'todo."list"' })).toBe('method POST\npath "/reports"\nintent "todo.\\"list\\""');
    expect(buildBody("flag", { description: "x", default: "true" })).toBe('description "x"\ndefault true');
    expect(buildBody("schedule", { every: "24h" })).toBe("every 24h");
    expect(buildBody("route", { path: "  " })).toBe("");
    expect(defaultAnswers("route")).toEqual({ method: "GET", path: "/" });
  });
});

describe("command palette search", () => {
  const run = vi.fn();
  const cmds: Command[] = [
    { id: "1", title: "Overview", group: "Go to", icon: "dash", run },
    { id: "2", title: "web.todos_list", subtitle: "Page or API endpoint", group: "Things", icon: "globe", keywords: "route 04_routes.bcl", run },
    { id: "3", title: "Add a connection", group: "Actions", icon: "action", keywords: "new create resource", run },
    { id: "4", title: "Turn on Developer view", group: "Actions", icon: "action", keywords: "technical raw bcl", run },
  ];

  it("ranks starts-with above contains, and needs every word to match", () => {
    expect(searchCommands(cmds, "over").map((c) => c.id)).toEqual(["1"]);
    expect(searchCommands(cmds, "todos").map((c) => c.id)).toEqual(["2"]);
    expect(searchCommands(cmds, "page endpoint").map((c) => c.id)).toEqual(["2"]);
    expect(searchCommands(cmds, "todos nothing")).toEqual([]);
    expect(score({ title: "Alpha", group: "g" }, "alp")).toBeGreaterThan(score({ title: "Beta alpha", group: "g" }, "alp"));
  });

  it("matches on keywords and returns everything for an empty query", () => {
    expect(searchCommands(cmds, "create").map((c) => c.id)).toEqual(["3"]);
    expect(searchCommands(cmds, "raw").map((c) => c.id)).toEqual(["4"]);
    expect(searchCommands(cmds, "  ")).toHaveLength(4);
  });

  it("groups in first-seen order", () => {
    expect(groupCommands(cmds).map(([g, cs]) => [g, cs.length])).toEqual([["Go to", 1], ["Things", 1], ["Actions", 2]]);
  });
});
