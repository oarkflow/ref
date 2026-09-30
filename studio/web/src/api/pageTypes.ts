// Wire types for page templates and static assets (docs/studio-api.md,
// "Assets and page templates"). Go encodes empty slices as null, so the client
// normalises them before they get here.
import type { Diagnostic } from "./types";

export type AssetKind = "template" | "layout" | "component" | "static" | "other";
export type AssetStatus = "added" | "modified" | "removed" | "unchanged";

export interface AssetInfo {
  path: string;
  size: number;
  kind: AssetKind;
  status: AssetStatus;
  /** The host has a file at this path, so the asset replaces it. */
  overridesDisk: boolean;
}

export interface AssetList {
  version: number;
  assets: AssetInfo[];
}

export interface AssetContent {
  path: string;
  content: string;
  version: number;
  kind: AssetKind;
  /** `draft` when the draft carries it, `disk` when it is the host's file. */
  source: "draft" | "disk";
}

export type TemplateKind = "page" | "layout" | "component";

export interface TemplateRoute {
  file: string;
  path: string;
  route: string;
  method: string;
  url: string;
  as: "template" | "layout";
  line: number;
}

export interface TemplateInfo {
  /** `pages/todos/list` */
  name: string;
  /** `templates/pages/todos/list.html` */
  path: string;
  kind: TemplateKind;
  /** `disk` = the host's file, `draft` = new in this draft, `override` = the draft replaces a host file. */
  source: "disk" | "draft" | "override";
  extends?: string;
  layouts: string[];
  includes: string[];
  vars: string[];
  blocks?: string[];
  unfilled?: string[];
  unknown?: string[];
  missing?: string[];
  diagnostics?: Diagnostic[];
  routes: TemplateRoute[];
  unused?: boolean;
}

export interface MissingRef {
  route: string;
  file?: string;
  line?: number;
  field?: string;
  template: string;
  as?: string;
}

export interface TemplateCatalog {
  version: number;
  templates: TemplateInfo[];
  missing: MissingRef[];
  globals: string[];
}

export interface PreviewData {
  name: string;
  version: number;
  guessed: boolean;
  vars: string[];
  data: Record<string, unknown>;
}

export interface AssetWriteResult {
  version: number;
  applied?: number;
  diagnostics?: Diagnostic[];
  changed?: string[];
  template?: TemplateInfo;
}

export interface ImportResult extends AssetWriteResult {
  imported: string[];
  skipped: string[];
}
