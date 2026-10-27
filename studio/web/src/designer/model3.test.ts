import { describe, expect, it } from "vitest";
import type { BlockNode } from "../api/types";
import {
  addActionOp, blockBody, contextRefs, copyFormOps, deleteFormOps, deleteSharedOps, detachSharedOps, formCopyBody, formUsage, parseCatalog, parseForms, parseStages,
  pipelinesIn, renameSharedOps, setActionOp, setStageOp, shareInlineOps, stageTargets, type WorkspaceModel,
} from "./model";

const f = (name: string, raw: string): BlockNode => ({ kind: "field", name, raw, path: "x/" + name, line: 0, start: 0, end: 0 });
const blk = (type: string, id: string | undefined, path: string, children: BlockNode[]): BlockNode => ({ kind: "block", type, id, path, children, line: 0, start: 0, end: 0 });
const P = "pipeline/p";

function pipeline(): BlockNode {
  return blk("pipeline", "p", P, [
    blk("input", "email", `${P}/input/email`, [f("label", '"Email"'), f("kind", "email"), f("help", '"For queries"')]),
    blk("form", "contact", `${P}/form/contact`, [f("title", '"Contact"'), f("fields", '["email"]'), blk("input", "phone", `${P}/form/contact/input/phone`, [f("label", '"Phone"'), f("kind", "phone")])]),
    blk("form", "billing", `${P}/form/billing`, [f("title", '"Billing"'), f("fields", '["email"]')]),
    blk("stage", "intake", `${P}/stage/intake`, [
      f("title", '"Intake"'), f("roles", '["clerk"]'), f("requires", '["x"]'),
      blk("page", undefined, `${P}/stage/intake/page`, [blk("group", "g", `${P}/stage/intake/page/group/g`, [f("forms", '["contact", "billing"]')])]),
      blk("action", "submit", `${P}/stage/intake/action/submit`, [f("label", '"Submit"'), f("outcome", "advance"), f("condition", `"actor.roles has 'clerk'"`), f("next", '"review"')]),
    ]),
    blk("stage", "review", `${P}/stage/review`, [f("title", '"Review"'), f("skip_if", `"case.org_unit == 'wi_a'"`), f("entry_conditions", `["contact.phone not empty", "billing.x == 1"]`), f("complete", "any"), f("auto_advance", "true")]),
    blk("stage", "done", `${P}/stage/done`, []),
  ]);
}

describe("stage flow", () => {
  const stages = () => parseStages(pipeline());
  it("parses skip, entry conditions, requires, completion and actions", () => {
    const [intake, review] = stages();
    expect(intake).toMatchObject({ roles: ["clerk"], requires: ["x"], skipIf: "" });
    expect(intake!.actions[0]).toMatchObject({ name: "submit", outcome: "advance", condition: "actor.roles has 'clerk'", next: "review" });
    expect(review).toMatchObject({ skipIf: "case.org_unit == 'wi_a'", entryConditions: ["contact.phone not empty", "billing.x == 1"], complete: "any", autoAdvance: true });
  });
  it("writes stage settings and clears empty ones", () => {
    const [intake] = stages();
    expect(setStageOp("a.bcl", intake!, "skip_if", "case.status == 'draft'")).toMatchObject({ op: "setField", value: '"case.status == \'draft\'"' });
    expect(setStageOp("a.bcl", intake!, "skip_if", "")).toMatchObject({ op: "removeField" });
    expect(setStageOp("a.bcl", intake!, "entry_conditions", ["a.b not empty"])).toMatchObject({ value: '["a.b not empty"]' });
    expect(setStageOp("a.bcl", intake!, "requires", [])).toMatchObject({ op: "removeField" });
    expect(setStageOp("a.bcl", intake!, "complete", "quorum")).toMatchObject({ value: "quorum" });
    expect(setStageOp("a.bcl", intake!, "auto_advance", true)).toMatchObject({ value: "true" });
    expect(setStageOp("a.bcl", intake!, "next", "done")).toMatchObject({ value: '"done"' });
  });
  it("writes action settings", () => {
    const a = stages()[0]!.actions[0]!;
    expect(setActionOp("a.bcl", a, "outcome", "reject")).toMatchObject({ value: "reject" });
    expect(setActionOp("a.bcl", a, "condition", "")).toMatchObject({ op: "removeField" });
    expect(setActionOp("a.bcl", a, "roles", ["qa"])).toMatchObject({ value: '["qa"]' });
    expect(setActionOp("a.bcl", a, "comment_required", true)).toMatchObject({ value: "true" });
    expect(addActionOp("a.bcl", stages()[0]!)).toMatchObject({ op: "addBlock", type: "action", id: "action", parent: `${P}/stage/intake` });
  });
  it("summarises where a stage leads", () => {
    const s = stages();
    expect(stageTargets(s, 0)).toEqual(["review"]);
    expect(stageTargets(s, 1)).toEqual(["done"]); // no next: the stage after it
  });
  it("lists both the usual next stage and where a return action sends the case", () => {
    const qa = parseStages(blk("pipeline", "p", P, [
      blk("stage", "coding", `${P}/stage/coding`, []),
      blk("stage", "qa", `${P}/stage/qa`, [blk("action", "approve", `${P}/stage/qa/action/approve`, [f("outcome", "approve")]), blk("action", "back", `${P}/stage/qa/action/back`, [f("outcome", "return")])]),
      blk("stage", "entry", `${P}/stage/entry`, []),
    ]));
    expect(stageTargets(qa, 1).sort()).toEqual(["coding", "entry"]);
    expect(stageTargets(qa, 2)).toEqual([]);
  });
});

describe("conditions context", () => {
  it("offers workspace, user, case and stage references", () => {
    const ws = [{ nodes: [{ id: "wi_a", parent: "", level: "workitem", name: "ER" }] }] as unknown as WorkspaceModel[];
    const refs = contextRefs(ws, parseStages(pipeline()));
    const byRef = Object.fromEntries(refs.map((r) => [r.ref, r]));
    expect(byRef["case.org_unit"]).toMatchObject({ kind: "workspace", options: ["wi_a"], labels: { wi_a: "ER (workitem)" } });
    expect(byRef["actor.roles"]).toMatchObject({ kind: "roles", options: ["clerk"] });
    expect(byRef["stages.review.status"]!.options).toContain("completed");
    expect(contextRefs([], parseStages(pipeline())).some((r) => r.ref === "case.org_unit")).toBe(false);
  });
});

describe("shared field snippets", () => {
  const forms = () => parseForms(pipeline());
  const shared = () => parseCatalog(pipeline())[0]!;
  it("renames a shared field in the catalog and in every form that lists it", () => {
    const ops = renameSharedOps("a.bcl", P, forms(), shared(), "contact_email");
    expect(ops[0]).toEqual({ op: "renameBlock", file: "a.bcl", path: `${P}/input/email`, newId: "contact_email" });
    expect(ops.slice(1).map((o) => (o as { path: string; value: string }).path + "=" + (o as { value: string }).value)).toEqual([`${P}/form/contact/fields=["contact_email"]`, `${P}/form/billing/fields=["contact_email"]`]);
  });
  it("deletes it everywhere, removing a now-empty fields list", () => {
    const ops = deleteSharedOps("a.bcl", forms(), shared());
    expect(ops).toEqual([
      { op: "removeField", file: "a.bcl", path: `${P}/form/contact/fields` },
      { op: "removeField", file: "a.bcl", path: `${P}/form/billing/fields` },
      { op: "removeBlock", file: "a.bcl", path: `${P}/input/email` },
    ]);
  });
  it("detaches into an inline copy for one form only", () => {
    const [contact] = forms();
    const ops = detachSharedOps("a.bcl", contact!, shared());
    expect(ops[0]).toMatchObject({ op: "addBlock", parent: `${P}/form/contact`, type: "input", id: "email" });
    expect((ops[0] as { body: string }).body).toBe('label "Email"\nkind email\nhelp "For queries"');
    expect(ops[1]).toMatchObject({ op: "removeField", path: `${P}/form/contact/fields` });
  });
  it("shares an inline field: catalog entry, removal, and a reference", () => {
    const [contact] = forms();
    const phone = contact!.inputs[0]!;
    const ops = shareInlineOps("a.bcl", P, contact!, parseCatalog(pipeline()), phone);
    expect(ops[0]).toMatchObject({ op: "addBlock", parent: P, id: "phone" });
    expect(ops[1]).toEqual({ op: "removeBlock", file: "a.bcl", path: `${P}/form/contact/input/phone` });
    expect(ops[2]).toEqual({ op: "setField", file: "a.bcl", path: `${P}/form/contact/fields`, value: '["email", "phone"]' });
  });
});

describe("form snippets", () => {
  it("writes a block's contents back out as source", () => {
    const form = pipeline().children!.find((c) => c.id === "contact")!;
    expect(blockBody(form)).toContain('input "phone" {\n  label "Phone"\n  kind phone\n}');
  });
  it("finds where a form is used", () => {
    expect(formUsage(parseStages(pipeline()), "contact")).toEqual(["Intake › g"]);
    expect(formUsage(parseStages(pipeline()), "nope")).toEqual([]);
  });
  it("copies a form, writing its shared fields out as inline inputs so it works in another pipeline", () => {
    const [contact] = parseForms(pipeline());
    const body = formCopyBody(contact!, parseCatalog(pipeline()), " (copy)");
    expect(body).toContain('title "Contact (copy)"');
    expect(body).toContain('input "phone"');
    expect(body).toContain('input "email" {');
    expect(body).not.toContain("fields");
    const [op] = copyFormOps("b.bcl", "pipeline/q", [], contact!, parseCatalog(pipeline()), " (copy)");
    expect(op).toMatchObject({ op: "addBlock", parent: "pipeline/q", type: "form", id: "contact" });
  });
  it("keeps shared-field references when duplicating within the same pipeline", () => {
    const [contact] = parseForms(pipeline());
    const body = formCopyBody(contact!, parseCatalog(pipeline()), " (copy)", true);
    expect(body).toContain('fields ["email"]');
    expect(body).not.toContain('input "email"');
    expect(body).toContain('input "phone"');
  });
  it("points a copy's rules at its own fields, not the original form's", () => {
    const orig = blk("pipeline", "p", P, [blk("form", "billing", `${P}/form/billing`, [f("title", '"Billing"'),
      blk("input", "status", `${P}/form/billing/input/status`, [f("kind", "select")]),
      blk("input", "reason", `${P}/form/billing/input/reason`, [f("kind", "textarea"), f("visible_if", `"billing.status == 'Hold'"`), f("required_if", `"billing.status == 'Hold' && other.status == 'x'"`)])])]);
    const [form] = parseForms(orig);
    const [op] = copyFormOps("a.bcl", P, [form!], form!, [], " (copy)", true) as { id: string; body: string }[];
    expect(op!.id).toBe("billing_2");
    expect(op!.body).toContain(`visible_if "billing_2.status == 'Hold'"`);
    expect(op!.body).toContain(`other.status == 'x'`); // another form's field is left alone
    expect(op!.body).not.toContain("billing.status");
    expect(formCopyBody(form!, [], "", true)).toContain("billing.status"); // an unrenamed copy is unchanged
  });
  it("gives a duplicate a unique name in the same pipeline", () => {
    const fs = parseForms(pipeline());
    expect(copyFormOps("a.bcl", P, fs, fs[0]!, [], " copy")[0]).toMatchObject({ id: "contact_2" });
  });
  it("deletes a form and takes it out of the pages that show it", () => {
    const [contact] = parseForms(pipeline());
    expect(deleteFormOps("a.bcl", parseStages(pipeline()), contact!)).toEqual([
      { op: "setField", file: "a.bcl", path: `${P}/stage/intake/page/group/g/forms`, value: '["billing"]' },
      { op: "removeBlock", file: "a.bcl", path: `${P}/form/contact` },
    ]);
  });
  it("lists every pipeline in the working copy with its forms and catalog", () => {
    const refs = pipelinesIn({ "a.bcl": [pipeline()], "b.bcl": [blk("pipeline", "q", "pipeline/q", [])] });
    expect(refs.map((r) => [r.file, r.name, r.forms.length, r.catalog.length])).toEqual([["a.bcl", "p", 2, 1], ["b.bcl", "q", 0, 0]]);
  });
});
