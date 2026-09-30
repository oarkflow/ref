// Real block trees (dumped from the starter and passport configs by
// tools/dumptrees) for tests: `npm run fixtures:trees` regenerates them.
import type { BlockNode } from "../api/types";
import treesJson from "../fixtures/trees.json";

export const trees = treesJson as unknown as Record<string, BlockNode[]>;

export function find(file: string, type: string, id: string): BlockNode {
  const n = trees[file]?.find((b) => b.type === type && b.id === id);
  if (!n) throw new Error(`fixture ${file} has no ${type} ${id}`);
  return n;
}

export const intents = (file: string) => trees[file]!.filter((b) => b.type === "intent");
