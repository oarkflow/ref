// The single source of truth for how Studio talks about things. UI code never
// shows a raw BCL keyword without going through here; the raw name stays
// available (tooltips, Developer view) via `raw`.
//
// Exports for other screens (e.g. the canvas): blockInfo, fieldInfo, fieldLabel,
// humanize, categoryOf, CATEGORIES, statusInfo, roleLabel, friendlyProblem, MODE_LABELS.

import type { Diagnostic } from "../api/types";

export type IconName =
  | "globe" | "workflow" | "plug" | "database" | "table" | "shapes" | "cog" | "clock" | "webhook" | "flag" | "folder"
  | "shield" | "users" | "key" | "git-branch" | "clipboard-check" | "settings" | "layers" | "layout-dashboard" | "history"
  | "coins" | "file-text" | "circle-help" | "sparkles" | "route" | "building" | "list-checks" | "lock" | "zap" | "box" | "layout-template" | "file-code";

export type CategoryId = "pages" | "flows" | "connections" | "data" | "automation" | "access" | "settings" | "other";

export interface Category {
  id: CategoryId;
  label: string;
  /** Short line under the title on the list screen. */
  blurb: string;
  icon: IconName;
}

export const CATEGORIES: Category[] = [
  { id: "pages", label: "Pages & APIs", blurb: "The addresses people and other systems can visit.", icon: "globe" },
  { id: "flows", label: "Logic flows", blurb: "What happens when something is requested: steps, decisions and approvals.", icon: "workflow" },
  { id: "connections", label: "Connections", blurb: "Databases, caches, email and other services this app talks to.", icon: "plug" },
  { id: "data", label: "Data", blurb: "The shape of your information and the tables behind it.", icon: "database" },
  { id: "automation", label: "Automation", blurb: "Background jobs, scheduled tasks and webhooks.", icon: "zap" },
  { id: "access", label: "Access & security", blurb: "Who can do what.", icon: "shield" },
  { id: "settings", label: "Settings", blurb: "Feature flags and environment-specific settings.", icon: "settings" },
  { id: "other", label: "Other", blurb: "Everything else in the configuration.", icon: "box" },
];

export function categoryInfo(id: CategoryId): Category {
  return CATEGORIES.find((c) => c.id === id) ?? CATEGORIES[CATEGORIES.length - 1]!;
}

export interface BlockInfo {
  /** The BCL keyword. */
  raw: string;
  label: string;
  plural: string;
  description: string;
  icon: IconName;
  category: CategoryId;
  /** Placeholder for the name field in the add flow. */
  namePlaceholder?: string;
  /** What the name is called in the add flow ("Address name" ...). */
  nameLabel?: string;
}

const B = (raw: string, label: string, plural: string, description: string, icon: IconName, category: CategoryId, namePlaceholder?: string, nameLabel = "Name"): BlockInfo =>
  ({ raw, label, plural, description, icon, category, namePlaceholder, nameLabel });

export const BLOCKS: Record<string, BlockInfo> = {
  route: B("route", "Page or API endpoint", "Pages & API endpoints", "An address that shows a page or answers a request, and the logic flow behind it.", "globe", "pages", "web.todos_list", "Endpoint name"),
  route_group: B("route_group", "Endpoint group", "Endpoint groups", "Shared settings (security, limits, prefix) applied to a set of endpoints.", "layers", "pages", "web", "Group name"),
  static: B("static", "Static files", "Static files", "Serves files such as images, styles and scripts from a folder.", "folder", "pages", "assets", "Name"),
  intent: B("intent", "Logic flow", "Logic flows", "A series of steps that runs when an endpoint is called.", "workflow", "flows", "todo.list", "Flow name"),
  process: B("process", "Approval workflow", "Approval workflows", "A long-running workflow that can wait for people, retries and deadlines.", "git-branch", "flows", "todo.workflow", "Workflow name"),
  pipeline: B("pipeline", "Review pipeline", "Review pipelines", "Multi-stage review and approval of a case or application.", "clipboard-check", "flows", "passport", "Pipeline name"),
  resource: B("resource", "Connection", "Connections", "A database, cache, queue, email service or other system the app uses.", "plug", "connections", "database", "Connection name"),
  secret: B("secret", "Secret", "Secrets", "A password or key that is read from the environment, never stored in the config.", "key", "connections", "SESSION_KEY", "Secret name"),
  shape: B("shape", "Data shape", "Data shapes", "Describes what a piece of data looks like, so it can be checked.", "shapes", "data", "user", "Shape name"),
  entity: B("entity", "Data table & API", "Data tables & APIs", "A database table together with an automatic API to list, create and edit its rows.", "table", "data", "todos", "Table name"),
  currency: B("currency", "Currency", "Currencies", "How amounts of money are stored and shown.", "coins", "data", "USD", "Currency code"),
  worker: B("worker", "Background job", "Background jobs", "Picks up queued jobs and runs a logic flow for each.", "cog", "automation", "welcome-delivery", "Job name"),
  schedule: B("schedule", "Scheduled task", "Scheduled tasks", "Runs something on a timetable, such as every night.", "clock", "automation", "daily-digest", "Task name"),
  trigger: B("trigger", "Webhook", "Webhooks", "Starts a logic flow when another system sends an event.", "webhook", "automation", "payment_webhook", "Webhook name"),
  role: B("role", "Role", "Roles", "A named set of permissions that can be given to people.", "users", "access", "reviewer", "Role name"),
  tenant: B("tenant", "Tenant", "Tenants", "A separate customer or organisation sharing this app.", "building", "access", "acme", "Tenant name"),
  flag: B("flag", "Feature flag", "Feature flags", "Turns a feature on or off, for everyone or for some people.", "flag", "settings", "priority_shipping", "Flag name"),
  profile: B("profile", "Environment settings", "Environment settings", "Overrides that apply in one environment, such as production.", "settings", "settings", "production", "Environment name"),
};

/** Nested blocks and anything without a dedicated entry still read sensibly. */
export function blockInfo(type: string | undefined): BlockInfo {
  const t = type ?? "";
  const known = BLOCKS[t];
  if (known) return known;
  const label = humanize(t) || "Item";
  return { raw: t, label, plural: `${label}s`, description: "", icon: "box", category: "other", nameLabel: "Name" };
}

export function categoryOf(type: string | undefined): CategoryId {
  return blockInfo(type).category;
}

/** Block types belonging to a category, in a stable friendly order. */
export function typesIn(category: CategoryId): string[] {
  return Object.values(BLOCKS).filter((b) => b.category === category).map((b) => b.raw);
}

// ---------------------------------------------------------------------------
// Fields
// ---------------------------------------------------------------------------

export type SectionId = "basics" | "security" | "performance" | "advanced";

export const SECTIONS: { id: SectionId; label: string; blurb: string; icon: IconName }[] = [
  { id: "basics", label: "Basics", blurb: "What it is and what it does.", icon: "sparkles" },
  { id: "security", label: "Security & access", blurb: "Who can reach it and how they prove who they are.", icon: "shield" },
  { id: "performance", label: "Performance & limits", blurb: "Timeouts, caching and protection against overload.", icon: "zap" },
  { id: "advanced", label: "Advanced", blurb: "Less common settings.", icon: "settings" },
];

export interface FieldInfo {
  label: string;
  help?: string;
  section?: SectionId;
}

const F = (label: string, help?: string, section?: SectionId): FieldInfo => ({ label, help, section });

/** Friendly labels and help for fields that appear in many places. */
export const FIELDS: Record<string, FieldInfo> = {
  method: F("Method", "How the request is made. GET reads something, POST creates or changes something."),
  path: F("Address", "The web address, starting with /. Use {name} for a changing part, like /todos/{id}."),
  intent: F("Logic flow", "The flow that runs when this is called."),
  process: F("Approval workflow", "Start a long-running workflow instead of a single flow."),
  mode: F("Response style", "Answer straight away, queue the work for later, stream updates, or send a file."),
  queue: F("Job queue", "Where background work waits until a job picks it up."),
  session: F("Login session", "The session store that remembers who is signed in."),
  auth: F("Sign-in method", "How visitors prove who they are.", "security"),
  allow_anonymous: F("Open to everyone", "Let people use this without signing in, such as the login page.", "security"),
  flag: F("Only when feature is on", "Hide this address (it answers ‘not found’) while the feature flag is off."),
  status: F("Success code", "The status code returned when it works. 200 means OK."),
  headers: F("Extra response headers", "Additional details sent back with every response."),
  cache_control: F("Browser caching", "Tells browsers how long they may keep a copy.", "performance"),
  template: F("Page template", "The page design to fill in with data."),
  layout: F("Page layout", "The frame around the page: header, menu, footer."),
  static: F("Static file", "Serve this file as the response."),
  description: F("Description", "A note for your team about what this is for."),
  tags: F("Tags", "Labels used to group things in documentation."),
  parameter: F("Inputs", "Values this endpoint accepts, from the address, the query or the body."),
  timeout: F("Time limit", "Give up if it takes longer than this. For example 30s or 5m.", "performance"),
  max_body_bytes: F("Largest upload (bytes)", "Reject requests bigger than this.", "performance"),
  authz: F("Who can access", "Only people with these roles get through.", "security"),
  rate_limit: F("Request limits", "Slow down or block people who make too many requests.", "performance"),
  idempotency: F("Repeat protection", "Make sure sending the same request twice only happens once.", "performance"),
  tenant: F("Tenant rules", "How this belongs to one customer or organisation.", "security"),
  route_audit: F("Activity log", "Record who used this and when.", "security"),
  cors: F("Cross-site access (CORS)", "Which other websites may call this from a browser.", "security"),
  request_data: F("Request data checks", "Rules for the data coming in."),
  response_data: F("Response data checks", "Rules for the data going out."),
  security_event: F("Security alerts", "Events to raise on successful or failed sign-ins.", "security"),
  kind: F("Type", "What kind of thing this is."),
  config: F("Settings", "Options for this connection or step."),
  depends_on: F("Needs to start after", "Connections that must be ready first.", "advanced"),
  region: F("Region", "Where this lives, for data-residency rules.", "advanced"),
  uses: F("Action", "What this step does."),
  requires: F("Waits for", "Information this step needs before it can run."),
  provides: F("Produces", "Information this step makes available to later steps."),
  response: F("Answer with", "Which result to send back to the caller."),
  max_db_queries: F("Most database queries", "Stop a flow that talks to the database too much.", "performance"),
  max_external_io: F("Most outside calls", "Stop a flow that calls other services too often.", "performance"),
  max_memory: F("Most memory (bytes)", undefined, "performance"),
  max_effects: F("Most changes", "Stop a flow that changes too many things.", "performance"),
  input_schema: F("Expected input", "The data shape a caller must send."),
  output_schema: F("Promised output", "The data shape this returns."),
  input_data: F("Input details", undefined, "advanced"),
  output_data: F("Output details", undefined, "advanced"),
  idempotent: F("Safe to repeat", "Running it twice gives the same result.", "performance"),
  node: F("Steps", "The steps of this flow, in the order they become ready."),
  step: F("Steps", "The stages of this workflow."),
  edge: F("Connections between steps", "How the workflow moves from one step to the next."),
  start: F("First step", "Where the workflow begins."),
  store: F("Where progress is saved", "The connection that remembers where each workflow run is up to."),
  version: F("Version"),
  migration_policy: F("When the design changes", "What happens to runs already in progress.", "advanced"),
  max_steps: F("Most steps per run", undefined, "performance"),
  max_visits: F("Most visits per step", "Stops endless loops.", "performance"),
  retry: F("Retry rules", "How often and how quickly to try again after a failure.", "performance"),
  sla: F("Deadline rules", "How long each step may take before it counts as late.", "performance"),
  concurrency: F("Run at once", "How many can run at the same time.", "performance"),
  retention: F("Keep for", "How long to keep finished runs.", "performance"),
  max_attempts: F("Most attempts", "Give up after this many tries.", "performance"),
  job_type: F("Job type", "Which kind of queued job this picks up."),
  disabled: F("Switched off", "Keep the settings but stop it from running."),
  every: F("Repeat every", "For example 30m or 24h."),
  cron: F("Timetable (cron)", "A schedule such as 0 3 * * * for 3am every day."),
  at: F("At a set time"),
  timezone: F("Time zone", "Times follow this zone, e.g. Asia/Kathmandu."),
  tenant_id: F("For tenant"),
  payload: F("Data sent with it"),
  jitter: F("Random delay", "Spread runs out a little so they don’t all start together.", "performance"),
  event: F("Event name"),
  secret: F("Signing secret", "Used to check that events really come from the other system.", "security"),
  signature_header: F("Signature header", undefined, "security"),
  signature_algorithm: F("Signature method", undefined, "security"),
  timestamp_header: F("Timestamp header", undefined, "security"),
  tolerance: F("Allowed clock difference", "Reject events that look too old or too new.", "security"),
  correlation_path: F("Where to find the event ID"),
  prefix: F("Address prefix", "Everything under this address."),
  root: F("Folder", "The folder on the server the files come from."),
  browse: F("Show folder listings"),
  compress: F("Compress files", "Send smaller files to speed up loading.", "performance"),
  max_age: F("Keep in browser for", undefined, "performance"),
  index: F("Home file", "The file shown for a folder address."),
  permissions: F("Permissions", "What people with this role are allowed to do.", "security"),
  inherits: F("Also includes roles", "This role gets everything those roles have.", "security"),
  roles: F("Roles", "The roles that are allowed.", "security"),
  authorizer: F("Permission check", undefined, "security"),
  display_name: F("Display name"),
  title: F("Title"),
  database: F("Database", "Which database connection to use."),
  table: F("Table name"),
  column: F("Columns", "The fields of each row."),
  tenant_scoped: F("Separate by tenant", "Each tenant only sees their own rows.", "security"),
  owner_scoped: F("Owners only", "People only see rows they created.", "security"),
  owner_bypass_roles: F("Roles that see everything", undefined, "security"),
  soft_delete: F("Keep deleted rows", "Mark rows as deleted instead of removing them."),
  versioned: F("Keep history of changes"),
  search: F("Searchable columns"),
  search_index: F("Speed up searching"),
  default_sort: F("Default order"),
  limit: F("Rows per page"),
  max_limit: F("Most rows per page", undefined, "performance"),
  export: F("Allow export"),
  aggregate: F("Allow totals and counts"),
  analytics: F("Allow analytics"),
  bulk: F("Allow bulk changes"),
  bulk_max: F("Most rows per bulk change", undefined, "performance"),
  migrate: F("Create the table automatically"),
  allow: F("Who can do what", undefined, "security"),
  on: F("When something happens"),
  default: F("Default value"),
  rule: F("Rules", "Who gets this feature."),
  variant: F("Variants"),
  env: F("Environment variable", "Where the secret is read from."),
  file: F("File", "A file that contains the value."),
  value: F("Value"),
  required: F("Required", "Refuse to start if it is missing."),
  driver: F("Database type"),
  dsn: F("Connection address"),
  minor_units: F("Decimal places"),
  symbol: F("Symbol"),
  name: F("Name"),
  grouping: F("Digit grouping"),
  rate_window: F("Limit window", "The period the request limit applies to.", "performance"),
  processes: F("Allowed workflows"),
  queues: F("Allowed queues"),
  constants: F("Fixed values"),
  metadata: F("Extra details"),
  reveal_roles: F("Roles that can see everything", undefined, "security"),
  number_format: F("Case number format"),
  stage: F("Stages", "The steps a case goes through."),
  form: F("Forms"),
  input: F("Information collected"),
  seal: F("Sealing", undefined, "security"),
  notify: F("Notifications"),
  notes: F("Notes"),
  subject: F("About"),
  certificate: F("Certificates"),
  calendar: F("Working calendar"),
  prop: F("Properties"),
  additional_properties: F("Allow extra properties"),
};

/** Which fields belong in "Basics" for each block type; other unclassified fields are "Advanced". */
const PRIMARY: Record<string, string[]> = {
  route: ["method", "path", "intent", "process", "template", "layout", "description", "mode", "status", "parameter"],
  route_group: ["prefix", "route", "description"],
  static: ["prefix", "root", "index"],
  intent: ["description", "response", "node"],
  process: ["description", "start", "step", "edge", "store"],
  pipeline: ["title", "description", "stage", "form", "input"],
  resource: ["kind", "description", "config"],
  secret: ["env", "file", "value", "required"],
  shape: ["kind", "description", "prop", "required"],
  entity: ["title", "description", "database", "table", "path", "column"],
  currency: ["name", "symbol", "minor_units"],
  worker: ["queue", "job_type", "intent", "process", "disabled"],
  schedule: ["every", "cron", "at", "timezone", "intent", "process", "disabled"],
  trigger: ["kind", "path", "event", "intent", "process", "queue", "disabled"],
  role: ["description", "permissions", "inherits"],
  tenant: ["display_name", "disabled", "region"],
  flag: ["description", "default", "disabled", "rule", "variant"],
};

/**
 * Some field names mean different things in different blocks ("limit" is rows
 * per page in a data table but requests allowed in a request limit). Entries
 * here, keyed "block.field", win over the general ones.
 */
export const FIELDS_IN: Record<string, FieldInfo> = {
  "rate_limit.limiter": F("Counter", "The connection that keeps count of requests."),
  "rate_limit.limit": F("Requests allowed", "How many requests one visitor may make in the window."),
  "rate_limit.window": F("Per time window", "The period the limit applies to, like 1m."),
  "rate_limit.message": F("Message when blocked", "What a visitor sees when they go over the limit."),
  "security_event.on_success": F("After a successful sign-in", "The security event to record."),
  "security_event.on_failure": F("After a failed sign-in", "The security event to record."),
  "authz.roles": F("Allowed roles", "Only people with one of these roles get through."),
  "authz.authorizer": F("Permission check", "The connection that decides who is allowed."),
  "retry.max_attempts": F("Attempts", "How many times to try before giving up."),
  "retry.backoff": F("Wait between tries", "How long to wait before trying again."),
  "cors.origins": F("Allowed websites", "The websites that may call this from a browser."),
  "parameter.name": F("Name", "What the input is called."),
  "parameter.in": F("Where it comes from", "The address, the query string or the request body."),
  "parameter.required": F("Must be provided", "Reject the request when it is missing."),
};

export function fieldInfo(name: string, blockType?: string): FieldInfo | undefined {
  return (blockType && FIELDS_IN[`${blockType}.${name}`]) || FIELDS[name];
}

/** The label shown for a field or nested block; falls back to a humanized name. */
export function fieldLabel(name: string, blockType?: string): string {
  return fieldInfo(name, blockType)?.label ?? humanize(name);
}

export function fieldHelp(name: string, blockType?: string): string | undefined {
  return fieldInfo(name, blockType)?.help;
}

export function sectionOf(blockType: string, field: string): SectionId {
  const own = FIELDS[field]?.section;
  if (own && own !== "advanced") return own;
  const primary = PRIMARY[blockType];
  if (primary?.includes(field)) return "basics";
  if (own) return own;
  // Unknown blocks (nested forms, plugins): everything is basic; nothing is worth hiding.
  return primary ? "advanced" : "basics";
}

// ---------------------------------------------------------------------------
// Words
// ---------------------------------------------------------------------------

const ACRONYMS = new Set(["id", "ids", "url", "uri", "http", "https", "sql", "api", "ip", "tls", "ssl", "ttl", "cors", "csrf", "jwt", "oidc", "smtp", "db", "ui", "uuid", "cpu", "io", "dsn", "sla", "rbac", "llm", "json", "html", "css", "pii", "otp", "sso"]);

/** snake_case / dotted / kebab-case -> Sentence case, keeping acronyms upper-case. */
export function humanize(raw: string | undefined): string {
  const words = (raw ?? "").replace(/([a-z0-9])([A-Z])/g, "$1 $2").split(/[\s_.\-/]+/).filter(Boolean);
  if (!words.length) return "";
  return words
    .map((w, i) => {
      const lw = w.toLowerCase();
      if (ACRONYMS.has(lw)) return lw.toUpperCase();
      return i === 0 ? lw[0]!.toUpperCase() + lw.slice(1) : lw;
    })
    .join(" ");
}

/** Lower-cases only the first letter, so acronyms survive: "Page or API endpoint" -> "page or API endpoint". */
export function lc(label: string): string {
  return label ? label[0]!.toLowerCase() + label.slice(1) : label;
}

/** "3 pages" / "1 page" style counts. */
export function count(n: number, singular: string, plural = `${singular}s`): string {
  return `${n} ${n === 1 ? singular : plural}`;
}

// ---------------------------------------------------------------------------
// Values, statuses, roles
// ---------------------------------------------------------------------------

export type ValueMode = "literal" | "env" | "expr";

export const MODE_LABELS: Record<ValueMode, { label: string; hint: string }> = {
  literal: { label: "Fixed value", hint: "Type the value directly." },
  env: { label: "From environment", hint: "Read the value from the server’s settings when the app starts, with an optional fallback." },
  expr: { label: "Formula", hint: "Work the value out from other information. For advanced use." },
};

export const STATUS: Record<string, { label: string; tone: "ok" | "warn" | "bad" | "info" | "muted"; help: string }> = {
  pending: { label: "Waiting for review", tone: "warn", help: "Someone other than the author needs to approve this version." },
  approved: { label: "Approved", tone: "info", help: "Approved and ready to go live." },
  rejected: { label: "Declined", tone: "bad", help: "A reviewer declined this version." },
  active: { label: "Live", tone: "ok", help: "This is the version the app is running now." },
  superseded: { label: "Previous", tone: "muted", help: "Was live before. You can roll back to it." },
  failed: { label: "Failed to start", tone: "bad", help: "This version could not be started, so the last good one kept running." },
};

export function statusInfo(status: string) {
  return STATUS[status] ?? { label: humanize(status), tone: "muted" as const, help: "" };
}

const ROLE_LABELS: Record<string, string> = { viewer: "Viewer", editor: "Editor", reviewer: "Reviewer", admin: "Administrator" };
export function roleLabel(r: string): string {
  return ROLE_LABELS[r] ?? humanize(r);
}

// ---------------------------------------------------------------------------
// Problems in plain language
// ---------------------------------------------------------------------------

export interface FriendlyProblem {
  /** One plain sentence. */
  text: string;
  /** True when the text was rewritten (the original is worth offering). */
  rewritten: boolean;
}

const PROBLEM_RULES: [RegExp, (m: RegExpMatchArray) => string][] = [
  [/path must start with \//i, () => "Addresses must start with “/”, for example /todos."],
  [/unknown intent[:\s]+"?([\w.\-]+)"?/i, (m) => `This points to a logic flow called “${m[1]}” that doesn’t exist yet.`],
  [/(?:intent|process|resource|role|queue|shape|flag)\s+"?([\w.\-]+)"?\s+(?:is )?(?:not found|not declared|does not exist|undefined)/i, (m) => `“${m[1]}” is used here but hasn’t been created.`],
  [/duplicate (\w+)\s+"?([\w.\-]+)"?/i, (m) => `There are two ${lc(blockInfo(m[1]).label)}s named “${m[2]}”. Names must be unique.`],
  [/(?:requires?|needs?) (?:fact |input )?"?([\w.\-]+)"?.*(?:no|nothing).*provid/i, (m) => `A step waits for “${m[1]}”, but no earlier step produces it.`],
  [/environment variable\s+([A-Z0-9_]+)\s+is not set/i, (m) => `The setting ${m[1]} isn’t defined on this computer. That’s fine for editing, but it must exist where the app runs.`],
  [/secret\s+([\w.\-]+): environment variable ([A-Z0-9_]+)/i, (m) => `The secret “${m[1]}” expects the environment setting ${m[2]}, which isn’t defined here.`],
  [/needs an intent, a process, a template,? or static/i, () => "This needs something to run or show. Choose a logic flow, an approval workflow, a page template or a static file."],
  [/unexpected end of file/i, () => "The file seems to end in the middle of something — a closing brace may be missing."],
  [/unterminated (?:string|block comment)/i, () => "A piece of text was opened with a quote or comment marker but never closed."],
  [/expected a duration|invalid duration/i, () => "Use a length of time like 30s, 5m or 2h."],
  [/expected (?:a )?(?:whole )?(?:number|integer)/i, () => "This needs to be a whole number."],
];

export function friendlyProblem(d: Pick<Diagnostic, "message" | "code">): FriendlyProblem {
  const msg = (d.message ?? "").trim();
  for (const [re, fn] of PROBLEM_RULES) {
    const m = msg.match(re);
    if (m) return { text: fn(m), rewritten: true };
  }
  if (d.code === "studio.text_only") return { text: "This file can only be edited as text.", rewritten: true };
  if (d.code?.startsWith("studio.pages.")) {
    const q = [...msg.matchAll(/"([^"]+)"/g)].map((x) => x[1]!);
    switch (d.code) {
      case "studio.pages.template_missing": return { text: `The page design “${q[1] ?? "?"}” doesn’t exist yet. Create it in Page designs or pick another.`, rewritten: true };
      case "studio.pages.layout_missing": return { text: `The layout “${q[1] ?? "?"}” doesn’t exist yet. Create it in Page designs or pick another.`, rewritten: true };
      case "studio.pages.include_missing": return { text: `The design “${q[0] ?? "?"}” uses “${q[1] ?? "?"}”, which doesn’t exist.`, rewritten: true };
      case "studio.pages.include_cycle": return { text: `The design “${q[0] ?? "?"}” ends up including itself, which would never finish drawing.`, rewritten: true };
      case "studio.pages.vars_unprovided": return { text: `The page “${q[0] ?? "?"}” shows information its logic flow never mentions, so it may come out blank.`, rewritten: true };
      case "studio.pages.syntax": return { text: msg.replace(/^parse:\s*/i, ""), rewritten: false };
    }
  }
  return { text: msg.replace(/^parse:\s*/i, ""), rewritten: false };
}

/** Where a problem is, in words: "Page or API endpoint · web.todos_list · Address". */
export function problemWhere(d: Pick<Diagnostic, "path" | "file" | "line">): string {
  if (!d.path) return d.file ? `${d.file}${d.line ? `, line ${d.line}` : ""}` : "";
  const segs = d.path.split("/");
  const [type, id, ...rest] = segs;
  const parts: string[] = [];
  if (type) parts.push(blockInfo(type).label);
  if (id) parts.push(id);
  if (rest.length) parts.push(fieldLabel(rest[rest.length - 1]!));
  return parts.join(" · ");
}

// ---- grouping ---------------------------------------------------------------
// Twenty near-identical "setting X isn't defined here" warnings drown the one real
// error. Repeats of the same kind are folded into a single collapsible group.

export type ProblemEntry<D extends Pick<Diagnostic, "message" | "code" | "severity"> = Diagnostic> =
  | { kind: "single"; d: D }
  | { kind: "group"; key: string; severity: D["severity"]; title: string; help: string; items: { d: D; name: string }[] };

const ENV_RE = /(?:environment variable\s+|expects the environment setting\s+)([A-Z][A-Z0-9_]+)/i;

/** Errors first, then groups, then one-off warnings. A group needs at least `min` members. */
export function groupProblems<D extends Pick<Diagnostic, "message" | "code" | "severity">>(list: D[], min = 3): ProblemEntry<D>[] {
  const env: { d: D; name: string }[] = [];
  const rest: D[] = [];
  for (const d of list) {
    const m = d.severity !== "error" ? d.message.match(ENV_RE) : null;
    if (m) env.push({ d, name: m[1]!.toUpperCase() });
    else rest.push(d);
  }
  const out: ProblemEntry<D>[] = [];
  const singles: D[] = [...rest];
  if (env.length >= min) {
    out.push({
      kind: "group", key: "env", severity: "warning",
      title: `${env.length} environment settings aren’t defined on this computer`,
      help: "That’s fine while editing. They just need to exist wherever the app runs.",
      items: env,
    });
  } else {
    for (const e of env) singles.push(e.d);
  }
  const rank = (d: D) => (d.severity === "error" ? 0 : 1);
  const sorted = singles.map((d) => ({ kind: "single" as const, d })).sort((a, b) => rank(a.d) - rank(b.d));
  return [...sorted.filter((e) => e.d.severity === "error"), ...out, ...sorted.filter((e) => e.d.severity !== "error")];
}
