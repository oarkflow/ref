import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { RuleEditor } from "./RuleEditor";
import type { FieldRef } from "./model";

const fields: FieldRef[] = [
  { ref: "billing.claim_status", label: "Charge posting › Claim status", kind: "select", options: ["Ready", "Hold"] },
  { ref: "routing.request_qa", label: "Routing › Request QA", kind: "boolean", options: [] },
  { ref: "billing.charge_total", label: "Charge posting › Total", kind: "number", options: [] },
];

function setup(value: string) {
  const onCommit = vi.fn();
  render(<RuleEditor label="Show only when" value={value} fields={fields} onCommit={onCommit} />);
  return onCommit;
}

describe("RuleEditor", () => {
  it("shows an existing simple condition as rows and edits a value", () => {
    const onCommit = setup("billing.claim_status == 'Hold'");
    expect(screen.getAllByLabelText("Field")[0]).toHaveValue("billing.claim_status");
    fireEvent.change(screen.getByLabelText("Value"), { target: { value: "Ready" } });
    expect(onCommit).toHaveBeenCalledWith("billing.claim_status == 'Ready'");
  });
  it("adds a condition and joins with and, then switches to or", () => {
    const onCommit = setup("billing.claim_status == 'Hold'");
    fireEvent.click(screen.getByText("+ Add a condition"));
    expect(onCommit).toHaveBeenLastCalledWith("billing.claim_status == 'Hold' && billing.claim_status == 'Ready'");
  });
  it("switches the join between and and or", () => {
    const onCommit = setup("billing.claim_status == 'Hold' && routing.request_qa == true");
    fireEvent.click(screen.getByText("and"));
    expect(onCommit).toHaveBeenCalledWith("billing.claim_status == 'Hold' || routing.request_qa == true");
  });
  it("changes the field, resetting the operator and value for its kind", () => {
    const onCommit = setup("billing.claim_status == 'Hold'");
    fireEvent.change(screen.getAllByLabelText("Field")[0]!, { target: { value: "routing.request_qa" } });
    expect(onCommit).toHaveBeenCalledWith("routing.request_qa == true");
  });
  it("offers comparisons only for numbers and 'is empty' for text", () => {
    setup("billing.charge_total >= 10");
    const ops = [...screen.getByLabelText("Operator").querySelectorAll("option")].map((o) => o.textContent);
    expect(ops).toContain("is greater than");
    expect(ops).toContain("is empty");
  });
  it("removes a condition, leaving no rule when it was the only one", () => {
    const onCommit = setup("billing.claim_status == 'Hold'");
    fireEvent.click(screen.getByLabelText("Remove condition"));
    expect(onCommit).toHaveBeenCalledWith("");
  });
  it("keeps a condition the builder cannot show as text and never rewrites it on its own", () => {
    const onCommit = setup("a.x == 1 && (a.y == 2 || a.z == 3)");
    expect(screen.getByLabelText("Show only when")).toHaveValue("a.x == 1 && (a.y == 2 || a.z == 3)");
    expect(screen.getByText(/more than the builder can show/)).toBeInTheDocument();
    fireEvent.blur(screen.getByLabelText("Show only when"));
    expect(onCommit).not.toHaveBeenCalled();
  });
  it("lets a simple condition be edited as text", () => {
    const onCommit = setup("billing.claim_status == 'Hold'");
    fireEvent.click(screen.getByText("Edit as text"));
    const box = screen.getByLabelText("Show only when");
    fireEvent.change(box, { target: { value: "billing.claim_status != 'Ready'" } });
    fireEvent.blur(box);
    expect(onCommit).toHaveBeenCalledWith("billing.claim_status != 'Ready'");
  });

  it("starts a new row on a plain text field as 'is filled in', never as 'is empty'", () => {
    const onCommit = vi.fn();
    render(<RuleEditor label="Show only when" value="" fields={[{ ref: "patient.mrn", label: "Patient › MRN", kind: "text", options: [] }]} onCommit={onCommit} />);
    fireEvent.click(screen.getByText("+ Add a condition"));
    expect(onCommit).toHaveBeenCalledWith("patient.mrn not empty");
  });

  it("flags a comparison with a blank value and offers the safe form in one click", () => {
    const onCommit = setup("billing.claim_status != ''");
    expect(screen.getByText(/can misfire/)).toBeInTheDocument();
    fireEvent.click(screen.getByText(/Use "is filled in" instead/));
    expect(onCommit).toHaveBeenCalledWith("billing.claim_status not empty");
  });
});
