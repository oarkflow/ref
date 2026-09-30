// Whether the canvas may animate. Off for people who ask for reduced motion and
// under test, where timers and transitions only add noise.
export function motionAllowed(): boolean {
  if (typeof window === "undefined") return false;
  if (import.meta.env.MODE === "test") return false;
  try {
    return !window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
  } catch {
    return true;
  }
}

/** Duration handed to React Flow's animated viewport calls. */
export const viewportMs = (on: boolean, ms = 260) => (on ? ms : 0);
