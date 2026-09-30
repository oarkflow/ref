// Shared fixtures for the Page designs tests.
import { render } from "@testing-library/react";
import type { ReactElement } from "react";
import { MemoryRouter } from "react-router-dom";
import { vi } from "vitest";
import type { AssetContent, AssetInfo, TemplateCatalog, TemplateInfo } from "../api/pageTypes";
import { StudioProvider } from "../state/context";
import { createStudioStore } from "../state/store";
import { fakeApi, meta, type FakeApi } from "../test/fakeApi";

export const tpl = (over: Partial<TemplateInfo> = {}): TemplateInfo => ({
  name: "pages/todos/list", path: "templates/pages/todos/list.html", kind: "page", source: "disk",
  extends: "layouts/base", layouts: ["layouts/base"], includes: ["components/row"], vars: ["appName", "todos"],
  blocks: ["content", "head"], unfilled: ["head"], unknown: [], missing: [], diagnostics: [], unused: false,
  routes: [{ file: "13_todo_pages.bcl", path: "route/web.todos_list", route: "web.todos_list", method: "GET", url: "/todos", as: "template", line: 3 }],
  ...over,
});

export const layout = () =>
  tpl({ name: "layouts/base", path: "templates/layouts/base.html", kind: "layout", extends: undefined, layouts: [], includes: ["components/alert"], vars: ["appName"], routes: [{ file: "13.bcl", path: "route/web.todos_list", route: "web.todos_list", method: "GET", url: "/todos", as: "layout", line: 4 }] });
export const component = (name = "components/row", over: Partial<TemplateInfo> = {}) =>
  tpl({ name, path: `templates/${name}.html`, kind: "component", extends: undefined, layouts: [], includes: [], vars: ["todo"], routes: [], ...over });

export const catalogOf = (templates: TemplateInfo[], over: Partial<TemplateCatalog> = {}): TemplateCatalog => ({
  version: 1, templates, missing: [], globals: ["appName", "title"], ...over,
});

export const defaultTemplates = () => [component("components/alert"), component("components/row"), layout(), tpl()];

export interface PagesFake extends FakeApi {
  files: Map<string, { content: string; source: "draft" | "disk" }>;
}

/** A fake API with the page endpoints, backed by a tiny in-memory file map. */
export function pagesApi(over: Partial<Record<string, unknown>> = {}, templates: TemplateInfo[] = defaultTemplates()): PagesFake {
  const files = new Map<string, { content: string; source: "draft" | "disk" }>();
  for (const t of templates) files.set(t.path, { content: `@extends("layouts/base.html")\n<!-- ${t.name} -->\n`, source: t.source === "disk" ? "disk" : "draft" });
  const api = fakeApi({
    templates: vi.fn(async () => catalogOf(templates.map((t) => ({ ...t, source: files.get(t.path)?.source === "draft" ? (t.source === "disk" ? "override" : t.source) : t.source })))),
    listAssets: vi.fn(async () => ({
      version: 1,
      assets: [...files.entries()].filter(([, f]) => f.source === "draft").map(([path]): AssetInfo => ({ path, size: 10, kind: "template", status: "added", overridesDisk: true })),
    })),
    getAsset: vi.fn(async (_id: string, path: string): Promise<AssetContent> => {
      const f = files.get(path);
      if (!f) throw new Error("not found");
      return { path, content: f.content, version: 1, kind: "template", source: f.source };
    }),
    putAsset: vi.fn(async (_id: string, path: string, content: string) => {
      files.set(path, { content, source: "draft" });
      return { version: 2, applied: 1, diagnostics: [], changed: [path] };
    }),
    deleteAsset: vi.fn(async (_id: string, path: string) => {
      const t = templates.find((x) => x.path === path);
      if (t && t.source === "disk") files.set(path, { content: files.get(path)!.content, source: "disk" });
      else files.delete(path);
      return { version: 3 };
    }),
    renameAsset: vi.fn(async (_id: string, from: string, to: string) => {
      const f = files.get(from);
      if (f) { files.delete(from); files.set(to, f); }
      return { version: 4 };
    }),
    importFromDisk: vi.fn(async (_id: string, paths: string[]) => {
      for (const p of paths) files.set(p, { content: files.get(p)?.content ?? "", source: "draft" });
      return { version: 2, imported: paths, skipped: [] };
    }),
    templateData: vi.fn(async (_id: string, name: string) => ({ name, version: 1, guessed: true, vars: ["todos"], data: { appName: "Sample App", todos: [{ id: 1, title: "Sample" }] } })),
    ...over,
  }) as PagesFake;
  api.files = files;
  return api;
}

export async function mount(ui: ReactElement, opts: { api?: PagesFake; roles?: ("viewer" | "editor" | "reviewer" | "admin")[]; route?: string } = {}) {
  const api = opts.api ?? pagesApi();
  if (opts.roles) api.meta = vi.fn(async () => meta(opts.roles)) as never;
  const store = createStudioStore(api, { batchDelay: 5, validateDelay: 60_000 });
  store.setState({ token: "t" });
  await store.getState().init();
  const view = render(
    <StudioProvider store={store}>
      <MemoryRouter initialEntries={[opts.route ?? "/pages"]}>{ui}</MemoryRouter>
    </StudioProvider>,
  );
  return { api, store, ...view };
}
