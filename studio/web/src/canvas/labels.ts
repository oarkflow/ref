// Plain-language wording for the canvas. Names for the kinds of block (flow,
// workflow, pipeline) and for problems come from the shared label map
// (src/labels); the names of individual step types are canvas-specific and live
// here. Every user-visible technical term on the canvas goes through this file.
import { blockInfo, count, humanize } from "../labels";

export const TERMS = {
  node: "Step",
  nodes: "Steps",
  requires: "Needs",
  provides: "Produces",
  request: "Incoming request",
  durable: "Waits",
  durableHint: "Pauses the run until something happens",
  terminal: "Ends the run",
  start: "First step",
  edge: "Connection",
  reliability: "If it goes wrong",
  advanced: "Advanced",
} as const;

export interface FamilyInfo {
  label: string;
  blurb: string;
}

export const FAMILIES: Record<string, FamilyInfo> = {
  compute: { label: "Work with data", blurb: "Calculate, reshape or check values" },
  process: { label: "People & waiting", blurb: "Tasks for people, timers and outside events" },
  observability: { label: "Record & watch", blurb: "Logs, audit entries and metrics" },
  identity: { label: "Who is asking", blurb: "Sign-in, sessions and permissions" },
  flow: { label: "Control flow", blurb: "Choose a path, repeat, run in parallel" },
  data: { label: "Stored data", blurb: "Read and write records, files and caches" },
  coordination: { label: "Safety limits", blurb: "Locks, rate limits and duplicate protection" },
  intelligence: { label: "AI", blurb: "Language models and search" },
  decision: { label: "Decisions", blurb: "Allow, deny or pick an outcome from rules" },
  integration: { label: "Other systems", blurb: "Call web services and connectors" },
  messaging: { label: "Messages", blurb: "Email, notifications, queues and events" },
  terminal: { label: "Finish", blurb: "Send the answer or stop" },
};

/** Friendly names for node types (the catalog names are technical). */
export const TYPE_LABELS: Record<string, string> = {
  action: "Runs an action", constant: "Fixed value", custom: "Custom action", script: "Calculate", template: "Fill in a template",
  transform: "Reshape data", validate: "Check the data",
  approval: "Approval", compensation: "Undo an earlier step", delay: "Wait for a while", escalation: "Escalate",
  external_task: "Wait for an outside system", form: "Collect information", human_task: "Task for a person",
  manual_review: "Manual review", process: "Start a process", subprocess: "Run a sub-process", timer: "Wait until a time",
  wait: "Wait", wait_event: "Wait for an event", workflow: "Start a workflow",
  audit: "Record an audit entry", log: "Write a log line", metric: "Record a metric", trace: "Trace",
  auth: "Check who is asking", authz: "Check permission", session: "Session",
  batch: "Handle in batches", branch: "Choose a path", fallback: "Fallback", foreach: "For each item", iterator: "For each item",
  join: "Join results", loop: "Repeat until", parallel: "Do at the same time", parallel_map: "Do for all items at once",
  pipeline: "Pipeline", quorum: "Wait for enough results", race: "First one wins", retry: "Try again", subflow: "Sub-flow",
  switch: "Match a value", timeout: "Time limit",
  cache: "Cache", crud: "Save or load a record", database: "Database query", db: "Database query", file: "File",
  search: "Search", storage: "Storage",
  circuit_breaker: "Stop calling a failing service", idempotency: "Prevent duplicates", lock: "Take a lock", rate_limit: "Limit how often",
  classifier: "Classify", embedding: "Embed text", llm: "Ask an AI model", rag: "Find related documents",
  condition: "Check a condition", decision: "Allow or deny", decision_matrix: "Decision table", rules: "Rules",
  connector: "Connector", graphql: "GraphQL call", grpc: "gRPC call", http: "Call a web service", service: "Call a service",
  tool: "Use a tool", websocket: "WebSocket",
  email: "Send an email", event: "Publish an event", inbox: "Receive once", notification: "Send a notification",
  outbox: "Queue for delivery", queue: "Add to a queue", stream: "Stream", webhook: "Send a webhook", worker: "Background job",
  noop: "Do nothing", response: "Send the answer", terminal: "Stop here",
};

export const EDGE_LABELS: Record<string, string> = {
  simple: "Then", branch: "If", switch: "Depending on value", conditional_fork: "Every one that matches",
  threshold: "By amount", weighted: "Random by weight", priority: "Highest priority first",
  fanout: "Split into parallel", dynamic_fanout: "Split by data", fanin: "Wait for all", join: "Join", quorum: "Wait for enough",
  parallel: "Run together", race: "First one wins", iterator: "For each item", batch_iterator: "For each batch",
  loop_until: "Repeat until", compensate: "Undo", error: "If it fails", fallback: "Fallback", rate_limited: "Slow down",
  retry: "Try again", timeout: "On timeout", delayed: "After a delay", escalation: "Escalate", manual: "When a person decides",
  wait_event: "When an event arrives", cancel: "Cancel", filter: "Only matching", stream_pipe: "Stream", transform: "Reshape",
};

const title = (s: string) => humanize(s);

export const typeLabel = (name: string | undefined): string => (name ? (TYPE_LABELS[name] ?? title(name)) : "Step");
export const familyLabel = (f: string | undefined): string => (f ? (FAMILIES[f]?.label ?? title(f)) : "");
export const edgeLabel = (kind: string | undefined): string => (kind ? (EDGE_LABELS[kind] ?? title(kind)) : "Then");
/** "Logic flow" / "Approval workflow" / "Review pipeline", from the shared map. */
export const friendlyKind = (k: "intent" | "process" | "pipeline"): string => blockInfo(k).label;

/** "3 things" / "1 thing" */
export const plural = count;
