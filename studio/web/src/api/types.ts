// Types mirror docs/studio-api.md. Revision JSON keeps deploy's snake_case;
// draft JSON is camelCase, as in the contract.

export type Role = "viewer" | "editor" | "reviewer" | "admin";

export type Severity = "error" | "warning" | "info";

export interface Diagnostic {
  severity: Severity;
  code?: string;
  message: string;
  file?: string;
  line?: number;
  column?: number;
  offset?: number;
  /** "route/web.todos_list/path" */
  path?: string;
}

export interface BlockNode {
  kind: "block" | "field";
  path: string;
  type?: string;
  id?: string;
  name?: string;
  raw?: string;
  comment?: string;
  line: number;
  start: number;
  end: number;
  children?: BlockNode[];
}

export type Op =
  | { op: "setField"; file: string; path: string; value: string }
  | { op: "removeField"; file: string; path: string }
  | { op: "addBlock"; file: string; parent?: string; type: string; id?: string; body?: string }
  | { op: "removeBlock"; file: string; path: string }
  | { op: "renameBlock"; file: string; path: string; newId: string }
  | { op: "moveBlock"; file: string; path: string; index: number }
  | { op: "addFile"; file: string; content?: string }
  | { op: "renameFile"; file: string; newFile: string }
  | { op: "removeFile"; file: string };

export interface DiagnosticCounts {
  errors: number;
  warnings: number;
}

export interface DraftSummary {
  id: string;
  owner: string;
  name: string;
  baseRevision?: string;
  version: number;
  files: string[];
  dirty: boolean;
  createdAt: string;
  updatedAt: string;
  /** In a summary: counts. In GET /drafts/{id}: the full list (see DraftDetail). */
  diagnostics: DiagnosticCounts;
  /** Optional hints; the mock server sends them, the contract may add them. */
  canUndo?: boolean;
  canRedo?: boolean;
}

export type DraftDetail = Omit<DraftSummary, "diagnostics"> & { diagnostics: Diagnostic[] };

export interface FileInfo {
  path: string;
  size: number;
  diagnostics: DiagnosticCounts;
}

export interface FileContent {
  path: string;
  content: string;
  version: number;
}

export interface OpResult {
  version: number;
  applied: number;
  diagnostics: Diagnostic[];
  changed: string[];
}

export interface ValidateResult {
  valid: boolean;
  diagnostics: Diagnostic[];
  summary?: Record<string, unknown>;
}

export interface DocumentChange {
  kind: string;
  name: string;
  change: "added" | "removed" | "changed";
}

export interface DraftDiff {
  files: { path: string; status: "added" | "modified" | "removed"; unified?: string }[];
  changes: DocumentChange[];
}

export interface Meta {
  app: string;
  version?: string;
  identity: { name: string; roles: Role[] };
  activeRevision?: string | null;
  features: { preview: boolean; pages: boolean };
}

// ---- schema -----------------------------------------------------------------

export type FieldKind =
  | "string" | "ident" | "int" | "number" | "bool" | "duration"
  | "list" | "map" | "block" | "blocks" | "any";

export interface FieldSchema {
  name: string;
  kind: FieldKind;
  items?: string;
  optional?: boolean;
  doc?: string;
  block?: BlockSchema;
  recursive?: boolean;
}

export interface BlockSchema {
  name?: string;
  go_type: string;
  has_id?: boolean;
  doc?: string;
  fields: FieldSchema[];
}

export type BlockSchemas = Record<string, BlockSchema>;

export interface ConfigField {
  name: string;
  type: string;
  required?: boolean;
  summary?: string;
  default?: string;
}

export interface Catalog {
  node_types: { name: string; family: string; summary: string; default_action?: string; resource_kinds?: string[]; terminal?: boolean; durable?: boolean }[];
  edge_types: { name: string; family: string; summary: string; fields?: string[]; multi?: boolean; parks?: boolean; error_path?: boolean }[];
  resource_kinds: { name: string; family: string; summary: string; config?: ConfigField[]; provides?: string[] }[];
  actions: { name: string; family: string; summary: string; resource_kind?: string; config?: ConfigField[]; provides?: string; kind?: string }[];
}

// ---- revisions --------------------------------------------------------------

export type RevisionStatus = "pending" | "approved" | "rejected" | "active" | "superseded" | "failed";

export interface Decision {
  by: string;
  at: string;
  comment?: string;
}

export interface FileChange {
  path: string;
  status: "added" | "modified" | "removed";
}

export interface RevisionSummary {
  id: string;
  seq: number;
  status: RevisionStatus;
  author: string;
  message?: string;
  created_at: string;
  activated_at?: string | null;
  approvals?: Decision[];
  changes?: DocumentChange[];
  checksum?: string;
  failure?: string;
  base_id?: string;
  /** Paths, in a list response. */
  files?: string[];
  changed_files?: FileChange[];
}

export interface Revision extends Omit<RevisionSummary, "files"> {
  app: string;
  source?: string;
  /** Full files, in a detail response. */
  files?: { path: string; content: string }[];
  rejection?: Decision;
  activated_by?: string;
  warnings?: string[];
}

export interface AuditEntry {
  at: string;
  who: string;
  action: string;
  target: string;
  detail?: string;
}

export type StudioEvent =
  | { type: "changed"; data: { version: number; changed?: string[] } }
  | { type: "diagnostics"; data: { diagnostics: Diagnostic[] } }
  | { type: "preview"; data: PreviewEventData };

export interface PreviewStatus {
  status: "starting" | "ready" | "failed";
  url?: string;
  /** Draft version the generation was built from (absent on older servers). */
  version?: number;
  error?: Diagnostic[];
  message?: string;
}

/** A preview SSE event: a PreviewStatus, or "stopped" when the generation is gone. */
export interface PreviewEventData {
  status: "starting" | "ready" | "failed" | "stopped";
  url?: string;
  version?: number;
  error?: Diagnostic[];
  message?: string;
}

/**
 * One request the preview served ("request") or one outbound call it stubbed
 * ("outbound"). Mirrors studio.RecordedRequest.
 */
export interface RecordedRequest {
  at: string;
  kind: "request" | "outbound";
  method?: string;
  url?: string;
  status?: number;
  durationMs?: number;
  /** request: route, intent, version. outbound: channel, preview, bytes. */
  detail?: { route?: string; intent?: string; version?: number; channel?: string; preview?: string; bytes?: number } & Record<string, unknown>;
}
