// The first few questions the "Add" flow asks per kind of thing, and how the
// answers become the block body. Everything else is filled in afterwards in the editor.
import { quote } from "../lib/bcl";

export type QuickKind = "text" | "ident" | "select" | "bool" | "duration" | "pick";

export interface QuickField {
  name: string;
  label: string;
  kind: QuickKind;
  placeholder?: string;
  help?: string;
  required?: boolean;
  options?: string[];
  default?: string;
  /** For "pick": which existing things to offer. */
  from?: "intent" | "process" | "flow" | "database" | "queue";
}

export const QUICK: Record<string, QuickField[]> = {
  route: [
    { name: "method", label: "Method", kind: "ident", options: ["GET", "POST", "PUT", "PATCH", "DELETE"], default: "GET", help: "GET reads something. POST creates or changes something." },
    { name: "path", label: "Address", kind: "text", placeholder: "/reports", default: "/", required: true, help: "Starts with /. Use {id} for a changing part." },
    { name: "intent", label: "Runs this logic flow", kind: "pick", from: "intent", placeholder: "Pick or type a flow name", help: "You can connect this later." },
  ],
  resource: [
    { name: "kind", label: "What kind of connection?", kind: "pick", from: undefined, required: true, placeholder: "e.g. database.sql" },
  ],
  intent: [{ name: "description", label: "What does it do?", kind: "text", placeholder: "Lists the current user’s to-dos" }],
  worker: [
    { name: "queue", label: "Picks jobs from queue", kind: "text", placeholder: "emails" },
    { name: "intent", label: "Runs this logic flow", kind: "pick", from: "intent", placeholder: "Pick or type a flow name" },
  ],
  schedule: [
    { name: "every", label: "Repeat every", kind: "duration", placeholder: "24h", help: "For example 30m or 24h. Use a cron timetable later if you need one." },
    { name: "intent", label: "Runs this logic flow", kind: "pick", from: "intent", placeholder: "Pick or type a flow name" },
  ],
  trigger: [
    { name: "path", label: "Address that receives events", kind: "text", placeholder: "/hooks/payments", default: "/hooks/" },
    { name: "intent", label: "Runs this logic flow", kind: "pick", from: "intent", placeholder: "Pick or type a flow name" },
  ],
  role: [{ name: "description", label: "What is this role for?", kind: "text", placeholder: "Can approve requests" }],
  flag: [
    { name: "description", label: "What does it turn on?", kind: "text", placeholder: "Priority shipping" },
    { name: "default", label: "Starts as", kind: "select", options: ["false", "true"], default: "false", help: "Off means nobody sees the feature until you switch it on." },
  ],
  secret: [{ name: "env", label: "Environment setting to read", kind: "text", placeholder: "SESSION_KEY", required: true, help: "The value is never stored in the config, only its name." }],
  static: [
    { name: "prefix", label: "Address", kind: "text", default: "/static", required: true },
    { name: "root", label: "Folder on the server", kind: "text", default: "static", required: true },
  ],
  entity: [
    { name: "table", label: "Table name", kind: "text", placeholder: "todos", required: true },
    { name: "database", label: "Database connection", kind: "pick", from: "database", placeholder: "Pick a connection" },
  ],
  shape: [{ name: "description", label: "What does it describe?", kind: "text", placeholder: "A user account" }],
  process: [{ name: "description", label: "What is this workflow for?", kind: "text", placeholder: "Approve a request" }],
  pipeline: [{ name: "title", label: "Title", kind: "text", placeholder: "Passport application" }],
};

/** Builds the block body (the lines between the braces) from the user's answers. */
export function buildBody(type: string, answers: Record<string, string>): string {
  const lines: string[] = [];
  for (const f of QUICK[type] ?? []) {
    const v = (answers[f.name] ?? "").trim();
    if (!v) continue;
    switch (f.kind) {
      case "ident":
      case "bool":
      case "duration":
        lines.push(`${f.name} ${v}`);
        break;
      case "select":
        lines.push(f.options?.every((o) => o === "true" || o === "false") ? `${f.name} ${v}` : `${f.name} ${quote(v)}`);
        break;
      default:
        lines.push(`${f.name} ${quote(v)}`);
    }
  }
  return lines.join("\n");
}

export function defaultAnswers(type: string): Record<string, string> {
  const a: Record<string, string> = {};
  for (const f of QUICK[type] ?? []) if (f.default !== undefined) a[f.name] = f.default;
  return a;
}
