// Guards the design rules in the stylesheet itself: one card shape, no pills or blobs, no dashed cards.
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const css = readFileSync(resolve(process.cwd(), "src/canvas/canvas.css"), "utf8");

/** The declarations of every rule whose selector list mentions `.cv-node` as a card (not a handle, tool or child). */
function nodeRules(): { selector: string; body: string }[] {
  const out: { selector: string; body: string }[] = [];
  for (const m of css.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    const selector = m[1]!.trim();
    if (/(^|,)\s*(\.canvas-page\s+)?\.cv-node(\.[\w-]+|:hover)*\s*(,|$)/.test(selector)) out.push({ selector, body: m[2]! });
  }
  return out;
}

describe("canvas stylesheet", () => {
  const rules = nodeRules();
  it("finds the card rules", () => expect(rules.length).toBeGreaterThan(3));

  it("cards have small square-ish corners: never a pill or a blob", () => {
    for (const r of rules) {
      const radius = /border-radius:\s*([^;]+);/.exec(r.body)?.[1]?.trim();
      if (!radius) continue;
      const px = parseFloat(radius);
      expect(px, `${r.selector} { border-radius: ${radius} }`).toBeLessThanOrEqual(8);
      expect(radius).not.toMatch(/999|50%/);
    }
  });

  it("cards are never dashed by default", () => {
    for (const r of rules) expect(r.body, r.selector).not.toMatch(/border(-style)?:[^;]*dashed/);
  });

  it("every card is one width", () => {
    const widths = new Set(rules.map((r) => /(^|[^-])width:\s*(\d+)px/.exec(r.body)?.[2]).filter(Boolean));
    expect([...widths]).toEqual(["264"]);
  });

  it("edge labels are square chips, not pills", () => {
    const label = /\.cv-elabel-text\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
    expect(parseFloat(/border-radius:\s*([^;]+);/.exec(label)?.[1] ?? "99")).toBeLessThanOrEqual(8);
  });
});
