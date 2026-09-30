import { AnimatePresence, LayoutGroup, MotionConfig, motion, useReducedMotion, type HTMLMotionProps } from "motion/react";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { DURATION, EASE, SPRING, enter, exit, motionOff, variants } from "./motion";

export { AnimatePresence, LayoutGroup, motion, useReducedMotion };

/** Wraps the app: honours the user's reduced-motion setting, disables animation in tests. */
export function MotionRoot({ children }: { children: ReactNode }) {
  const off = motionOff();
  return (
    <MotionConfig reducedMotion={off ? "always" : "user"} transition={off ? { duration: 0 } : undefined}>
      {children}
    </MotionConfig>
  );
}

/** AnimatePresence that renders plainly when motion is off. */
export function Presence({ children, mode = "popLayout", initial = false }: { children: ReactNode; mode?: "sync" | "wait" | "popLayout"; initial?: boolean }) {
  if (motionOff()) return <>{children}</>;
  return <AnimatePresence mode={mode} initial={initial}>{children}</AnimatePresence>;
}

type DivProps = Omit<HTMLMotionProps<"div">, "variants" | "initial" | "animate" | "exit">;

const MOTION_ONLY = new Set(["layout", "layoutId", "transition", "whileHover", "whileTap", "whileFocus", "whileInView", "drag", "dragConstraints", "onAnimationComplete", "onAnimationStart", "custom"]);
/** Props safe to put on a plain <div> when motion is off. */
function plain(rest: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(rest)) if (!MOTION_ONLY.has(k)) out[k] = v;
  return out;
}

/** Fade + slight rise. Use `presence` children inside <Presence>. */
export function Enter({ children, delay = 0, ...rest }: DivProps & { delay?: number }) {
  if (motionOff()) return <div {...(plain(rest) as object)}>{children as ReactNode}</div>;
  return (
    <motion.div {...rest} variants={variants.slideUp} initial="hidden" animate="show" exit="exit" transition={{ ...enter, delay }}>
      {children}
    </motion.div>
  );
}

/** Scale + fade, for menus, popovers, dialogs. */
export function Pop({ children, ...rest }: DivProps) {
  if (motionOff()) return <div {...(plain(rest) as object)}>{children as ReactNode}</div>;
  return (
    <motion.div {...rest} variants={variants.pop} initial="hidden" animate="show" exit="exit">
      {children}
    </motion.div>
  );
}

/** Cross-fade + slight slide between routes. Key it by the location. */
export function PageTransition({ id, children, className }: { id: string; children: ReactNode; className?: string }) {
  if (motionOff()) return <div className={className}>{children}</div>;
  return (
    <motion.div key={id} className={className} variants={variants.page} initial="hidden" animate="show">
      {children}
    </motion.div>
  );
}

/** Children stagger in. */
export function Stagger({ children, className, as = "div" }: { children: ReactNode; className?: string; as?: "div" | "ul" | "ol" }) {
  if (motionOff()) {
    const Tag = as;
    return <Tag className={className}>{children}</Tag>;
  }
  const M = motion[as];
  return (
    <M className={className} variants={variants.list} initial="hidden" animate="show">
      {children}
    </M>
  );
}
export function StaggerItem({ children, className, as = "div" }: { children: ReactNode; className?: string; as?: "div" | "li" }) {
  if (motionOff()) {
    const Tag = as;
    return <Tag className={className}>{children}</Tag>;
  }
  const M = motion[as];
  return (
    <M className={className} variants={variants.item}>
      {children}
    </M>
  );
}

/**
 * Height-animated collapse. `open=false` unmounts children after the exit
 * animation. Height is the one layout property we animate, and only here.
 */
export function Collapse({ open, children, className }: { open: boolean; children: ReactNode; className?: string }) {
  if (motionOff()) return open ? <div className={className}>{children}</div> : null;
  return (
    <AnimatePresence initial={false}>
      {open && (
        <motion.div
          key="c"
          className={className}
          style={{ overflow: "hidden" }}
          initial={{ height: 0, opacity: 0 }}
          animate={{ height: "auto", opacity: 1, transition: { height: { duration: DURATION.base, ease: EASE.out }, opacity: { duration: DURATION.base, delay: 0.04 } } }}
          exit={{ height: 0, opacity: 0, transition: { height: { duration: DURATION.fast + 0.04, ease: EASE.in }, opacity: { duration: DURATION.fast } } }}
        >
          {children}
        </motion.div>
      )}
    </AnimatePresence>
  );
}

/** A list row that animates in, out and when its siblings reorder. */
export function Row({ children, className, ...rest }: DivProps) {
  if (motionOff()) return <div className={className} {...(plain(rest) as object)}>{children as ReactNode}</div>;
  return (
    <motion.div
      {...rest}
      className={className}
      layout="position"
      initial={{ opacity: 0, y: 6 }}
      animate={{ opacity: 1, y: 0, transition: enter }}
      exit={{ opacity: 0, x: -8, transition: exit }}
      transition={SPRING.layout}
    >
      {children}
    </motion.div>
  );
}

/** Count that eases from its previous value. Purely cosmetic: the text is always the true value at rest. */
export function AnimatedNumber({ value, className }: { value: number; className?: string }) {
  const [shown, setShown] = useState(value);
  const from = useRef(value);
  const reduce = useReducedMotion();
  useEffect(() => {
    if (motionOff() || reduce || from.current === value) {
      from.current = value;
      setShown(value);
      return;
    }
    const start = from.current;
    const t0 = performance.now();
    const dur = 360;
    let raf = 0;
    const tick = (t: number) => {
      const p = Math.min(1, (t - t0) / dur);
      const e = 1 - Math.pow(1 - p, 3);
      setShown(Math.round(start + (value - start) * e));
      if (p < 1) raf = requestAnimationFrame(tick);
      else from.current = value;
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [value, reduce]);
  return <span className={className}>{shown}</span>;
}

/** Applies a one-shot CSS animation class whenever `token` changes (skips the first render). */
export function useOnce(token: unknown, cls: string): { ref: React.RefObject<HTMLElement | null> } {
  const ref = useRef<HTMLElement | null>(null);
  const first = useRef(true);
  useEffect(() => {
    if (first.current) {
      first.current = false;
      return;
    }
    const el = ref.current;
    if (!el || motionOff()) return;
    el.classList.remove(cls);
    void el.offsetWidth;
    el.classList.add(cls);
  }, [token, cls]);
  return { ref };
}

export function Skeleton({ lines = 3, title = false }: { lines?: number; title?: boolean }) {
  return (
    <div className="skeleton-group" aria-busy="true" aria-label="Loading">
      {title && <div className="skeleton title" />}
      {Array.from({ length: lines }, (_, i) => <div key={i} className="skeleton line" style={{ width: `${88 - i * 14}%` }} />)}
    </div>
  );
}

/** Animated tick for success states. */
export function CheckDraw() {
  return (
    <svg className="check-draw" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M5 12.5l4.5 4.5L19 7.5" />
    </svg>
  );
}
