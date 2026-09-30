import { describe, expect, it } from "vitest";
import { cleanLabel, edgeWords, entityCount, groupWarnings, pageTitle, typeText, warningText } from "./text";
import type { FlowNode, FlowWarning } from "./types";

const el = (label: string, subkind = "link", data: Record<string, unknown> = {}): FlowNode => ({ id: "e", kind: "element", subkind, label, data });

describe("cleanLabel", () => {
  it("keeps real text", () => {
    expect(cleanLabel(el("Save draft", "form"))).toEqual({ text: "Save draft", guessed: false });
    expect(cleanLabel(el("→ Admin portal")).guessed).toBe(false);
  });
  it("replaces a placeholder with the address it goes to", () => {
    expect(cleanLabel(el("…", "link", { url: "/todos" }))).toEqual({ text: "Link to /todos", guessed: true });
    expect(cleanLabel(el("R/ … v…", "link", { url: "/register" })).guessed).toBe(true);
    expect(cleanLabel(el("", "button"))).toEqual({ text: "Button", guessed: true });
  });
  it("shortens a script request", () => {
    expect(cleanLabel(el("Request POST /api/v1/todos/:param/subtasks", "fetch")).text).toBe("POST /api/v1/todos/:param/subtasks");
  });
});

describe("plain words", () => {
  it("names each line", () => {
    expect(edgeWords("navigates", "opens").text).toBe("goes to");
    expect(edgeWords("calls", "post").text).toBe("sends POST");
    expect(edgeWords("calls", undefined).text).toBe("sends");
    expect(edgeWords("runs", undefined).text).toBe("runs");
    expect(edgeWords("renders", "shows").text).toBe("shows");
    expect(edgeWords("redirects", "on success").text).toBe("on success, goes to");
    expect(edgeWords("redirects", "on success").tip).toContain("sent on to this page");
    expect(edgeWords("uses", "database", { category: "database" }).tip).toContain("Database");
  });
  it("keeps the raw kind for the Developer view", () => {
    expect(edgeWords("redirects", "on success").raw).toBe("redirects · on success");
    expect(edgeWords("runs", undefined).raw).toBe("runs");
  });
  it("says what a request is", () => {
    const route = (data: Record<string, unknown>): FlowNode => ({ id: "r", kind: "route", label: "x", data });
    expect(typeText(route({ method: "GET", template: "pages/x" }))).toBe("Shows a page");
    expect(typeText(route({ method: "GET" }))).toBe("Reads data");
    expect(typeText(route({ method: "POST" }))).toBe("Changes data");
    expect(typeText(route({ method: "DELETE" }))).toBe("Deletes data");
    expect(typeText(route({ method: "POST", process: "wf" }))).toBe("Starts a workflow");
    expect(typeText({ id: "i", kind: "intent", subkind: "process", label: "w" })).toBe("Approval workflow");
  });
});

describe("warnings", () => {
  const w = (code: string, severity: "warning" | "info" = "info", message = code): FlowWarning => ({ code, severity, message });
  it("explains the codes in plain words", () => {
    expect(warningText(w("flows.unresolved")).title).toMatch(/nothing answers/);
    expect(warningText(w("flows.entities")).title).toMatch(/Data tables/);
    expect(warningText(w("flows.unknown_code", "info", "odd one")).title).toBe("odd one");
  });
  it("groups by code, real problems first", () => {
    const groups = groupWarnings([w("flows.dynamic_target"), w("flows.unresolved", "warning"), w("flows.dynamic_target"), w("flows.entities")]);
    expect(groups.map((g) => g.code)).toEqual(["flows.unresolved", "flows.dynamic_target", "flows.entities"]);
    expect(groups[1]!.items).toHaveLength(2);
  });
  it("reads the entity count", () => {
    expect(entityCount([w("flows.entities", "info", "3 entity block(s) generate REST routes that are not drawn here")])).toBe(3);
    expect(entityCount([])).toBe(0);
  });
});

describe("pageTitle", () => {
  const p = (label: string, template: string): FlowNode => ({ id: `page:${template}`, kind: "page", label, data: { template } });
  it("keeps a real heading", () => expect(pageTitle(p("Welcome back", "pages/auth/login"))).toBe("Welcome back"));
  it("falls back to the template name when the heading is computed", () => {
    expect(pageTitle(p("Hello, …", "pages/dashboard/index"))).toBe("Dashboard");
    expect(pageTitle(p("…", "pages/todos/show"))).toBe("Todos show");
    expect(pageTitle(p("", "pages/errors/error"))).toBe("Errors error");
  });
});
