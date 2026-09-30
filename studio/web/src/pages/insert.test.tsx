import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { InsertDialog, InsertMenu } from "./InsertDialogs";
import { routeChoices } from "./routes";
import { component, layout, tpl } from "./testKit";
import type { RouteChoice } from "./lib";

const routes: RouteChoice[] = [
  { name: "web.todos_list", method: "GET", path: "/todos", hasTemplate: true },
  { name: "web.todos_show", method: "GET", path: "/todos/{id}", hasTemplate: true },
  { name: "api.health", method: "GET", path: "/health", hasTemplate: false },
  { name: "web.todos_create", method: "POST", path: "/todos", hasTemplate: false },
  { name: "web.todos_delete", method: "DELETE", path: "/todos/{id}", hasTemplate: false },
];

function setup(kind: Parameters<typeof InsertDialog>[0]["kind"], over: Partial<Parameters<typeof InsertDialog>[0]> = {}) {
  const onInsert = vi.fn();
  const onClose = vi.fn();
  render(
    <InsertDialog
      kind={kind}
      templates={[tpl(), layout(), component("components/alert"), component("components/row")]}
      variables={["appName", "title", "todos"]}
      routes={routes}
      ownUrl="/todos"
      onInsert={onInsert}
      onClose={onClose}
      {...over}
    />,
  );
  return { onInsert, onClose, dialog: screen.getByRole("dialog") };
}

describe("Insert menu", () => {
  it("offers each kind of thing, in plain words", async () => {
    const onPick = vi.fn();
    render(<InsertMenu onPick={onPick} />);
    await userEvent.click(screen.getByRole("button", { name: "Insert something" }));
    const items = screen.getAllByRole("menuitem").map((i) => i.textContent?.trim());
    expect(items).toEqual(["Reusable piece…", "Information from the page…", "Button", "Link to a page…", "Form that calls an endpoint…"]);
    await userEvent.click(screen.getByRole("menuitem", { name: /Link to a page/ }));
    expect(onPick).toHaveBeenCalledWith("link");
  });
});

describe("Insert a reusable piece", () => {
  it("lists components but never layouts or pages", () => {
    const { dialog } = setup("component");
    const names = within(dialog).getAllByRole("option").map((o) => o.textContent);
    expect(names.some((n) => n?.includes("Alert"))).toBe(true);
    expect(names.some((n) => n?.includes("Base"))).toBe(false);
  });
  it("inserts an include the engine accepts", async () => {
    const { dialog, onInsert, onClose } = setup("component");
    await userEvent.click(within(dialog).getByRole("option", { name: /Alert/ }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Insert" }));
    expect(onInsert).toHaveBeenCalledWith('@include("components/alert.html")');
    expect(onClose).toHaveBeenCalled();
  });
  it("needs a choice first", () => {
    const { dialog } = setup("component");
    expect(within(dialog).getByRole("button", { name: "Insert" })).toBeDisabled();
  });
});

describe("Insert information from the page", () => {
  it("inserts a value in one click", async () => {
    const { dialog, onInsert } = setup("variable");
    await userEvent.click(within(dialog).getByRole("option", { name: "title" }));
    expect(onInsert).toHaveBeenCalledWith("${title}");
  });
  it("accepts a name that is not on the list", async () => {
    const { dialog, onInsert } = setup("variable");
    await userEvent.type(within(dialog).getByRole("textbox"), "user.email");
    await userEvent.click(within(dialog).getByRole("button", { name: "Insert" }));
    expect(onInsert).toHaveBeenCalledWith("${user.email}");
  });
  it("rejects a name that is not a valid value", async () => {
    const { dialog } = setup("variable");
    await userEvent.type(within(dialog).getByRole("textbox"), "not valid!");
    expect(within(dialog).getByRole("alert")).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Insert" })).toBeDisabled();
  });
});

describe("Insert a button", () => {
  it("uses the text given", async () => {
    const { dialog, onInsert } = setup("button");
    const box = within(dialog).getByRole("textbox");
    await userEvent.clear(box);
    await userEvent.type(box, "Send it");
    await userEvent.click(within(dialog).getByRole("button", { name: "Insert" }));
    expect(onInsert).toHaveBeenCalledWith('<button type="button" class="btn btn-primary">Send it</button>');
  });
});

describe("Insert a link", () => {
  it("only offers pages people can open", () => {
    const { dialog } = setup("link");
    const opts = within(dialog).getAllByRole("option").map((o) => o.textContent);
    expect(opts.some((o) => o?.includes("/todos/{id}"))).toBe(true);
    expect(opts.some((o) => o?.includes("/health"))).toBe(false); // no page behind it
    expect(opts.some((o) => o?.includes("POST"))).toBe(false);
  });
  it("links with parameters turned into expressions", async () => {
    const { dialog, onInsert } = setup("link");
    await userEvent.selectOptions(within(dialog).getByRole("combobox"), "web.todos_show");
    await userEvent.click(within(dialog).getByRole("button", { name: "Insert" }));
    expect(onInsert).toHaveBeenCalledWith('<a href="/todos/${id}" class="btn">Open</a>');
  });
});

describe("Insert a form", () => {
  it("offers only endpoints that change something", () => {
    const { dialog } = setup("form");
    const opts = within(dialog).getAllByRole("option").map((o) => o.textContent);
    expect(opts.every((o) => o === "Choose…" || /POST|DELETE/.test(o ?? ""))).toBe(true);
  });
  it("follows the app's redirect convention and offers to come back to this page", async () => {
    const { dialog, onInsert } = setup("form");
    await userEvent.selectOptions(within(dialog).getByRole("combobox"), "web.todos_create");
    await userEvent.clear(within(dialog).getByPlaceholderText("title, description"));
    await userEvent.type(within(dialog).getByPlaceholderText("title, description"), "title, notes");
    await userEvent.click(within(dialog).getByRole("button", { name: "Insert" }));
    const html = onInsert.mock.calls[0]![0] as string;
    expect(html).toContain('<form method="POST" action="/todos?redirect=/todos">');
    expect(html).toContain('name="title"');
    expect(html).toContain('name="notes"');
  });
  it("can leave out the way back", async () => {
    const { dialog, onInsert } = setup("form");
    await userEvent.selectOptions(within(dialog).getByRole("combobox"), "web.todos_create");
    await userEvent.click(within(dialog).getByRole("checkbox"));
    await userEvent.click(within(dialog).getByRole("button", { name: "Insert" }));
    expect(onInsert.mock.calls[0]![0]).toContain('action="/todos"');
  });
  it("explains that browsers only send forms as POST", async () => {
    const { dialog } = setup("form");
    await userEvent.selectOptions(within(dialog).getByRole("combobox"), "web.todos_delete");
    expect(within(dialog).getByText(/only send forms as POST/)).toBeInTheDocument();
  });
});

describe("routeChoices", () => {
  it("reads method, address and whether a page is behind each route", () => {
    const mk = (id: string, fields: [string, string][]) => ({
      key: id, file: "f", path: `route/${id}`, type: "route", id, name: id, category: "pages" as const,
      node: { kind: "block", path: `route/${id}`, type: "route", id, line: 1, start: 0, end: 1, children: fields.map(([name, raw]) => ({ kind: "field", path: `route/${id}/${name}`, name, raw, line: 1, start: 0, end: 1 })) },
    });
    const out = routeChoices([
      mk("b", [["method", "POST"], ["path", '"/b"']]),
      mk("a", [["path", '"/a"'], ["template", '"pages/a"']]),
      mk("nopath", [["method", "GET"]]),
    ] as never);
    expect(out).toEqual([
      { name: "a", method: "GET", path: "/a", hasTemplate: true },
      { name: "b", method: "POST", path: "/b", hasTemplate: false },
    ]);
  });
});
