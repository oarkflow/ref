// A small vocabulary for writing descriptors. Each helper builds one FieldDef;
// `key` is the config key (in the step's `config` block) unless `at` says otherwise.
import { registerNodeType } from "../resolve";
import type { At, Col, FieldDef, NodeTypeDef, Option, OptionSource, SectionDef } from "../types";
import type { Row } from "../../look";

type Opt = {
  help?: string; placeholder?: string; default?: string; required?: boolean; advanced?: boolean;
  at?: At; id?: string; visibleWhen?: FieldDef["visibleWhen"]; validate?: FieldDef["validate"];
};

const mk = <K extends FieldDef["kind"]>(kind: K, key: string, label: string, o: Opt & Record<string, unknown>): FieldDef => {
  const at: At = o.at ?? ["config", key];
  const { at: _a, id, ...rest } = o;
  void _a;
  return { kind, id: id ?? at.join("."), at, label, ...rest } as unknown as FieldDef;
};

export const f = {
  text: (key: string, label: string, o: Opt = {}) => mk("text", key, label, o),
  long: (key: string, label: string, o: Opt & { rows?: number } = {}) => mk("longText", key, label, o),
  int: (key: string, label: string, o: Opt = {}) => mk("number", key, label, { integer: true, ...o }),
  num: (key: string, label: string, o: Opt = {}) => mk("number", key, label, o),
  bool: (key: string, label: string, o: Opt = {}) => mk("boolean", key, label, o),
  dur: (key: string, label: string, o: Opt = {}) => mk("duration", key, label, o),
  expr: (key: string, label: string, o: Opt & { rows?: number } = {}) => mk("expression", key, label, o),
  code: (key: string, label: string, o: Opt & { language?: "sql" | "json" | "template" | "text"; rows?: number } = {}) => mk("code", key, label, o),
  select: (key: string, label: string, options: readonly Option[] | OptionSource, o: Opt & { bare?: boolean; allowNone?: boolean } = {}) => mk("select", key, label, { options, ...o }),
  kv: (key: string, label: string, o: Opt & { keyLabel?: string; valueLabel?: string } = {}) => mk("keyValue", key, label, o),
  list: (key: string, label: string, o: Opt & { item?: "text" | "number" } = {}) => mk("list", key, label, o),
  strs: (key: string, label: string, o: Opt & { options?: readonly Option[]; addLabel?: string } = {}) => mk("stringList", key, label, o),
  fact: (key: string, label: string, o: Opt & { role?: "needs" | "produces" | "any" } = {}) => mk("factPicker", key, label, o),
  facts: (key: string, label: string, o: Opt & { role?: "needs" | "produces" | "any" } = {}) => mk("factPicker", key, label, { multiple: true, ...o }),
  resource: (key: string, label: string, o: Opt & { kinds?: readonly string[] | "fromType" } = {}) => mk("resourcePicker", key, label, { kinds: "fromType", at: ["resource"], ...o }),
  intent: (key: string, label: string, o: Opt = {}) => mk("intentPicker", key, label, o),
  intents: (key: string, label: string, o: Opt = {}) => mk("intentPicker", key, label, { multiple: true, ...o }),
  route: (key: string, label: string, o: Opt = {}) => mk("routePicker", key, label, o),
  cond: (key: string, label: string, o: Opt & { lead?: string } = {}) => mk("conditions", key, label, o),
  secret: (key: string, label: string, o: Opt = {}) => mk("secretRef", key, label, o),
  cases: (key: string, o: { rowLabel: string; addLabel: string; seed: string; cols: readonly Col[]; empty?: string } & Opt) => mk("cases", key, o.rowLabel, o),
  custom: (id: string, label: string, render: Extract<FieldDef, { kind: "custom" }>["render"], o: Opt = {}) => mk("custom", id, label, { render, at: ["config", id], ...o }),
};

/** A field that lives on the step itself (`timeout`) rather than in its config. */
export const onStep = (name: string): At => [name];

export const section = (id: string, title: string, fields: readonly FieldDef[], o: Partial<SectionDef> = {}): SectionDef => ({ id, title, fields, ...o });

type DefBase = Omit<NodeTypeDef, "id" | "match" | "priority"> & { id?: string; priority?: number };

/** Describe several step types that share a shape but differ in wording, defaults or emphasis. */
export function define(types: readonly string[], base: DefBase): void {
  const set = new Set(types);
  registerNodeType({ ...base, id: base.id ?? `type:${types[0]}`, priority: base.priority ?? 100, match: (typeName) => set.has(typeName) });
}

/**
 * Describe whatever step runs one of these actions, whatever its type. Beats a type
 * descriptor (150 against 100), so a `decision` step that runs `decision.table`
 * gets the table form.
 */
export function defineAction(actions: readonly string[], base: DefBase): void {
  const set = new Set(actions);
  registerNodeType({ ...base, id: base.id ?? `action:${actions[0]}`, priority: base.priority ?? 150, match: (_t, action) => !!action && set.has(action) });
}

/** Rows made of "label: value" for the values that are set. */
export function pick(s: { text(at: At): string | undefined }, ...specs: [label: string, at: At, opts?: { code?: boolean; max?: number }][]): Row[] {
  const out: Row[] = [];
  for (const [label, at, o] of specs) {
    const v = s.text(at)?.replace(/\s+/g, " ").trim();
    if (!v) continue;
    const max = o?.max ?? 46;
    out.push({ label, value: v.length > max ? v.slice(0, max - 1) + "…" : v, code: o?.code });
  }
  return out;
}

export const cfg = (key: string): At => ["config", key];
