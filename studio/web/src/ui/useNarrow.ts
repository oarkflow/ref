import { useEffect, useState } from "react";

/** Below this width the sidebar is a rail and the side panel floats over the content. */
export const NARROW_QUERY = "(max-width: 1279px)";

export function useNarrow(): boolean {
  const get = () => (typeof window !== "undefined" && typeof window.matchMedia === "function" ? window.matchMedia(NARROW_QUERY).matches : false);
  const [narrow, setNarrow] = useState(get);
  useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") return;
    const mq = window.matchMedia(NARROW_QUERY);
    const on = () => setNarrow(mq.matches);
    on();
    mq.addEventListener?.("change", on);
    return () => mq.removeEventListener?.("change", on);
  }, []);
  return narrow;
}
