import { describe, expect, it } from "vitest";
import { blankClause, compileCondition, normalise, parseCondition, withOp, withValue } from "./conditions";

describe("parseCondition", () => {
  it("reads one comparison", () => {
    expect(parseCondition("result.action == 'approve'")).toEqual({ join: "and", clauses: [{ left: "result.action", op: "==", right: "approve", kind: "text" }] });
  });
  it("reads numbers, booleans and both quote styles", () => {
    expect(parseCondition("order.total >= 1000")!.clauses[0]).toMatchObject({ op: ">=", right: "1000", kind: "number" });
    expect(parseCondition("user.active == true")!.clauses[0]).toMatchObject({ right: "true", kind: "bool" });
    expect(parseCondition('kind != "gift"')!.clauses[0]).toMatchObject({ op: "!=", right: "gift", kind: "text" });
  });
  it("reads is set / is not set", () => {
    expect(parseCondition("token")!.clauses[0]).toMatchObject({ left: "token", op: "set" });
    expect(parseCondition("!token")!.clauses[0]).toMatchObject({ left: "token", op: "unset" });
  });
  it("joins with all-of or any-of", () => {
    expect(parseCondition("a > 1 && b == 'x'")!.join).toBe("and");
    expect(parseCondition("a > 1 || b == 'x'")!.join).toBe("or");
    expect(parseCondition("a > 1 || b == 'x' || c")!.clauses).toHaveLength(3);
  });
  it("treats an empty formula as no clauses", () => {
    expect(parseCondition("")).toEqual({ join: "and", clauses: [] });
    expect(parseCondition("   ")).toEqual({ join: "and", clauses: [] });
  });
  it("keeps quoted && and || as text", () => {
    expect(parseCondition("note == 'a && b'")!.clauses).toEqual([{ left: "note", op: "==", right: "a && b", kind: "text" }]);
  });
  it("refuses anything it cannot represent, instead of guessing", () => {
    expect(parseCondition("len(items) > 0")).toBeNull();
    expect(parseCondition("a == 1 && b == 2 || c == 3")).toBeNull(); // mixed: precedence would be lost
    expect(parseCondition("(a == 1 || b == 2) && c")).toBeNull();
    expect(parseCondition("a == b")).toBeNull(); // comparing two facts
    expect(parseCondition("a == 'unterminated")).toBeNull();
    expect(parseCondition("a in [1,2]")).toBeNull();
  });
  it("unwraps one pair of brackets around the whole thing", () => {
    expect(parseCondition("(a == 1)")!.clauses).toHaveLength(1);
  });
});

describe("compileCondition", () => {
  it("writes the formula the platform reads", () => {
    expect(compileCondition({ join: "and", clauses: [{ left: "a", op: "==", right: "x", kind: "text" }, { left: "b", op: ">", right: "3", kind: "number" }] })).toBe("a == 'x' && b > 3");
    expect(compileCondition({ join: "or", clauses: [{ left: "ok", op: "set", right: "", kind: "text" }, { left: "t", op: "unset", right: "", kind: "text" }] })).toBe("ok || !t");
  });
  it("escapes quotes in text", () => {
    expect(compileCondition({ join: "and", clauses: [{ left: "n", op: "==", right: "it's", kind: "text" }] })).toBe("n == 'it\\'s'");
  });
  it("skips clauses with no left side", () => {
    expect(compileCondition({ join: "and", clauses: [blankClause(), { left: "a", op: "set", right: "", kind: "text" }] })).toBe("a");
  });
});

describe("round trip", () => {
  const cases = [
    "result.action == 'approve'",
    "order.total > 1000 && user.role == 'admin'",
    "a == 1 || b == 2 || !c",
    "flags.urgent == true",
    "note == 'it\\'s fine'",
  ];
  for (const c of cases) it(`${c}`, () => expect(normalise(c)).toBe(c));

  it("normalises spacing and quote style without changing meaning", () => {
    expect(normalise("a==\"x\"&&b>=2")).toBe("a == 'x' && b >= 2");
  });
});

describe("editing a clause", () => {
  it("an ordered operator on a number-looking value makes it a number", () => {
    expect(withOp({ left: "n", op: "==", right: "5", kind: "text" }, ">")).toMatchObject({ op: ">", kind: "number" });
  });
  it("is set / is not set drop the value", () => {
    expect(withOp({ left: "n", op: "==", right: "5", kind: "number" }, "set")).toMatchObject({ op: "set", right: "" });
  });
  it("a typed value becomes a number, a boolean or text", () => {
    const c = blankClause("x");
    expect(withValue(c, "12")).toMatchObject({ kind: "number", right: "12" });
    expect(withValue(c, "true")).toMatchObject({ kind: "bool" });
    expect(withValue(c, "hello")).toMatchObject({ kind: "text", right: "hello" });
  });
});
