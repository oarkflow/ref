import { describe, expect, it } from "vitest";
import type { BlockNode } from "../api/types";
import { connectSourceOp, createServiceOps, hostOf, parseConnections } from "./model";
import { addSharedInputOps, usesOfShared } from "./model";
import { defFromNew, defFromRaw, setDeps } from "./model";
import { addSetOp, createSourceOps, findCasesResource, parseLookupSources, setText } from "./model";
import { spanMinutes } from "./SlaPanel";
import { addEscalationOp, addLookupOp, appendLookupRaw, lookupSets, objectsOf, parseCalendars, parseCatalog, parseForms, parseInput, parseSla, parseWorkspaces, setPropOp, setSlaOp, splitList } from "./model";

const f = (name: string, raw: string): BlockNode => ({ kind: "field", name, raw, path: "x/" + name, line: 0, start: 0, end: 0 });
const blk = (type: string, id: string | undefined, path: string, children: BlockNode[]): BlockNode => ({ kind: "block", type, id, path, children, line: 0, start: 0, end: 0 });

const LOOKUPS = `[
      # Modifiers: the base list, then per-workspace changes.
      { set "modifiers" code "25" label "25 - Significant, separately identifiable E/M" sort 1 },
      { set "modifiers" code "91" node "wi_memorial_er" disabled true },
      { set "em_codes" code "99283" label "99283 - ED level 3" sort 3 attrs { mdm_level ["Low", "Moderate"] } }
    ]`;
const NODES = `[
      { id "comp_clear_01" level "company" name "Edelberg Care Associates" },
      { id "fac_memorial" parent "comp_clear_01" level "facility" name "Memorial General Hospital" }
    ]`;

describe("extended input properties", () => {
  const node = blk("input", "em_code", "pipeline/p/form/em/input/em_code", [
    f("label", '"Assigned E&M code"'), f("kind", "select"), f("lookup", '"em_codes"'), f("depends_on", '["mdm_level"]'),
    f("visible_if", `"em.em_code != ''"`), f("required_if", `"em.a == 'x'"`), f("view_roles", '["coder", "qa"]'), f("pii", "true"), f("min", '"1"'),
  ]);
  it("parses lookup, dependency, rules, roles and flags", () => {
    expect(parseInput(node)).toMatchObject({ lookup: "em_codes", dependsOn: ["mdm_level"], visibleIf: "em.em_code != ''", requiredIf: "em.a == 'x'", viewRoles: ["coder", "qa"], pii: true, min: "1" });
  });
  it("writes them back", () => {
    const i = parseInput(node);
    expect(setPropOp("a.bcl", i, "lookup", "modifiers")).toMatchObject({ value: '"modifiers"' });
    expect(setPropOp("a.bcl", i, "depends_on", ["a", "b"])).toMatchObject({ value: '["a", "b"]' });
    expect(setPropOp("a.bcl", i, "visible_if", "x == 1")).toMatchObject({ value: '"x == 1"' });
    expect(setPropOp("a.bcl", i, "pii", true)).toMatchObject({ value: "true" });
    expect(setPropOp("a.bcl", i, "lookup", "")).toMatchObject({ op: "removeField" });
    expect(splitList(" a, b ,, c ")).toEqual(["a", "b", "c"]);
  });
});

describe("shared catalog fields", () => {
  const pipe = blk("pipeline", "p", "pipeline/p", [
    blk("input", "email", "pipeline/p/input/email", [f("label", '"Email"'), f("kind", "email")]),
    blk("form", "contact", "pipeline/p/form/contact", [f("title", '"Contact"'), f("fields", '["email", "missing"]')]),
  ]);
  it("resolves a form's fields against the catalog", () => {
    expect(parseCatalog(pipe).map((i) => i.name)).toEqual(["email"]);
    const [form] = parseForms(pipe);
    expect(form!.fields).toEqual(["email", "missing"]);
    expect(form!.shared.map((i) => i.name)).toEqual(["email"]);
  });
});

describe("sla and calendars", () => {
  const stage = blk("stage", "coding", "pipeline/p/stage/coding", [
    blk("sla", undefined, "pipeline/p/stage/coding/sla", [
      f("duration", '"24h"'), f("warn_before", '"4h"'), f("calendar", '"clinic_hours"'), f("on_breach", "notify"),
      blk("escalate", "supervisor", "pipeline/p/stage/coding/sla/escalate/supervisor", [f("after", '"12h"'), f("assign_roles", '["manager"]'), f("notify", '["coding-managers"]')]),
    ]),
  ]);
  it("parses the sla block and its escalations", () => {
    const sla = parseSla(stage)!;
    expect(sla).toMatchObject({ duration: "24h", warnBefore: "4h", calendar: "clinic_hours", onBreach: "notify" });
    expect(sla.escalations[0]).toMatchObject({ name: "supervisor", after: "12h", assignRoles: ["manager"], notify: ["coding-managers"] });
  });
  it("writes bare keywords for breach actions and quotes action names", () => {
    const sla = parseSla(stage)!;
    expect(setSlaOp("a.bcl", sla, "on_breach", "reassign")).toMatchObject({ value: "reassign" });
    expect(setSlaOp("a.bcl", sla, "on_breach", "submit")).toMatchObject({ value: '"submit"' });
    expect(setSlaOp("a.bcl", sla, "pickup_timeout", "")).toMatchObject({ op: "removeField" });
    expect(addEscalationOp("a.bcl", sla)).toMatchObject({ op: "addBlock", id: "level", parent: "pipeline/p/stage/coding/sla" });
  });
  it("parses calendars", () => {
    const pipe = blk("pipeline", "p", "pipeline/p", [blk("calendar", "clinic_hours", "pipeline/p/calendar/clinic_hours", [f("timezone", '"UTC"'), f("hours", '"mon-fri 08:00-18:00"')])]);
    expect(parseCalendars(pipe)[0]).toMatchObject({ name: "clinic_hours", timezone: "UTC", hours: "mon-fri 08:00-18:00" });
  });
});

describe("workspaces and lookups", () => {
  const cfg = blk("config", undefined, "resource/org/config", [f("levels", '["company", "facility"]'), f("nodes", NODES), { ...f("lookups", LOOKUPS), path: "resource/org/config/lookups" }]);
  const res = blk("resource", "org", "resource/org", [f("kind", '"org.hierarchy"'), cfg]);
  const ws = parseWorkspaces({ "r.bcl": [res] });
  it("finds org.hierarchy resources, nodes and lookup entries", () => {
    expect(ws).toHaveLength(1);
    expect(ws[0]!.levels).toEqual(["company", "facility"]);
    expect(ws[0]!.nodes.map((n) => [n.id, n.parent, n.level])).toEqual([["comp_clear_01", "", "company"], ["fac_memorial", "comp_clear_01", "facility"]]);
    expect(ws[0]!.lookups.map((l) => [l.set, l.code, l.node, l.disabled])).toEqual([["modifiers", "25", "", false], ["modifiers", "91", "wi_memorial_er", true], ["em_codes", "99283", "", false]]);
    expect(lookupSets(ws)).toEqual(["modifiers", "em_codes"]);
  });
  it("lists only top-level objects, ignoring nested attrs and comments", () => {
    expect(objectsOf(LOOKUPS)).toHaveLength(3);
  });
  it("appends an entry without touching comments or existing entries", () => {
    const next = appendLookupRaw(LOOKUPS, { set: "modifiers", code: "GT", label: 'GT "tele"', sort: 9 });
    expect(next).toContain("# Modifiers: the base list");
    expect(next).toContain('{ set "modifiers" code "25"');
    expect(next).toMatch(/\},\n\s+\{ set "modifiers" code "GT" label "GT \\"tele\\"" sort 9 \}\n\s+\]$/);
    expect(objectsOf(next)).toHaveLength(4);
    expect(addLookupOp("r.bcl", ws[0]!, { set: "x", code: "1", label: "One" })).toMatchObject({ op: "setField", path: "resource/org/config/lookups" });
  });
  it("handles an empty list", () => {
    expect(appendLookupRaw("[]", { set: "a", code: "1", label: "A" })).toBe('[\n      { set "a" code "1" label "A" }\n    ]');
  });
});

describe("span minutes", () => {
  it("adds up compound spans and rejects junk", () => {
    expect(spanMinutes("1d4h30m")).toBe(1440 + 240 + 30);
    expect(spanMinutes("90m")).toBe(90);
    expect(spanMinutes("soon")).toBeNull();
  });
});

describe("lookup sources", () => {
  const SETS = `[
      { name "sex" type "static" entries [ { value "F" label "Female" }, { value "M" label "Male" } ] },
      # comment kept
      { name "em_codes" type "sql" query "SELECT code AS value, label FROM em WHERE mdm = ?" args ["mdm"] },
      { name "payers" type "http" path "/v1/payers" items_path "data" }
    ]`;
  const res = blk("resource", "catalog", "resource/catalog", [f("kind", '"lookup.sources"'), blk("config", undefined, "resource/catalog/config", [f("database", '"db"'), { ...f("sets", SETS), path: "resource/catalog/config/sets" }])]);
  const trees = { "r.bcl": [res] };
  it("lists sets with their type and detail", () => {
    const [m] = parseLookupSources(trees);
    expect(m!.hasDatabase).toBe(true);
    expect(m!.hasService).toBe(false);
    expect(m!.sets.map(({ name, type, detail }) => ({ name, type, detail }))).toEqual([
      { name: "sex", type: "static", detail: "2 entries" },
      { name: "em_codes", type: "sql", detail: "SELECT code AS value, label FROM em WHERE mdm = ?" },
      { name: "payers", type: "http", detail: "/v1/payers" },
    ]);
  });
  it("renders each kind of new set", () => {
    expect(setText({ name: "a", type: "static", entries: [{ value: "1", label: "One" }] })).toBe('{ name "a" type "static" entries [ { value "1" label "One" } ] }');
    expect(setText({ name: "b", type: "sql", query: "SELECT 1", args: ["x"], ttl: "10m" })).toBe('{ name "b" type "sql" query "SELECT 1" args ["x"] ttl "10m" }');
    expect(setText({ name: "c", type: "http", path: "/p", itemsPath: "data", valueField: "id" })).toBe('{ name "c" type "http" path "/p" items_path "data" value_field "id" }');
  });
  it("appends a set and keeps comments", () => {
    const [m] = parseLookupSources(trees);
    const op = addSetOp(m!, { name: "z", type: "static", entries: [{ value: "1", label: "" }] }) as { value: string };
    expect(op.value).toContain("# comment kept");
    expect(op.value).toContain('{ name "z" type "static" entries [ { value "1" label "1" } ] }');
    expect(objectsOf(op.value)).toHaveLength(4);
  });
  it("wires a new resource to the cases resource that lists the pipeline", () => {
    const cases = blk("resource", "cases", "resource/cases", [f("kind", '"pipeline.cases"'), blk("config", undefined, "resource/cases/config", [f("pipelines", '["medical_coding"]')])]);
    const found = findCasesResource({ "c.bcl": [cases] }, "medical_coding")!;
    expect(found).toMatchObject({ file: "c.bcl", path: "resource/cases/config", lookupResource: "" });
    expect(findCasesResource({ "c.bcl": [cases] }, "other")).toBeUndefined();
    const ops = createSourceOps("r.bcl", "catalog", found, { name: "s", type: "static", entries: [{ value: "a", label: "A" }] });
    expect(ops[0]).toMatchObject({ op: "addBlock", type: "resource", id: "catalog" });
    expect(ops[1]).toEqual({ op: "setField", file: "c.bcl", path: "resource/cases/config/lookup_resource", value: '"catalog"' });
  });
});

describe("lookup source connections", () => {
  const db = blk("resource", "database", "resource/database", [f("kind", '"database.sql"')]);
  const api = blk("resource", "payer_api", "resource/payer_api", [f("kind", '"service.http"'), blk("config", undefined, "resource/payer_api/config", [f("base_url", '"https://api.example.com/v1"')])]);
  const src = blk("resource", "catalog", "resource/catalog", [f("kind", '"lookup.sources"'), blk("config", undefined, "resource/catalog/config", [{ ...f("sets", "[]"), path: "resource/catalog/config/sets" }])]);
  it("lists databases and API services", () => {
    expect(parseConnections({ "a.bcl": [db, api, src] })).toEqual({ databases: ["database"], services: [{ name: "payer_api", baseUrl: "https://api.example.com/v1" }] });
  });
  it("connects and disconnects a source", () => {
    const [m] = parseLookupSources({ "a.bcl": [src] });
    expect(m!.database).toBe("");
    expect(connectSourceOp(m!, "database", "database")).toEqual({ op: "setField", file: "a.bcl", path: "resource/catalog/config/database", value: '"database"' });
    expect(connectSourceOp(m!, "service", "")).toMatchObject({ op: "removeField", path: "resource/catalog/config/service" });
  });
  it("derives the allowlisted host and writes only that", () => {
    expect(hostOf("https://API.Example.com:8443/v1/")).toBe("api.example.com");
    expect(hostOf("ftp://x")).toBe("");
    expect(hostOf("not a url")).toBe("");
    const [op] = createServiceOps("a.bcl", "payer_api", "https://api.example.com/v1/", false) as { body: string }[];
    expect(op!.body).toContain('base_url "https://api.example.com/v1"');
    expect(op!.body).toContain('allowed_hosts ["api.example.com"]');
    expect(op!.body).not.toContain("allow_private_networks");
    expect((createServiceOps("a.bcl", "x", "http://127.0.0.1:8765", true)[0] as { body: string }).body).toContain("allow_private_networks true");
  });
  it("renders http params and method", () => {
    expect(setText({ name: "p", type: "http", path: "/p", method: "POST", params: [{ name: "region", value: "{region}" }] })).toBe('{ name "p" type "http" path "/p" method "POST" params { region "{region}" } }');
  });
});

describe("shared fields", () => {
  const pipe = blk("pipeline", "p", "pipeline/p", [
    blk("input", "email", "pipeline/p/input/email", [f("label", '"Email"'), f("kind", "email")]),
    blk("form", "a", "pipeline/p/form/a", [f("title", '"A"'), f("fields", '["email"]')]),
    blk("form", "b", "pipeline/p/form/b", [f("title", '"B"'), f("fields", '["email"]')]),
    blk("form", "c", "pipeline/p/form/c", [f("title", '"C"')]),
  ]);
  it("creates a catalog input and references it from the form", () => {
    const forms = parseForms(pipe), catalog = parseCatalog(pipe);
    const [add, ref] = addSharedInputOps("a.bcl", "pipeline/p", forms[2]!, catalog, { kind: "text", label: "Email", hint: "" });
    expect(add).toMatchObject({ op: "addBlock", parent: "pipeline/p", type: "input", id: "email_2" });
    expect(ref).toEqual({ op: "setField", file: "a.bcl", path: "pipeline/p/form/c/fields", value: '["email_2"]' });
  });
  it("lists where a shared field is used", () => {
    expect(usesOfShared(parseForms(pipe), "email")).toEqual(["A", "B"]);
  });
});

describe("testing a set", () => {
  it("rebuilds a runnable definition from a set's text", () => {
    expect(defFromRaw('{ name "em" type "sql" query "SELECT code AS value, label FROM em WHERE m = ? AND s = ?" args ["mdm", "site"] ttl "10m" }')).toEqual({
      name: "em", type: "sql", query: "SELECT code AS value, label FROM em WHERE m = ? AND s = ?", args: ["mdm", "site"], ttl: "10m",
    });
    expect(defFromRaw('{ name "p" type "http" path "/p" items_path "data.items" value_field "id" params { region "{region}" fmt "json" } }')).toMatchObject({
      type: "http", path: "/p", items_path: "data.items", value_field: "id", params: { region: "{region}", fmt: "json" },
    });
    expect(defFromRaw('{ name "s" type "static" entries [ { value "F" label "Female" }, { value "M" label "Male" } ] }').entries).toEqual([{ value: "F", label: "Female" }, { value: "M", label: "Male" }]);
  });
  it("finds what a set needs values for", () => {
    expect(setDeps(defFromRaw('{ name "a" type "sql" query "x" args ["mdm"] }'))).toEqual(["mdm"]);
    expect(setDeps(defFromRaw('{ name "b" type "http" path "/p" params { r "{region}" q "{region}-{tier}" } }')).sort()).toEqual(["region", "tier"]);
    expect(setDeps(defFromRaw('{ name "c" type "static" entries [] }'))).toEqual([]);
  });
  it("tests a set before it is added", () => {
    expect(defFromNew({ name: "x", type: "sql", query: "SELECT 1", args: ["a"] })).toMatchObject({ type: "sql", query: "SELECT 1", args: ["a"] });
  });
});
