import { fireEvent, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { block, field, renderBlock, topBlock } from "../test/helpers";

const route = () =>
  topBlock("route", "web.todos_list", [
    ["method", "GET"],
    ["path", '"/todos"'],
    ["intent", '"todo.list"'],
    ["status", "200"],
    ["timeout", "30s"],
    ["allow_anonymous", "false"],
  ]);

/** The container of one field, found by its label. */
function fieldBox(name: string): HTMLElement {
  const el = document.querySelector<HTMLElement>(`[data-path$="/${name}"]`);
  if (!el) throw new Error(`no field ${name}`);
  return el;
}

describe("scalar widgets", () => {
  it("string: quotes what is typed", () => {
    const t = renderBlock(route());
    fireEvent.change(within(fieldBox("path")).getByRole("textbox"), { target: { value: '/my "todos"' } });
    expect(t.last()).toEqual([{ op: "setField", file: "test.bcl", path: "route/web.todos_list/path", value: '"/my \\"todos\\""' }]);
  });

  it("int: accepts digits, reports anything else and sends nothing", () => {
    const t = renderBlock(route());
    const input = within(fieldBox("status")).getByRole("textbox");
    fireEvent.change(input, { target: { value: "201" } });
    expect(t.last()).toEqual([{ op: "setField", file: "test.bcl", path: "route/web.todos_list/status", value: "201" }]);
    t.edit.mockClear();
    fireEvent.change(input, { target: { value: "20x" } });
    expect(t.edit).not.toHaveBeenCalled();
    expect(within(fieldBox("status")).getByRole("alert")).toHaveTextContent(/whole number/);
  });

  it("duration: accepts 30m style and rejects a bare number", () => {
    const t = renderBlock(route());
    const input = within(fieldBox("timeout")).getByRole("textbox");
    fireEvent.change(input, { target: { value: "1h30m" } });
    expect(t.last()[0]).toMatchObject({ op: "setField", path: "route/web.todos_list/timeout", value: "1h30m" });
    t.edit.mockClear();
    fireEvent.change(input, { target: { value: "90" } });
    expect(t.edit).not.toHaveBeenCalled();
    expect(within(fieldBox("timeout")).getByRole("alert")).toHaveTextContent(/length of time/);
  });

  it("bool: a checkbox emits true/false", async () => {
    const t = renderBlock(route());
    await userEvent.click(within(fieldBox("allow_anonymous")).getByRole("checkbox"));
    expect(t.last()[0]).toMatchObject({ op: "setField", path: "route/web.todos_list/allow_anonymous", value: "true" });
  });

  it("ident: offers the known values and emits a bare identifier", () => {
    const t = renderBlock(route());
    const select = within(fieldBox("method")).getByRole("combobox");
    expect(within(select).getAllByRole("option").map((o) => o.textContent)).toContain("POST");
    fireEvent.change(select, { target: { value: "POST" } });
    expect(t.last()[0]).toMatchObject({ op: "setField", path: "route/web.todos_list/method", value: "POST" });
  });

  it("removing a set field emits removeField; an unset one has nothing to remove", async () => {
    const t = renderBlock(route());
    await userEvent.click(within(fieldBox("intent")).getByRole("button", { name: "Remove intent" }));
    expect(t.last()).toEqual([{ op: "removeField", file: "test.bcl", path: "route/web.todos_list/intent" }]);
  });
});

describe("value modes: literal / env() / expression", () => {
  it("infers the mode from the current value", () => {
    renderBlock(
      topBlock("route", "r", [["path", 'env("BASE_PATH","/x")'], ["intent", "input.name"], ["layout", '"l"']]),
    );
    const on = (name: string) => within(fieldBox(name)).getByRole("radio", { checked: true }).textContent;
    expect(on("path")).toBe("From environment");
    expect(on("intent")).toBe("Formula");
    expect(on("layout")).toBe("Fixed value");
  });

  it('env mode emits env("PORT","8080")', async () => {
    const t = renderBlock(route());
    const box = fieldBox("path");
    await userEvent.click(within(box).getByRole("radio", { name: "From environment" }));
    fireEvent.change(within(box).getByLabelText("path environment variable"), { target: { value: "PORT" } });
    expect(t.last()[0]).toMatchObject({ value: 'env("PORT")' });
    fireEvent.change(within(box).getByLabelText("path default value"), { target: { value: "8080" } });
    expect(t.last()).toEqual([{ op: "setField", file: "test.bcl", path: "route/web.todos_list/path", value: 'env("PORT","8080")' }]);
  });

  it("env mode sends nothing until there is a variable name", async () => {
    const t = renderBlock(route());
    const box = fieldBox("path");
    await userEvent.click(within(box).getByRole("radio", { name: "From environment" }));
    fireEvent.change(within(box).getByLabelText("path default value"), { target: { value: "8080" } });
    expect(t.edit).not.toHaveBeenCalled();
  });

  it("reads an existing env() call back into its two inputs", () => {
    renderBlock(topBlock("route", "r", [["path", 'env("BASE_PATH","/x")']]));
    const box = fieldBox("path");
    expect(within(box).getByLabelText("path environment variable")).toHaveValue("BASE_PATH");
    expect(within(box).getByLabelText("path default value")).toHaveValue("/x");
  });

  it("works for non-strings too: a duration from the environment", async () => {
    const t = renderBlock(route());
    const box = fieldBox("timeout");
    await userEvent.click(within(box).getByRole("radio", { name: "From environment" }));
    fireEvent.change(within(box).getByLabelText("timeout environment variable"), { target: { value: "REQ_TIMEOUT" } });
    fireEvent.change(within(box).getByLabelText("timeout default value"), { target: { value: "30s" } });
    expect(t.last()[0]).toMatchObject({ value: 'env("REQ_TIMEOUT","30s")' });
  });

  it("expression mode passes the text through untouched", async () => {
    const t = renderBlock(route());
    const box = fieldBox("intent");
    await userEvent.click(within(box).getByRole("radio", { name: "Formula" }));
    fireEvent.change(within(box).getByLabelText("intent expression"), { target: { value: 'upper(input.name) + "-x"' } });
    expect(t.last()[0]).toMatchObject({ op: "setField", value: 'upper(input.name) + "-x"' });
  });
});

describe("lists", () => {
  const authz = () =>
    topBlock("route", "r", [block("route/r", "authz", undefined, [field("route/r/authz", "roles", '["user", "admin"]')])]);

  it("renders items and appends a new one", async () => {
    const t = renderBlock(authz());
    await userEvent.click(screen.getByRole("button", { name: "Add roles item" }));
    expect(t.last()).toEqual([{ op: "setField", file: "test.bcl", path: "route/r/authz/roles", value: '["user", "admin", ""]' }]);
  });

  it("edits an item in place", () => {
    const t = renderBlock(authz());
    fireEvent.change(screen.getAllByRole("textbox", { name: /roles item 2|^$/ })[0]!, { target: { value: "x" } }); // first textbox is item 1
    expect(t.last()[0]).toMatchObject({ value: '["x", "admin"]' });
  });

  it("removes and reorders items", async () => {
    const t = renderBlock(authz());
    await userEvent.click(screen.getByRole("button", { name: "Remove roles item 1" }));
    expect(t.last()[0]).toMatchObject({ value: '["admin"]' });
    await userEvent.click(screen.getByRole("button", { name: "Move roles item 1 down" }));
    expect(t.last()[0]).toMatchObject({ value: '["admin", "user"]' });
  });

  it("falls back to raw text for a list it cannot rewrite safely", () => {
    renderBlock(topBlock("route", "r", [block("route/r", "authz", undefined, [field("route/r/authz", "roles", "split(env(\"ROLES\"), \",\")")])]));
    const box = document.querySelector<HTMLElement>('[data-path="route/r/authz/roles"]')!;
    expect(box.dataset.kind).toBe("raw");
    expect(within(box).getByRole("textbox")).toHaveValue('split(env("ROLES"), ",")');
  });
});

describe("maps", () => {
  it("edits and adds entries of an existing map block", async () => {
    const headers = block("route/r", "headers", undefined, [field("route/r/headers", "X-Mode", '"dev"')]);
    const t = renderBlock(topBlock("route", "r", [headers]));
    fireEvent.change(within(document.querySelector<HTMLElement>('[data-path="route/r/headers/X-Mode"]')!).getByRole("textbox"), { target: { value: "prod" } });
    expect(t.last()[0]).toMatchObject({ op: "setField", path: "route/r/headers/X-Mode", value: '"prod"' });

    fireEvent.change(screen.getByLabelText("New headers key"), { target: { value: "X-New" } });
    await userEvent.click(screen.getByRole("button", { name: "Add headers entry" }));
    expect(t.last()).toEqual([{ op: "setField", file: "test.bcl", path: "route/r/headers/X-New", value: '""' }]);
  });

  it("creates the map block first when it does not exist yet", async () => {
    const t = renderBlock(topBlock("route", "r", [["path", '"/x"']]));
    await userEvent.selectOptions(screen.getByRole("combobox", { name: "Add a setting" }), "headers");
    fireEvent.change(screen.getByLabelText("New headers key"), { target: { value: "X-A" } });
    await userEvent.click(screen.getByRole("button", { name: "Add headers entry" }));
    expect(t.last()).toEqual([
      { op: "addBlock", file: "test.bcl", parent: "route/r", type: "headers" },
      { op: "setField", file: "test.bcl", path: "route/r/headers/X-A", value: '""' },
    ]);
  });
});

describe("nested blocks", () => {
  it("adds and removes a single nested block", async () => {
    const t = renderBlock(topBlock("route", "r", [["path", '"/x"']]));
    await userEvent.selectOptions(screen.getByRole("combobox", { name: "Add a setting" }), "authz");
    await userEvent.click(screen.getByRole("button", { name: "Add authz" }));
    expect(t.last()).toEqual([{ op: "addBlock", file: "test.bcl", parent: "route/r", type: "authz" }]);
  });

  it("removes it", async () => {
    const t = renderBlock(topBlock("route", "r", [block("route/r", "authz", undefined, [field("route/r/authz", "roles", '["a"]')])]));
    await userEvent.click(screen.getByRole("button", { name: "Remove authz" }));
    expect(t.last()).toEqual([{ op: "removeBlock", file: "test.bcl", path: "route/r/authz" }]);
  });

  it("adds, moves and removes repeated blocks", async () => {
    const p1 = block("route/r", "parameter", undefined, [field("route/r/parameter[0]", "name", '"id"')]);
    p1.path = "route/r/parameter[0]";
    const p2 = block("route/r", "parameter", undefined, [field("route/r/parameter[1]", "name", '"q"')]);
    p2.path = "route/r/parameter[1]";
    const t = renderBlock(topBlock("route", "r", [["path", '"/x"'], p1, p2]));

    await userEvent.click(screen.getByRole("button", { name: "Move parameter 2 up" }));
    // children are [path, p1, p2]; p2 goes before p1 => index 1
    expect(t.last()).toEqual([{ op: "moveBlock", file: "test.bcl", path: "route/r/parameter[1]", index: 1 }]);
    await userEvent.click(screen.getByRole("button", { name: "Move parameter 1 down" }));
    expect(t.last()).toEqual([{ op: "moveBlock", file: "test.bcl", path: "route/r/parameter[0]", index: 3 }]);
    await userEvent.click(screen.getByRole("button", { name: "Remove parameter 2" }));
    expect(t.last()).toEqual([{ op: "removeBlock", file: "test.bcl", path: "route/r/parameter[1]" }]);
    expect(screen.getByRole("button", { name: "Add parameter" })).toBeDisabled(); // parameters need an id
    fireEvent.change(screen.getByLabelText("New parameter id"), { target: { value: "limit" } });
    await userEvent.click(screen.getByRole("button", { name: "Add parameter" }));
    expect(t.last()).toEqual([{ op: "addBlock", file: "test.bcl", parent: "route/r", type: "parameter", id: "limit" }]);
  });
});

describe("resource config from the catalog", () => {
  const resource = (kids: (ReturnType<typeof block> | [string, string])[]) => topBlock("resource", "database", [["kind", '"database.sql"'], ...kids]);

  it("lists the kind's settings and marks required ones", () => {
    renderBlock(resource([]));
    const cfg = document.querySelector<HTMLElement>('[data-kind="config"]')!;
    expect(cfg).toBeTruthy();
    expect(within(cfg).getAllByText(/\*/).length).toBeGreaterThan(0);
  });

  it("adds the config block together with the first setting", () => {
    const t = renderBlock(resource([]));
    const cfg = document.querySelector<HTMLElement>('[data-kind="config"]')!;
    const box = cfg.querySelector<HTMLElement>('[data-path="resource/database/config/driver"]')!;
    fireEvent.change(within(box).getByRole("textbox"), { target: { value: "sqlite" } });
    expect(t.last()).toEqual([
      { op: "addBlock", file: "test.bcl", parent: "resource/database", type: "config" },
      { op: "setField", file: "test.bcl", path: "resource/database/config/driver", value: '"sqlite"' },
    ]);
  });

  it("keeps settings the catalog does not know", () => {
    const config = block("resource/database", "config", undefined, [
      field("resource/database/config", "driver", '"sqlite"'),
      field("resource/database/config", "mystery_flag", "true"),
    ]);
    renderBlock(resource([config]));
    expect(screen.getByText("mystery_flag")).toBeInTheDocument();
  });

  it("suggests the registered kinds for the kind field", () => {
    renderBlock(resource([]));
    const opts = Array.from(document.querySelectorAll("datalist option")).map((o) => o.getAttribute("value"));
    expect(opts).toContain("database.sql");
  });
});

describe("never drops what it does not understand", () => {
  it("shows unknown fields and unknown nested blocks", () => {
    const odd = block("route/r", "experimental", "x1", [field("route/r/experimental/x1", "inner_flag", "true")]);
    renderBlock(topBlock("route", "r", [["totally_unknown", '"v"'], odd]));
    expect(screen.getByLabelText("totally_unknown")).toHaveValue('"v"');
    expect(screen.getByText("Experimental x1")).toBeInTheDocument();
    expect(screen.getByText("inner_flag")).toBeInTheDocument();
  });

  it("edits an unknown field as raw text", () => {
    const t = renderBlock(topBlock("route", "r", [["totally_unknown", '"v"']]));
    fireEvent.change(screen.getByLabelText("totally_unknown"), { target: { value: '"w"' } });
    expect(t.last()[0]).toMatchObject({ op: "setField", path: "route/r/totally_unknown", value: '"w"' });
  });

  it("has no schema for a block type? falls back to raw rows", () => {
    renderBlock(topBlock("not_a_real_block", "x", [["a", "1"]]));
    expect(screen.getByLabelText("a")).toHaveValue("1");
  });
});

describe("sections", () => {
  it("groups fields under friendly headings, with less common settings collapsed", () => {
    renderBlock(topBlock("route", "r", [["path", '"/x"'], ["timeout", "30s"], ["cache_control", '"no-store"']]), { expandAll: false });
    expect(screen.getByRole("button", { name: /Basics/ })).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("button", { name: /Performance & limits/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Advanced/ })).toBeNull(); // nothing advanced is set
  });

  it("keeps advanced settings collapsed until opened", async () => {
    renderBlock(topBlock("route", "r", [["path", '"/x"'], ["tags", '["a"]']]), { expandAll: false });
    const adv = screen.getByRole("button", { name: /Advanced/ });
    expect(adv).toHaveAttribute("aria-expanded", "false");
    expect(document.querySelector('[data-path="route/r/tags"]')).toBeNull();
    await userEvent.click(adv);
    expect(document.querySelector('[data-path="route/r/tags"]')).not.toBeNull();
  });

  it("opens the section that holds a setting a problem points to", () => {
    renderBlock(topBlock("route", "r", [["path", '"/x"'], ["tags", '["a"]']]), { expandAll: false, focusPath: "route/r/tags" });
    expect(screen.getByRole("button", { name: /Advanced/ })).toHaveAttribute("aria-expanded", "true");
  });

  it("shows friendly labels, keeping the raw name in the tooltip", () => {
    renderBlock(route());
    const label = within(fieldBox("allow_anonymous")).getByText("Open to everyone");
    expect(label.closest("label")).toHaveAttribute("title", expect.stringContaining("allow_anonymous"));
  });
});

describe("diagnostics, pending edits and read-only", () => {
  it("shows a field's diagnostics next to it", () => {
    renderBlock(route(), { diagnostics: [{ severity: "error", message: "path must start with /", path: "route/web.todos_list/path" }] });
    expect(within(fieldBox("path")).getByRole("alert")).toHaveTextContent("Addresses must start with");
    expect(within(fieldBox("intent")).queryByRole("alert")).toBeNull();
  });

  it("ignores diagnostics that belong to another file", () => {
    renderBlock(route(), { diagnostics: [{ severity: "error", message: "elsewhere", path: "route/web.todos_list/path", file: "other.bcl" }] });
    expect(screen.queryByText("elsewhere")).toBeNull();
  });

  it("shows a pending (optimistic) value before the server confirms it", () => {
    renderBlock(route(), { overlay: { "test.bcl|route/web.todos_list/path": { raw: '"/pending"' } } });
    expect(within(fieldBox("path")).getByRole("textbox")).toHaveValue("/pending");
  });

  it("disables editing for viewers", () => {
    renderBlock(route(), { readOnly: true });
    expect(within(fieldBox("path")).getByRole("textbox")).toBeDisabled();
    expect(screen.queryByRole("combobox", { name: "Add a setting" })).toBeNull();
  });
});
