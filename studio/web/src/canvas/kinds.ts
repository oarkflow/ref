// What each kind of step looks like and what it says about itself. One place
// decides shape, colour and icon from the step's type, so a decision never looks
// like an HTTP call and a loop never looks like a plain action.
import type { BlockNode, Catalog } from "../api/types";
import { blocksOf, fieldOf } from "../lib/paths";
import { unquote } from "../lib/bcl";
import { parseObjectList } from "./objectList";

// Every step is the same card; the shape only records the kind, for styling hooks and tests.
export type Shape = "card" | "decision" | "branch" | "container";

export interface Visual {
  shape: Shape;
  /** colour family: drives --cv-tone */
  tone: string;
  icon: string;
  /** shown as a small badge next to the type name */
  badge?: string;
  /** dashed "parks the run" styling */
  parks?: boolean;
}

const TONE_OF_FAMILY: Record<string, string> = {
  compute: "compute", process: "process", observability: "watch", identity: "identity", flow: "flow", data: "data",
  coordination: "safety", intelligence: "ai", decision: "decision", integration: "plug", messaging: "message", terminal: "end",
};

const ICON: Record<string, string> = {
  // compute
  script: "code", transform: "code", template: "doc", validate: "shield", constant: "flag", action: "bolt", custom: "wrench",
  // process
  approval: "user", human_task: "user", manual_review: "user", form: "doc", escalation: "bell",
  delay: "clock", timer: "clock", wait: "clock", wait_event: "clock", external_task: "plug",
  compensation: "undo", process: "play", subprocess: "play", workflow: "play",
  // flow
  branch: "branch", switch: "branch", foreach: "loop", iterator: "loop", batch: "loop", loop: "loop", parallel: "loop",
  parallel_map: "loop", race: "bolt", quorum: "check", retry: "loop", fallback: "undo", join: "branch", pipeline: "list",
  subflow: "list", timeout: "clock",
  // decision
  decision: "diamond", condition: "diamond", decision_matrix: "diamond", rules: "diamond",
  // data
  crud: "database", database: "database", db: "database", cache: "database", file: "doc", search: "eye", storage: "database",
  // integration & messaging
  http: "plug", service: "plug", tool: "plug", graphql: "plug", grpc: "plug", websocket: "plug", connector: "plug",
  email: "mail", notification: "bell", webhook: "send", event: "send", queue: "send", outbox: "send", inbox: "send", stream: "send",
  worker: "gear",
  // identity, safety, ai, watch
  auth: "shield", authz: "shield", session: "lock", lock: "lock", rate_limit: "gauge", idempotency: "shield", circuit_breaker: "gauge",
  llm: "spark", classifier: "spark", embedding: "spark", rag: "spark",
  audit: "eye", log: "eye", metric: "gauge", trace: "eye",
  // ends
  response: "send", terminal: "stop", noop: "flag",
};

const SHAPE: Record<string, Shape> = {
  decision: "decision", condition: "decision", decision_matrix: "decision", rules: "decision",
  branch: "branch", switch: "branch",
  foreach: "container", iterator: "container", batch: "container", loop: "container", parallel: "container",
  parallel_map: "container", race: "container", quorum: "container",
};

const PARKS = new Set(["delay", "timer", "wait", "wait_event", "external_task", "subprocess", "compensation", "approval", "human_task", "manual_review", "form"]);
const HUMAN = new Set(["approval", "human_task", "manual_review", "form", "escalation"]);

/** The catalog type a block belongs to: its `family`, else the type whose default action it uses. */
export function typeNameOf(block: BlockNode, catalog?: Catalog | null): string {
  const fam = unquote(fieldOf(block, "family")?.raw ?? "") ?? fieldOf(block, "family")?.raw?.trim();
  if (fam) return fam;
  const uses = unquote(fieldOf(block, "uses")?.raw ?? "");
  if (uses && catalog) {
    const t = catalog.node_types.find((x) => x.default_action === uses);
    if (t) return t.name;
    const a = catalog.actions.find((x) => x.name === uses);
    if (a?.kind === "decision") return "decision";
  }
  if (uses === "collect") return "join";
  return "action";
}

export function visualFor(typeName: string, catalog?: Catalog | null, opts: { terminal?: boolean; start?: boolean; human?: boolean } = {}): Visual {
  const info = catalog?.node_types.find((t) => t.name === typeName);
  const tone = TONE_OF_FAMILY[info?.family ?? ""] ?? "compute";
  let shape: Shape = SHAPE[typeName] ?? "card";
  let icon = ICON[typeName] ?? "gear";
  if (opts.start) icon = "play";
  else if (opts.terminal) icon = "stop";
  if (opts.human) icon = "user";
  const parks = !!info?.durable || PARKS.has(typeName) || !!opts.human;
  const badge = HUMAN.has(typeName) || opts.human ? "person" : parks ? "clock" : undefined;
  return { shape, tone, icon, badge, parks };
}

// ---------------------------------------------------------------------------
// what a node says about itself, from its config

export interface CaseInfo {
  label: string;
  target?: string;
}

export interface NodeSummary {
  /** one short line under the title */
  line?: string;
  /** branch/switch rows */
  cases: CaseInfo[];
  /** loop container: "for each <items> run <intent>" */
  loop?: { items?: string; intent?: string; concurrency?: string };
}

const val = (b: BlockNode | undefined, name: string): string | undefined => {
  const raw = fieldOf(b, name)?.raw;
  return raw === undefined ? undefined : (unquote(raw) ?? raw.trim());
};

const clip = (s: string, n = 44) => {
  const t = s.replace(/\s+/g, " ").trim();
  return t.length > n ? t.slice(0, n - 1) + "…" : t;
};

export function summarize(block: BlockNode, typeName: string): NodeSummary {
  const cfg = blocksOf(block, "config")[0];
  const out: NodeSummary = { cases: [] };
  if (typeName === "branch" || typeName === "switch") {
    const raw = fieldOf(cfg, "cases")?.raw;
    const items = raw ? parseObjectList(raw) : null;
    for (const it of items ?? []) {
      const get = (k: string) => {
        const r = it.pairs.find((p) => p.key === k)?.raw;
        return r === undefined ? undefined : (unquote(r) ?? r);
      };
      out.cases.push({ label: get("name") ?? get("label") ?? "case", target: get("intent") });
    }
    const def = val(cfg, "default_intent");
    if (def) out.cases.push({ label: "otherwise", target: def });
    out.line = typeName === "switch" && val(cfg, "on") ? `on ${clip(val(cfg, "on")!, 32)}` : undefined;
  } else if (["foreach", "iterator", "batch", "loop", "parallel_map"].includes(typeName)) {
    out.loop = { items: val(cfg, "items_fact"), intent: val(cfg, "intent"), concurrency: val(cfg, "concurrency") };
  } else if (["parallel", "race", "quorum"].includes(typeName)) {
    const raw = fieldOf(cfg, "branches")?.raw;
    const items = raw ? parseObjectList(raw) : null;
    for (const it of items ?? []) {
      const nm = it.pairs.find((p) => p.key === "name")?.raw;
      const tg = it.pairs.find((p) => p.key === "intent")?.raw;
      out.cases.push({ label: nm ? (unquote(nm) ?? nm) : "branch", target: tg ? (unquote(tg) ?? tg) : undefined });
    }
  } else if (fieldOf(cfg, "rules")) {
    // any decision that evaluates a rule table, whatever its type is called
    const items = parseObjectList(fieldOf(cfg, "rules")!.raw ?? "");
    if (items) out.line = `${items.length} rule${items.length === 1 ? "" : "s"}`;
  } else if (["decision", "condition", "script"].includes(typeName)) {
    const e = val(cfg, "expression");
    if (e) out.line = clip(e);
  } else if (["http", "service", "tool"].includes(typeName)) {
    const url = val(cfg, "url");
    if (url) out.line = clip(`${val(cfg, "method") ?? "GET"} ${url}`);
  }
  return out;
}

