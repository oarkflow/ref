// Pure helpers for the Page designs screens: names, statuses, and the
// snippets the Insert menu writes. No React, no network, so they are unit-tested.
import type { AssetInfo, TemplateInfo, TemplateKind } from "../api/pageTypes";
import { humanize } from "../labels";

export const TEMPLATE_PREFIX = "templates/";
export const STATIC_PREFIX = "static/";
/** File types a draft may carry (mirrors platform.NewAssets). */
export const ASSET_EXTENSIONS = [".html", ".css", ".js", ".json", ".txt", ".svg"] as const;
export const MAX_ASSET_BYTES = 1 << 20;
/** Small text files can be uploaded from disk into the editor. */
export const MAX_UPLOAD_BYTES = 256 * 1024;

export type Status = "original" | "customized" | "new";

export const templatePath = (name: string) => `${TEMPLATE_PREFIX}${name}.html`;
export const templateName = (path: string) => path.replace(/^templates\//, "").replace(/\.html$/, "");
export const isTemplatePath = (p: string) => p.startsWith(TEMPLATE_PREFIX) && p.endsWith(".html");
export const isStaticPath = (p: string) => p.startsWith(STATIC_PREFIX);
export const editorUrl = (path: string) => `/pages/edit?path=${encodeURIComponent(path)}`;

export function statusOf(t: Pick<TemplateInfo, "source">): Status {
  return t.source === "override" ? "customized" : t.source === "draft" ? "new" : "original";
}

export function assetStatus(a: Pick<AssetInfo, "overridesDisk">): Status {
  return a.overridesDisk ? "customized" : "new";
}

export const STATUS_LABEL: Record<Status, string> = { original: "Original", customized: "Customized", new: "New" };
export const STATUS_HELP: Record<Status, string> = {
  original: "This is the app’s own file. Nothing here has been changed.",
  customized: "You changed this file. The original stays untouched and comes back if you revert.",
  new: "You created this file. It exists only in this version.",
};
export const KIND_LABEL: Record<TemplateKind, string> = { page: "Page", layout: "Layout", component: "Component" };
export const KIND_HELP: Record<TemplateKind, string> = {
  page: "A whole screen people can visit.",
  layout: "The frame around pages: header, footer and where the content goes.",
  component: "A reusable piece other designs include, like a menu or an alert.",
};

/** `pages/todos/list` -> title "Todos list", folder "pages". */
export function friendlyName(name: string): { title: string; folder: string } {
  const parts = name.split("/");
  const last = parts.pop() ?? name;
  const parent = parts.length > 1 ? parts[parts.length - 1]! : "";
  const kindDir = parts[0] ?? "";
  const title = humanize(parent && kindDir === "pages" ? `${parent} ${last}` : last);
  return { title, folder: parts.join("/") };
}

/** Where a new design of `kind` goes, from a plain name like "About us". */
export function newTemplateName(kind: TemplateKind, input: string): string | null {
  // Dots are dropped, so ".." can never survive; each path segment is tidied on its own.
  const slug = input.trim().toLowerCase().replace(/[^a-z0-9/_-]+/g, "-").replace(/-+/g, "-").split("/").map((seg) => seg.replace(/^-+|-+$/g, "")).filter(Boolean).join("/");
  if (!slug) return null;
  const dir = kind === "page" ? "pages" : kind === "layout" ? "layouts" : "components";
  return slug.startsWith(`${dir}/`) ? slug : `${dir}/${slug}`;
}

export function newStaticPath(input: string): string | null {
  let p = input.trim().replace(/^\/+/, "");
  if (!p) return null;
  if (!p.startsWith(STATIC_PREFIX)) p = `${STATIC_PREFIX}${p}`;
  if (p.split("/").some((s) => s === ".." || s === "." || s === "" || s.startsWith("."))) return null;
  if (!ASSET_EXTENSIONS.some((e) => p.endsWith(e))) return null;
  return p;
}

/** A starting point for a brand-new design of the given kind. */
export function starterSource(kind: TemplateKind, name: string): string {
  const { title } = friendlyName(name);
  switch (kind) {
    case "layout":
      return `<!doctype html>\n<html lang="en">\n<head>\n  <meta charset="utf-8">\n  <title>\${title} · \${appName}</title>\n  @block("head") {}\n</head>\n<body>\n  @block("content") {}\n</body>\n</html>\n`;
    case "component":
      return `<div class="${name.split("/").pop()}">\n  \${label}\n</div>\n`;
    default:
      return `@extends("layouts/base.html")\n\n@define("content") {\n  <h1 class="page-title">${title}</h1>\n}\n`;
  }
}

export function languageOf(path: string): "html" | "css" | "js" | "json" | "text" {
  if (path.endsWith(".html") || path.endsWith(".svg")) return "html";
  if (path.endsWith(".css")) return "css";
  if (path.endsWith(".js")) return "js";
  if (path.endsWith(".json")) return "json";
  return "text";
}

// ---- snippets (the Insert menu) --------------------------------------------
// SPL notes: `${x}` prints a value; `@include("components/x.html")` pulls a file
// in; an `@if` condition must be a comparison, not a bare name (a bare name is
// treated as a client-side toggle by the engine).

export const includeSnippet = (name: string) => `@include("${name.replace(/\.html$/, "")}.html")`;
export const variableSnippet = (name: string) => `\${${name}}`;

export const buttonSnippet = (label: string) => `<button type="button" class="btn btn-primary">${escapeHtml(label || "Click me")}</button>`;

/** `/todos/{id}` -> `/todos/${id}` so the link works inside a repeat. */
export function urlForTemplate(routePath: string): string {
  return routePath.replace(/\{([A-Za-z_][\w.]*)\}/g, "${$1}").replace(/:([A-Za-z_]\w*)/g, "${$1}");
}

export function linkSnippet(routePath: string, label: string): string {
  return `<a href="${urlForTemplate(routePath)}" class="btn">${escapeHtml(label || "Open")}</a>`;
}

/**
 * A form that calls a route. The app's convention for "where to go afterwards"
 * is a `redirect` query parameter on the action (`/todos?redirect=/todos`).
 */
export function formSnippet(method: string, routePath: string, opts: { redirect?: string; submit?: string; fields?: string[] } = {}): string {
  const m = method.toUpperCase();
  const httpMethod = m === "GET" ? "GET" : "POST"; // HTML forms only send GET and POST
  const action = urlForTemplate(routePath) + (opts.redirect ? `?redirect=${urlForTemplate(opts.redirect)}` : "");
  const fields = (opts.fields?.length ? opts.fields : ["title"]).map((f) => {
    const safe = f.replace(/[^\w-]/g, "");
    return `  <div class="form-field">\n    <label class="form-label" for="${safe}">${escapeHtml(humanize(safe))}</label>\n    <input type="text" id="${safe}" name="${safe}" class="form-control">\n  </div>`;
  });
  return `<form method="${httpMethod}" action="${action}">\n${fields.join("\n")}\n  <button type="submit" class="btn btn-primary">${escapeHtml(opts.submit || "Save")}</button>\n</form>`;
}

export function escapeHtml(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

// ---- guessed sample data ------------------------------------------------------

/** Plain words for a variable's guessed type. */
export function describeValue(v: unknown): string {
  if (Array.isArray(v)) return "a list";
  if (v === null || v === undefined) return "anything";
  switch (typeof v) {
    case "number": return "a number";
    case "boolean": return "yes or no";
    case "object": return "a record";
    default: return /^\d{4}-\d\d-\d\dT/.test(String(v)) ? "a date" : "text";
  }
}

export interface RouteChoice {
  name: string;
  method: string;
  path: string;
  hasTemplate: boolean;
}

export function bytesLabel(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(n < 10 * 1024 ? 1 : 0)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}
