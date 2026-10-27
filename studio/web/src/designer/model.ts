import type { BlockNode, Op } from "../api/types";
import { parseList, quote, unquote } from "../lib/bcl";
import { childPath } from "../lib/paths";

/** The input kinds a pipeline form understands (pipeline/model.go). */
export interface PaletteItem {
  kind: string;
  label: string;
  hint: string;
  /** Extra fields written when the input is created. */
  defaults?: Record<string, string>;
}

export const PALETTE: PaletteItem[] = [
  { kind: "text", label: "Short text", hint: "One line" },
  { kind: "textarea", label: "Long text", hint: "Several lines" },
  { kind: "number", label: "Number", hint: "Decimal" },
  { kind: "integer", label: "Whole number", hint: "No decimals" },
  { kind: "email", label: "Email", hint: "Validated address" },
  { kind: "phone", label: "Phone", hint: "Phone number" },
  { kind: "date", label: "Date", hint: "Calendar date" },
  { kind: "datetime", label: "Date & time", hint: "Date with time" },
  { kind: "select", label: "Dropdown", hint: "Pick one", defaults: { options: '["Option 1", "Option 2"]' } },
  { kind: "multiselect", label: "Multi-select", hint: "Pick several", defaults: { options: '["Option 1", "Option 2"]' } },
  { kind: "radio", label: "Radio group", hint: "Pick one, all visible", defaults: { options: '["Option 1", "Option 2"]' } },
  { kind: "boolean", label: "Checkbox", hint: "Yes / no" },
  { kind: "file", label: "File upload", hint: "Documents, photos" },
];

export const CHOICE_KINDS = ["select", "multiselect", "radio"];

export interface DesignInput {
  node: BlockNode;
  path: string;
  name: string;
  kind: string;
  label: string;
  help: string;
  placeholder: string;
  required: boolean;
  span: number;
  options: string[];
  pattern: string;
  minLength: string;
  maxLength: string;
  accept: string[];
  lookup: string;
  dependsOn: string[];
  visibleIf: string;
  requiredIf: string;
  disabledIf: string;
  viewRoles: string[];
  editRoles: string[];
  sensitive: boolean;
  pii: boolean;
  sealed: boolean;
  def: string;
  min: string;
  max: string;
  compute: string;
  /** True for a shared catalog input referenced by a form's `fields`. */
  shared?: boolean;
}

export interface DesignForm {
  node: BlockNode;
  path: string;
  name: string;
  title: string;
  inputs: DesignInput[];
  description: string;
  repeatable: boolean;
  minItems: number;
  maxItems: number;
  columns: number;
  visibleIf: string;
  disabledIf: string;
  viewRoles: string[];
  editRoles: string[];
  /** Names of shared catalog inputs this form lists in `fields`. */
  fields: string[];
  /** The shared inputs, resolved (read-only here: edit them in the catalog). */
  shared: DesignInput[];
}

const field = (n: BlockNode, name: string): string | undefined => n.children?.find((c) => c.kind === "field" && c.name === name)?.raw;
const str = (raw: string | undefined): string => (raw === undefined ? "" : (unquote(raw) ?? raw));
const strList = (raw: string | undefined): string[] => (parseList(raw ?? "") ?? []).map((x) => unquote(x) ?? x);

export function parseInput(node: BlockNode): DesignInput {
  return {
    node,
    path: node.path,
    name: node.id ?? "",
    kind: (field(node, "kind") ?? "text").trim(),
    label: str(field(node, "label")),
    help: str(field(node, "help")),
    placeholder: str(field(node, "placeholder")),
    required: field(node, "required")?.trim() === "true",
    span: Number(field(node, "span") ?? 0) || 0,
    options: strList(field(node, "options")),
    pattern: str(field(node, "pattern")),
    minLength: (field(node, "min_length") ?? "").trim(),
    maxLength: (field(node, "max_length") ?? "").trim(),
    accept: strList(field(node, "accept")),
    lookup: str(field(node, "lookup")),
    dependsOn: strList(field(node, "depends_on")),
    visibleIf: str(field(node, "visible_if")),
    requiredIf: str(field(node, "required_if")),
    disabledIf: str(field(node, "disabled_if")),
    viewRoles: strList(field(node, "view_roles")),
    editRoles: strList(field(node, "edit_roles")),
    sensitive: field(node, "sensitive")?.trim() === "true",
    pii: field(node, "pii")?.trim() === "true",
    sealed: field(node, "sealed")?.trim() === "true",
    def: str(field(node, "default")),
    min: str(field(node, "min")),
    max: str(field(node, "max")),
    compute: str(field(node, "compute")),
  };
}

/** The pipeline's shared field catalog: `input` blocks directly under the pipeline. */
export function parseCatalog(pipeline: BlockNode | undefined): DesignInput[] {
  return (pipeline?.children ?? []).filter((c) => c.kind === "block" && c.type === "input").map((n) => ({ ...parseInput(n), shared: true }));
}

export function parseForms(pipeline: BlockNode | undefined): DesignForm[] {
  const catalog = parseCatalog(pipeline);
  return (pipeline?.children ?? [])
    .filter((c) => c.kind === "block" && c.type === "form")
    .map((node) => {
      const refs = strList(field(node, "fields"));
      return {
        node,
        path: node.path,
        name: node.id ?? "",
        title: str(field(node, "title")),
        description: str(field(node, "description")),
        repeatable: field(node, "repeatable")?.trim() === "true",
        minItems: Number(field(node, "min_items") ?? 0) || 0,
        maxItems: Number(field(node, "max_items") ?? 0) || 0,
        columns: Number(field(node, "columns") ?? 0) || 0,
        visibleIf: str(field(node, "visible_if")),
        disabledIf: str(field(node, "disabled_if")),
        viewRoles: strList(field(node, "view_roles")),
        editRoles: strList(field(node, "edit_roles")),
        inputs: (node.children ?? []).filter((c) => c.kind === "block" && c.type === "input").map(parseInput),
        fields: refs,
        shared: refs.map((r) => catalog.find((c) => c.name === r)).filter((x): x is DesignInput => !!x),
      };
    });
}

/** A name unused among `taken`, derived from `base` ("field", "field_2", …). */
export function uniqueName(base: string, taken: string[]): string {
  const slug = base.toLowerCase().replace(/[^a-z0-9]+/g, "_").replace(/^_+|_+$/g, "") || "field";
  const start = /^[0-9]/.test(slug) ? "f_" + slug : slug;
  if (!taken.includes(start)) return start;
  for (let i = 2; ; i++) if (!taken.includes(`${start}_${i}`)) return `${start}_${i}`;
}

export function allInputNames(form: DesignForm): string[] {
  return form.inputs.map((i) => i.name);
}

/** The ops that add an input of the palette item to the end of a form. */
export function addInputOps(file: string, form: DesignForm, item: PaletteItem, at?: number): Op[] {
  const name = uniqueName(item.label, allInputNames(form));
  const lines = [`label ${quote(item.label)}`, `kind ${item.kind}`, ...Object.entries(item.defaults ?? {}).map(([k, v]) => `${k} ${v}`)];
  const ops: Op[] = [{ op: "addBlock", file, parent: form.path, type: "input", id: name, body: lines.join("\n") }];
  if (at !== undefined && at < form.inputs.length) ops.push({ op: "moveBlock", file, path: childPath(childPath(form.path, "input"), name), index: childIndex(form, at) });
  return ops;
}

/** Position among all of the form's children (fields and blocks) of its at-th input. */
export function childIndex(form: DesignForm, at: number): number {
  const target = form.inputs[at]?.node;
  const idx = (form.node.children ?? []).findIndex((c) => c === target);
  return idx < 0 ? (form.node.children ?? []).length : idx;
}

export function moveInputOps(file: string, form: DesignForm, from: number, to: number): Op[] {
  if (from === to || from < 0 || to < 0 || from >= form.inputs.length || to >= form.inputs.length) return [];
  return [{ op: "moveBlock", file, path: form.inputs[from]!.path, index: childIndex(form, to) }];
}

export type InputProp =
  | "label" | "help" | "placeholder" | "required" | "span" | "options" | "pattern" | "min_length" | "max_length" | "kind" | "accept"
  | "lookup" | "depends_on" | "visible_if" | "required_if" | "disabled_if" | "view_roles" | "edit_roles"
  | "sensitive" | "pii" | "sealed" | "default" | "min" | "max" | "compute";

const BOOL_PROPS: InputProp[] = ["required", "sensitive", "pii", "sealed"];
const NUM_PROPS: InputProp[] = ["span", "min_length", "max_length"];

/** setField / removeField for one property; an empty value removes the field. */
export function setPropOp(file: string, input: DesignInput, prop: InputProp, value: string | boolean | number | string[]): Op {
  const path = childPath(input.path, prop);
  if (value === "" || value === false || value === 0 || (Array.isArray(value) && value.length === 0)) {
    return prop === "kind" ? { op: "setField", file, path, value: "text" } : { op: "removeField", file, path };
  }
  let raw: string;
  if (Array.isArray(value)) raw = "[" + value.map(quote).join(", ") + "]";
  else if (typeof value === "boolean") raw = String(value);
  else if (typeof value === "number") raw = String(value);
  else if (prop === "kind") raw = value;
  else if (NUM_PROPS.includes(prop)) raw = String(Number(value) || 0);
  else if (BOOL_PROPS.includes(prop)) raw = value === "true" ? "true" : "false";
  else raw = quote(value);
  return { op: "setField", file, path, value: raw };
}

/** "a, b ,c" -> ["a","b","c"]. */
export function splitList(text: string): string[] {
  return text.split(",").map((x) => x.trim()).filter(Boolean);
}

/** Sample value so the preview shows what a user would see. */
export function sampleLabel(i: DesignInput): string {
  return i.label || i.name;
}

// ---------------------------------------------------------------------------
// Stage pages: layout and groups

export const LAYOUTS = [
  { value: "stacked", label: "Stacked", hint: "All groups one after another" },
  { value: "tabbed", label: "Tabs", hint: "One group at a time, switched by tabs" },
  { value: "wizard", label: "Wizard", hint: "Step by step with Back and Next" },
  { value: "accordion", label: "Accordion", hint: "Collapsible sections" },
  { value: "grid", label: "Grid", hint: "Groups side by side" },
] as const;

export const GROUP_MODES = [
  { value: "editable", label: "Editable" },
  { value: "readonly", label: "Read-only" },
  { value: "summary", label: "Summary" },
  { value: "hidden", label: "Hidden" },
] as const;

export interface DesignGroup {
  node: BlockNode;
  path: string;
  name: string;
  title: string;
  mode: string;
  columns: number;
  collapsed: boolean;
  forms: string[];
  description: string;
  visibleIf: string;
  disabledIf: string;
  roles: string[];
  editRoles: string[];
}

export interface DesignPage {
  node: BlockNode;
  path: string;
  title: string;
  layout: string;
  submitLabel: string;
  groups: DesignGroup[];
}

export interface DesignAction {
  node: BlockNode; path: string; name: string; label: string; outcome: string;
  condition: string; next: string; roles: string[]; commentRequired: boolean; confirm: string; returnTo: string;
}

export interface DesignStage {
  node: BlockNode;
  path: string;
  name: string;
  title: string;
  kind: string;
  page?: DesignPage;
  roles: string[];
  viewRoles: string[];
  skipIf: string;
  entryConditions: string[];
  requires: string[];
  next: string;
  complete: string;
  autoAdvance: boolean;
  actions: DesignAction[];
}

export function parseGroup(node: BlockNode): DesignGroup {
  return {
    node,
    path: node.path,
    name: node.id ?? "",
    title: str(field(node, "title")),
    mode: (field(node, "mode") ?? "editable").trim(),
    columns: Number(field(node, "columns") ?? 0) || 0,
    collapsed: field(node, "collapsed")?.trim() === "true",
    forms: strList(field(node, "forms")),
    description: str(field(node, "description")),
    visibleIf: str(field(node, "visible_if")),
    disabledIf: str(field(node, "disabled_if")),
    roles: strList(field(node, "roles")),
    editRoles: strList(field(node, "edit_roles")),
  };
}

export function parseStages(pipeline: BlockNode | undefined): DesignStage[] {
  return (pipeline?.children ?? [])
    .filter((c) => c.kind === "block" && c.type === "stage")
    .map((node) => {
      const pageNode = (node.children ?? []).find((c) => c.kind === "block" && c.type === "page");
      return {
        node,
        path: node.path,
        name: node.id ?? "",
        title: str(field(node, "title")),
        kind: (field(node, "kind") ?? "").trim(),
        roles: strList(field(node, "roles")),
        viewRoles: strList(field(node, "view_roles")),
        skipIf: str(field(node, "skip_if")),
        entryConditions: strList(field(node, "entry_conditions")),
        requires: strList(field(node, "requires")),
        next: str(field(node, "next")),
        complete: (field(node, "complete") ?? "").trim(),
        autoAdvance: field(node, "auto_advance")?.trim() === "true",
        actions: (node.children ?? []).filter((c) => c.kind === "block" && c.type === "action").map((a) => ({
          node: a, path: a.path, name: a.id ?? "", label: str(field(a, "label")), outcome: (field(a, "outcome") ?? "advance").trim(),
          condition: str(field(a, "condition")), next: str(field(a, "next")), roles: strList(field(a, "roles")),
          commentRequired: field(a, "comment_required")?.trim() === "true", confirm: str(field(a, "confirm")), returnTo: str(field(a, "return_to")),
        })),
        page: pageNode && {
          node: pageNode,
          path: pageNode.path,
          title: str(field(pageNode, "title")),
          layout: (field(pageNode, "layout") ?? "stacked").trim(),
          submitLabel: str(field(pageNode, "submit_label")),
          groups: (pageNode.children ?? []).filter((c) => c.kind === "block" && c.type === "group").map(parseGroup),
        },
      };
    });
}

const formList = (forms: string[]): string => "[" + forms.map(quote).join(", ") + "]";

export function setPageOp(file: string, page: DesignPage, prop: "title" | "layout" | "submit_label", value: string): Op {
  const path = childPath(page.path, prop);
  if (value === "" || (prop === "layout" && value === "stacked")) return { op: "removeField", file, path };
  return { op: "setField", file, path, value: prop === "layout" ? value : quote(value) };
}

/** Creates the stage's page, with one group holding the given forms. */
export function addPageOps(file: string, stage: DesignStage, forms: string[], title: string): Op[] {
  const group = `group ${quote("main")} {\n  title ${quote(title || "Details")}\n  forms ${formList(forms)}\n}`;
  return [{ op: "addBlock", file, parent: stage.path, type: "page", body: `title ${quote(stage.title || stage.name)}\n${group}` }];
}

export function addGroupOp(file: string, page: DesignPage, forms: string[] = []): Op {
  const name = uniqueName("group", page.groups.map((g) => g.name));
  const body = [`title ${quote("New group")}`, `forms ${formList(forms)}`].join("\n");
  return { op: "addBlock", file, parent: page.path, type: "group", id: name, body };
}

export function setGroupFormsOp(file: string, group: DesignGroup, forms: string[]): Op {
  return { op: "setField", file, path: childPath(group.path, "forms"), value: formList(forms) };
}

export type GroupProp = "title" | "mode" | "columns" | "collapsed" | "description" | "visible_if" | "disabled_if" | "roles" | "edit_roles";

const TEXT_GROUP_PROPS: GroupProp[] = ["title", "description", "visible_if", "disabled_if"];

export function setGroupPropOp(file: string, group: DesignGroup, prop: GroupProp, value: string | number | boolean | string[]): Op {
  const path = childPath(group.path, prop);
  if (value === "" || value === false || value === 0 || (prop === "mode" && value === "editable") || (Array.isArray(value) && value.length === 0)) return { op: "removeField", file, path };
  if (Array.isArray(value)) return { op: "setField", file, path, value: formList(value) };
  const raw = TEXT_GROUP_PROPS.includes(prop) ? quote(String(value)) : String(value);
  return { op: "setField", file, path, value: raw };
}

export type FormProp = "title" | "description" | "repeatable" | "min_items" | "max_items" | "columns" | "visible_if" | "disabled_if" | "view_roles" | "edit_roles";
const TEXT_FORM_PROPS: FormProp[] = ["title", "description", "visible_if", "disabled_if"];

/** Form-level settings: repeatable entries, columns, conditions and role limits. An empty value removes the field. */
export function setFormPropOp(file: string, form: DesignForm, prop: FormProp, value: string | number | boolean | string[]): Op {
  const path = childPath(form.path, prop);
  if (value === "" || value === false || value === 0 || (Array.isArray(value) && value.length === 0)) return { op: "removeField", file, path };
  if (Array.isArray(value)) return { op: "setField", file, path, value: formList(value) };
  return { op: "setField", file, path, value: TEXT_FORM_PROPS.includes(prop) ? quote(String(value)) : String(value) };
}

/** Every input of every form as a reference a condition can use ("form.input"), with what the builder needs to offer values. */
export interface FieldRef { ref: string; label: string; kind: string; options: string[]; /** Display names for options (e.g. workspace ids). */ labels?: Record<string, string> }
export function fieldRefs(forms: DesignForm[]): FieldRef[] {
  return forms.flatMap((f) => [...f.inputs, ...f.shared].map((i) => ({ ref: `${f.name}.${i.name}`, label: `${f.title || f.name} › ${i.label || i.name}`, kind: i.kind, options: i.options })));
}

/** Moves a form between groups (or reorders within one); `at` is the index in the target. */
export function moveFormOps(file: string, groups: DesignGroup[], form: string, fromGroup: number, toGroup: number, at: number): Op[] {
  const from = groups[fromGroup], to = groups[toGroup];
  if (!from || !to) return [];
  const src = from.forms.filter((f) => f !== form);
  if (fromGroup === toGroup) {
    const without = src;
    const pos = Math.min(Math.max(at, 0), without.length);
    const next = [...without.slice(0, pos), form, ...without.slice(pos)];
    return next.join() === from.forms.join() ? [] : [setGroupFormsOp(file, from, next)];
  }
  const dst = to.forms.filter((f) => f !== form);
  const pos = Math.min(Math.max(at, 0), dst.length);
  return [setGroupFormsOp(file, from, src), setGroupFormsOp(file, to, [...dst.slice(0, pos), form, ...dst.slice(pos)])];
}

export function moveGroupOps(file: string, page: DesignPage, from: number, to: number): Op[] {
  if (from === to || from < 0 || to < 0 || from >= page.groups.length || to >= page.groups.length) return [];
  const target = page.groups[to]!.node;
  const idx = (page.node.children ?? []).findIndex((c) => c === target);
  return [{ op: "moveBlock", file, path: page.groups[from]!.path, index: idx }];
}

/** Forms of the pipeline that no group of the page shows. */
export function unplacedForms(all: DesignForm[], page: DesignPage | undefined): string[] {
  const used = new Set((page?.groups ?? []).flatMap((g) => g.forms));
  return all.map((f) => f.name).filter((n) => !used.has(n));
}

// ---------------------------------------------------------------------------
// Stage SLA and calendars

export interface DesignEscalation { node: BlockNode; path: string; name: string; after: string; assignRoles: string[]; notify: string[] }
export interface DesignSla {
  node: BlockNode; path: string;
  duration: string; warnBefore: string; calendar: string; onBreach: string; returnTo: string; pickupTimeout: string; onPickupTimeout: string;
  escalations: DesignEscalation[];
}
export interface DesignCalendar { node: BlockNode; path: string; name: string; timezone: string; hours: string; holidays: string[] }

export function parseSla(stage: BlockNode | undefined): DesignSla | undefined {
  const node = (stage?.children ?? []).find((c) => c.kind === "block" && c.type === "sla");
  if (!node) return undefined;
  return {
    node, path: node.path,
    duration: str(field(node, "duration")), warnBefore: str(field(node, "warn_before")), calendar: str(field(node, "calendar")),
    onBreach: str(field(node, "on_breach")), returnTo: str(field(node, "return_to")),
    pickupTimeout: str(field(node, "pickup_timeout")), onPickupTimeout: str(field(node, "on_pickup_timeout")),
    escalations: (node.children ?? []).filter((c) => c.kind === "block" && c.type === "escalate").map((e) => ({
      node: e, path: e.path, name: e.id ?? "", after: str(field(e, "after")), assignRoles: strList(field(e, "assign_roles")), notify: strList(field(e, "notify")),
    })),
  };
}

export function parseCalendars(pipeline: BlockNode | undefined): DesignCalendar[] {
  return (pipeline?.children ?? []).filter((c) => c.kind === "block" && c.type === "calendar").map((n) => ({
    node: n, path: n.path, name: n.id ?? "", timezone: str(field(n, "timezone")), hours: str(field(n, "hours")), holidays: strList(field(n, "holidays")),
  }));
}

/** Names of the stage's actions (the automatic actions an SLA breach may take). */
export function stageActions(stage: BlockNode | undefined): string[] {
  return (stage?.children ?? []).filter((c) => c.kind === "block" && c.type === "action").map((a) => a.id ?? "").filter(Boolean);
}

export const DURATION_RE = /^(\d+(\.\d+)?(ns|us|ms|s|m|h|d))+$/;
export const BREACH_ACTIONS = ["notify", "reassign", "release", "return"];

export type SlaProp = "duration" | "warn_before" | "calendar" | "on_breach" | "return_to" | "pickup_timeout" | "on_pickup_timeout";

export function addSlaOp(file: string, stage: DesignStage): Op {
  return { op: "addBlock", file, parent: stage.path, type: "sla", body: `duration ${quote("24h")}\non_breach notify` };
}

export function setSlaOp(file: string, sla: DesignSla, prop: SlaProp, value: string): Op {
  const path = childPath(sla.path, prop);
  if (value === "") return { op: "removeField", file, path };
  return { op: "setField", file, path, value: prop === "on_breach" && BREACH_ACTIONS.includes(value) ? value : quote(value) };
}

export function addEscalationOp(file: string, sla: DesignSla): Op {
  const name = uniqueName("level", sla.escalations.map((e) => e.name));
  return { op: "addBlock", file, parent: sla.path, type: "escalate", id: name, body: `after ${quote("12h")}` };
}

export function setEscalationOp(file: string, e: DesignEscalation, prop: "after" | "assign_roles" | "notify", value: string | string[]): Op {
  const path = childPath(e.path, prop);
  if (Array.isArray(value)) return value.length ? { op: "setField", file, path, value: "[" + value.map(quote).join(", ") + "]" } : { op: "removeField", file, path };
  return value ? { op: "setField", file, path, value: quote(value) } : { op: "removeField", file, path };
}

export function setCalendarOp(file: string, cal: DesignCalendar, prop: "timezone" | "hours", value: string): Op {
  const path = childPath(cal.path, prop);
  return value ? { op: "setField", file, path, value: quote(value) } : { op: "removeField", file, path };
}

// ---------------------------------------------------------------------------
// Workspaces (org.hierarchy resources) and their lookups

export interface WorkspaceNode { id: string; parent: string; level: string; name: string }
export interface LookupEntry { set: string; code: string; label: string; node: string; sort: string; disabled: boolean }
export interface WorkspaceModel {
  file: string; resourcePath: string; name: string; levels: string[];
  nodes: WorkspaceNode[]; lookups: LookupEntry[]; lookupsPath: string; lookupsRaw: string;
}

const kv = (obj: string, key: string): string => {
  const m = new RegExp(`(?:^|[\\s{,])${key}\\s+("(?:[^"\\\\]|\\\\.)*"|[^\\s,}\\]]+)`).exec(obj);
  return m ? (unquote(m[1]!) ?? m[1]!) : "";
};

/** Reads the `{ ... }` objects of a list field without disturbing comments. */
export function objectsOf(raw: string): string[] {
  const out: string[] = [];
  let depth = 0, start = -1, inStr = false, comment = false;
  for (let i = 0; i < raw.length; i++) {
    const ch = raw[i]!;
    if (comment) { if (ch === "\n") comment = false; continue; }
    if (inStr) { if (ch === "\\") i++; else if (ch === '"') inStr = false; continue; }
    if (ch === "#") comment = true;
    else if (ch === '"') inStr = true;
    else if (ch === "{") { if (depth++ === 0) start = i; }
    else if (ch === "}" && --depth === 0 && start >= 0) { out.push(raw.slice(start, i + 1)); start = -1; }
  }
  return out;
}

export function parseWorkspaces(trees: Record<string, BlockNode[]>): WorkspaceModel[] {
  const out: WorkspaceModel[] = [];
  const walk = (file: string, nodes: BlockNode[] | undefined) => {
    for (const n of nodes ?? []) {
      if (n.kind !== "block" || n.type !== "resource") continue;
      if ((field(n, "kind") ?? "").replace(/"/g, "") !== "org.hierarchy") continue;
      const cfg = (n.children ?? []).find((c) => c.kind === "block" && c.type === "config");
      const lk = cfg?.children?.find((c) => c.kind === "field" && c.name === "lookups");
      out.push({
        file, resourcePath: n.path, name: n.id ?? "",
        levels: strList(field(cfg ?? n, "levels")),
        nodes: objectsOf(field(cfg ?? n, "nodes") ?? "").map((o) => ({ id: kv(o, "id"), parent: kv(o, "parent"), level: kv(o, "level"), name: kv(o, "name") })),
        lookups: objectsOf(lk?.raw ?? "").map((o) => ({ set: kv(o, "set"), code: kv(o, "code"), label: kv(o, "label"), node: kv(o, "node"), sort: kv(o, "sort"), disabled: kv(o, "disabled") === "true" })),
        lookupsPath: lk?.path ?? "", lookupsRaw: lk?.raw ?? "",
      });
    }
  };
  for (const [file, nodes] of Object.entries(trees)) walk(file, nodes);
  return out;
}

export function lookupSets(ws: WorkspaceModel[]): string[] {
  return [...new Set(ws.flatMap((w) => w.lookups.map((l) => l.set)).filter(Boolean))];
}

/** Appends one entry to the `lookups [ ... ]` list text, leaving every existing line and comment as it was. */
export function appendLookupRaw(raw: string, e: { set: string; code: string; label: string; node?: string; sort?: number }): string {
  return appendListItem(raw, `{ set ${quote(e.set)} code ${quote(e.code)}${e.node ? ` node ${quote(e.node)}` : ""} label ${quote(e.label)}${e.sort ? ` sort ${e.sort}` : ""} }`);
}

/** Appends an item to a `[ ... ]` list text without touching what is already there. */
export function appendListItem(raw: string, entry: string): string {
  const close = raw.lastIndexOf("]");
  if (close < 0) return raw;
  const before = raw.slice(0, close).replace(/\s+$/, "");
  const sep = before.endsWith("[") || before.endsWith(",") ? "" : ",";
  return `${before}${sep}\n      ${entry}\n    ]`;
}

export function addLookupOp(file: string, w: WorkspaceModel, e: { set: string; code: string; label: string; node?: string; sort?: number }): Op {
  return { op: "setField", file, path: w.lookupsPath, value: appendLookupRaw(w.lookupsRaw, e) };
}

// ---------------------------------------------------------------------------
// Lookup sources (lookup.sources resources): static lists, SQL queries, HTTP APIs

export type SourceKind = "static" | "sql" | "http";
export interface SourceSet { name: string; type: string; detail: string; raw: string }
export interface LookupSourceModel {
  file: string; resourcePath: string; configPath: string; name: string; hasDatabase: boolean; hasService: boolean;
  database: string; service: string;
  setsPath: string; setsRaw: string; sets: SourceSet[];
}

export function parseLookupSources(trees: Record<string, BlockNode[]>): LookupSourceModel[] {
  const out: LookupSourceModel[] = [];
  for (const [file, nodes] of Object.entries(trees)) {
    for (const n of nodes) {
      if (n.kind !== "block" || n.type !== "resource" || (field(n, "kind") ?? "").replace(/"/g, "") !== "lookup.sources") continue;
      const cfg = (n.children ?? []).find((c) => c.kind === "block" && c.type === "config") ?? n;
      const sets = cfg.children?.find((c) => c.kind === "field" && c.name === "sets");
      out.push({
        file, resourcePath: n.path, configPath: cfg.path, name: n.id ?? "",
        hasDatabase: !!field(cfg, "database"), hasService: !!field(cfg, "service"),
        database: str(field(cfg, "database")), service: str(field(cfg, "service")),
        setsPath: sets?.path ?? childPath(cfg.path, "sets"), setsRaw: sets?.raw ?? "[]",
        sets: objectsOf(sets?.raw ?? "").map((o) => {
          const type = kv(o, "type") || "static";
          const detail = type === "sql" ? kv(o, "query") : type === "http" ? kv(o, "path") : `${objectsOf(o.slice(o.indexOf("entries"))).length} entries`;
          return { name: kv(o, "name"), type, detail, raw: o };
        }),
      });
    }
  }
  return out;
}

export interface NewSet {
  name: string; type: SourceKind;
  entries?: { value: string; label: string }[];
  query?: string; args?: string[];
  path?: string; itemsPath?: string; valueField?: string; labelField?: string;
  method?: string; params?: { name: string; value: string }[];
  ttl?: string;
}

export function setText(s: NewSet): string {
  const parts = [`name ${quote(s.name)}`, `type ${quote(s.type)}`];
  if (s.type === "static") parts.push(`entries [ ${(s.entries ?? []).map((e) => `{ value ${quote(e.value)} label ${quote(e.label || e.value)} }`).join(", ")} ]`);
  if (s.type === "sql") { parts.push(`query ${quote(s.query ?? "")}`); if (s.args?.length) parts.push(`args [${s.args.map(quote).join(", ")}]`); }
  if (s.type === "http") {
    parts.push(`path ${quote(s.path ?? "")}`);
    if (s.itemsPath) parts.push(`items_path ${quote(s.itemsPath)}`);
    if (s.valueField) parts.push(`value_field ${quote(s.valueField)}`);
    if (s.labelField) parts.push(`label_field ${quote(s.labelField)}`);
    if (s.method && s.method !== "GET") parts.push(`method ${quote(s.method)}`);
    if (s.params?.length) parts.push(`params { ${s.params.map((p) => `${p.name} ${quote(p.value)}`).join(" ")} }`);
  }
  if (s.ttl) parts.push(`ttl ${quote(s.ttl)}`);
  return `{ ${parts.join(" ")} }`;
}

export function addSetOp(m: LookupSourceModel, s: NewSet): Op {
  return { op: "setField", file: m.file, path: m.setsPath, value: appendListItem(m.setsRaw, setText(s)) };
}

/** A pipeline.cases resource that lists this pipeline, and the file it is in. */
export function findCasesResource(trees: Record<string, BlockNode[]>, pipeline: string): { file: string; path: string; lookupResource: string } | undefined {
  for (const [file, nodes] of Object.entries(trees)) {
    for (const n of nodes) {
      if (n.kind !== "block" || n.type !== "resource" || (field(n, "kind") ?? "").replace(/"/g, "") !== "pipeline.cases") continue;
      const cfg = (n.children ?? []).find((c) => c.kind === "block" && c.type === "config");
      if (cfg && strList(field(cfg, "pipelines")).includes(pipeline)) return { file, path: cfg.path, lookupResource: str(field(cfg, "lookup_resource")) };
    }
  }
  return undefined;
}

/** Creates a lookup.sources resource and points the pipeline's cases resource at it. */
export function createSourceOps(file: string, name: string, cases: { file: string; path: string }, set: NewSet, conn: { database?: string; service?: string } = {}): Op[] {
  const links = [conn.database && `database ${quote(conn.database)}`, conn.service && `service ${quote(conn.service)}`].filter(Boolean).join("\n  ");
  const ops: Op[] = [{ op: "addBlock", file, type: "resource", id: name, body: `kind ${quote("lookup.sources")}\nconfig {\n  ${links ? links + "\n  " : ""}sets [\n    ${setText(set)}\n  ]\n}` }];
  ops.push({ op: "setField", file: cases.file, path: childPath(cases.path, "lookup_resource"), value: quote(name) });
  return ops;
}

// ---------------------------------------------------------------------------
// Connections a lookup source can use

export interface Connections { databases: string[]; services: { name: string; baseUrl: string }[] }

export function parseConnections(trees: Record<string, BlockNode[]>): Connections {
  const out: Connections = { databases: [], services: [] };
  for (const nodes of Object.values(trees)) {
    for (const n of nodes) {
      if (n.kind !== "block" || n.type !== "resource") continue;
      const kind = (field(n, "kind") ?? "").replace(/"/g, "");
      const cfg = (n.children ?? []).find((c) => c.kind === "block" && c.type === "config");
      if (kind === "database.sql") out.databases.push(n.id ?? "");
      if (kind === "service.http") out.services.push({ name: n.id ?? "", baseUrl: str(field(cfg ?? n, "base_url")) });
    }
  }
  return out;
}

/** Points a lookup.sources resource at an existing database or API service. */
export function connectSourceOp(m: LookupSourceModel, key: "database" | "service", name: string): Op {
  const path = childPath(m.configPath, key);
  return name ? { op: "setField", file: m.file, path, value: quote(name) } : { op: "removeField", file: m.file, path };
}

export const HOST_RE = /^https?:\/\/([^/:?#\s]+)(:\d+)?(\/[^\s]*)?$/i;

/** The host an API's base URL names (the allowlist entry), or "" when the URL is not usable. */
export function hostOf(baseUrl: string): string {
  const m = HOST_RE.exec(baseUrl.trim());
  return m ? m[1]!.toLowerCase() : "";
}

/** A service.http resource for the API (only its own host is allowed). */
export function createServiceOps(file: string, name: string, baseUrl: string, allowPrivate: boolean): Op[] {
  const lines = [`base_url ${quote(baseUrl.trim().replace(/\/+$/, ""))}`, `allowed_hosts [${quote(hostOf(baseUrl))}]`];
  if (allowPrivate) lines.push("allow_private_networks true");
  return [{ op: "addBlock", file, type: "resource", id: name, body: `kind ${quote("service.http")}\nconfig {\n  ${lines.join("\n  ")}\n}` }];
}

// ---------------------------------------------------------------------------
// Shared fields (the pipeline's input catalog)

/** Creates a catalog input and lists it in the form's `fields`. */
export function addSharedInputOps(file: string, pipelinePath: string, form: DesignForm, catalog: DesignInput[], item: PaletteItem): Op[] {
  const name = uniqueName(item.label, [...catalog.map((c) => c.name), ...form.inputs.map((i) => i.name)]);
  const lines = [`label ${quote(item.label)}`, `kind ${item.kind}`, ...Object.entries(item.defaults ?? {}).map(([k, v]) => `${k} ${v}`)];
  return [
    { op: "addBlock", file, parent: pipelinePath, type: "input", id: name, body: lines.join("\n") },
    { op: "setField", file, path: childPath(form.path, "fields"), value: formList([...form.fields, name]) },
  ];
}

/** The forms that list a shared input, by title. */
export function usesOfShared(forms: DesignForm[], name: string): string[] {
  return forms.filter((f) => f.fields.includes(name)).map((f) => f.title || f.name);
}

// ---------------------------------------------------------------------------
// Testing a set against the running app

export type SetDef = Record<string, unknown>;

/** The text between the braces of `key { ... }`, ignoring braces inside strings. */
export function braceBody(raw: string, key: string): string | null {
  const m = new RegExp(`(?:^|[\\s{,])${key}\\s*\\{`).exec(raw);
  if (!m) return null;
  let depth = 1, inStr = false;
  const start = m.index + m[0].length;
  for (let i = start; i < raw.length; i++) {
    const ch = raw[i]!;
    if (inStr) { if (ch === "\\") i++; else if (ch === '"') inStr = false; continue; }
    if (ch === '"') inStr = true;
    else if (ch === "{") depth++;
    else if (ch === "}" && --depth === 0) return raw.slice(start, i);
  }
  return null;
}

/** The runnable definition of a set written as BCL text (`{ name "x" type "sql" ... }`). */
export function defFromRaw(raw: string): SetDef {
  const def: SetDef = {};
  for (const k of ["name", "type", "query", "path", "method", "items_path", "value_field", "label_field", "ttl", "timeout"]) {
    const v = kv(raw, k);
    if (v) def[k] = v;
  }
  const args = /(?:^|[\s{,])args\s*\[([^\]]*)\]/.exec(raw);
  if (args) def.args = [...args[1]!.matchAll(/"((?:[^"\\]|\\.)*)"/g)].map((x) => unquote(`"${x[1]}"`) ?? x[1]!);
  const params = braceBody(raw, "params");
  if (params !== null) def.params = Object.fromEntries([...params.matchAll(/([A-Za-z_][\w.-]*)\s+("(?:[^"\\]|\\.)*")/g)].map((x) => [x[1]!, unquote(x[2]!) ?? x[2]!]));
  const i = raw.indexOf("entries");
  if (i >= 0) def.entries = objectsOf(raw.slice(i + 7)).map((o) => ({ value: kv(o, "value"), label: kv(o, "label") }));
  if (!def.type) def.type = "static";
  return def;
}

/** The runnable definition of a set being added. */
export function defFromNew(s: NewSet): SetDef {
  return defFromRaw(setText(s));
}

/** The names a set needs values for before it can run (query parameters and {field} placeholders). */
export function setDeps(def: SetDef): string[] {
  const names = new Set<string>(Array.isArray(def.args) ? (def.args as string[]) : []);
  for (const v of Object.values((def.params as Record<string, string>) ?? {})) for (const m of String(v).matchAll(/\{([A-Za-z_][\w.-]*)\}/g)) names.add(m[1]!);
  return [...names];
}

export interface TestResult { options: { value: string; label: string }[]; total: number; duration_ms: number }

/** Asks the running app (through Studio) to evaluate the set. */
export async function testSet(token: string | null, body: { database?: string; service?: string; set: SetDef; deps: Record<string, string> }): Promise<TestResult> {
  const res = await fetch("api/v1/lookup-test", { method: "POST", headers: { "content-type": "application/json", authorization: `Bearer ${token ?? ""}` }, body: JSON.stringify(body) });
  const data = (await res.json().catch(() => ({}))) as { error?: { message?: string } } & Partial<TestResult>;
  if (!res.ok) throw new Error(data.error?.message ?? `The test failed (${res.status}).`);
  return { options: data.options ?? [], total: data.total ?? 0, duration_ms: data.duration_ms ?? 0 };
}

// ---------------------------------------------------------------------------
// Stage flow: when a stage is skipped or may open, where it goes next, and its actions

export const OUTCOMES = ["advance", "return", "reject", "approve", "withdraw", "hold"] as const;
export const COMPLETE_RULES = ["", "all", "any", "quorum"] as const;

export type StageProp = "skip_if" | "entry_conditions" | "requires" | "next" | "complete" | "auto_advance";

export function setStageOp(file: string, stage: DesignStage, prop: StageProp, value: string | boolean | string[]): Op {
  const path = childPath(stage.path, prop);
  if (value === "" || value === false || (Array.isArray(value) && value.length === 0)) return { op: "removeField", file, path };
  if (Array.isArray(value)) return { op: "setField", file, path, value: formList(value) };
  if (typeof value === "boolean") return { op: "setField", file, path, value: String(value) };
  return { op: "setField", file, path, value: prop === "complete" ? value : quote(value) };
}

export type ActionProp = "label" | "outcome" | "condition" | "next" | "roles" | "comment_required" | "confirm" | "return_to";
const ACTION_TEXT: ActionProp[] = ["label", "condition", "next", "confirm", "return_to"];

export function setActionOp(file: string, a: DesignAction, prop: ActionProp, value: string | boolean | string[]): Op {
  const path = childPath(a.path, prop);
  if (value === "" || value === false || (Array.isArray(value) && value.length === 0)) return prop === "outcome" ? { op: "setField", file, path, value: "advance" } : { op: "removeField", file, path };
  if (Array.isArray(value)) return { op: "setField", file, path, value: formList(value) };
  if (typeof value === "boolean") return { op: "setField", file, path, value: String(value) };
  return { op: "setField", file, path, value: ACTION_TEXT.includes(prop) ? quote(value) : value };
}

export function addActionOp(file: string, stage: DesignStage): Op {
  const name = uniqueName("action", stage.actions.map((a) => a.name));
  return { op: "addBlock", file, parent: stage.path, type: "action", id: name, body: `label ${quote("New action")}\noutcome advance` };
}

/** Where a stage can lead: explicit targets, plus the stage after it when something advances without naming one. */
export function stageTargets(stages: DesignStage[], i: number): string[] {
  const s = stages[i]!;
  const out = new Set<string>();
  if (s.next) out.add(s.next);
  for (const a of s.actions) {
    if (a.outcome === "return") { if (a.returnTo) out.add(a.returnTo); else if (i > 0) out.add(stages[i - 1]!.name); }
    else if (a.outcome === "advance" || a.outcome === "approve") { if (a.next) out.add(a.next); else if (!s.next && stages[i + 1]) out.add(stages[i + 1]!.name); }
  }
  if (!s.actions.length && !s.next && stages[i + 1]) out.add(stages[i + 1]!.name);
  return [...out];
}

// ---------------------------------------------------------------------------
// Conditions that can read the workspace, the user and earlier stages

export const CASE_STATUSES = ["draft", "in_progress", "returned", "requires_information", "approved", "rejected", "withdrawn", "completed"];
export const STAGE_STATUSES = ["pending", "active", "returned", "completed", "rejected", "skipped"];

/** References a condition may use besides form fields: the workspace, the signed-in user, the case and other stages. */
export function contextRefs(workspaces: WorkspaceModel[], stages: DesignStage[]): FieldRef[] {
  const nodes = workspaces.flatMap((w) => w.nodes);
  const roles = [...new Set(stages.flatMap((st) => [...st.roles, ...st.viewRoles]))].sort();
  const out: FieldRef[] = [];
  if (nodes.length) out.push({ ref: "case.org_unit", label: "Workspace › the case's workspace", kind: "workspace", options: nodes.map((n) => n.id), labels: Object.fromEntries(nodes.map((n) => [n.id, `${n.name || n.id} (${n.level})`])) });
  out.push({ ref: "actor.roles", label: "User › roles", kind: "roles", options: roles });
  out.push({ ref: "actor.is_applicant", label: "User › is the applicant", kind: "boolean", options: [] });
  out.push({ ref: "case.status", label: "Case › status", kind: "select", options: CASE_STATUSES });
  for (const st of stages) out.push({ ref: `stages.${st.name}.status`, label: `Stage › ${st.title || st.name} is`, kind: "select", options: STAGE_STATUSES });
  return out;
}

// ---------------------------------------------------------------------------
// Snippets: shared fields (the input catalog) and reusable forms

/** Source text for a block's contents, from the parsed tree. */
export function blockBody(node: BlockNode, indent = ""): string {
  const lines: string[] = [];
  for (const c of node.children ?? []) {
    if (c.kind === "field") lines.push(`${indent}${c.name} ${c.raw ?? ""}`);
    else {
      const head = c.id ? `${c.type} ${quote(c.id)}` : String(c.type);
      lines.push(`${indent}${head} {`, blockBody(c, indent + "  "), `${indent}}`);
    }
  }
  return lines.join("\n");
}

const catalogNames = (catalog: DesignInput[]) => catalog.map((c) => c.name);

/** Renames a shared field everywhere it is listed. */
export function renameSharedOps(file: string, pipelinePath: string, forms: DesignForm[], shared: DesignInput, newName: string): Op[] {
  const ops: Op[] = [{ op: "renameBlock", file, path: shared.path, newId: newName }];
  for (const f of forms.filter((x) => x.fields.includes(shared.name))) ops.push({ op: "setField", file, path: childPath(f.path, "fields"), value: formList(f.fields.map((n) => (n === shared.name ? newName : n))) });
  void pipelinePath;
  return ops;
}

/** Deletes a shared field and removes it from every form that lists it. */
export function deleteSharedOps(file: string, forms: DesignForm[], shared: DesignInput): Op[] {
  const ops: Op[] = [];
  for (const f of forms.filter((x) => x.fields.includes(shared.name))) {
    const rest = f.fields.filter((n) => n !== shared.name);
    ops.push(rest.length ? { op: "setField", file, path: childPath(f.path, "fields"), value: formList(rest) } : { op: "removeField", file, path: childPath(f.path, "fields") });
  }
  ops.push({ op: "removeBlock", file, path: shared.path });
  return ops;
}

/** Turns a shared field into an inline copy in one form (the catalog entry and other forms are left alone). */
export function detachSharedOps(file: string, form: DesignForm, shared: DesignInput): Op[] {
  const name = uniqueName(shared.name, form.inputs.map((i) => i.name));
  const rest = form.fields.filter((n) => n !== shared.name);
  return [
    { op: "addBlock", file, parent: form.path, type: "input", id: name, body: blockBody(shared.node) },
    rest.length ? { op: "setField", file, path: childPath(form.path, "fields"), value: formList(rest) } : { op: "removeField", file, path: childPath(form.path, "fields") },
  ];
}

/** Moves an inline field into the catalog and lists it from the form instead. */
export function shareInlineOps(file: string, pipelinePath: string, form: DesignForm, catalog: DesignInput[], input: DesignInput): Op[] {
  const name = uniqueName(input.name, catalogNames(catalog));
  return [
    { op: "addBlock", file, parent: pipelinePath, type: "input", id: name, body: blockBody(input.node) },
    { op: "removeBlock", file, path: input.path },
    { op: "setField", file, path: childPath(form.path, "fields"), value: formList([...form.fields, name]) },
  ];
}

/** Where a form appears: "Stage › Group", across all stages' pages. */
export function formUsage(stages: DesignStage[], form: string): string[] {
  return stages.flatMap((st) => (st.page?.groups ?? []).filter((g) => g.forms.includes(form)).map((g) => `${st.title || st.name} › ${g.title || g.name}`));
}

/** A copy of a form (inline inputs and, with `catalog`, its shared fields written out as inline inputs). */
export function formCopyBody(form: DesignForm, catalog: DesignInput[], titleSuffix: string, keepShared = false, newName = form.name): string {
  const own = (form.node.children ?? []).filter((c) => !(c.kind === "field" && (c.name === "fields" || c.name === "title")));
  const title = `title ${quote((form.title || form.name) + titleSuffix)}`;
  const inline = keepShared ? [] : form.fields.map((n) => catalog.find((c) => c.name === n)).filter((x): x is DesignInput => !!x)
    .map((c) => `input ${quote(c.name)} {\n${blockBody(c.node, "  ")}\n}`);
  const refs = keepShared && form.fields.length ? [`fields ${formList(form.fields)}`] : [];
  return renameSelfRefs([title, blockBody({ ...form.node, children: own }), ...refs, ...inline].filter(Boolean).join("\n"), form, catalog, newName);
}

/** A copy's rules and computed values mention its own fields as "oldform.field"; point them at the copy. */
function renameSelfRefs(body: string, form: DesignForm, catalog: DesignInput[], newName: string): string {
  if (newName === form.name) return body;
  const own = [...form.inputs.map((i) => i.name), ...form.fields.filter((n) => catalog.some((c) => c.name === n))];
  if (!own.length) return body;
  const re = new RegExp(`(?<![\\w.])${form.name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\.(${own.join("|")})(?![\\w])`, "g");
  return body.replace(re, `${newName}.$1`);
}

export function copyFormOps(file: string, targetPipelinePath: string, existing: DesignForm[], form: DesignForm, sourceCatalog: DesignInput[], titleSuffix = "", keepShared = false): Op[] {
  const name = uniqueName(form.name, existing.map((f) => f.name));
  return [{ op: "addBlock", file, parent: targetPipelinePath, type: "form", id: name, body: formCopyBody(form, sourceCatalog, titleSuffix, keepShared, name) }];
}

/** Deletes a form and takes it out of every page group that shows it. */
export function deleteFormOps(file: string, stages: DesignStage[], form: DesignForm): Op[] {
  const ops: Op[] = [];
  for (const st of stages) for (const g of st.page?.groups ?? []) {
    if (g.forms.includes(form.name)) ops.push({ op: "setField", file, path: childPath(g.path, "forms"), value: formList(g.forms.filter((n) => n !== form.name)) });
  }
  ops.push({ op: "removeBlock", file, path: form.path });
  return ops;
}

export interface PipelineRef { file: string; path: string; name: string; forms: DesignForm[]; catalog: DesignInput[] }

/** Every pipeline in the working copy, so forms can be copied between them. */
export function pipelinesIn(trees: Record<string, BlockNode[]>): PipelineRef[] {
  const out: PipelineRef[] = [];
  for (const [file, nodes] of Object.entries(trees)) for (const n of nodes) if (n.kind === "block" && n.type === "pipeline") out.push({ file, path: n.path, name: n.id ?? "", forms: parseForms(n), catalog: parseCatalog(n) });
  return out;
}

/** A new, unused catalog entry. */
export function addCatalogInputOp(file: string, pipelinePath: string, catalog: DesignInput[], item: PaletteItem): Op {
  const name = uniqueName(item.label, catalogNames(catalog));
  const lines = [`label ${quote(item.label)}`, `kind ${item.kind}`, ...Object.entries(item.defaults ?? {}).map(([k, v]) => `${k} ${v}`)];
  return { op: "addBlock", file, parent: pipelinePath, type: "input", id: name, body: lines.join("\n") };
}
