// The node-type registry: one descriptor per kind of step says what its card
// shows and what its settings form contains. One generic renderer draws every
// descriptor, so adding a step type is data, not UI code (see README.md).
import type { ReactNode } from "react";
import type { BlockNode, Catalog } from "../../api/types";
import type { Row } from "../look";

export interface Option {
  value: string;
  label: string;
  hint?: string;
}

/**
 * Where a field's value lives on the step's block. `["timeout"]` is a field of
 * the step; `["config", "url"]` is a field of its `config` block. (A second
 * segment names a child block; deeper nesting is not needed by any step.)
 */
export type At = readonly [string] | readonly [string, string];

/** What a form can read about the step being edited: raw BCL by field id. */
export interface Values {
  /** The raw BCL of a field of this descriptor, or undefined when it is not set. */
  raw(id: string): string | undefined;
  /** The raw BCL at an explicit place, for conditions that look at fields the descriptor does not list. */
  rawAt(at: At): string | undefined;
  /** The value with its quotes removed, when it is a string literal. */
  text(id: string): string | undefined;
  node: BlockNode;
}

/** Where a picker gets its choices. */
export type OptionSource =
  | { from: "nodeTypes" }
  | { from: "actions" }
  | { from: "intents" }
  | { from: "processes" }
  | { from: "shapes" }
  | { from: "edgeTypes" }
  | { from: "resources"; kinds?: readonly string[] };

interface Base {
  /** Unique within the descriptor; the key in `Values`. */
  id: string;
  label: string;
  /** One sentence under the input. */
  help?: string;
  at: At;
  placeholder?: string;
  /** Raw BCL used when a new step of this type is created. */
  default?: string;
  required?: boolean;
  /** Under "More settings" instead of the main form. */
  advanced?: boolean;
  visibleWhen?(v: Values): boolean;
  /** A plain-language problem with the current value, or null. Runs on the raw BCL. */
  validate?(raw: string | undefined, v: Values): string | null;
}

export type ColKind = "text" | "expr" | "intent" | "number";
export interface Col {
  key: string;
  label: string;
  kind: ColKind;
  placeholder?: string;
  title?: boolean;
}

export type FieldDef = Base &
  (
    | { kind: "text" }
    | { kind: "longText"; rows?: number }
    | { kind: "number"; integer?: boolean }
    | { kind: "boolean" }
    | { kind: "duration" }
    /** A formula over the facts the step needs; the facts are offered to insert. */
    | { kind: "expression"; rows?: number }
    /** A small code box (SQL, JSON, a template body). */
    | { kind: "code"; language?: "sql" | "json" | "template" | "text"; rows?: number }
    | { kind: "select"; options: readonly Option[] | OptionSource; /** written as a bare word (`get`) instead of a string */ bare?: boolean; allowNone?: boolean }
    /** `{ name "value" ... }` */
    | { kind: "keyValue"; keyLabel?: string; valueLabel?: string }
    /** `[1, 2, 3]` or a list of texts */
    | { kind: "list"; item?: "text" | "number" }
    | { kind: "stringList"; addLabel?: string; options?: readonly Option[] }
    /** One or several of the facts this step reads or publishes. */
    | { kind: "factPicker"; multiple?: boolean; role?: "needs" | "produces" | "any" }
    | { kind: "resourcePicker"; kinds?: readonly string[] | "fromType" }
    | { kind: "intentPicker"; multiple?: boolean }
    | { kind: "routePicker" }
    /** Rows of `{ ... }` objects: cases, branches, rules. `at` names the list. */
    | { kind: "cases"; rowLabel: string; addLabel: string; seed: string; cols: readonly Col[]; empty?: string }
    /** A visual condition builder that writes the formula. */
    | { kind: "conditions"; /** wording before the checks, e.g. "Follow this when" */ lead?: string }
    | { kind: "secretRef" }
    | { kind: "custom"; render(props: CustomProps): ReactNode }
  );

export type FieldKind = FieldDef["kind"];

export interface CustomProps {
  field: FieldDef;
  values: Values;
  node: BlockNode;
  readOnly: boolean;
  /** Commit a raw BCL value for this field (undefined removes it). */
  set(raw: string | undefined): void;
}

export interface SectionDef {
  id: string;
  title: string;
  /** Show only when editing a step of a flow or of a process. Both when omitted. */
  scope?: "flow" | "process";
  hint?: string;
  open?: boolean;
  fields: readonly FieldDef[];
  visibleWhen?(v: Values): boolean;
}

/** What a card needs to know about a step, independent of React Flow. */
export interface StepFacts {
  id: string;
  typeName: string;
  uses?: string;
  requires: readonly string[];
  provides: readonly string[];
  /** Raw BCL at a place on the step. */
  get(at: At): string | undefined;
  /** The same, with string quotes removed. */
  text(at: At): string | undefined;
}

export interface Problem {
  field?: string;
  message: string;
}

export interface NodeTypeDef {
  /** Unique; registering the same id again replaces the earlier entry. */
  id: string;
  label: string;
  icon: string;
  /** The colour family: a catalog family name ("flow", "decision", ...). */
  category: string;
  /** One line under the type's name in the form. */
  blurb?: string;
  /** Does this descriptor describe a step of this type / running this action? */
  match(typeName: string, actionName?: string): boolean;
  /** Highest wins. Built-ins use 100 for a type, 150 for an action-specific override; the generic fallback is 0. */
  priority: number;
  sections: readonly SectionDef[];
  /** Rows shown on the card. Defaults to the values of the first few required fields. */
  summaryRows?(s: StepFacts): Row[];
  /** Extra checks over the whole form. */
  validate?(v: Values): Problem[];
  /** Also list the action's own config keys that no field above covers. Default true. */
  actionConfig?: boolean;
  /** Only meaningful inside a process (waits, tasks) or a flow. Default: both. */
  scope?: "flow" | "process";
}

export interface Resolved {
  def: NodeTypeDef;
  /** True when no registered descriptor matched and the form was generated from the catalog. */
  generic: boolean;
}

export type { Catalog };
