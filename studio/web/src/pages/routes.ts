// Reads the routes out of the open draft's configuration, for the Insert menu
// (link/form targets) and the page designer.
import { useEffect, useMemo } from "react";
import { unquote } from "../lib/bcl";
import { fieldOf } from "../lib/paths";
import { collectItems } from "../nav/model";
import { useStudio, useStudioStore } from "../state/context";
import type { RouteChoice } from "./lib";

/** All routes of the draft, with their method and address. Loads the trees on demand. */
export function useRouteChoices(): RouteChoice[] {
  const store = useStudioStore();
  const nav = useStudio((s) => s.nav);
  const trees = useStudio((s) => s.trees);
  const draftId = useStudio((s) => s.draft?.id);
  useEffect(() => { void store.getState().ensureTrees(); }, [store, draftId]);
  return useMemo(() => routeChoices(collectItems(nav, trees)), [nav, trees]);
}

export function routeChoices(items: ReturnType<typeof collectItems>): RouteChoice[] {
  const out: RouteChoice[] = [];
  for (const it of items) {
    if (it.type !== "route") continue;
    const val = (n: string) => {
      const raw = fieldOf(it.node, n)?.raw;
      return raw === undefined ? undefined : (unquote(raw) ?? raw.trim());
    };
    const path = val("path");
    if (!path) continue;
    out.push({ name: it.id || it.name, method: (val("method") ?? "GET").toUpperCase(), path, hasTemplate: !!val("template") });
  }
  return out.sort((a, b) => a.path.localeCompare(b.path) || a.method.localeCompare(b.method));
}
