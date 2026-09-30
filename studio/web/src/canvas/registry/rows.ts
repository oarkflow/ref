// What a card says about a step: the descriptor's summary rows, from the step's
// settings alone (no block tree, no React).
import type { Catalog } from "../../api/types";
import { unquote } from "../../lib/bcl";
import type { Row } from "../look";
import type { Settings } from "../model";
import { resolveNodeType } from "./resolve";
import type { At, FieldDef, NodeTypeDef, StepFacts } from "./types";

const key = (at: At) => at.join(".");

export function factsOf(o: { id: string; typeName: string; uses?: string; requires: readonly string[]; provides: readonly string[]; settings: Settings }): StepFacts {
  const text = (at: At) => {
    const raw = o.settings[key(at)];
    return raw === undefined ? undefined : (unquote(raw) ?? raw.trim());
  };
  return { id: o.id, typeName: o.typeName, uses: o.uses, requires: o.requires, provides: o.provides, get: (at) => o.settings[key(at)], text };
}

const clip = (s: string, n: number) => {
  const t = s.replace(/\s+/g, " ").trim();
  return t.length > n ? t.slice(0, n - 1) + "…" : t;
};

/** When a descriptor says nothing, describe the step by its first required (or first set) settings. */
export function defaultRows(def: NodeTypeDef, facts: StepFacts, _catalog?: Catalog | null): Row[] {
  void _catalog;
  const rows: Row[] = [];
  const seen = new Set<string>();
  const consider = (fields: readonly FieldDef[]) => {
    for (const f of fields) {
      if (rows.length >= 2 || f.kind === "custom" || f.kind === "boolean" || f.kind === "cases" || f.kind === "keyValue") continue;
      const v = facts.text(f.at);
      if (!v || seen.has(f.id)) continue;
      seen.add(f.id);
      rows.push({ label: f.label.length > 12 ? f.label.split(" ").slice(0, 2).join(" ") : f.label, value: clip(v, 40), code: f.kind === "code" || f.kind === "expression" || f.kind === "factPicker" });
    }
  };
  for (const s of def.sections) consider(s.fields.filter((f) => f.required));
  for (const s of def.sections) consider(s.fields);
  if (rows.length === 0 && facts.uses) {
    rows.push({ label: "Action", value: facts.uses, code: true });
  }
  return rows;
}

export function rowsFor(
  info: { id: string; typeName: string; uses?: string; requires: readonly string[]; provides: readonly string[]; settings: Settings },
  catalog?: Catalog | null,
  scope: "flow" | "process" = "flow",
): Row[] {
  const { def } = resolveNodeType(info.typeName, info.uses, catalog);
  const facts = factsOf(info);
  const own = def.summaryRows?.(facts) ?? defaultRows(def, facts, catalog);
  if (scope === "flow") return own;
  // A step of a process runs a flow or a sub-process, or waits for a person: say so first.
  const base: Row[] = [];
  const intent = facts.text(["intent"]);
  const proc = facts.text(["process"]);
  if (intent) base.push({ label: "Runs", value: intent, code: true });
  if (proc && !own.some((r) => r.label === "Runs" || r.label === "Starts")) base.push({ label: "Starts", value: proc, code: true });
  if (Object.keys(info.settings).some((k) => k.startsWith("task.")) && !own.some((r) => r.label === "Waits for")) base.push({ label: "Waits for", value: "a person" });
  return [...base, ...own.filter((r) => !base.some((b) => b.label === r.label))];
}
