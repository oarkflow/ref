import { render } from "@testing-library/react";
import { vi } from "vitest";
import type { BlockNode, BlockSchemas, Catalog, Diagnostic, Op } from "../api/types";
import { BlockForm } from "../forms/BlockForm";
import { FormProvider, type FormContext } from "../forms/ctx";
import schemaJson from "../fixtures/schema-blocks.json";
import catalogJson from "../fixtures/catalog.json";

export const schemas = schemaJson as unknown as BlockSchemas;
export const catalog = catalogJson as unknown as Catalog;

let line = 1;
export const field = (parent: string, name: string, raw: string): BlockNode => ({
  kind: "field", path: `${parent}/${name}`, name, raw, line: line++, start: 0, end: 0,
});
export const block = (parent: string, type: string, id: string | undefined, children: BlockNode[] = []): BlockNode => ({
  kind: "block", path: `${parent}/${type}${id ? `/${id}` : ""}`.replace(/^\//, ""), type, id, line: line++, start: 0, end: 0, children,
});

/** A top-level block whose children are given as [name, raw] pairs or nodes. */
export function topBlock(type: string, id: string, kids: (BlockNode | [string, string])[]): BlockNode {
  const path = `${type}/${id}`;
  return {
    kind: "block", path, type, id, line: 1, start: 0, end: 0,
    children: kids.map((k) => (Array.isArray(k) ? field(path, k[0], k[1]) : k)),
  };
}

export interface RenderOpts {
  diagnostics?: Diagnostic[];
  overlay?: FormContext["overlay"];
  readOnly?: boolean;
  focusPath?: string | null;
  useCatalog?: boolean;
  /** Open collapsed sections (default true so tests reach every field). */
  expandAll?: boolean;
}

/** Renders the form for one top-level block and returns a spy for the ops it emits. */
export function renderBlock(node: BlockNode, opts: RenderOpts = {}) {
  const edit = vi.fn<(ops: Op[]) => void>();
  const ctx: FormContext = {
    file: "test.bcl",
    schemas,
    catalog: opts.useCatalog === false ? null : catalog,
    diagnostics: opts.diagnostics ?? [],
    focusPath: opts.focusPath ?? null,
    overlay: opts.overlay ?? {},
    readOnly: opts.readOnly,
    expandAll: opts.expandAll ?? true,
    edit,
  };
  const type = node.type ?? "";
  const ui = (n: BlockNode) => (
    <FormProvider value={ctx}>
      <BlockForm node={n} schema={schemas[type]} blockType={type} />
    </FormProvider>
  );
  const r = render(ui(node));
  return {
    ...r,
    edit,
    rerenderNode: (n: BlockNode) => r.rerender(ui(n)),
    /** The ops of the most recent edit call. */
    last: () => edit.mock.calls.at(-1)?.[0] ?? [],
  };
}
