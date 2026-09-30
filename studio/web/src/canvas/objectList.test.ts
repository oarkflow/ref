import { describe, expect, it } from "vitest";
import { emitObjectList, freshName, getPair, parseObjectList, withPair, type ObjItem } from "./objectList";

const CASES = `[
  { name "large" condition "input.amount > 500" intent "orders.tier_large" },
  { name "small" condition "input.amount <= 500" intent "orders.tier_small" }
]`;

describe("parseObjectList", () => {
  it("reads the real starter shapes", () => {
    const items = parseObjectList(CASES)!;
    expect(items).toHaveLength(2);
    expect(getPair(items[0]!, "name")).toBe('"large"');
    expect(getPair(items[1]!, "condition")).toBe('"input.amount <= 500"');
    expect(getPair(items[1]!, "intent")).toBe('"orders.tier_small"');
  });

  it("keeps commas, brackets and quotes inside values intact", () => {
    const items = parseObjectList(`[{ name "a" condition "x in ['a', 'b'] and y > 2, z" outcome "ok" reason "with \\"quotes\\"" score 2 }]`)!;
    expect(getPair(items[0]!, "condition")).toBe(`"x in ['a', 'b'] and y > 2, z"`);
    expect(getPair(items[0]!, "reason")).toBe(`"with \\"quotes\\""`);
    expect(getPair(items[0]!, "score")).toBe("2");
  });

  it("reads bare values, env() calls and nested lists as raw text", () => {
    const items = parseObjectList(`[{ name "n" enabled true limit env("LIMIT", "5") tags ["a", "b"] }]`)!;
    expect(items[0]!.pairs.map((p) => [p.key, p.raw])).toEqual([
      ["name", '"n"'], ["enabled", "true"], ["limit", 'env("LIMIT", "5")'], ["tags", '["a", "b"]'],
    ]);
  });

  it("is not fooled by a comment or something that is not a list of objects", () => {
    expect(parseObjectList(`[{ name "a" } # note\n]`)).toBeNull();
    expect(parseObjectList(`["a", "b"]`)).toBeNull();
    expect(parseObjectList(`concat(a, b)`)).toBeNull();
    expect(parseObjectList(`[{ name }]`)).toBeNull();
    expect(parseObjectList(`[{ name "unterminated }]`)).toBeNull();
  });

  it("reads an empty list", () => {
    expect(parseObjectList("[]")).toEqual([]);
  });
});

describe("emitObjectList", () => {
  it("round-trips the real shapes exactly (one object per line)", () => {
    const items = parseObjectList(CASES)!;
    const again = parseObjectList(emitObjectList(items))!;
    expect(again).toEqual(items);
    expect(emitObjectList(items)).toBe(
      `[\n{ name "large" condition "input.amount > 500" intent "orders.tier_large" },\n{ name "small" condition "input.amount <= 500" intent "orders.tier_small" }\n]`,
    );
    expect(emitObjectList([])).toBe("[]");
  });

  it("edits a single pair without disturbing the rest", () => {
    const items = parseObjectList(CASES)!;
    const edited: ObjItem[] = [withPair(items[0]!, "condition", '"input.amount > 900"'), items[1]!];
    const back = parseObjectList(emitObjectList(edited))!;
    expect(getPair(back[0]!, "condition")).toBe('"input.amount > 900"');
    expect(getPair(back[0]!, "intent")).toBe('"orders.tier_large"');
    expect(back[1]).toEqual(items[1]);
    expect(withPair(items[0]!, "intent", undefined).pairs.map((p) => p.key)).toEqual(["name", "condition"]);
    expect(withPair(items[0]!, "reason", '"r"').pairs.map((p) => p.key)).toEqual(["name", "condition", "intent", "reason"]);
  });

  it("suggests fresh names", () => {
    const items = parseObjectList(CASES)!;
    expect(freshName(items, "case")).toBe("case3");
    expect(freshName([], "rule")).toBe("rule1");
  });
});

import { trees } from "./fixtures";
import type { BlockNode } from "../api/types";

describe("on the real starter and passport configs", () => {
  const found: { file: string; path: string; name: string; raw: string }[] = [];
  const walk = (file: string, n: BlockNode) => {
    if (n.kind === "field" && ["cases", "rules", "branches"].includes(n.name ?? "") && n.raw?.trim().startsWith("[") && n.path.includes("/config/")) {
      found.push({ file, path: n.path, name: n.name!, raw: n.raw });
    }
    n.children?.forEach((c) => walk(file, c));
  };
  for (const [file, roots] of Object.entries(trees)) roots.forEach((r) => walk(file, r));

  it("finds real lists to check", () => {
    expect(found.length).toBeGreaterThanOrEqual(3); // 09_workflow_example: two cases lists and one rules table
    expect(new Set(found.map((f) => f.name))).toEqual(new Set(["cases", "rules"]));
  });

  it("parses every one and reproduces the same objects after an edit-free round trip", () => {
    for (const f of found) {
      const items = parseObjectList(f.raw);
      expect(items, `${f.file} ${f.path}`).not.toBeNull();
      expect(items!.length).toBeGreaterThan(0);
      expect(parseObjectList(emitObjectList(items!)), `${f.file} ${f.path}`).toEqual(items);
    }
  });
});
