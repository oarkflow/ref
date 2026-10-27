import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { BlockNode, Op } from "../api/types";
import { fieldRefs, contextRefs, parseCatalog, parseForms, parseStages, pipelinesIn } from "./model";
import { SnippetsPanel } from "./SnippetsPanel";
import { StagePanel } from "./StagePanel";

const f = (name: string, raw: string): BlockNode => ({ kind: "field", name, raw, path: "x/" + name, line: 0, start: 0, end: 0 });
const blk = (type: string, id: string | undefined, path: string, children: BlockNode[]): BlockNode => ({ kind: "block", type, id, path, children, line: 0, start: 0, end: 0 });
const P = "pipeline/p";
const pipe = () => blk("pipeline", "p", P, [
  blk("input", "email", `${P}/input/email`, [f("label", '"Email"'), f("kind", "email")]),
  blk("form", "contact", `${P}/form/contact`, [f("title", '"Contact"'), f("fields", '["email"]'), blk("input", "phone", `${P}/form/contact/input/phone`, [f("label", '"Phone"'), f("kind", "text")])]),
  blk("form", "billing", `${P}/form/billing`, [f("title", '"Billing"'), f("fields", '["email"]')]),
  blk("stage", "intake", `${P}/stage/intake`, [f("title", '"Intake"'), blk("page", undefined, `${P}/stage/intake/page`, [blk("group", "g", `${P}/stage/intake/page/group/g`, [f("forms", '["contact"]')])]),
    blk("action", "submit", `${P}/stage/intake/action/submit`, [f("label", '"Submit"'), f("outcome", "advance")])]),
  blk("stage", "review", `${P}/stage/review`, [f("title", '"Review"'), f("skip_if", `"case.status == 'draft'"`), f("entry_conditions", `["contact.phone not empty"]`)]),
]);
const other = blk("pipeline", "q", "pipeline/q", [blk("form", "kyc", "pipeline/q/form/kyc", [f("title", '"KYC"'), blk("input", "id", "pipeline/q/form/kyc/input/id", [f("kind", "text")])])]);

describe("StagePanel", () => {
  const setup = () => {
    const apply = vi.fn();
    const p = pipe();
    render(<StagePanel file="a.bcl" pipeline={p} refs={[...fieldRefs(parseForms(p)), ...contextRefs([], parseStages(p))]} canEdit apply={apply} />);
    return apply;
  };
  it("shows the stage order with each stage's conditions", () => {
    setup();
    const flow = screen.getByLabelText("Stage order");
    expect(within(flow).getByText(/Review/)).toBeInTheDocument();
    expect(within(flow).getByText(/can be skipped · 1 entry rule/)).toBeInTheDocument();
  });
  it("edits a stage's skip rule and entry rules through the builder", () => {
    const apply = setup();
    fireEvent.click(within(screen.getByLabelText("Stage order")).getByText(/Review/));
    expect(screen.getByLabelText("Rules for review")).toBeInTheDocument();
    fireEvent.click(screen.getAllByLabelText("Remove condition")[0]!); // the skip rule's only row
    expect(apply).toHaveBeenLastCalledWith([expect.objectContaining({ op: "removeField", path: `${P}/stage/review/skip_if` })]);
    fireEvent.click(screen.getByText("+ Add an entry rule"));
    expect(apply).toHaveBeenLastCalledWith([expect.objectContaining({ op: "setField", path: `${P}/stage/review/entry_conditions`, value: '["contact.phone not empty", "contact.phone not empty"]' })]);
  });
  it("sets required stages and the next stage", () => {
    const apply = setup();
    fireEvent.click(screen.getByLabelText("Review")); // intake needs review completed
    expect(apply).toHaveBeenLastCalledWith([expect.objectContaining({ path: `${P}/stage/intake/requires`, value: '["review"]' })]);
    fireEvent.change(screen.getByLabelText("After this stage, go to"), { target: { value: "review" } });
    expect(apply).toHaveBeenLastCalledWith([expect.objectContaining({ path: `${P}/stage/intake/next`, value: '"review"' })]);
  });
  it("adds and edits an action's condition", () => {
    const apply = setup();
    fireEvent.click(screen.getByText("+ Add an action"));
    expect(apply).toHaveBeenLastCalledWith([expect.objectContaining({ op: "addBlock", type: "action", id: "action" })]);
    fireEvent.change(screen.getByLabelText("What it does"), { target: { value: "reject" } });
    expect(apply).toHaveBeenLastCalledWith([expect.objectContaining({ path: `${P}/stage/intake/action/submit/outcome`, value: "reject" })]);
  });
});

describe("SnippetsPanel", () => {
  const setup = () => {
    const apply = vi.fn(), onOpenForm = vi.fn();
    const p = pipe();
    const forms = parseForms(p);
    render(<SnippetsPanel file="a.bcl" pipelinePath={P} pipelineName="p" forms={forms} catalog={parseCatalog(p)} stages={parseStages(p)} pipelines={pipelinesIn({ "a.bcl": [p], "b.bcl": [other] })} canEdit apply={apply} onOpenForm={onOpenForm} />);
    return { apply, onOpenForm };
  };
  it("lists shared fields with the forms that use them", () => {
    setup();
    expect(screen.getByText(/Used by Contact, Billing/)).toBeInTheDocument();
  });
  it("renames a shared field everywhere", () => {
    const { apply } = setup();
    fireEvent.change(screen.getByLabelText("Name of email"), { target: { value: "contact_email" } });
    fireEvent.click(screen.getByText(/Rename \(updates 2 forms\)/));
    const ops = apply.mock.calls[0]![0] as Op[];
    expect(ops[0]).toMatchObject({ op: "renameBlock", newId: "contact_email" });
    expect(ops).toHaveLength(3);
  });
  it("asks before deleting a shared field in use, then removes it from its forms", () => {
    const { apply } = setup();
    fireEvent.click(screen.getAllByText("Delete")[0]!);
    expect(apply).not.toHaveBeenCalled();
    expect(screen.getByText(/remove it from 2 forms\?/)).toBeInTheDocument();
    fireEvent.click(screen.getByText("Yes, delete"));
    expect((apply.mock.calls[0]![0] as Op[]).map((o) => o.op)).toEqual(["removeField", "removeField", "removeBlock"]);
  });
  it("can cancel a delete", () => {
    const { apply } = setup();
    fireEvent.click(screen.getAllByText("Delete")[0]!);
    fireEvent.click(screen.getByText("Cancel"));
    expect(apply).not.toHaveBeenCalled();
    expect(screen.getAllByText("Delete").length).toBeGreaterThan(0);
  });
  it("duplicates a form keeping its shared fields, and opens a form", () => {
    const { apply, onOpenForm } = setup();
    fireEvent.click(screen.getAllByText("Duplicate")[0]!);
    expect(apply.mock.calls[0]![0][0]).toMatchObject({ op: "addBlock", type: "form", id: "contact_2" });
    expect((apply.mock.calls[0]![0][0] as { body: string }).body).toContain('fields ["email"]');
    fireEvent.click(screen.getAllByText("Open")[1]!);
    expect(onOpenForm).toHaveBeenCalledWith("billing");
  });
  it("copies a form from another pipeline", () => {
    const { apply } = setup();
    fireEvent.click(screen.getByText("Copy here"));
    expect(apply.mock.calls[0]![0][0]).toMatchObject({ op: "addBlock", parent: P, type: "form", id: "kyc" });
  });
  it("warns before deleting a form that pages show", () => {
    const { apply } = setup();
    const row = screen.getByText("Contact").closest("tr")!;
    fireEvent.click(within(row).getByText("Delete"));
    expect(screen.getByText(/remove it from 1 page group\?/)).toBeInTheDocument();
    fireEvent.click(screen.getByText("Yes, delete"));
    expect(apply.mock.calls[0]![0]).toEqual([
      expect.objectContaining({ op: "setField", path: `${P}/stage/intake/page/group/g/forms`, value: "[]" }),
      expect.objectContaining({ op: "removeBlock", path: `${P}/form/contact` }),
    ]);
  });
});
