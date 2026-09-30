// Shared motion tokens. The CSS twin lives in styles/motion.css; keep them in sync.
// Exported so other screens (the canvas, previews) animate the same way.
import type { Transition, Variants } from "motion/react";

/** Seconds, as the motion library expects. */
export const DURATION = { fast: 0.12, base: 0.2, slow: 0.32 } as const;

export const EASE = {
  out: [0.16, 1, 0.3, 1],
  in: [0.7, 0, 0.84, 0],
  inOut: [0.65, 0, 0.35, 1],
} as const;

export const SPRING = {
  /** Panels sliding or resizing: quick, slightly springy. */
  panel: { type: "spring", stiffness: 380, damping: 34, mass: 0.9 },
  /** Reordering / layout changes. */
  layout: { type: "spring", stiffness: 500, damping: 38 },
  /** Small pops: menus, toggles, badges. */
  pop: { type: "spring", stiffness: 520, damping: 30 },
} as const satisfies Record<string, Transition>;

export const enter: Transition = { duration: DURATION.base, ease: EASE.out };
export const exit: Transition = { duration: DURATION.fast, ease: EASE.in };

/**
 * True when animation must be skipped entirely: unit tests (deterministic,
 * no timers) or an explicit opt-out. `prefers-reduced-motion` is handled by
 * <MotionRoot> (opacity only) and by the CSS media query.
 */
export function motionOff(): boolean {
  if (import.meta.env.MODE === "test") return true;
  try {
    if (typeof window !== "undefined" && (window as unknown as { __STUDIO_MOTION__?: string }).__STUDIO_MOTION__ === "off") return true;
    return localStorage.getItem("studio.motion") === "off";
  } catch {
    return false;
  }
}

/** Reusable variants. */
export const variants = {
  fade: {
    hidden: { opacity: 0 },
    show: { opacity: 1, transition: enter },
    exit: { opacity: 0, transition: exit },
  },
  slideUp: {
    hidden: { opacity: 0, y: 8 },
    show: { opacity: 1, y: 0, transition: enter },
    exit: { opacity: 0, y: -4, transition: exit },
  },
  pop: {
    hidden: { opacity: 0, scale: 0.96, y: 4 },
    show: { opacity: 1, scale: 1, y: 0, transition: SPRING.pop },
    exit: { opacity: 0, scale: 0.98, transition: exit },
  },
  page: {
    hidden: { opacity: 0, y: 6 },
    show: { opacity: 1, y: 0, transition: { duration: DURATION.base, ease: EASE.out } },
    exit: { opacity: 0, transition: { duration: DURATION.fast, ease: EASE.in } },
  },
  list: {
    hidden: {},
    show: { transition: { staggerChildren: 0.03, delayChildren: 0.02 } },
  },
  item: {
    hidden: { opacity: 0, y: 8 },
    show: { opacity: 1, y: 0, transition: enter },
  },
} satisfies Record<string, Variants>;
