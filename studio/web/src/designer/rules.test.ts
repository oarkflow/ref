import { describe, expect, it } from "vitest";
import { buildCondition, isBlankComparison, opsFor, parseCondition } from "./rules";

describe("rule builder", () => {
  it("round-trips the simple comparisons pipelines use", () => {
    for (const e of ["billing.claim_status == 'Hold'", "routing.request_qa == true", "em.mdm_level == 'Moderate' && em.em_code == '99283'", "a.x != 'p' || a.y > 3", "patient.mrn != ''", "patient.fin == ''", "patient.mrn empty", "patient.mrn not empty", "a.x not empty && b.y empty"]) {
      const c = parseCondition(e);
      expect(c, e).not.toBeNull();
      expect(buildCondition(c!), e).toBe(e);
    }
  });
  it("understands empty and filled as the language's own keywords", () => {
    expect(parseCondition("a.b empty")!.clauses[0]).toMatchObject({ op: "empty" });
    expect(parseCondition("a.b not empty")!.clauses[0]).toMatchObject({ op: "filled" });
    expect(buildCondition({ join: "&&", clauses: [{ field: "a.b", op: "filled", value: "", kind: "string" }] })).toBe("a.b not empty");
  });
  it("does not mistake == '' for 'is empty': a field never filled in is not equal to ''", () => {
    expect(parseCondition("a.b == ''")!.clauses[0]).toMatchObject({ op: "==", value: "" });
    expect(isBlankComparison(parseCondition("a.b != ''")!.clauses[0]!)).toBe(true);
    expect(isBlankComparison(parseCondition("a.b empty")!.clauses[0]!)).toBe(false);
  });
  it("keeps quotes and && inside a value intact", () => {
    const c = parseCondition("a.note == 'it\\'s && fine'");
    expect(c!.clauses).toHaveLength(1);
    expect(c!.clauses[0]!.value).toBe("it's && fine");
    expect(buildCondition(c!)).toBe("a.note == 'it\\'s && fine'");
  });
  it("leaves anything it cannot represent as text", () => {
    for (const e of ["a.x == 1 && (a.y == 2 || a.z == 3)", "!a.flag", "a.x == 1 && a.y == 2 || a.z == 3", "contains(a.x, 'q')", "a.x == a.y", "a.flag > true"]) expect(parseCondition(e), e).toBeNull();
  });
  it("reads and writes workspace, role and stage conditions", () => {
    for (const e of ["case.org_unit == 'wi_memorial_er'", "case.org_unit in ['wi_a', 'wi_b']", "actor.roles has 'qa'", "stages.coding.status == 'completed' && actor.is_applicant == false", "case.org_unit in ['x'] || actor.roles has 'admin'"]) {
      const c = parseCondition(e);
      expect(c, e).not.toBeNull();
      expect(buildCondition(c!), e).toBe(e);
    }
    expect(parseCondition("case.org_unit in ['a', 'b']")!.clauses[0]).toMatchObject({ op: "in", values: ["a", "b"] });
    expect(parseCondition("actor.roles has 'qa'")!.clauses[0]).toMatchObject({ op: "has", value: "qa" });
  });
  it("skips an 'is one of' row with nothing chosen", () => {
    expect(buildCondition({ join: "&&", clauses: [{ field: "case.org_unit", op: "in", value: "", kind: "string", values: [] }] })).toBe("");
  });
  it("offers includes for roles and is-one-of for workspaces", () => {
    expect(opsFor("roles")).toEqual(["has"]);
    expect(opsFor("workspace")).toContain("in");
  });
  it("treats an empty condition as no rows", () => {
    expect(parseCondition("  ")).toEqual({ join: "&&", clauses: [] });
    expect(buildCondition({ join: "&&", clauses: [] })).toBe("");
  });
  it("builds values by kind and skips unfinished rows", () => {
    expect(buildCondition({ join: "||", clauses: [{ field: "a.n", op: ">=", value: "10", kind: "number" }, { field: "", op: "==", value: "x", kind: "string" }, { field: "a.t", op: "==", value: "O'Neil", kind: "string" }] })).toBe("a.n >= 10 || a.t == 'O\\'Neil'");
  });
  it("offers operators by field kind", () => {
    expect(opsFor("boolean")).toEqual(["==", "!="]);
    expect(opsFor("number")).toContain(">=");
    expect(opsFor("text")).not.toContain(">");
  });
});
