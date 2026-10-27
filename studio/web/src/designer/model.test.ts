import { describe, expect, it } from "vitest";
import type { BlockNode } from "../api/types";
import { fieldRefs, setFormPropOp } from "./model";
import { addGroupOp, addInputOps, moveFormOps, moveGroupOps, moveInputOps, parseForms, parseStages, PALETTE, setGroupPropOp, setPageOp, setPropOp, uniqueName, unplacedForms } from "./model";

const f = (name: string, raw: string): BlockNode => ({ kind: "field", name, raw, path: "x/" + name, line: 0, start: 0, end: 0 });
const input = (id: string, kids: BlockNode[]): BlockNode => ({ kind: "block", type: "input", id, path: `pipeline/p/form/a/input/${id}`, children: kids, line: 0, start: 0, end: 0 });
const pipeline: BlockNode = {
  kind: "block", type: "pipeline", id: "p", path: "pipeline/p", line: 0, start: 0, end: 0,
  children: [
    { kind: "block", type: "form", id: "a", path: "pipeline/p/form/a", line: 0, start: 0, end: 0, children: [
      f("title", '"Form A"'),
      input("name", [f("label", '"Name"'), f("kind", "text"), f("required", "true"), f("span", "6")]),
      input("gender", [f("label", '"Gender"'), f("kind", "select"), f("options", '["male", "female"]')]),
    ] },
  ],
};

describe("designer model", () => {
  it("parses forms and inputs", () => {
    const [form] = parseForms(pipeline);
    expect(form!.title).toBe("Form A");
    expect(form!.inputs.map((i) => i.name)).toEqual(["name", "gender"]);
    expect(form!.inputs[0]).toMatchObject({ label: "Name", kind: "text", required: true, span: 6 });
    expect(form!.inputs[1]!.options).toEqual(["male", "female"]);
  });
  it("makes unique names", () => {
    expect(uniqueName("Short text", ["short_text"])).toBe("short_text_2");
    expect(uniqueName("9 lives", [])).toBe("f_9_lives");
  });
  it("adds an input with defaults and positions it", () => {
    const [form] = parseForms(pipeline);
    const ops = addInputOps("a.bcl", form!, PALETTE.find((p) => p.kind === "select")!, 0);
    expect(ops[0]).toMatchObject({ op: "addBlock", parent: "pipeline/p/form/a", type: "input", id: "dropdown" });
    expect((ops[0] as { body: string }).body).toContain('options ["Option 1", "Option 2"]');
    expect(ops[1]).toMatchObject({ op: "moveBlock", index: 1 }); // after the title field
  });
  it("moves inputs by child position", () => {
    const [form] = parseForms(pipeline);
    expect(moveInputOps("a.bcl", form!, 0, 1)).toEqual([{ op: "moveBlock", file: "a.bcl", path: "pipeline/p/form/a/input/name", index: 2 }]);
    expect(moveInputOps("a.bcl", form!, 1, 1)).toEqual([]);
  });
  it("writes and clears properties", () => {
    const [form] = parseForms(pipeline);
    const i = form!.inputs[0]!;
    expect(setPropOp("a.bcl", i, "label", 'Say "hi"')).toMatchObject({ op: "setField", value: '"Say \\"hi\\""' });
    expect(setPropOp("a.bcl", i, "required", true)).toMatchObject({ value: "true" });
    expect(setPropOp("a.bcl", i, "required", false)).toMatchObject({ op: "removeField" });
    expect(setPropOp("a.bcl", i, "options", ["x", "y"])).toMatchObject({ value: '["x", "y"]' });
    expect(setPropOp("a.bcl", i, "kind", "date")).toMatchObject({ value: "date" });
  });
});

describe("page layout model", () => {
  const grp = (id: string, forms: string): BlockNode => ({ kind: "block", type: "group", id, path: `pipeline/p/stage/s/page/group/${id}`, line: 0, start: 0, end: 0, children: [f("title", `"${id}"`), f("forms", forms)] });
  const stagePipeline: BlockNode = {
    kind: "block", type: "pipeline", id: "p", path: "pipeline/p", line: 0, start: 0, end: 0,
    children: [
      { kind: "block", type: "form", id: "a", path: "pipeline/p/form/a", line: 0, start: 0, end: 0, children: [] },
      { kind: "block", type: "form", id: "b", path: "pipeline/p/form/b", line: 0, start: 0, end: 0, children: [] },
      { kind: "block", type: "form", id: "c", path: "pipeline/p/form/c", line: 0, start: 0, end: 0, children: [] },
      { kind: "block", type: "stage", id: "s", path: "pipeline/p/stage/s", line: 0, start: 0, end: 0, children: [
        f("title", '"Apply"'),
        { kind: "block", type: "page", path: "pipeline/p/stage/s/page", line: 0, start: 0, end: 0, children: [f("layout", "wizard"), grp("one", '["a", "b"]'), grp("two", '["c"]')] },
      ] },
    ],
  };
  const stage = () => parseStages(stagePipeline)[0]!;

  it("parses stages, pages and groups", () => {
    const s = stage();
    expect(s.title).toBe("Apply");
    expect(s.page!.layout).toBe("wizard");
    expect(s.page!.groups.map((g) => [g.name, g.forms])).toEqual([["one", ["a", "b"]], ["two", ["c"]]]);
  });
  it("moves a form between groups with two list rewrites", () => {
    const g = stage().page!.groups;
    const ops = moveFormOps("x.bcl", g, "b", 0, 1, 0);
    expect(ops).toEqual([
      { op: "setField", file: "x.bcl", path: "pipeline/p/stage/s/page/group/one/forms", value: '["a"]' },
      { op: "setField", file: "x.bcl", path: "pipeline/p/stage/s/page/group/two/forms", value: '["b", "c"]' },
    ]);
  });
  it("reorders within a group and ignores a no-op", () => {
    const g = stage().page!.groups;
    expect(moveFormOps("x.bcl", g, "b", 0, 0, 0)).toEqual([{ op: "setField", file: "x.bcl", path: "pipeline/p/stage/s/page/group/one/forms", value: '["b", "a"]' }]);
    expect(moveFormOps("x.bcl", g, "a", 0, 0, 0)).toEqual([]);
  });
  it("writes group conditions and roles", () => {
    const { page } = stage();
    const g = page!.groups[0]!;
    expect(setGroupPropOp("x.bcl", g, "visible_if", "a.b == 'x'")).toMatchObject({ op: "setField", value: '"a.b == \'x\'"' });
    expect(setGroupPropOp("x.bcl", g, "roles", ["qa", "manager"])).toMatchObject({ value: '["qa", "manager"]' });
    expect(setGroupPropOp("x.bcl", g, "edit_roles", [])).toMatchObject({ op: "removeField" });
    expect(setGroupPropOp("x.bcl", g, "visible_if", "")).toMatchObject({ op: "removeField" });
  });
  it("writes layout and group properties, omitting defaults", () => {
    const { page } = stage();
    expect(setPageOp("x.bcl", page!, "layout", "tabbed")).toMatchObject({ op: "setField", value: "tabbed" });
    expect(setPageOp("x.bcl", page!, "layout", "stacked")).toMatchObject({ op: "removeField" });
    expect(setGroupPropOp("x.bcl", page!.groups[0]!, "mode", "readonly")).toMatchObject({ value: "readonly" });
    expect(setGroupPropOp("x.bcl", page!.groups[0]!, "mode", "editable")).toMatchObject({ op: "removeField" });
    expect(setGroupPropOp("x.bcl", page!.groups[0]!, "collapsed", true)).toMatchObject({ value: "true" });
  });
  it("adds a group with a unique name and moves groups", () => {
    const { page } = stage();
    expect(addGroupOp("x.bcl", page!)).toMatchObject({ op: "addBlock", parent: "pipeline/p/stage/s/page", type: "group", id: "group" });
    expect(moveGroupOps("x.bcl", page!, 0, 1)).toEqual([{ op: "moveBlock", file: "x.bcl", path: "pipeline/p/stage/s/page/group/one", index: 2 }]);
  });
  it("lists forms no group shows", () => {
    expect(unplacedForms(parseForms(stagePipeline), stage().page)).toEqual([]);
    expect(unplacedForms(parseForms(stagePipeline), { ...stage().page!, groups: [stage().page!.groups[0]!] })).toEqual(["c"]);
  });
});

describe("form-level settings", () => {
  const pipe: BlockNode = { kind: "block", type: "pipeline", id: "p", path: "pipeline/p", line: 0, start: 0, end: 0, children: [
    { kind: "block", type: "form", id: "addr", path: "pipeline/p/form/addr", line: 0, start: 0, end: 0, children: [
      f("title", '"Addresses"'), f("repeatable", "true"), f("min_items", "1"), f("max_items", "5"), f("columns", "2"), f("visible_if", `"a.b == 'x'"`), f("edit_roles", '["clerk"]'),
      input("line", [f("label", '"Line"'), f("kind", "text")]),
    ] },
  ] };
  it("parses repeatable entries, limits, conditions and roles", () => {
    const [form] = parseForms(pipe);
    expect(form).toMatchObject({ repeatable: true, minItems: 1, maxItems: 5, columns: 2, visibleIf: "a.b == 'x'", editRoles: ["clerk"], disabledIf: "" });
  });
  it("writes them and clears empty values", () => {
    const [form] = parseForms(pipe);
    expect(setFormPropOp("a.bcl", form!, "repeatable", true)).toEqual({ op: "setField", file: "a.bcl", path: "pipeline/p/form/addr/repeatable", value: "true" });
    expect(setFormPropOp("a.bcl", form!, "repeatable", false)).toMatchObject({ op: "removeField" });
    expect(setFormPropOp("a.bcl", form!, "max_items", 8)).toMatchObject({ value: "8" });
    expect(setFormPropOp("a.bcl", form!, "visible_if", "a.b == 'y'")).toMatchObject({ value: '"a.b == \'y\'"' });
    expect(setFormPropOp("a.bcl", form!, "view_roles", ["a", "b"])).toMatchObject({ value: '["a", "b"]' });
    expect(setFormPropOp("a.bcl", form!, "edit_roles", [])).toMatchObject({ op: "removeField" });
  });
  it("lists every field as a condition reference", () => {
    expect(fieldRefs(parseForms(pipe))).toEqual([{ ref: "addr.line", label: "Addresses › Line", kind: "text", options: [] }]);
  });
});
