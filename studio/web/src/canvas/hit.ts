// Which connection is under the pointer. React Flow draws every edge as an SVG
// group with a wide, invisible hit path (`data-id` is the edge id), so the
// browser's own hit test answers this without any geometry of ours.

export function edgeIdAt(x: number, y: number): string | null {
  if (typeof document === "undefined" || typeof document.elementsFromPoint !== "function") return null;
  for (const el of document.elementsFromPoint(x, y)) {
    const id = el.closest(".react-flow__edge")?.getAttribute("data-id");
    if (id) return id;
  }
  return null;
}
