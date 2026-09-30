import type { Catalog } from "../../api/types";
import { humanize } from "../../labels";
import { GENERIC, fieldsFromConfig } from "./generic";
import type { FieldDef, NodeTypeDef, Resolved } from "./types";

export type ActionInfoLike = Catalog["actions"][number];

const defs = new Map<string, NodeTypeDef>();
let sorted: NodeTypeDef[] | null = null;

/** Adds a descriptor. Registering an id again replaces it, so a host can override a built-in. */
export function registerNodeType(def: NodeTypeDef): void {
  defs.set(def.id, def);
  sorted = null;
}

export function unregisterNodeType(id: string): void {
  defs.delete(id);
  sorted = null;
}

/** Every registered descriptor, best first (highest priority; ties by registration order, later wins). */
export function listNodeTypes(): NodeTypeDef[] {
  if (!sorted) sorted = [...defs.values()].map((d, i) => ({ d, i })).sort((a, b) => b.d.priority - a.d.priority || b.i - a.i).map((x) => x.d);
  return sorted;
}

/**
 * The descriptor for a step: the highest-priority registered one that matches its
 * type and action; else a generic one built from the catalog, so that a step of an
 * unknown type still opens a real form.
 */
export function resolveNodeType(typeName: string, actionName?: string, catalog?: Catalog | null): Resolved {
  for (const d of listNodeTypes()) if (d.match(typeName, actionName)) return { def: d, generic: false };
  const info = catalog?.node_types.find((t) => t.name === typeName);
  const def: NodeTypeDef = {
    ...GENERIC,
    id: `generic:${typeName}`,
    label: humanize(typeName),
    category: info?.family ?? GENERIC.category,
    blurb: info?.summary,
  };
  return { def, generic: true };
}

/** Config keys of the action a step runs, for the fields the descriptor did not list itself. */
export function actionFields(actionName: string | undefined, catalog: Catalog | null | undefined, covered: ReadonlySet<string>): FieldDef[] {
  if (!actionName || !catalog) return [];
  const action = catalog.actions.find((a) => a.name === actionName);
  return fieldsFromConfig(action?.config).filter((f) => !covered.has(f.at.join(".")));
}

/** Every field a descriptor lists explicitly, by `at` ("config.url"). */
export function coveredKeys(def: NodeTypeDef): Set<string> {
  const s = new Set<string>();
  for (const sec of def.sections) for (const f of sec.fields) s.add(f.at.join("."));
  return s;
}

export function isGeneric(def: NodeTypeDef): boolean {
  return def.id.startsWith("generic");
}
