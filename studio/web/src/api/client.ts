import type {
  AssetContent, AssetList, AssetWriteResult, ImportResult, PreviewData, TemplateCatalog, TemplateInfo,
} from "./pageTypes";
import type { FlowGraph } from "../journeys/types";
import type {
  AuditEntry, BlockNode, BlockSchemas, Catalog, DraftDetail, DraftDiff, DraftSummary, FileContent, FileInfo,
  Diagnostic, Meta, Op, OpResult, PreviewStatus, RecordedRequest, Revision, RevisionSummary, StudioEvent, ValidateResult,
} from "./types";

/** An error response. Accepts both {"error":{code,message,details}} (contract) and {"error":"text"} (deploy.Admin). */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly details?: unknown,
  ) {
    super(message);
    this.name = "ApiError";
  }

  /** Diagnostics attached to a 422, when the server sent any. */
  get diagnostics(): Diagnostic[] {
    const d = this.details as { diagnostics?: Diagnostic[] } | undefined;
    return d?.diagnostics ?? [];
  }

  get isStale() {
    return this.status === 409 && this.code === "stale";
  }
}

export type FetchFn = typeof fetch;

export interface ClientOptions {
  base?: string;
  token?: () => string | null;
  fetch?: FetchFn;
  onUnauthorized?: () => void;
}

export const TOKEN_KEY = "studio.token";

export function defaultBase(): string {
  // Relative to the page, so the app works under any mount prefix.
  try {
    return new URL("api/v1", document.baseURI).pathname;
  } catch {
    return "/api/v1";
  }
}

export class StudioApi {
  private base: string;
  private tokenFn: () => string | null;
  private f: FetchFn;
  private onUnauthorized?: () => void;

  constructor(opts: ClientOptions = {}) {
    this.base = (opts.base ?? defaultBase()).replace(/\/$/, "");
    this.tokenFn = opts.token ?? (() => safeGet(TOKEN_KEY));
    this.f = opts.fetch ?? ((...a) => fetch(...a));
    this.onUnauthorized = opts.onUnauthorized;
  }

  private headers(json = false): Record<string, string> {
    const h: Record<string, string> = {};
    const t = this.tokenFn();
    if (t) h.Authorization = `Bearer ${t}`;
    if (json) h["Content-Type"] = "application/json";
    return h;
  }

  private async req<T>(method: string, path: string, body?: unknown): Promise<T> {
    const res = await this.f(this.base + path, {
      method,
      headers: this.headers(body !== undefined),
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (res.status === 204) return undefined as T;
    const text = await res.text();
    let data: unknown;
    try {
      data = text ? JSON.parse(text) : undefined;
    } catch {
      data = undefined;
    }
    if (!res.ok) {
      if (res.status === 401) this.onUnauthorized?.();
      throw toError(res.status, data, text);
    }
    return data as T;
  }

  private get = <T>(p: string) => this.req<T>("GET", p);
  private post = <T>(p: string, b: unknown = {}) => this.req<T>("POST", p, b);

  // identity / schema
  meta = () => this.get<Meta>("/meta");
  blockSchemas = () => this.get<BlockSchemas>("/schema/blocks");
  catalog = () => this.get<Catalog>("/schema/catalog");

  // drafts
  listDrafts = () => this.get<DraftSummary[]>("/drafts");
  createDraft = (body: { name?: string; from: string }) => this.post<DraftSummary>("/drafts", body);
  getDraft = (id: string) => this.get<DraftDetail>(`/drafts/${id}`);
  deleteDraft = (id: string) => this.req<void>("DELETE", `/drafts/${id}`);
  listFiles = (id: string) => this.get<FileInfo[]>(`/drafts/${id}/files`);
  getFile = (id: string, file: string) => this.get<FileContent>(`/drafts/${id}/files/${encodeURIComponent(file)}`);
  putFile = (id: string, file: string, content: string, ifVersion: number) =>
    this.req<OpResult>("PUT", `/drafts/${id}/files/${encodeURIComponent(file)}`, { content, ifVersion });
  fileTree = (id: string, file: string) => this.get<BlockNode[]>(`/drafts/${id}/files/${encodeURIComponent(file)}/tree`);
  tree = (id: string) => this.get<Record<string, BlockNode[]>>(`/drafts/${id}/tree`);
  ops = (id: string, ops: Op[], ifVersion: number, dryRun = false) =>
    this.post<OpResult>(`/drafts/${id}/ops`, { ops, ifVersion, ...(dryRun ? { dryRun } : {}) });
  undo = (id: string) => this.post<OpResult>(`/drafts/${id}/undo`);
  redo = (id: string) => this.post<OpResult>(`/drafts/${id}/redo`);
  format = (id: string) => this.post<OpResult>(`/drafts/${id}/format`);
  validate = (id: string) => this.post<ValidateResult>(`/drafts/${id}/validate`);
  /** Go serialises empty slices as null; normalise so callers can rely on arrays. */
  diff = (id: string) => this.get<DraftDiff>(`/drafts/${id}/diff`).then((d) => ({ files: d?.files ?? [], changes: d?.changes ?? [] }));
  propose = (id: string, message: string) => this.post<Revision>(`/drafts/${id}/propose`, { message });

  // revisions
  listRevisions = (limit = 100) => this.get<RevisionSummary[]>(`/revisions?limit=${limit}`).then((r) => r ?? []);
  getRevision = (id: string) => this.get<Revision>(`/revisions/${id}`);
  approve = (id: string, comment = "") => this.post<Revision>(`/revisions/${id}/approve`, { comment });
  reject = (id: string, comment = "") => this.post<Revision>(`/revisions/${id}/reject`, { comment });
  activate = (id: string) => this.post<Revision>(`/revisions/${id}/activate`);
  rollback = (to?: string, reason = "") => this.post<Revision>("/rollback", { to, reason });
  audit = (limit = 200) => this.get<AuditEntry[]>(`/audit?limit=${limit}`).then((r) => r ?? []);

  // how the app behaves for a visitor: pages, buttons, requests, logic flows (docs/studio-api.md, "Journeys")
  flows = (id: string, o: { focus?: string; depth?: number } = {}) => {
    const q = new URLSearchParams();
    if (o.focus) q.set("focus", o.focus);
    if (o.focus && o.depth !== undefined) q.set("depth", String(o.depth));
    const qs = q.toString();
    return this.get<FlowGraph>(`/drafts/${id}/flows${qs ? `?${qs}` : ""}`).then((g) => ({
      ...g, nodes: g?.nodes ?? [], edges: g?.edges ?? [], warnings: g?.warnings ?? [],
    }) as FlowGraph);
  };

  // page templates and static files (assets)
  private assetPath = (p: string) => p.split("/").map(encodeURIComponent).join("/");
  listAssets = (id: string) => this.get<AssetList>(`/drafts/${id}/assets`).then((r) => ({ version: r?.version ?? 0, assets: r?.assets ?? [] }));
  getAsset = (id: string, path: string) => this.get<AssetContent>(`/drafts/${id}/assets/${this.assetPath(path)}`);
  putAsset = (id: string, path: string, content: string, ifVersion: number, force = false) =>
    this.req<AssetWriteResult>("PUT", `/drafts/${id}/assets/${this.assetPath(path)}${force ? "?force=1" : ""}`, { content, ifVersion });
  deleteAsset = (id: string, path: string, ifVersion: number) =>
    this.req<AssetWriteResult>("DELETE", `/drafts/${id}/assets/${this.assetPath(path)}?ifVersion=${ifVersion}`);
  renameAsset = (id: string, from: string, to: string, ifVersion: number) =>
    this.post<AssetWriteResult>(`/drafts/${id}/assets/rename`, { from, to, ifVersion });
  importFromDisk = (id: string, paths: string[], ifVersion: number) =>
    this.post<ImportResult>(`/drafts/${id}/assets/import-from-disk`, { paths, ifVersion });
  templates = (id: string) =>
    this.get<TemplateCatalog>(`/drafts/${id}/templates`).then((r) => ({
      version: r?.version ?? 0,
      globals: r?.globals ?? [],
      missing: r?.missing ?? [],
      templates: (r?.templates ?? []).map(normTemplate),
    }));
  template = (id: string, name: string) => this.get<TemplateInfo>(`/drafts/${id}/templates/${this.assetPath(name)}`).then(normTemplate);
  templateData = (id: string, name: string) => this.get<PreviewData>(`/drafts/${id}/templates/${this.assetPath(name)}/preview-data`);

  // preview (placeholder wiring; UI lives in src/preview)
  startPreview = (id: string) => this.post<PreviewStatus>(`/drafts/${id}/preview`);
  stopPreview = (id: string) => this.req<void>("DELETE", `/drafts/${id}/preview`);
  previewRequests = (id: string) => this.get<RecordedRequest[]>(`/drafts/${id}/preview/requests`);

  /**
   * Subscribes to a draft's server-sent events. EventSource cannot send an
   * Authorization header, so this reads the stream with fetch; it reconnects
   * with backoff until the returned function is called.
   */
  subscribe(id: string, onEvent: (e: StudioEvent) => void, onState?: (connected: boolean) => void): () => void {
    let stopped = false;
    const ctl = new AbortController();
    const run = async () => {
      let delay = 500;
      while (!stopped) {
        try {
          const res = await this.f(`${this.base}/drafts/${id}/events`, {
            headers: { ...this.headers(), Accept: "text/event-stream" },
            signal: ctl.signal,
          });
          if (!res.ok || !res.body) throw new Error(`events: ${res.status}`);
          onState?.(true);
          delay = 500;
          const reader = res.body.getReader();
          const dec = new TextDecoder();
          const parser = new SSEParser((ev) => onEvent(ev));
          for (;;) {
            const { value, done } = await reader.read();
            if (done) break;
            parser.push(dec.decode(value, { stream: true }));
          }
        } catch {
          if (stopped) return;
        }
        onState?.(false);
        if (stopped) return;
        await new Promise((r) => setTimeout(r, delay));
        delay = Math.min(delay * 2, 10_000);
      }
    };
    void run();
    return () => {
      stopped = true;
      ctl.abort();
    };
  }
}

/** Incremental parser for the text/event-stream wire format. */
export class SSEParser {
  private buf = "";
  private event = "message";
  private data: string[] = [];

  constructor(private emit: (e: StudioEvent) => void) {}

  push(chunk: string) {
    this.buf += chunk;
    let i: number;
    while ((i = this.buf.search(/\r?\n/)) >= 0) {
      const line = this.buf.slice(0, i);
      this.buf = this.buf.slice(i + (this.buf[i] === "\r" ? 2 : 1));
      this.line(line);
    }
  }

  private line(line: string) {
    if (line === "") {
      if (this.data.length) {
        try {
          const data = JSON.parse(this.data.join("\n"));
          this.emit({ type: this.event, data } as StudioEvent);
        } catch {
          /* ignore malformed event */
        }
      }
      this.event = "message";
      this.data = [];
      return;
    }
    if (line.startsWith(":")) return;
    const c = line.indexOf(":");
    const field = c < 0 ? line : line.slice(0, c);
    const value = c < 0 ? "" : line.slice(c + 1).replace(/^ /, "");
    if (field === "event") this.event = value;
    else if (field === "data") this.data.push(value);
  }
}

function normTemplate(t: TemplateInfo): TemplateInfo {
  return {
    ...t,
    layouts: t.layouts ?? [], includes: t.includes ?? [], vars: t.vars ?? [], routes: t.routes ?? [],
    blocks: t.blocks ?? [], unfilled: t.unfilled ?? [], unknown: t.unknown ?? [], missing: t.missing ?? [], diagnostics: t.diagnostics ?? [],
  };
}

function toError(status: number, data: unknown, text: string): ApiError {
  const e = (data as { error?: unknown } | undefined)?.error;
  if (e && typeof e === "object") {
    const o = e as { code?: string; message?: string; details?: unknown };
    return new ApiError(status, o.code ?? String(status), o.message ?? `HTTP ${status}`, o.details);
  }
  if (typeof e === "string") return new ApiError(status, String(status), e);
  return new ApiError(status, String(status), text || `HTTP ${status}`);
}

function safeGet(k: string): string | null {
  try {
    return localStorage.getItem(k);
  } catch {
    return null;
  }
}
