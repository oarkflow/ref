import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { Diagnostic } from "../api/types";
import { NeedsPanel, OutlinePanel, UsedByPanel, previewPathFor, unprovidedVars } from "./panels";
import { component, layout, tpl } from "./testKit";

const byName = { "layouts/base": layout(), "components/row": component("components/row"), "pages/todos/list": tpl() };

describe("OutlinePanel", () => {
  const props = { t: tpl(), byName, cycles: [], canEdit: true, onOpen: vi.fn(), onCreate: vi.fn() };

  it("shows the layout around the page, the page itself and what it includes", () => {
    render(<OutlinePanel {...props} />);
    const items = screen.getAllByRole("listitem").map((li) => li.textContent);
    expect(items[0]).toContain("Base");
    expect(items.some((t) => t?.includes("Todos list") && t.includes("You are here"))).toBe(true);
    expect(items.some((t) => t?.includes("Row"))).toBe(true);
  });

  it("opens a related design", async () => {
    const onOpen = vi.fn();
    render(<OutlinePanel {...props} onOpen={onOpen} />);
    await userEvent.click(screen.getByRole("button", { name: "Open Base" }));
    expect(onOpen).toHaveBeenCalledWith("templates/layouts/base.html");
  });

  it("marks an include that does not exist and offers to create it", async () => {
    const onCreate = vi.fn();
    render(<OutlinePanel {...props} t={tpl({ includes: ["components/gone"], missing: ["components/gone"] })} onCreate={onCreate} />);
    expect(screen.getByText("Doesn’t exist")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /Create/ }));
    expect(onCreate).toHaveBeenCalledWith("components/gone");
  });

  it("hides Create from someone who cannot edit", () => {
    render(<OutlinePanel {...props} canEdit={false} t={tpl({ includes: ["components/gone"], missing: ["components/gone"] })} />);
    expect(screen.queryByRole("button", { name: /Create/ })).toBeNull();
  });

  it("warns about a design that includes itself", () => {
    render(<OutlinePanel {...props} cycles={[{ severity: "warning", message: "cycle" }]} />);
    expect(screen.getByRole("alert")).toHaveTextContent(/including itself/);
  });

  it("lists the places to fill in, and which are empty", () => {
    render(<OutlinePanel {...props} />);
    const row = (name: string) => screen.getByText(name).closest("li")!;
    expect(within(row("content")).getByText("Filled")).toBeInTheDocument();
    expect(within(row("head")).getByText("Empty")).toBeInTheDocument();
  });

  it("flags a place the layout does not offer", () => {
    render(<OutlinePanel {...props} t={tpl({ unknown: ["sidebar"] })} />);
    expect(screen.getByText(/“sidebar”, which the layout doesn’t offer/)).toBeInTheDocument();
  });
});

describe("NeedsPanel", () => {
  const sample = { name: "pages/todos/list", version: 1, guessed: true, vars: [], data: { todos: [{}], appName: "x" } };

  it("separates what the app fills in from what the page's logic supplies", () => {
    render(<NeedsPanel t={tpl()} globals={["appName"]} sample={sample} unprovided={new Set()} />);
    const list = screen.getByRole("list");
    expect(within(list).getByText("todos")).toBeInTheDocument();
    expect(within(list).queryByText("appName")).toBeNull();
    expect(screen.getByRole("button", { name: /Also 1 filled in by the app/ })).toBeInTheDocument();
  });

  it("guesses a plain-language type for each value", () => {
    render(<NeedsPanel t={tpl()} globals={["appName"]} sample={sample} unprovided={new Set()} />);
    expect(screen.getByText("a list")).toBeInTheDocument();
  });

  it("marks a value the logic flow never mentions", () => {
    render(<NeedsPanel t={tpl()} globals={[]} sample={sample} unprovided={new Set(["todos"])} />);
    expect(screen.getByText("Not provided")).toBeInTheDocument();
  });

  it("says unchecked when no page uses the design", () => {
    render(<NeedsPanel t={tpl({ routes: [] })} globals={[]} sample={null} unprovided={new Set()} />);
    expect(screen.getAllByText("Unchecked").length).toBeGreaterThan(0);
  });

  it("reveals the app's own values on request", async () => {
    render(<NeedsPanel t={tpl()} globals={["appName"]} sample={sample} unprovided={new Set()} />);
    await userEvent.click(screen.getByRole("button", { name: /filled in by the app/ }));
    expect(screen.getByText("appName")).toBeInTheDocument();
  });
});

describe("unprovidedVars", () => {
  it("reads the names out of the server's warning", () => {
    const d: Diagnostic = { severity: "warning", code: "studio.pages.vars_unprovided", message: 'template "pages/todos/list" reads todos, owner, which intent "todo.list" never mentions' };
    expect([...unprovidedVars([d], "pages/todos/list")]).toEqual(["todos", "owner"]);
  });
  it("ignores warnings about other templates and other codes", () => {
    const other: Diagnostic = { severity: "warning", code: "studio.pages.vars_unprovided", message: 'template "pages/other" reads x, which intent "i" never mentions' };
    const wrong: Diagnostic = { severity: "warning", code: "warning", message: 'template "pages/todos/list" reads y, which' };
    expect(unprovidedVars([other, wrong], "pages/todos/list").size).toBe(0);
  });
});

describe("UsedByPanel", () => {
  it("lists the routes and opens one", async () => {
    const onOpenRoute = vi.fn();
    render(<UsedByPanel t={tpl()} onOpenRoute={onOpenRoute} />);
    await userEvent.click(screen.getByRole("button", { name: /GET \/todos/ }));
    expect(onOpenRoute).toHaveBeenCalledWith(expect.objectContaining({ route: "web.todos_list" }));
  });
  it("says so when nothing shows the design", () => {
    render(<UsedByPanel t={tpl({ routes: [] })} onOpenRoute={vi.fn()} />);
    expect(screen.getByText("Nothing shows this design directly.")).toBeInTheDocument();
  });
});

describe("previewPathFor", () => {
  const r = (url: string, method = "GET", as: "template" | "layout" = "template") => ({ file: "f", path: "p", route: "r", method, url, as, line: 1 });
  it("prefers an address without parameters", () => {
    expect(previewPathFor([r("/todos/{id}"), r("/todos")])).toBe("/todos");
  });
  it("fills parameters with 1", () => {
    expect(previewPathFor([r("/todos/{id}/edit")])).toBe("/todos/1/edit");
    expect(previewPathFor([r("/todos/:id")])).toBe("/todos/1");
  });
  it("only uses GET routes that show the design itself", () => {
    expect(previewPathFor([r("/x", "POST"), r("/y", "GET", "layout")])).toBeNull();
    expect(previewPathFor([])).toBeNull();
  });
});
