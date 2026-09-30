import { createContext, useContext, useMemo, type ReactNode } from "react";
import type { BlockNode, BlockSchemas, Catalog, Diagnostic, Op } from "../api/types";
import { friendlyProblem } from "../labels";
import { isWithin } from "../lib/paths";

export interface FormContext {
  file: string;
  schemas: BlockSchemas | null;
  catalog: Catalog | null;
  diagnostics: Diagnostic[];
  focusPath: string | null;
  /** Unacknowledged edits keyed "file|path"; raw null = pending removal. */
  overlay: Record<string, { raw: string | null }>;
  readOnly?: boolean;
  /** Open every collapsed section (tests, print). */
  expandAll?: boolean;
  edit(ops: Op[]): void;
}

const Ctx = createContext<FormContext | null>(null);

export function FormProvider({ value, children }: { value: FormContext; children: ReactNode }) {
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useForm(): FormContext {
  const c = useContext(Ctx);
  if (!c) throw new Error("useForm outside FormProvider");
  return c;
}

/** The value to show for the field at `path`: a pending edit wins over the server's. */
export function useRaw(node: BlockNode | undefined, path: string): string | undefined {
  const { file, overlay } = useForm();
  const o = overlay[`${file}|${path}`];
  if (o) return o.raw ?? undefined;
  return node?.raw;
}

/** Diagnostics attached exactly at `path`, and those beneath it. */
export function useDiagnostics(path: string): { here: Diagnostic[]; below: Diagnostic[] } {
  const { diagnostics, file } = useForm();
  return useMemo(() => {
    const here: Diagnostic[] = [];
    const below: Diagnostic[] = [];
    for (const d of diagnostics) {
      if (!d.path) continue;
      if (d.file && d.file !== file) continue;
      if (d.path === path) here.push(d);
      else if (isWithin(d.path, path)) below.push(d);
    }
    return { here, below };
  }, [diagnostics, path, file]);
}

/** Problems attached to one field or block, in plain language. */
export function Problems({ items }: { items: Diagnostic[] }) {
  if (!items.length) return null;
  return (
    <ul className="problems" role="alert">
      {items.map((d, i) => (
        <li key={i} className={`problem ${d.severity}`} title={d.message}>
          {friendlyProblem(d).text}
        </li>
      ))}
    </ul>
  );
}
