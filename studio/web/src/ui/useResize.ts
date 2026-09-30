import { useCallback, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type PointerEvent as ReactPointerEvent } from "react";

/**
 * Drag + keyboard resizing for a panel edge. `read` is the current size,
 * `write` commits a new one (already clamped by the caller's store).
 * `dir` is +1 when dragging right grows the panel (left sidebar), -1 when it shrinks it (right panel).
 */
export function useResize(read: () => number, write: (n: number) => void, dir: 1 | -1, reset?: () => void) {
  const [dragging, setDragging] = useState(false);
  const start = useRef<{ x: number; size: number } | null>(null);

  const onPointerDown = useCallback((e: ReactPointerEvent<HTMLElement>) => {
    e.preventDefault();
    (e.currentTarget as HTMLElement).setPointerCapture?.(e.pointerId);
    start.current = { x: e.clientX, size: read() };
    setDragging(true);
    document.body.classList.add("resizing");
  }, [read]);

  const onPointerMove = useCallback((e: ReactPointerEvent<HTMLElement>) => {
    if (!start.current) return;
    write(start.current.size + dir * (e.clientX - start.current.x));
  }, [write, dir]);

  const end = useCallback((e: ReactPointerEvent<HTMLElement>) => {
    if (!start.current) return;
    start.current = null;
    (e.currentTarget as HTMLElement).releasePointerCapture?.(e.pointerId);
    setDragging(false);
    document.body.classList.remove("resizing");
  }, []);

  const onKeyDown = useCallback((e: ReactKeyboardEvent<HTMLElement>) => {
    const step = e.shiftKey ? 48 : 16;
    if (e.key === "ArrowLeft") { e.preventDefault(); write(read() - dir * step); }
    else if (e.key === "ArrowRight") { e.preventDefault(); write(read() + dir * step); }
    else if (e.key === "Home" || e.key === "Enter") { if (reset) { e.preventDefault(); reset(); } }
  }, [read, write, dir, reset]);

  return { dragging, handleProps: { onPointerDown, onPointerMove, onPointerUp: end, onPointerCancel: end, onKeyDown, onDoubleClick: reset } };
}
