import { createContext, useContext, useMemo, type ReactNode } from "react";
import type { BlockNode, Catalog } from "../../api/types";
import { blocksOf, fieldOf } from "../../lib/paths";
import { unquote } from "../../lib/bcl";
import { useStudio } from "../../state/context";
import type { CanvasGraph } from "../model";
import { INPUT_FACT } from "../model";
import type { BlockIO } from "../panels/kit";

/** What the field renderers need to know about the step being edited. */
export interface SettingsCtx {
  node: BlockNode;
  io: BlockIO;
  catalog?: Catalog | null;
  graph?: CanvasGraph;
  readOnly: boolean;
  /** The facts this step reads / publishes, and every fact it could read. */
  needs: string[];
  produces: string[];
  available: string[];
}

const Ctx = createContext<SettingsCtx | null>(null);
export const SettingsProvider = ({ value, children }: { value: SettingsCtx; children: ReactNode }) => <Ctx.Provider value={value}>{children}</Ctx.Provider>;

export function useSettings(): SettingsCtx {
  const c = useContext(Ctx);
  if (!c) throw new Error("useSettings outside SettingsProvider");
  return c;
}

/** Facts for a step of a flow: what it reads, publishes, and what other steps offer. */
export function factsFor(graph: CanvasGraph | undefined, selfId: string): Pick<SettingsCtx, "needs" | "produces" | "available"> {
  if (graph?.kind !== "intent") return { needs: [], produces: [], available: [INPUT_FACT] };
  const me = graph.nodes.find((n) => n.id === selfId);
  const all = new Set<string>([INPUT_FACT]);
  for (const n of graph.nodes) if (n.id !== selfId) for (const f of n.provides) all.add(f);
  return { needs: me?.requires ?? [], produces: me?.provides ?? [], available: [...all] };
}

const strOf = (b: BlockNode | undefined, name: string) => {
  const raw = fieldOf(b, name)?.raw;
  return raw === undefined ? undefined : (unquote(raw) ?? raw.trim());
};

function useBlocks(type: string): BlockNode[] {
  const trees = useStudio((s) => s.trees);
  return useMemo(() => {
    const out: BlockNode[] = [];
    for (const roots of Object.values(trees)) for (const b of roots) if (b.kind === "block" && b.type === type && b.id) out.push(b);
    return out;
  }, [trees, type]);
}

/** Resource ids in the draft, optionally only those whose kind starts with one of `kinds`. */
export function useResourceIds(kinds?: readonly string[]): { id: string; kind?: string }[] {
  const blocks = useBlocks("resource");
  return useMemo(
    () =>
      blocks
        .map((b) => ({ id: b.id!, kind: strOf(b, "kind") }))
        .filter((r) => !kinds || kinds.length === 0 || kinds.some((k) => r.kind?.startsWith(k)))
        .sort((a, b) => a.id.localeCompare(b.id)),
    [blocks, kinds],
  );
}

export const useRouteIds = () => {
  const blocks = useBlocks("route");
  return useMemo(() => blocks.map((b) => b.id!).sort(), [blocks]);
};
export const useSecretIds = () => {
  const blocks = useBlocks("secret");
  return useMemo(() => blocks.map((b) => b.id!).sort(), [blocks]);
};
export const useShapeIds = () => {
  const blocks = useBlocks("shape");
  return useMemo(() => blocks.map((b) => b.id!).sort(), [blocks]);
};
export const useProcessIds = () => {
  const blocks = useBlocks("process");
  return useMemo(() => blocks.map((b) => b.id!).sort(), [blocks]);
};

export { blocksOf };
