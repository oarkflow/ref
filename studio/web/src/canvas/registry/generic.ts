// The generic fallback: a form generated from the catalog. Any action the backend
// registers gets a usable form and card with no frontend change, because the
// catalog says what its config keys are. Registered descriptors override it.
import type { ConfigField } from "../../api/types";
import { humanize } from "../../labels";
import type { FieldDef, NodeTypeDef, SectionDef, At } from "./types";

/** The catalog's config type names, as they appear on ConfigField.type. */
export const CONFIG_TYPES = [
  "string", "template", "expression", "bool", "int", "number", "duration", "fact", "[]fact", "[]string", "[]template", "[]int",
  "map", "object", "[]object", "intent", "[]intent", "process", "resource", "sql", "any", "block",
] as const;

const sentence = (s: string | undefined) => {
  const t = (s ?? "").trim();
  return t ? t.charAt(0).toUpperCase() + t.slice(1) + (/[.!?]$/.test(t) ? "" : ".") : undefined;
};

/** A field for one catalog config key. `at` says where it lives on the step. */
export function fieldFromConfig(cf: ConfigField, at: At = ["config", cf.name]): FieldDef {
  const base = {
    id: at.join("."),
    label: humanize(cf.name),
    help: sentence(cf.summary),
    at,
    required: cf.required,
    default: cf.default,
  };
  switch (cf.type) {
    case "bool": return { ...base, kind: "boolean" };
    case "int": return { ...base, kind: "number", integer: true };
    case "number": return { ...base, kind: "number" };
    case "duration": return { ...base, kind: "duration", placeholder: "e.g. 30s" };
    case "expression": return { ...base, kind: "expression" };
    case "template": return { ...base, kind: "longText", rows: 2 };
    case "[]template": return { ...base, kind: "stringList" };
    case "sql": return { ...base, kind: "code", language: "sql", rows: 5 };
    case "fact": return { ...base, kind: "factPicker", role: "needs" };
    case "[]fact": return { ...base, kind: "factPicker", multiple: true, role: "needs" };
    case "[]string": return { ...base, kind: "stringList" };
    case "[]int": return { ...base, kind: "list", item: "number" };
    case "map": return { ...base, kind: "keyValue" };
    case "intent": return { ...base, kind: "intentPicker" };
    case "[]intent": return { ...base, kind: "intentPicker", multiple: true };
    case "process": return { ...base, kind: "select", options: { from: "processes" }, bare: false };
    case "resource": return { ...base, kind: "resourcePicker" };
    case "object":
    case "block":
    case "[]object":
    case "any":
      // Shapes the catalog does not describe field by field: written as BCL, shown in a small code box.
      return { ...base, kind: "code", language: "text", rows: 4 };
    default: return { ...base, kind: "text" };
  }
}

/** The fields of an action's config, required ones first. */
export function fieldsFromConfig(config: readonly ConfigField[] | undefined): FieldDef[] {
  const fields = (config ?? []).map((cf) => fieldFromConfig(cf));
  return [...fields.filter((f) => f.required), ...fields.filter((f) => !f.required)];
}

/** A section for an action's settings, with the optional ones folded away. */
export function sectionsFromConfig(config: readonly ConfigField[] | undefined, title = "Settings"): SectionDef[] {
  const fields = fieldsFromConfig(config);
  if (fields.length === 0) return [];
  return [{ id: "settings", title, fields }];
}

/** The descriptor used when nothing else matches. Its form is generated per action (see SettingsPanel). */
export const GENERIC: NodeTypeDef = {
  id: "generic",
  label: "Step",
  icon: "gear",
  category: "compute",
  match: () => true,
  priority: 0,
  sections: [],
  actionConfig: true,
};
