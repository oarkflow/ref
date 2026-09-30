import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import type { BlockNode, Catalog, Op } from "../../api/types";
import { FormProvider } from "../../forms/ctx";
import { StudioProvider } from "../../state/context";
import { createStudioStore } from "../../state/store";
import { fakeApi } from "../../test/fakeApi";
import { block, catalog as catalogJson, field, schemas, topBlock } from "../../test/helpers";
import { buildIntentGraph, buildProcessGraph } from "../model";
import { childSchema } from "../NodePanel";
import { registerNodeType, unregisterNodeType } from ".";
import { SettingsPanel } from "./SettingsPanel";
import { f, section } from "./builtins/dsl";
import type { FieldDef, NodeTypeDef } from "./types";

const catalog = catalogJson as unknown as Catalog;
const FILE = "t.bcl";
const NODE = "intent/t/node/n";

beforeAll(() => {
  Element.prototype.scrollIntoView = () => {};
});
afterEach(cleanup);

/** A flow with one step `n` of type `typeName` (config given as raw BCL), plus other blocks for the pickers to offer. */
function mount(typeName: string, opts: { config?: Record<string, string>; top?: Record<string, string>; extra?: BlockNode[]; nodes?: [string, Record<string, string>][]; readOnly?: boolean; cat?: Catalog; noConfig?: boolean; noFamily?: boolean } = {}) {
  const config = Object.entries(opts.config ?? {});
  const kids: BlockNode[] = [
    ...(opts.noFamily ? [] : [field(NODE, "family", typeName)]),
    ...Object.entries(opts.top ?? {}).map(([k, v]) => field(NODE, k, v)),
    ...(opts.noConfig ? [] : [block(NODE, "config", undefined, config.map(([k, v]) => field(`${NODE}/config`, k, v)))]),
  ];
  const node: BlockNode = { kind: "block", path: NODE, type: "node", id: "n", line: 1, start: 0, end: 0, children: kids };
  const others = (opts.nodes ?? []).map(([id, fields]): BlockNode => ({
    kind: "block", path: `intent/t/node/${id}`, type: "node", id, line: 1, start: 0, end: 0,
    children: Object.entries(fields).map(([k, v]) => field(`intent/t/node/${id}`, k, v)),
  }));
  const root: BlockNode = { ...topBlock("intent", "t", []), children: [node, ...others] };
  const cat = opts.cat ?? catalog;
  const graph = buildIntentGraph(root, cat);
  const store = createStudioStore(fakeApi());
  store.setState({ trees: { [FILE]: [root, ...(opts.extra ?? [])] }, catalog: cat });
  const edit = vi.fn<(ops: Op[]) => void>();
  const ui = render(
    <StudioProvider store={store}>
      <FormProvider value={{ file: FILE, schemas, catalog: cat, diagnostics: [], focusPath: null, overlay: {}, readOnly: opts.readOnly, expandAll: true, edit }}>
        <SettingsPanel kind="node" node={node} graph={graph} catalog={cat} schema={childSchema(schemas, "intent", "node")} />
      </FormProvider>
    </StudioProvider>,
  );
  return { ...ui, edit, node, panel: ui.container.querySelector<HTMLElement>(".cv-typepanel")! };
}

/** The button that goes with an 'add' input (there are several 'Add' buttons in a panel). */
const addButton = (input: HTMLElement) => within(input.closest("form")!).getByRole("button", { name: "Add" });
const lastOps = (edit: ReturnType<typeof vi.fn>) => edit.mock.calls.at(-1)![0] as Op[];
const set = (path: string, value: string): Op => ({ op: "setField", file: FILE, path, value });
const C = `${NODE}/config`;

describe("one form per kind of step", () => {
  it("shows the fields of the kind, not another's", () => {
    const http = mount("http");
    expect(within(http.panel).getByLabelText("Address")).toBeInTheDocument();
    expect(within(http.panel).queryByLabelText("Allow when")).toBeNull();
    http.unmount();
    const d = mount("decision");
    expect(within(d.panel).getByLabelText("Allow when")).toBeInTheDocument();
    expect(within(d.panel).queryByLabelText("Address")).toBeNull();
  });

  it("shows the kind a step gets from its action when it names no family, and writes nothing until changed", () => {
    const { panel, edit } = mount("validate", { noFamily: true, top: { uses: '"validate.schema"' } });
    const kind = within(panel).getByLabelText("Kind of step") as HTMLSelectElement;
    expect(kind.value).toBe("validate");
    expect(edit).not.toHaveBeenCalled();
  });

  it("names the kind and says what it does", () => {
    const { panel } = mount("rate_limit");
    expect(within(panel).getByText("Limit how often", { selector: "strong" })).toBeInTheDocument();
    expect(within(panel).getByText(/Turns requests away when someone asks too often/)).toBeInTheDocument();
  });

  it("lists what is still missing, in plain words", () => {
    const { panel } = mount("rate_limit");
    const todo = within(panel).getByRole("status", { name: "Still to do" });
    expect(todo).toHaveTextContent("“Count separately for each” is needed.");
    expect(todo).toHaveTextContent("“Allowed” is needed.");
    expect(todo).toHaveTextContent("“Within” is needed.");
  });

  it("the list shrinks as they are filled in", () => {
    const { panel } = mount("rate_limit", { config: { key: '"principal.id"', limit: "10", window: "1m" }, top: { resource: '"limits"' } });
    expect(within(panel).queryByRole("status", { name: "Still to do" })).toBeNull();
  });

  it("read-only: nothing to change", () => {
    const { panel } = mount("script", { config: { expression: '"a + b"' }, readOnly: true });
    expect(within(panel).getByLabelText("Work out")).toBeDisabled();
  });
});

describe("conditional fields", () => {
  it("'if nothing comes back, say' appears only when the query is required to return rows", () => {
    const off = mount("database", { config: { statement: '"SELECT 1"' } });
    expect(within(off.panel).queryByLabelText("If nothing comes back, say")).toBeNull();
    off.unmount();
    const on = mount("database", { config: { statement: '"SELECT 1"', require_rows: "true" } });
    expect(within(on.panel).getByLabelText("If nothing comes back, say")).toBeInTheDocument();
  });

  it("a body only for methods that carry one", () => {
    const get = mount("http", { config: { method: "GET" } });
    expect(within(get.panel).queryByLabelText("Send this as the body")).toBeNull();
    get.unmount();
    const post = mount("http", { config: { method: "POST" } });
    expect(within(post.panel).getByLabelText("Send this as the body")).toBeInTheDocument();
  });

  it("'this is only for a flow' sections do not show in a process, and the reverse", () => {
    const flow = mount("approval");
    expect(within(flow.panel).getByText("Which task", { selector: "summary span" })).toBeInTheDocument();
    expect(within(flow.panel).queryByText("Task for a person", { selector: "summary span" })).toBeNull();
  });
});

describe("each kind of field writes the right BCL", () => {
  const KINDS = "kinds-demo";
  const only = (fields: FieldDef[], id = "t:kinds"): NodeTypeDef => ({
    id, label: "Demo", icon: "gear", category: "compute", priority: 500, blurb: "demo", match: (t) => t === KINDS, actionConfig: false,
    sections: [section("s", "All", fields)],
  });
  const reg: string[] = [];
  const use = (fields: FieldDef[]) => { const d = only(fields, `t:kinds:${reg.length}`); registerNodeType(d); reg.push(d.id); };
  afterEach(() => { while (reg.length) unregisterNodeType(reg.pop()!); });

  it("text", () => {
    use([f.text("greeting", "Greeting")]);
    const { edit, panel } = mount(KINDS);
    fireEvent.change(within(panel).getByLabelText("Greeting"), { target: { value: "hi there" } });
    expect(lastOps(edit)).toEqual([set(`${C}/greeting`, '"hi there"')]);
  });

  it("longText: on blur, quoted", () => {
    use([f.long("note", "Note text")]);
    const { edit, panel } = mount(KINDS);
    const box = within(panel).getByLabelText("Note text");
    fireEvent.change(box, { target: { value: "line one" } });
    expect(edit).not.toHaveBeenCalled();
    fireEvent.blur(box);
    expect(lastOps(edit)).toEqual([set(`${C}/note`, '"line one"')]);
  });

  it("number: whole numbers only, bare", () => {
    use([f.int("count", "How many")]);
    const { edit, panel } = mount(KINDS);
    fireEvent.change(within(panel).getByLabelText("How many"), { target: { value: "7" } });
    expect(lastOps(edit)).toEqual([set(`${C}/count`, "7")]);
    edit.mockClear();
    fireEvent.change(within(panel).getByLabelText("How many"), { target: { value: "seven" } });
    expect(edit).not.toHaveBeenCalled();
    expect(within(panel).getByText(/whole number/i)).toBeInTheDocument();
  });

  it("boolean", () => {
    use([f.bool("loud", "Be loud")]);
    const { edit, panel } = mount(KINDS);
    fireEvent.click(within(panel).getByLabelText("Be loud"));
    expect(lastOps(edit)).toEqual([set(`${C}/loud`, "true")]);
  });

  it("duration", () => {
    use([f.dur("wait", "Wait for")]);
    const { edit, panel } = mount(KINDS);
    fireEvent.change(within(panel).getByLabelText("Wait for"), { target: { value: "30s" } });
    expect(lastOps(edit)).toEqual([set(`${C}/wait`, "30s")]);
    edit.mockClear();
    fireEvent.change(within(panel).getByLabelText("Wait for"), { target: { value: "soon" } });
    expect(edit).not.toHaveBeenCalled();
  });

  it("expression: offers the facts the step needs, and inserts one where the caret is", () => {
    use([f.expr("formula", "Work out")]);
    const { edit, panel } = mount(KINDS, { top: { requires: "[price, qty]" }, nodes: [["p", { provides: "[price]" }], ["q", { provides: "[qty]" }]] });
    const box = within(panel).getByLabelText("Work out") as HTMLTextAreaElement;
    fireEvent.change(box, { target: { value: "* 2" } });
    box.setSelectionRange(0, 0);
    fireEvent.click(within(panel).getByRole("button", { name: "price" }));
    expect(lastOps(edit)).toEqual([set(`${C}/formula`, '"price* 2"')]);
    expect(within(panel).getByRole("button", { name: "qty" })).toBeInTheDocument();
  });

  it("code", () => {
    use([f.code("body", "Query text", { language: "sql" })]);
    const { edit, panel } = mount(KINDS);
    const box = within(panel).getByLabelText("Query text");
    fireEvent.change(box, { target: { value: "SELECT 1" } });
    fireEvent.blur(box);
    expect(lastOps(edit)).toEqual([set(`${C}/body`, '"SELECT 1"')]);
  });

  it("select: a bare word or a string, as the field says", () => {
    use([f.select("verb", "Verb", [{ value: "get", label: "Get" }, { value: "put", label: "Put" }]), f.select("mode", "Mode", [{ value: "a", label: "A" }], { bare: false })]);
    const { edit, panel } = mount(KINDS);
    fireEvent.change(within(panel).getByLabelText("Verb"), { target: { value: "put" } });
    expect(lastOps(edit)).toEqual([set(`${C}/verb`, "put")]);
    fireEvent.change(within(panel).getByLabelText("Mode"), { target: { value: "a" } });
    expect(lastOps(edit)).toEqual([set(`${C}/mode`, '"a"')]);
  });

  it("select from the catalog: the kinds of step", () => {
    use([f.select("kind", "Of kind", { from: "nodeTypes" })]);
    const { panel } = mount(KINDS);
    const sel = within(panel).getByLabelText("Of kind") as HTMLSelectElement;
    expect([...sel.options].map((o) => o.value)).toContain("branch");
    expect([...sel.options].find((o) => o.value === "branch")!.textContent).toBe("Choose a path");
  });

  it("keyValue: the first entry creates the map", () => {
    use([f.kv("headers", "Headers")]);
    const { edit, panel } = mount(KINDS);
    fireEvent.change(within(panel).getByLabelText("Add to Headers"), { target: { value: "Accept" } });
    fireEvent.click(addButton(within(panel).getByLabelText("Add to Headers")));
    expect(lastOps(edit)).toEqual([
      { op: "addBlock", file: FILE, parent: C, type: "headers" },
      set(`${C}/headers/Accept`, '""'),
    ]);
  });

  it("keyValue: a name that cannot be a key is refused, with a reason", () => {
    use([f.kv("headers", "Headers")]);
    const { edit, panel } = mount(KINDS);
    fireEvent.change(within(panel).getByLabelText("Add to Headers"), { target: { value: "not ok!" } });
    fireEvent.click(addButton(within(panel).getByLabelText("Add to Headers")));
    expect(edit).not.toHaveBeenCalled();
    expect(within(panel).getByRole("alert")).toHaveTextContent(/letters, digits/);
  });

  it("a step with no config block yet gets one when its first setting is set", () => {
    use([f.text("greeting", "Greeting")]);
    const { edit, panel } = mount(KINDS, { noConfig: true });
    fireEvent.change(within(panel).getByLabelText("Greeting"), { target: { value: "hi" } });
    expect(lastOps(edit)).toEqual([{ op: "addBlock", file: FILE, parent: NODE, type: "config" }, set(`${C}/greeting`, '"hi"')]);
  });

  it("keyValue: existing entries are shown, editable and removable", () => {
    use([f.kv("headers", "Headers")]);
    const nodePath = NODE;
    const headers = block(`${nodePath}/config`, "headers", undefined, [field(`${nodePath}/config/headers`, "Accept", '"text/plain"')]);
    // build by hand: a config block that contains a headers block
    const config = { ...block(nodePath, "config", undefined, []), children: [headers] };
    const node: BlockNode = { kind: "block", path: nodePath, type: "node", id: "n", line: 1, start: 0, end: 0, children: [field(nodePath, "family", KINDS), config] };
    const root = { ...topBlock("intent", "t", []), children: [node] };
    const store = createStudioStore(fakeApi());
    store.setState({ trees: { [FILE]: [root] }, catalog });
    const edit = vi.fn<(ops: Op[]) => void>();
    render(
      <StudioProvider store={store}>
        <FormProvider value={{ file: FILE, schemas, catalog, diagnostics: [], focusPath: null, overlay: {}, expandAll: true, edit }}>
          <SettingsPanel kind="node" node={node} graph={buildIntentGraph(root, catalog)} catalog={catalog} schema={childSchema(schemas, "intent", "node")} />
        </FormProvider>
      </StudioProvider>,
    );
    expect(document.querySelector(".cv-kvkey")).toHaveTextContent("Accept");
    fireEvent.click(screen.getByRole("button", { name: "Remove Accept" }));
    expect(lastOps(edit)).toEqual([{ op: "removeField", file: FILE, path: `${nodePath}/config/headers/Accept` }]);
  });

  it("list of numbers: only numbers, written as a list", () => {
    use([f.list("codes", "Good codes", { item: "number" })]);
    const { edit, panel } = mount(KINDS);
    const input = within(panel).getByLabelText("Add to Good codes");
    fireEvent.change(input, { target: { value: "200" } });
    fireEvent.click(addButton(input));
    expect(lastOps(edit)).toEqual([set(`${C}/codes`, "[200]")]);
    edit.mockClear();
    fireEvent.change(input, { target: { value: "abc" } });
    fireEvent.click(addButton(input));
    expect(edit).not.toHaveBeenCalled();
  });

  it("stringList: chips you can add and remove", () => {
    use([f.strs("roles", "Roles")]);
    const { edit, panel } = mount(KINDS, { config: { roles: '["admin"]' } });
    expect(within(panel).getByText("admin")).toBeInTheDocument();
    fireEvent.change(within(panel).getByLabelText("Add to Roles"), { target: { value: "editor" } });
    fireEvent.click(addButton(within(panel).getByLabelText("Add to Roles")));
    expect(lastOps(edit)).toEqual([set(`${C}/roles`, '["admin", "editor"]')]);
    fireEvent.click(within(panel).getByRole("button", { name: "Remove admin" }));
    expect(lastOps(edit)).toEqual([{ op: "removeField", file: FILE, path: `${C}/roles` }]);
  });

  it("factPicker: only real facts, single or several", () => {
    use([f.fact("source", "Take it from", { role: "any" }), f.facts("args", "Fill in with", { role: "any" })]);
    const { edit, panel } = mount(KINDS, { nodes: [["p", { provides: "[price]" }], ["q", { provides: "[qty]" }]] });
    const one = within(panel).getByLabelText("Take it from") as HTMLSelectElement;
    expect([...one.options].map((o) => o.value)).toEqual(expect.arrayContaining(["price", "qty", "input"]));
    fireEvent.change(one, { target: { value: "price" } });
    expect(lastOps(edit)).toEqual([set(`${C}/source`, '"price"')]);
    fireEvent.change(within(panel).getByLabelText("Add to Fill in with"), { target: { value: "qty" } });
    expect(lastOps(edit)).toEqual([set(`${C}/args`, "[qty]")]);
  });

  it("resourcePicker: only connections of the right kind", () => {
    use([f.resource("resource", "Connection", { kinds: ["database."] })]);
    const extra = [
      topBlock("resource", "main_db", [["kind", '"database.sql"']]),
      topBlock("resource", "mailer", [["kind", '"service.smtp"']]),
      topBlock("resource", "cache", [["kind", '"cache.memory"']]),
    ];
    const { edit, panel } = mount(KINDS, { extra });
    const sel = within(panel).getByLabelText("Connection") as HTMLSelectElement;
    expect([...sel.options].map((o) => o.value).filter(Boolean)).toEqual(["main_db"]);
    fireEvent.change(sel, { target: { value: "main_db" } });
    expect(lastOps(edit)).toEqual([set(`${NODE}/resource`, '"main_db"')]);
  });

  it("intentPicker: the flows of the draft; several as a list", () => {
    use([f.intent("run", "Run"), f.intents("all", "Run all")]);
    const extra = [topBlock("intent", "orders.create", []), topBlock("intent", "orders.cancel", [])];
    const { edit, panel } = mount(KINDS, { extra });
    fireEvent.change(within(panel).getByLabelText("Run"), { target: { value: "orders.cancel" } });
    expect(lastOps(edit)).toEqual([set(`${C}/run`, '"orders.cancel"')]);
    fireEvent.change(within(panel).getByLabelText("Add to Run all"), { target: { value: "orders.create" } });
    expect(lastOps(edit)).toEqual([set(`${C}/all`, '["orders.create"]')]);
  });

  it("routePicker and secretRef offer the draft's routes and secrets", () => {
    use([f.route("go", "Send to"), f.secret("key", "Signing secret")]);
    const extra = [topBlock("route", "web.home", []), topBlock("secret", "hook_secret", [])];
    const { edit, panel } = mount(KINDS, { extra });
    fireEvent.change(within(panel).getByLabelText("Send to"), { target: { value: "web.home" } });
    expect(lastOps(edit)).toEqual([set(`${C}/go`, '"web.home"')]);
    fireEvent.change(within(panel).getByLabelText("Signing secret"), { target: { value: "hook_secret" } });
    expect(lastOps(edit)).toEqual([set(`${C}/key`, '"hook_secret"')]);
  });

  it("cases: rows you can add, each with its own columns", () => {
    use([f.cases("routes", { rowLabel: "Way", addLabel: "Add a way", seed: "name", cols: [{ key: "name", label: "Name", kind: "text", title: true }, { key: "when", label: "When", kind: "expr" }] })]);
    const { edit, panel } = mount(KINDS);
    fireEvent.click(within(panel).getByRole("button", { name: "+ Add a way" }));
    const op = lastOps(edit)[0] as Extract<Op, { op: "setField" }>;
    expect(op).toMatchObject({ op: "setField", path: `${C}/routes` });
    expect(op.value).toMatch(/^\[\s*\{ name "[^"]+" \}\s*\]$/);
  });

  it("conditions: builds a formula from checks, and reads it back", () => {
    use([f.cond("when", "Only when")]);
    const { edit, panel, unmount } = mount(KINDS);
    fireEvent.click(within(panel).getByRole("button", { name: "Add a check" }));
    // a blank check is a row on screen, but there is no formula to write yet: no edit at all
    expect(edit).not.toHaveBeenCalled();
    expect(within(panel).getByLabelText("Check 1: what to look at")).toHaveValue("");
    fireEvent.change(within(panel).getByLabelText("Check 1: what to look at"), { target: { value: "amount" } });
    expect(lastOps(edit)).toEqual([set(`${C}/when`, `"amount == ''"`)]);
    unmount();

    const has = mount(KINDS, { config: { when: `"result.action == 'approve'"` } });
    expect(within(has.panel).getByLabelText("Check 1: what to look at")).toHaveValue("result.action");
    expect(within(has.panel).getByLabelText("Check 1: how to compare")).toHaveValue("==");
    expect(within(has.panel).getByLabelText("Check 1: the value")).toHaveValue("approve");
    expect(within(has.panel).getByText("Reads as: If approved")).toBeInTheDocument();

    fireEvent.change(within(has.panel).getByLabelText("Check 1: the value"), { target: { value: "reject" } });
    fireEvent.blur(within(has.panel).getByLabelText("Check 1: the value"));
    expect(lastOps(has.edit)).toEqual([set(`${C}/when`, `"result.action == 'reject'"`)]);

    fireEvent.change(within(has.panel).getByLabelText("Check 1: how to compare"), { target: { value: "!=" } });
    // the row keeps what was just typed (the mocked edit does not update the tree)
    expect(lastOps(has.edit)).toEqual([set(`${C}/when`, `"result.action != 'reject'"`)]);
  });

  it("conditions: two checks can be joined all-of or any-of", () => {
    use([f.cond("when", "Only when")]);
    const { edit, panel } = mount(KINDS, { config: { when: `"a > 1 && b == 'x'"` } });
    expect(within(panel).getByLabelText("How the checks combine")).toHaveValue("and");
    fireEvent.change(within(panel).getByLabelText("How the checks combine"), { target: { value: "or" } });
    expect(lastOps(edit)).toEqual([set(`${C}/when`, `"a > 1 || b == 'x'"`)]);
  });

  it("conditions: a formula the builder cannot read stays a formula, untouched", () => {
    use([f.cond("when", "Only when")]);
    const { panel, edit } = mount(KINDS, { config: { when: `"len(items) > 0"` } });
    expect(within(panel).getByLabelText("Only when")).toHaveValue("len(items) > 0");
    expect(within(panel).getByText(/written as a formula/)).toBeInTheDocument();
    expect(within(panel).queryByRole("button", { name: "Add a check" })).toBeNull();
    expect(edit).not.toHaveBeenCalled();
  });

  it("custom: draws whatever it likes and can write the value", () => {
    use([f.custom("pick", "Pick", ({ set: write }) => <button type="button" onClick={() => write('"chosen"')}>Choose it</button>)]);
    const { edit, panel } = mount(KINDS);
    fireEvent.click(within(panel).getByRole("button", { name: "Choose it" }));
    expect(lastOps(edit)).toEqual([set(`${C}/pick`, '"chosen"')]);
  });

  it("a setting the form does not know is shown as written and never dropped", () => {
    use([f.text("known", "Known")]);
    const { panel } = mount(KINDS, { config: { known: '"a"', mystery: '"still here"' } });
    expect(within(panel).getByText(/not part of the standard form/)).toBeInTheDocument();
    expect(within(panel).getByDisplayValue("still here")).toBeInTheDocument();
  });

  it("advanced fields are folded under 'More settings'", () => {
    use([f.text("plain", "Plain"), f.text("fancy", "Fancy", { advanced: true })]);
    const { panel } = mount(KINDS);
    const more = within(panel).getByText("More settings", { selector: "summary span" }).closest("details")!;
    expect(within(more).getByLabelText("Fancy")).toBeInTheDocument();
    expect(more.contains(within(panel).getByLabelText("Plain"))).toBe(false);
  });
});

describe("a step the registry has never heard of", () => {
  it("still opens a real form, built from what the platform says the action takes", () => {
    const cat: Catalog = {
      ...catalog,
      actions: [...catalog.actions, {
        name: "acme.ship", family: "acme", summary: "Ship an order",
        config: [
          { name: "carrier", type: "string", required: true, summary: "who delivers" },
          { name: "weight", type: "number", summary: "in kilos" },
          { name: "insured", type: "bool" },
          { name: "wait", type: "duration" },
          { name: "labels", type: "map" },
          { name: "sender", type: "resource" },
        ],
      }],
    };
    const { panel, edit } = mount("acme_thing", { top: { uses: '"acme.ship"' }, cat });
    expect(within(panel).getByText("Its settings come from what the platform says this step takes.")).toBeInTheDocument();
    for (const l of ["Carrier", "Weight", "Insured", "Wait", "Labels", "Sender"]) expect(within(panel).getAllByText(l).length).toBeGreaterThan(0);
    fireEvent.change(within(panel).getByLabelText("Carrier"), { target: { value: "DHL" } });
    expect(lastOps(edit)).toEqual([set(`${C}/carrier`, '"DHL"')]);
    expect(within(panel).getByRole("status", { name: "Still to do" })).not.toHaveTextContent("Weight");
  });

  it("a registered descriptor overrides the generated form", () => {
    const d: NodeTypeDef = { id: "t:acme", label: "Ship it", icon: "send", category: "messaging", priority: 400, blurb: "Ours.", match: (_t, a) => a === "acme.ship", sections: [section("s", "Shipping", [f.text("carrier", "Carrier name")])] };
    registerNodeType(d);
    try {
      const { panel } = mount("acme_thing", { top: { uses: '"acme.ship"' } });
      expect(within(panel).getByText("Ship it", { selector: "strong" })).toBeInTheDocument();
      expect(within(panel).getByLabelText("Carrier name")).toBeInTheDocument();
    } finally {
      unregisterNodeType("t:acme");
    }
  });
});

describe("steps of a process", () => {
  it("a person's task has its own form", () => {
    const step: BlockNode = {
      kind: "block", path: "process/p/step/review", type: "step", id: "review", line: 1, start: 0, end: 0,
      children: [field("process/p/step/review", "family", "approval"), block("process/p/step/review", "task", undefined, [field("process/p/step/review/task", "role", '"reviewer"')])],
    };
    const root: BlockNode = { kind: "block", path: "process/p", type: "process", id: "p", line: 1, start: 0, end: 0, children: [step] };
    const store = createStudioStore(fakeApi());
    store.setState({ trees: { [FILE]: [root] }, catalog });
    const edit = vi.fn<(ops: Op[]) => void>();
    render(
      <StudioProvider store={store}>
        <FormProvider value={{ file: FILE, schemas, catalog, diagnostics: [], focusPath: null, overlay: {}, expandAll: true, edit }}>
          <SettingsPanel kind="step" node={step} graph={buildProcessGraph(root, catalog)} catalog={catalog} schema={childSchema(schemas, "process", "step")} />
        </FormProvider>
      </StudioProvider>,
    );
    expect(screen.getByLabelText("Anyone with the role")).toHaveValue("reviewer");
    fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Please review" } });
    expect(lastOps(edit)).toEqual([{ op: "setField", file: FILE, path: "process/p/step/review/task/title", value: '"Please review"' }]);
    // and the ordinary things every step has
    expect(screen.getByLabelText("Run this flow")).toBeInTheDocument();
    expect(screen.getByText("Skip it when", { selector: "summary span" })).toBeInTheDocument();
    void act;
  });
});
