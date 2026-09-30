import { afterEach, describe, expect, it } from "vitest";
import type { Catalog, ConfigField } from "../../api/types";
import { catalog as catalogJson } from "../../test/helpers";
import {
  CONFIG_TYPES, actionFields, coveredKeys, fieldFromConfig, fieldsFromConfig, isGeneric, listNodeTypes, registerNodeType,
  resolveNodeType, rowsFor, unregisterNodeType, type FieldKind, type NodeTypeDef,
} from ".";

const catalog = catalogJson as unknown as Catalog;

const def = (over: Partial<NodeTypeDef> & { id: string }): NodeTypeDef => ({
  label: over.id, icon: "gear", category: "compute", match: () => false, priority: 100, sections: [], ...over,
});

describe("matching and priority", () => {
  const added: string[] = [];
  const add = (d: NodeTypeDef) => { registerNodeType(d); added.push(d.id); };
  afterEach(() => { while (added.length) unregisterNodeType(added.pop()!); });

  it("finds a registered type by its name", () => {
    add(def({ id: "t:zap", label: "Zap", match: (t) => t === "zap" }));
    const r = resolveNodeType("zap", undefined, catalog);
    expect(r.def.label).toBe("Zap");
    expect(r.generic).toBe(false);
  });

  it("the highest priority wins, whatever the order of registration", () => {
    add(def({ id: "t:hi", label: "High", priority: 200, match: (t) => t === "zap" }));
    add(def({ id: "t:lo", label: "Low", priority: 50, match: (t) => t === "zap" }));
    expect(resolveNodeType("zap", undefined, catalog).def.label).toBe("High");
  });

  it("on a tie the later registration wins, so a host can override a built-in", () => {
    add(def({ id: "t:a", label: "First", match: (t) => t === "zap" }));
    add(def({ id: "t:b", label: "Second", match: (t) => t === "zap" }));
    expect(resolveNodeType("zap", undefined, catalog).def.label).toBe("Second");
  });

  it("registering the same id again replaces it", () => {
    add(def({ id: "t:same", label: "Old", match: (t) => t === "zap" }));
    add(def({ id: "t:same", label: "New", match: (t) => t === "zap" }));
    expect(listNodeTypes().filter((d) => d.id === "t:same")).toHaveLength(1);
    expect(resolveNodeType("zap", undefined, catalog).def.label).toBe("New");
  });

  it("an action-specific descriptor beats the type's, whatever the type", () => {
    // built in: a step that runs the rule table gets the table form even when its type is plain "decision"
    const r = resolveNodeType("decision", "decision.table", catalog);
    expect(r.def.id).toBe("action:decision.table");
    expect(resolveNodeType("decision", "decision.expression", catalog).def.id).toBe("type:decision");
  });

  it("matches on the action too", () => {
    add(def({ id: "t:byact", label: "By action", priority: 300, match: (_t, a) => a === "my.action" }));
    expect(resolveNodeType("anything", "my.action", catalog).def.label).toBe("By action");
    expect(resolveNodeType("anything", "other", catalog).generic).toBe(true);
  });

  it("an unknown type still gets a descriptor, built from the catalog, and says so", () => {
    const r = resolveNodeType("brand_new", "x.y", catalog);
    expect(r.generic).toBe(true);
    expect(isGeneric(r.def)).toBe(true);
    expect(r.def.label).toBe("Brand new");
    expect(r.def.actionConfig).toBe(true);
  });
});

describe("every kind of step in the catalog has its own descriptor", () => {
  const types = catalog.node_types;

  it("covers all of them (none falls back to the generic form)", () => {
    expect(types.length).toBeGreaterThan(70);
    const generic = types.filter((t) => resolveNodeType(t.name, t.default_action, catalog).generic).map((t) => t.name);
    expect(generic).toEqual([]);
  });

  it("gives each its own name, so the steps are told apart", () => {
    const labels = types.map((t) => resolveNodeType(t.name, undefined, catalog).def.label);
    const dupes = labels.filter((l, i) => labels.indexOf(l) !== i);
    expect(dupes).toEqual([]);
  });

  it("says what each one is for", () => {
    const blank = types.filter((t) => !resolveNodeType(t.name, undefined, catalog).def.blurb).map((t) => t.name);
    expect(blank).toEqual([]);
  });

  it("gives each a form of its own: settings for what it runs (or, for the few that take none, none)", () => {
    const NO_FORM = new Set(["action", "custom", "noop", "auth"]);
    for (const t of types) {
      const { def } = resolveNodeType(t.name, undefined, catalog);
      const fields = def.sections.flatMap((s) => s.fields);
      const action = t.default_action ? catalog.actions.find((a) => a.name === t.default_action) : undefined;
      const hasConfig = (action?.config?.length ?? 0) > 0 || def.scope === "process" || def.sections.some((s) => s.scope === "process");
      if (hasConfig && !NO_FORM.has(t.name)) expect(fields.length, `${t.name} has no fields`).toBeGreaterThan(0);
    }
  });

  it("every field of every descriptor is well formed", () => {
    for (const d of listNodeTypes()) {
      const ids = new Set<string>();
      for (const s of d.sections) {
        for (const f of s.fields) {
          expect(f.label !== undefined, `${d.id}.${f.id} has no label`).toBe(true);
          expect(f.at.length, `${d.id}.${f.id} at`).toBeGreaterThanOrEqual(1);
          if (f.kind !== "custom" || f.label) expect(ids.has(`${s.id}/${f.id}`), `${d.id}: ${f.id} twice in a section`).toBe(false);
          ids.add(`${s.id}/${f.id}`);
          if (f.kind === "select") expect(f.options, `${d.id}.${f.id}`).toBeTruthy();
          if (f.kind === "cases") expect(f.cols.length, `${d.id}.${f.id} columns`).toBeGreaterThan(0);
        }
      }
    }
  });

  it("the fields a descriptor lists are real settings of what the step runs", () => {
    // A field for a config key the action does not take would be silently ignored by the platform.
    const bad: string[] = [];
    for (const t of types) {
      if (!t.default_action) continue;
      const action = catalog.actions.find((a) => a.name === t.default_action);
      if (!action?.config) continue;
      const known = new Set(action.config.map((c) => c.name));
      const { def } = resolveNodeType(t.name, t.default_action, catalog);
      for (const s of def.sections) {
        if (s.scope === "process") continue;
        for (const f of s.fields) if (f.at.length === 2 && f.at[0] === "config" && f.kind !== "custom" && !known.has(f.at[1])) bad.push(`${t.name}: ${f.at[1]}`);
      }
    }
    expect(bad).toEqual([]);
  });
});

describe("the generic fallback", () => {
  const KINDS: Record<string, FieldKind> = {
    string: "text", template: "longText", expression: "expression", bool: "boolean", int: "number", number: "number", duration: "duration",
    fact: "factPicker", "[]fact": "factPicker", "[]string": "stringList", "[]template": "stringList", "[]int": "list", map: "keyValue",
    intent: "intentPicker", "[]intent": "intentPicker", process: "select", resource: "resourcePicker", sql: "code", any: "code",
    object: "code", block: "code", "[]object": "code",
  };

  it("knows every config type the catalog uses", () => {
    const used = new Set(catalog.actions.flatMap((a) => (a.config ?? []).map((c) => c.type)));
    for (const t of used) expect(CONFIG_TYPES as readonly string[], `config type ${t}`).toContain(t);
  });

  it("turns each config type into a usable kind of field", () => {
    for (const [type, kind] of Object.entries(KINDS)) {
      const f = fieldFromConfig({ name: "k", type, summary: "does a thing" });
      expect(f.kind, type).toBe(kind);
      expect(f.at).toEqual(["config", "k"]);
      expect(f.label).toBe("K");
    }
  });

  it("carries the summary as help, the default and whether it is required", () => {
    const f = fieldFromConfig({ name: "max_rows", type: "int", required: true, summary: "limit the rows", default: "100" });
    expect(f).toMatchObject({ label: "Max rows", help: "Limit the rows.", required: true, default: "100", integer: true });
  });

  it("a form for every action in the catalog: nothing throws, nothing is dropped", () => {
    for (const a of catalog.actions) {
      const fields = fieldsFromConfig(a.config);
      expect(fields.length).toBe(a.config?.length ?? 0);
      // required settings come first
      const firstOptional = fields.findIndex((f) => !f.required);
      if (firstOptional >= 0) expect(fields.slice(firstOptional).every((f) => !f.required)).toBe(true);
    }
  });

  it("a new action appears with a form and no frontend change", () => {
    const cfg: ConfigField[] = [
      { name: "endpoint", type: "template", required: true, summary: "where to send" },
      { name: "retries", type: "int" },
      { name: "sign", type: "bool" },
      { name: "labels", type: "map" },
    ];
    const cat: Catalog = { ...catalog, actions: [...catalog.actions, { name: "acme.ship", family: "acme", summary: "Ship it", config: cfg }] };
    const { def, generic } = resolveNodeType("acme_thing", "acme.ship", cat);
    expect(generic).toBe(true);
    const fields = actionFields("acme.ship", cat, coveredKeys(def));
    expect(fields.map((f) => [f.label, f.kind])).toEqual([["Endpoint", "longText"], ["Retries", "number"], ["Sign", "boolean"], ["Labels", "keyValue"]]);
  });

  it("does not repeat a setting the descriptor already shows", () => {
    const { def } = resolveNodeType("http", "service.http", catalog);
    const extra = actionFields("service.http", catalog, coveredKeys(def));
    const own = new Set(coveredKeys(def));
    for (const f of extra) expect(own.has(f.at.join("."))).toBe(false);
  });
});

describe("summary rows on a card", () => {
  const settings = (o: Record<string, string>) => o;
  const facts = (typeName: string, uses: string | undefined, s: Record<string, string>) => ({ id: "n", typeName, uses, requires: [], provides: [], settings: settings(s) });

  it("a web call reads as 'Calls GET https://…'", () => {
    const rows = rowsFor(facts("http", "service.http", { "config.method": "GET", "config.url": '"https://x.test/a"' }), catalog);
    expect(rows).toEqual([{ label: "Calls", value: "GET https://x.test/a", code: true }]);
  });

  it("an email says who and what", () => {
    const rows = rowsFor(facts("email", "service.smtp", { "config.to": '["a@x.test"]', "config.subject": '"Welcome"' }), catalog);
    expect(rows.map((r) => r.label)).toEqual(["To", "Subject"]);
    expect(rows[1]!.value).toBe("Welcome");
  });

  it("a rate limit reads as 'Allows 10 per 1m'", () => {
    const rows = rowsFor(facts("rate_limit", "rate_limit.check", { "config.limit": "10", "config.window": "1m", "config.key": '"principal.id"' }), catalog);
    expect(rows[0]).toEqual({ label: "Allows", value: "10 per 1m" });
  });

  it("a branch lists its cases as ways out", () => {
    const rows = rowsFor(facts("branch", "flow.branch", { "config.cases": '[{ name "big" condition "x > 1" intent "a.big" }, { name "small" intent "a.small" }]', "config.default_intent": '"a.other"' }), catalog);
    expect(rows.map((r) => [r.label, r.value, r.dot])).toEqual([["big", "a.big", true], ["small", "a.small", true], ["otherwise", "a.other", true]]);
  });

  it("a step nothing is known about is described by its action", () => {
    expect(rowsFor(facts("brand_new", "x.y", {}), catalog)).toEqual([{ label: "Action", value: "x.y", code: true }]);
  });

  it("in a process, a step says what it runs and who it waits for", () => {
    const rows = rowsFor(facts("approval", undefined, { intent: '"todo.review"', "task.role": '"reviewer"' }), catalog, "process");
    expect(rows).toEqual([{ label: "Runs", value: "todo.review", code: true }, { label: "Waits for", value: "a person (reviewer)" }]);
  });
});
