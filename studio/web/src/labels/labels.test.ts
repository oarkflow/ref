import { describe, expect, it } from "vitest";
import schemaJson from "../fixtures/schema-blocks.json";
import { BLOCKS, CATEGORIES, blockInfo, categoryOf, fieldLabel, friendlyProblem, groupProblems, humanize, problemWhere, sectionOf, statusInfo, typesIn } from "./index";

describe("label map", () => {
  it("has a friendly entry for every block type the schema knows", () => {
    for (const type of Object.keys(schemaJson)) {
      const info = BLOCKS[type];
      expect(info, `missing label for block "${type}"`).toBeTruthy();
      expect(info!.label).not.toMatch(/_/);
      expect(info!.description.length).toBeGreaterThan(10);
      expect(CATEGORIES.some((c) => c.id === info!.category)).toBe(true);
    }
  });

  it("uses the agreed plain-language names", () => {
    expect(blockInfo("route").label).toBe("Page or API endpoint");
    expect(blockInfo("resource").label).toBe("Connection");
    expect(blockInfo("intent").label).toBe("Logic flow");
    expect(blockInfo("worker").label).toBe("Background job");
    expect(blockInfo("trigger").label).toBe("Webhook");
    expect(categoryOf("schedule")).toBe("automation");
  });

  it("falls back to a humanized name for anything unknown", () => {
    expect(blockInfo("brand_new_thing").label).toBe("Brand new thing");
    expect(blockInfo("brand_new_thing").category).toBe("other");
    expect(humanize("cache_control")).toBe("Cache control");
    expect(humanize("max_db_queries")).toBe("Max DB queries");
    expect(humanize("http.api-url")).toBe("HTTP API URL");
    expect(humanize("")).toBe("");
    expect(fieldLabel("some_unlisted_setting")).toBe("Some unlisted setting");
    expect(fieldLabel("allow_anonymous")).toBe("Open to everyone");
  });

  it("puts every block type in exactly one category", () => {
    const seen = new Set<string>();
    for (const c of CATEGORIES) for (const t of typesIn(c.id)) { expect(seen.has(t)).toBe(false); seen.add(t); }
    expect(seen.size).toBe(Object.keys(BLOCKS).length);
  });

  it("sorts fields into sections", () => {
    expect(sectionOf("route", "path")).toBe("basics");
    expect(sectionOf("route", "authz")).toBe("security");
    expect(sectionOf("route", "timeout")).toBe("performance");
    expect(sectionOf("route", "tags")).toBe("advanced");
    expect(sectionOf("some_plugin_block", "anything")).toBe("basics");
  });

  it("explains statuses in plain words", () => {
    expect(statusInfo("pending").label).toBe("Waiting for review");
    expect(statusInfo("active").label).toBe("Live");
    expect(statusInfo("weird_state").label).toBe("Weird state");
  });
});

describe("plain-language problems", () => {
  it("rewrites what it recognises and keeps everything else", () => {
    expect(friendlyProblem({ message: "path must start with /" })).toEqual({ text: "Addresses must start with “/”, for example /todos.", rewritten: true });
    expect(friendlyProblem({ message: 'unknown intent "todo.nope"' }).text).toContain("todo.nope");
    expect(friendlyProblem({ message: "duplicate route web.home" }).text).toMatch(/two page or API endpoints named “web.home”/i);
    expect(friendlyProblem({ message: "something odd happened" })).toEqual({ text: "something odd happened", rewritten: false });
    expect(friendlyProblem({ message: "parse: boom" }).text).toBe("boom");
  });

  it("says where a problem is in words", () => {
    expect(problemWhere({ path: "route/web.todos_list/path" })).toBe("Page or API endpoint · web.todos_list · Address");
    expect(problemWhere({ file: "a.bcl", line: 4 })).toBe("a.bcl, line 4");
    expect(problemWhere({})).toBe("");
  });
});

describe("more plain-language problems", () => {
  it("explains a route with nothing to run", () => {
    expect(friendlyProblem({ message: 'route "web.reports" needs an intent, a process, a template, or static' }).text).toMatch(/needs something to run or show/);
  });
});


describe("groupProblems", () => {
  const env = (n: string) => ({ severity: "warning" as const, message: `environment variable ${n} is not set here` });
  it("folds repeated environment warnings into one group and keeps errors first", () => {
    const list = [env("A_ONE"), { severity: "error" as const, message: "boom" }, env("B_TWO"), env("C_THREE"), { severity: "warning" as const, message: "odd" }];
    const out = groupProblems(list);
    expect(out[0]).toMatchObject({ kind: "single", d: { message: "boom" } });
    expect(out[1]).toMatchObject({ kind: "group", key: "env" });
    const g = out[1];
    expect(g && g.kind === "group" ? g.items.map((i) => i.name) : []).toEqual(["A_ONE", "B_TWO", "C_THREE"]);
    expect(out[2]).toMatchObject({ kind: "single", d: { message: "odd" } });
  });
  it("leaves a couple of repeats alone", () => {
    expect(groupProblems([env("A"), env("B")]).every((e) => e.kind === "single")).toBe(true);
  });
});
