import { createContext, useContext, type ReactNode } from "react";
import { useStore } from "zustand";
import type { StudioState, StudioStore } from "./store";

const Ctx = createContext<StudioStore | null>(null);

export function StudioProvider({ store, children }: { store: StudioStore; children: ReactNode }) {
  return <Ctx.Provider value={store}>{children}</Ctx.Provider>;
}

export function useStudioStore(): StudioStore {
  const s = useContext(Ctx);
  if (!s) throw new Error("useStudioStore outside StudioProvider");
  return s;
}

/** Like useStudioStore, but null outside a provider (for optional extras inside reusable components). */
export function useOptionalStudioStore(): StudioStore | null {
  return useContext(Ctx);
}

export function useStudio<T>(selector: (s: StudioState) => T): T {
  return useStore(useStudioStore(), selector);
}
