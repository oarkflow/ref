import { childPath, splitPath } from "../lib/paths";

/** Hash link that opens the canvas for a top-level block. */
export function canvasHref(file: string, path: string): string {
  return `#/canvas?file=${encodeURIComponent(file)}&path=${encodeURIComponent(path)}`;
}

/** "intent/x/node/y/requires" -> "intent/x" (the block a canvas opens). */
export function topLevelPath(path: string): string {
  const segs = splitPath(path);
  return segs.length >= 2 ? childPath(segs[0]!, segs[1]!) : path;
}

/** The block type at the head of a path. */
export function canvasTypeOf(path: string): string {
  return splitPath(path)[0] ?? "";
}
