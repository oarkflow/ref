// Plain words for the journey map: what each card and line is called, and what the
// server's warning codes mean to someone who does not know what a "route" is.
// Pure, so the wording is tested. The Developer view shows the raw names instead.
import { humanize } from "../labels";
import type { FlowEdgeKind, FlowNode, FlowWarning } from "./types";

export type ElementKind = "link" | "form" | "button" | "fetch";

export const ELEMENT_WORDS: Record<ElementKind, { one: string; many: string; verb: string }> = {
  link: { one: "Link", many: "Links", verb: "goes to" },
  form: { one: "Form", many: "Forms", verb: "sends" },
  button: { one: "Button", many: "Buttons", verb: "does" },
  fetch: { one: "Script call", many: "Script calls", verb: "sends" },
};

export const elementKind = (n: Pick<FlowNode, "subkind">): ElementKind =>
  (n.subkind && n.subkind in ELEMENT_WORDS ? n.subkind : "link") as ElementKind;

const str = (v: unknown): string => (typeof v === "string" ? v : "");

/**
 * What a link, form or button says. The server takes it from the template, where it may be a
 * placeholder ("…", "R/ … v…") because the text is computed; then the address it goes to is used.
 */
export function cleanLabel(n: Pick<FlowNode, "label" | "subkind" | "data">): { text: string; guessed: boolean } {
  const raw = (n.label ?? "").replace(/\s+/g, " ").trim();
  const meaningful = raw.replace(/[\s…\-–—_./\\|:;,>›→←·•*]+/g, "");
  const onlyPlaceholder = meaningful.length < 2 || /^R\/?\s*…?/.test(raw) && meaningful.length < 4;
  if (raw && !onlyPlaceholder) return { text: raw.replace(/^Request (GET|POST|PUT|PATCH|DELETE) /, "$1 "), guessed: false };
  const url = str(n.data?.url) || str(n.data?.rawUrl);
  const kind = ELEMENT_WORDS[elementKind(n)].one;
  return { text: url ? `${kind} to ${url}` : kind, guessed: true };
}

/**
 * A page's title. The server takes it from the template's heading, which may be computed
 * ("Hello, …"); then the template's own name is used: pages/dashboard/index -> "Dashboard".
 */
export function pageTitle(n: Pick<FlowNode, "label" | "id" | "data">): string {
  const label = (n.label ?? "").trim();
  if (label && !label.includes("…")) return label;
  const name = (typeof n.data?.template === "string" ? (n.data.template as string) : n.id.replace(/^page:/, "")).replace(/^pages\//, "");
  const parts = name.split("/").filter(Boolean);
  if (parts.length > 1 && parts[parts.length - 1] === "index") parts.pop();
  const last = parts.length ? parts.join(" ") : label.replace(/…/g, "").trim();
  return humanize(last) || "Page";
}

const ROUTE_CHANGES: Record<string, string> = { DELETE: "Deletes data", GET: "Reads data" };

/** The line under a card's title. */
export function typeText(n: FlowNode): string {
  switch (n.kind) {
    case "page":
      return "Page";
    case "route": {
      const method = str(n.data?.method).toUpperCase();
      if (str(n.data?.template)) return "Shows a page";
      if (n.data?.process) return "Starts a workflow";
      return ROUTE_CHANGES[method] ?? "Changes data";
    }
    case "intent":
      return n.subkind === "process" ? "Approval workflow" : "Logic flow";
    case "resource":
      return RESOURCE_WORDS[str(n.data?.category)] ?? humanize(str(n.data?.category) || "connection");
    case "external":
      return "Another website";
    case "unresolved":
      return "Nothing answers this";
    case "element":
      return ELEMENT_WORDS[elementKind(n)].one;
  }
}

export const RESOURCE_WORDS: Record<string, string> = {
  database: "Database",
  queue: "Job queue",
  cache: "Cache",
  http: "Web service",
  email: "Email",
  smtp: "Email",
  session: "Sessions",
  rules: "Rules engine",
  circuit_breaker: "Safety breaker",
  files: "File storage",
  storage: "File storage",
  auth: "Sign-in",
  authz: "Permissions",
};

/** A route's title: its address. A route without one falls back to its name. */
export function routeTitle(n: FlowNode): string {
  return str(n.data?.path) || str(n.data?.name) || n.label;
}

export interface EdgeWords {
  /** The chip on the line. */
  text: string;
  /** What the Developer view shows: the kind and the server's label as written. */
  raw: string;
  /** Hover text. */
  tip: string;
}

export function edgeWords(kind: FlowEdgeKind, serverLabel: string | undefined, extra: { category?: string } = {}): EdgeWords {
  const raw = serverLabel ? `${kind} · ${serverLabel}` : kind;
  switch (kind) {
    case "navigates":
      return { text: "goes to", raw, tip: "A visitor who clicks this link arrives at this address." };
    case "calls":
      return { text: serverLabel ? `sends ${serverLabel.toUpperCase()}` : "sends", raw, tip: "The form, button or script sends a request to this address." };
    case "runs":
      return { text: "runs", raw, tip: "This request runs this logic flow." };
    case "renders":
      return { text: "shows", raw, tip: "This request answers with this page." };
    case "redirects":
      return { text: "on success, goes to", raw, tip: "When it works, the visitor is sent on to this page." };
    case "uses":
      return { text: "uses", raw, tip: extra.category ? `Uses a ${RESOURCE_WORDS[extra.category] ?? extra.category}.` : "Uses this connection." };
    default:
      return { text: "", raw, tip: "" };
  }
}

// ---------------------------------------------------------------------------------------------

export interface WarningText {
  title: string;
  hint?: string;
}

const WARNING_TEXT: Record<string, WarningText> = {
  "flows.unresolved": { title: "A button or link goes somewhere nothing answers", hint: "Add a page or endpoint for that address, or change where it points." },
  "flows.redirect_unresolved": { title: "A form sends people on to an address nothing answers", hint: "Check the address after “redirect=”." },
  "flows.intent_missing": { title: "A request points at a logic flow that doesn’t exist", hint: "Create the flow or pick another one on the request." },
  "flows.process_missing": { title: "A request starts a workflow that doesn’t exist", hint: "Create the workflow or pick another one." },
  "flows.template_missing": { title: "A request shows a page design that doesn’t exist", hint: "Create the design in Page designs or pick another one." },
  "flows.resource_missing": { title: "A logic flow uses a connection that doesn’t exist", hint: "Add the connection or change the step that uses it." },
  "flows.dynamic_target": { title: "Some buttons go to addresses worked out while the app runs", hint: "They aren’t followed on this map." },
  "flows.relative_target": { title: "Some links use a relative address", hint: "They aren’t followed on this map. Use a full path like /todos." },
  "flows.script_missing": { title: "A page loads a script that couldn’t be found", hint: "Requests that script makes aren’t shown." },
  "flows.dynamic_route": { title: "Some requests have an address that is worked out while the app runs", hint: "They aren’t drawn." },
  "flows.entities": { title: "Data tables create endpoints of their own", hint: "Those aren’t drawn on this map." },
};

export function warningText(w: Pick<FlowWarning, "code" | "message">): WarningText {
  return WARNING_TEXT[w.code] ?? { title: w.message };
}

export interface WarningGroup extends WarningText {
  code: string;
  severity: "warning" | "info";
  items: FlowWarning[];
}

/** Warnings of one code together, real problems first. */
export function groupWarnings(ws: readonly FlowWarning[]): WarningGroup[] {
  const by = new Map<string, WarningGroup>();
  for (const w of ws) {
    let g = by.get(w.code);
    if (!g) {
      g = { code: w.code, severity: w.severity, ...warningText(w), items: [] };
      by.set(w.code, g);
    }
    g.items.push(w);
  }
  return [...by.values()].sort((a, b) => (a.severity === b.severity ? a.code.localeCompare(b.code) : a.severity === "warning" ? -1 : 1));
}

/** The entity note carries a count in its message; pull it out so the headline can say it. */
export function entityCount(ws: readonly FlowWarning[]): number {
  const w = ws.find((x) => x.code === "flows.entities");
  const m = w && /^(\d+)/.exec(w.message);
  return m ? Number(m[1]) : 0;
}
