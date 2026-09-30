import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { Op } from "../api/types";
import { StudioProvider } from "../state/context";
import { createStudioStore, type StudioStore } from "../state/store";
import { detail, fakeApi, meta } from "../test/fakeApi";
import { catalog, schemas } from "../test/helpers";
import { CanvasPage } from "./CanvasPage";
import { find, trees } from "./fixtures";
import { typeLabel } from "./labels";

const FILE = "12_todo_workflow_example.bcl";
const WF = "09_workflow_example.bcl";

beforeAll(() => {
  class RO { observe() {} unobserve() {} disconnect() {} }
  Object.assign(globalThis, { ResizeObserver: RO });
  class Matrix { m22 = 1; constructor(_?: string) {} }
  Object.assign(globalThis, { DOMMatrixReadOnly: Matrix });
  Element.prototype.scrollIntoView = () => {};
  Object.defineProperty(window, "innerWidth", { value: 1600, configurable: true }); // rail and drawer open by default
});

function setup(path: string, opts: { file?: string; roles?: ("viewer" | "editor")[] } = {}) {
  const file = opts.file ?? FILE;
  const store: StudioStore = createStudioStore(fakeApi());
  const edit = vi.fn<(ops: Op[]) => void>();
  store.setState({
    meta: meta(opts.roles ?? ["editor"]), schemas, catalog, draft: detail({ id: "d1", files: [file] }), trees: { [file]: trees[file]! },
    nav: { [file]: trees[file]! }, diagnostics: [], edit,
  });
  render(
    <StudioProvider store={store}>
      <MemoryRouter initialEntries={[`/canvas?file=${encodeURIComponent(file)}&path=${encodeURIComponent(path)}`]}>
        <Routes><Route path="/canvas" element={<CanvasPage />} /></Routes>
      </MemoryRouter>
    </StudioProvider>,
  );
  return { store, edit, file };
}

/** The button for a kind of step in the step list. */
const typeButton = (name: string) => screen.getAllByText(typeLabel(name), { selector: "strong" }).map((e) => e.closest("button")).find((b) => b?.classList.contains("cv-typebtn"))!;
const node = (id: string) => document.querySelector(`[data-id="${id}"] .cv-node`) as HTMLElement;
const select = (store: StudioStore, file: string, p: string) => act(async () => { await store.getState().select(file, p); });

beforeEach(() => localStorage.clear());

describe("CanvasPage: process", () => {
  it("draws every step of the todo workflow, marks the first step and the ones that end the run", async () => {
    setup("process/todo.workflow");
    for (const id of ["review", "mark_pending_approval", "approval", "revise", "done", "rejected", "cancelled"]) {
      expect(await screen.findByText(id, { selector: ".cv-titles strong" })).toBeInTheDocument();
    }
    expect(document.querySelectorAll(".cv-node.is-start")).toHaveLength(1);
    expect(document.querySelectorAll(".cv-node.is-terminal")).toHaveLength(3);
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Approval workflow todo.workflow");
  });

  it("every step is the same kind of card: roles are badges and colours, not shapes", async () => {
    setup("process/todo.workflow");
    await screen.findByText("review", { selector: ".cv-titles strong" });
    // the first step, a person to wait for
    expect(node("review")).toHaveClass("is-start", "role-wait", "tone-process");
    expect(within(node("review")).getByText("Start")).toBeInTheDocument();
    expect(within(node("review")).getByText(/^a person \(/)).toBeInTheDocument(); // and who: its role
    // endings take the colour of how they end and say so
    expect(node("done")).toHaveClass("is-terminal", "tone-end-success", "outcome-success");
    expect(within(node("done")).getByText("Ends the run · success")).toBeInTheDocument();
    expect(node("rejected")).toHaveClass("is-terminal", "tone-end-failed");
    expect(within(node("rejected")).getByText("Ends the run · failed")).toBeInTheDocument();
    expect(node("cancelled")).toHaveClass("is-terminal", "tone-end-cancelled");
    // an ordinary action
    expect(node("mark_pending_approval")).toHaveClass("shape-card", "tone-compute");
    expect(within(node("mark_pending_approval")).getByText("Runs")).toBeInTheDocument();
    // no silhouettes: nothing is a pill
    expect(document.querySelectorAll(".shape-pill")).toHaveLength(0);
  });

  it("an ending step has no way out, and a waiting step says what it waits for", async () => {
    setup("process/todo.workflow");
    await screen.findByText("review", { selector: ".cv-titles strong" });
    expect(node("done").querySelector(".react-flow__handle-left")).not.toBeNull();
    expect(node("done")).toHaveClass("is-terminal"); // the stylesheet hides its output handle for this class
    expect(within(node("approval")).getByText("Waits for")).toBeInTheDocument();
  });

  it("offers a step list in plain words and adds a step with the catalog family", async () => {
    const { edit } = setup("process/todo.workflow");
    await screen.findByText("review", { selector: ".cv-titles strong" });
    expect(screen.getByText("Add a step", { selector: "h3" })).toBeInTheDocument();
    fireEvent.click(typeButton("approval"));
    await act(async () => {});
    expect(edit).toHaveBeenCalledTimes(1);
    expect(edit.mock.calls[0]![0]).toEqual([
      { op: "addBlock", file: FILE, parent: "process/todo.workflow", type: "step", id: "approval_2", body: "family approval" },
    ]);
  });

  it("shows a step's own settings in plain words, and renames it with every reference rewritten", async () => {
    const { store, edit } = setup("process/todo.workflow");
    await select(store, FILE, "process/todo.workflow/step/revise");
    const panel = await screen.findByTestId("node-panel");
    expect(within(panel).getByRole("heading", { level: 2 })).toHaveTextContent("revise");
    expect(within(panel).getByText("Task for a person", { selector: "summary span" })).toBeInTheDocument();
    expect(within(panel).getByText("What happens here", { selector: "summary span" })).toBeInTheDocument();
    fireEvent.click(within(panel).getByRole("button", { name: "Rename" }));
    fireEvent.change(within(panel).getByLabelText("New name"), { target: { value: "rework" } });
    fireEvent.click(within(panel).getByRole("button", { name: "Rename" }));
    await act(async () => {});
    const ops = edit.mock.calls[0]![0];
    expect(ops[0]).toEqual({ op: "renameBlock", file: FILE, path: "process/todo.workflow/step/revise", newId: "rework" });
    const refs = ops.slice(1).map((o) => (o.op === "setField" ? `${o.path.split("/").slice(-2).join("/")}=${o.value}` : o.op));
    expect(refs).toEqual(expect.arrayContaining(['review_changes/to="rework"', 'revise_resubmit/from="rework"', 'revise_cancel/from="rework"']));
    expect(ops).toHaveLength(1 + 3);
  });

  it("removes a step together with the edges that reach it", async () => {
    const { store, edit } = setup("process/todo.workflow");
    await select(store, FILE, "process/todo.workflow/step/cancelled");
    const panel = await screen.findByTestId("node-panel");
    fireEvent.click(within(panel).getByRole("button", { name: "Remove" }));
    fireEvent.click(within(panel).getByRole("button", { name: "Confirm remove" }));
    await act(async () => {});
    expect(edit.mock.calls[0]![0]).toEqual([
      { op: "removeBlock", file: FILE, path: "process/todo.workflow/edge/revise_cancel" },
      { op: "removeBlock", file: FILE, path: "process/todo.workflow/step/cancelled" },
    ]);
  });

  it("makes a step the first one from the node itself", async () => {
    const { edit } = setup("process/todo.workflow");
    await screen.findByText("approval", { selector: ".cv-titles strong" });
    fireEvent.click(within(node("approval")).getByTitle("Make this the first step"));
    await act(async () => {});
    expect(edit.mock.calls[0]![0]).toEqual([{ op: "setField", file: FILE, path: "process/todo.workflow/start", value: '"approval"' }]);
  });

  it("offers a '+' after a step that leads nowhere, and adds the next step joined by a connection", async () => {
    const view = setup("process/todo.workflow");
    const { edit } = view;
    const { store } = view;
    await screen.findByText("cancelled", { selector: ".cv-titles strong" });
    // every step here already leads somewhere, and the ones that end the run never offer "+"
    expect(screen.queryByLabelText("Add a step after this one")).toBeNull();
    // cut the way on from one step: now it offers "+"
    const proc = find(FILE, "process", "todo.workflow");
    await act(async () => {
      store.setState({ trees: { [FILE]: trees[FILE]!.map((b) => (b.path === proc.path ? { ...proc, children: proc.children!.filter((c) => c.id !== "to_approval") } : b)) } });
    });
    expect(screen.getAllByLabelText("Add a step after this one")).toHaveLength(1);
    fireEvent.click(within(node("mark_pending_approval")).getByLabelText("Add a step after this one"));
    const dlg = await screen.findByRole("dialog", { name: "What happens next?" });
    fireEvent.click(within(dlg).getAllByText("Wait for a while", { selector: "strong" })[0]!.closest("button")!);
    await act(async () => {});
    expect(edit.mock.calls[0]![0]).toEqual([
      { op: "addBlock", file: FILE, parent: "process/todo.workflow", type: "step", id: "delay", body: "family delay" },
      { op: "addBlock", file: FILE, parent: "process/todo.workflow", type: "edge", id: "mark_pending_approval_to_delay", body: 'kind simple\nfrom "mark_pending_approval"\nto "delay"' },
    ]);
  });

  it("lists the kinds of connection that can be created, in plain words, grouped by family", async () => {
    setup("process/todo.workflow");
    const select = await screen.findByLabelText("Kind of connection to create");
    const groups = Array.from(select.querySelectorAll("optgroup")).map((g) => g.label);
    expect(groups).toEqual(expect.arrayContaining(["sequence", "concurrency"]));
    expect(within(select as HTMLElement).queryByRole("option", { name: "By amount" })).toBeNull(); // no explicit target
    expect(within(select as HTMLElement).getByRole("option", { name: "Split into parallel" })).toBeInTheDocument();
  });

  it("pins the first layout: a new step leaves every existing node where it was", async () => {
    const { store } = setup("process/todo.workflow");
    const at = (id: string) => (document.querySelector(`[data-id="${id}"]`) as HTMLElement).style.transform;
    await screen.findByText("review", { selector: ".cv-titles strong" });
    const before = Object.fromEntries(["review", "revise", "approval", "done"].map((id) => [id, at(id)]));
    const proc = find(FILE, "process", "todo.workflow");
    const extra = { ...structuredClone(proc.children![0]!), kind: "block" as const, type: "step", id: "extra", path: `${proc.path}/step/extra`, children: [] };
    await act(async () => {
      store.setState({ trees: { [FILE]: trees[FILE]!.map((b) => (b.path === proc.path ? { ...proc, children: [...proc.children!, extra] } : b)) } });
    });
    expect(await screen.findByText("extra", { selector: ".cv-titles strong" })).toBeInTheDocument();
    for (const [id, t] of Object.entries(before)) expect(at(id), id).toBe(t);
    const saved = JSON.parse(localStorage.getItem(`studio.canvas.v1|d1|${FILE}|process/todo.workflow`) ?? "{}");
    expect(Object.keys(saved)).toEqual(expect.arrayContaining(["review", "revise", "approval", "done"]));
  });

  it("keeps a node measured across an edit, so lines do not blink out", async () => {
    const { store } = setup("process/todo.workflow");
    await screen.findByText("review", { selector: ".cv-titles strong" });
    const proc = find(FILE, "process", "todo.workflow");
    await act(async () => {
      store.setState({ trees: { [FILE]: trees[FILE]!.map((b) => (b.path === proc.path ? { ...proc, children: proc.children!.filter((c) => c.id !== "revise_cancel") } : b)) } });
    });
    // same nodes, none re-created
    expect(document.querySelectorAll(".react-flow__node")).toHaveLength(7);
  });

  it("is read-only for a viewer role", async () => {
    setup("process/todo.workflow", { roles: ["viewer"] });
    await screen.findByText("review", { selector: ".cv-titles strong" });
    expect(screen.queryByTitle("Make this the first step")).toBeNull();
    expect(screen.queryByLabelText("Add a step after this one")).toBeNull();
    expect(typeButton("approval")).toBeDisabled();
  });
});

describe("CanvasPage: intent", () => {
  it("draws steps with plain-worded ports, and a response step as an ending", async () => {
    setup("intent/todo.list");
    expect(await screen.findByText("user-rows", { selector: ".cv-titles strong" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Logic flow todo.list");
    expect(document.querySelectorAll(".cv-port.unresolved")).toHaveLength(0);
    expect(screen.queryByText(/missing input/)).toBeNull();
    expect(screen.getAllByText("+ needs").length).toBeGreaterThan(3);
    expect(node("response")).toHaveClass("is-terminal");
    expect(document.querySelectorAll(".shape-pill")).toHaveLength(0);
  });

  it("does not offer steps that wait in a request flow", async () => {
    setup("intent/todo.list");
    await screen.findByText("user-rows", { selector: ".cv-titles strong" });
    const durable = catalog.node_types.filter((t) => t.durable).map((t) => t.name);
    expect(durable.length).toBeGreaterThan(0);
    for (const name of durable) expect(screen.queryAllByText(typeLabel(name), { selector: ".cv-typebtn strong" })).toHaveLength(0);
  });

  it("flags needs that no step produces, in red, and counts them in plain words", async () => {
    const { store } = setup("intent/todo.list");
    await screen.findByText("user-rows", { selector: ".cv-titles strong" });
    const it = find(FILE, "intent", "todo.list");
    const broken = structuredClone(it);
    broken.children!.find((c) => c.id === "response")!.children!.find((c) => c.name === "requires")!.raw = "[todos, user, ghost]";
    await act(async () => {
      store.setState({ trees: { [FILE]: trees[FILE]!.map((b) => (b.path === it.path ? broken : b)) } });
    });
    expect(await screen.findByText("1 missing input")).toBeInTheDocument();
    expect(document.querySelectorAll(".cv-port.unresolved")).toHaveLength(1);
  });

  it("adds a step from the list with a unique result name", async () => {
    const { edit } = setup("intent/todo.list");
    await screen.findByText("user-rows", { selector: ".cv-titles strong" });
    fireEvent.click(typeButton("transform"));
    await act(async () => {});
    expect(edit.mock.calls[0]![0]).toEqual([
      expect.objectContaining({ op: "addBlock", file: FILE, parent: "intent/todo.list", type: "node", id: "transform", body: expect.stringContaining("provides [transform]") }),
    ]);
  });

  it("puts problems on the step they belong to and lists them", async () => {
    const { store } = setup("intent/todo.list");
    await screen.findByText("user-rows", { selector: ".cv-titles strong" });
    await act(async () => {
      store.setState({ diagnostics: [{ severity: "error", message: "bad query", path: "intent/todo.list/node/todos/config", file: FILE }] });
    });
    expect(within(node("todos")).getByLabelText("1 errors")).toBeInTheDocument();
    expect(node("todos")).toHaveClass("has-error");
    // the top bar says so, and opens the list
    fireEvent.click(screen.getByRole("button", { name: /1 problem/ }));
    expect(screen.getByRole("button", { name: /bad query/ })).toHaveAttribute("title", "bad query");
  });

  it("collapses a step's details", async () => {
    setup("intent/orders.create", { file: WF });
    await screen.findByText("classify", { selector: ".cv-titles strong" });
    const n = node("classify");
    const details = n.querySelector<HTMLElement>(".cv-details")!;
    expect(details.dataset.open).toBe("true");
    fireEvent.click(within(n).getByLabelText("Hide details of classify"));
    expect(details.dataset.open).toBe("false");
    fireEvent.click(within(n).getByLabelText("Show details of classify"));
    expect(details.dataset.open).toBe("true");
  });
});

describe("node kinds look and read differently (real starter examples)", () => {
  it("a branch lists its cases as labelled ways out", async () => {
    setup("intent/orders.create", { file: WF });
    await screen.findByText("classify", { selector: ".cv-titles strong" });
    const n = node("classify");
    expect(n).toHaveClass("shape-branch", "tone-flow");
    const rows = [...n.querySelectorAll(".cv-cases li")].map((li) => li.textContent);
    expect(rows).toEqual(["large→ orders.tier_large", "small→ orders.tier_small"]);
    expect(within(n).getByText("Choose a path")).toBeInTheDocument();
  });

  it("a switch shows what it looks at and every case, and a decision table shows how many rules", async () => {
    setup("intent/orders.transition", { file: WF });
    await screen.findByText("apply", { selector: ".cv-titles strong" });
    const sw = node("apply");
    expect(sw).toHaveClass("shape-branch");
    expect(within(sw).getByText("on transition.outcome")).toBeInTheDocument();
    expect([...sw.querySelectorAll(".cv-cases li")].map((li) => li.textContent)).toEqual([
      "apply→ orders.apply_status", "apply_priority→ orders.apply_priority_status", "cancel→ orders.cancel",
    ]);
    const table = node("classify");
    expect(table).toHaveClass("shape-decision", "tone-decision");
    expect(within(table).getByText("5 rules")).toBeInTheDocument();
  });

  it("shows the other families in their own colour", async () => {
    setup("intent/orders.create", { file: WF });
    await screen.findByText("classify", { selector: ".cv-titles strong" });
    const tones = new Set([...document.querySelectorAll(".cv-node")].map((n) => [...n.classList].find((c) => c.startsWith("tone-"))));
    expect(tones.size).toBeGreaterThan(2);
  });
});

describe("typed panels: each kind of step has its own settings", () => {
  it("branch: cases as rows with a flow picker, and 'otherwise'", async () => {
    const { store, edit } = setup("intent/orders.create", { file: WF });
    await select(store, WF, "intent/orders.create/node/classify");
    const panel = await screen.findByTestId("node-panel");
    expect(within(panel).getByText("Choose a path", { selector: "summary span" })).toBeInTheDocument();
    expect(within(panel).getByText("Case 1")).toBeInTheDocument();
    const when = within(panel).getAllByLabelText("When")[0]!;
    fireEvent.change(when, { target: { value: "input.amount > 900" } });
    fireEvent.blur(when);
    await act(async () => {});
    const op = edit.mock.calls[0]![0][0] as Extract<Op, { op: "setField" }>;
    expect(op).toMatchObject({ op: "setField", file: WF, path: "intent/orders.create/node/classify/config/cases" });
    expect(op.value).toContain('{ name "large" condition "input.amount > 900" intent "orders.tier_large" }');
    expect(op.value).toContain('{ name "small" condition "input.amount <= 500" intent "orders.tier_small" }'); // untouched
    expect(within(panel).getByLabelText("Otherwise run")).toBeInTheDocument();
  });

  it("branch: adding and removing a case keeps the others as written", async () => {
    const { store, edit } = setup("intent/orders.create", { file: WF });
    await select(store, WF, "intent/orders.create/node/classify");
    const panel = await screen.findByTestId("node-panel");
    fireEvent.click(within(panel).getByRole("button", { name: "+ Add a case" }));
    await act(async () => {});
    const added = (edit.mock.calls[0]![0][0] as Extract<Op, { op: "setField" }>).value;
    expect(added.match(/\{ name /g)).toHaveLength(3);
    fireEvent.click(within(panel).getByRole("button", { name: "Remove Case 1" }));
    await act(async () => {});
    const removed = (edit.mock.calls[1]![0][0] as Extract<Op, { op: "setField" }>).value;
    expect(removed).not.toContain('"large"');
    expect(removed).toContain('"small"');
  });

  it("decision table: rules as rows with when / outcome / because", async () => {
    const { store, edit } = setup("intent/orders.transition", { file: WF });
    await select(store, WF, "intent/orders.transition/node/classify");
    const panel = await screen.findByTestId("node-panel");
    expect(within(panel).getByText("The rules", { selector: "summary span" })).toBeInTheDocument();
    expect(within(panel).getAllByText(/^Rule \d$/)).toHaveLength(5);
    const outcome = within(panel).getAllByLabelText("Then the outcome is")[2]!;
    fireEvent.change(outcome, { target: { value: "ship_now" } });
    fireEvent.blur(outcome);
    await act(async () => {});
    const v = (edit.mock.calls[0]![0][0] as Extract<Op, { op: "setField" }>).value;
    expect(v).toContain('name "ship" condition "order.status == \'paid\' and input.status == \'shipped\'" outcome "ship_now"');
    expect(within(panel).getByLabelText("If nothing matches")).toBeInTheDocument();
  });

  it("switch: 'look at' and its cases, with the flow picker", async () => {
    const { store } = setup("intent/orders.transition", { file: WF });
    await select(store, WF, "intent/orders.transition/node/apply");
    const panel = await screen.findByTestId("node-panel");
    expect(within(panel).getByLabelText("Look at")).toHaveValue("transition.outcome");
    expect(within(panel).getAllByLabelText("When the value is")).toHaveLength(3);
    expect(within(panel).getAllByLabelText("Run this flow")[0]).toHaveValue("orders.apply_status");
  });

  it("needs and produces: chips you can remove, and only real facts to add", async () => {
    const { store, edit } = setup("intent/todo.list");
    await select(store, FILE, "intent/todo.list/node/response");
    const panel = await screen.findByTestId("node-panel");
    const sec = within(panel).getByText("What it works with", { selector: "summary span" }).closest("details")!;
    expect(within(sec).getByText("todos")).toBeInTheDocument();
    fireEvent.click(within(sec).getByLabelText("Remove todos"));
    await act(async () => {});
    expect(edit.mock.calls[0]![0]).toEqual([{ op: "setField", file: FILE, path: "intent/todo.list/node/response/requires", value: "[user]" }]);
    const add = within(sec).getByLabelText("Add something it needs") as HTMLSelectElement;
    const options = [...add.options].map((o) => o.value).filter(Boolean);
    expect(options).toContain("input");
    expect(options).not.toContain("todos"); // already needed
    expect(options).not.toContain("response"); // its own result is not offered
  });

  it("if it goes wrong: retry shows its own settings only when switched on", async () => {
    const { store, edit } = setup("intent/todo.list");
    await select(store, FILE, "intent/todo.list/node/todos");
    const panel = await screen.findByTestId("node-panel");
    const sec = within(panel).getByText("If it goes wrong", { selector: "summary span" }).closest("details")!;
    expect(within(sec).queryByLabelText("Tries in total")).toBeNull();
    fireEvent.click(within(sec).getByLabelText("Try again when it fails"));
    await act(async () => {});
    expect(edit.mock.calls[0]![0]).toEqual([{ op: "addBlock", file: FILE, parent: "intent/todo.list/node/todos", type: "retry" }]);
  });

  it("process step: task for a person, with its choices as chips", async () => {
    const { store, edit } = setup("process/todo.workflow");
    await select(store, FILE, "process/todo.workflow/step/review");
    const panel = await screen.findByTestId("node-panel");
    const task = within(panel).getByText("Task for a person", { selector: "summary span" }).closest("details")!;
    expect(within(task).getByLabelText("Title")).toBeInTheDocument();
    expect(within(task).getByText("approve")).toBeInTheDocument();
    expect(within(task).getByText("request_changes")).toBeInTheDocument();
    fireEvent.click(within(task).getByLabelText("Remove approve"));
    await act(async () => {});
    expect(edit.mock.calls[0]![0]).toEqual([{ op: "setField", file: FILE, path: "process/todo.workflow/step/review/task/actions", value: "[request_changes]" }]);
    expect(within(panel).getByLabelText("Run this flow")).toBeInTheDocument();
  });

  it("connection: only the settings its kind reads, and the steps as pickers", async () => {
    const { store } = setup("process/todo.workflow");
    await select(store, FILE, "process/todo.workflow/edge/review_approved");
    const panel = await screen.findByTestId("node-panel");
    expect(within(panel).getByRole("heading", { level: 2 })).toHaveTextContent("review_approved");
    expect(within(panel).getByLabelText("Kind of connection")).toHaveValue("branch");
    expect(within(panel).getByLabelText("From")).toHaveValue("review");
    expect(within(panel).getByLabelText("To")).toHaveValue("mark_pending_approval");
    expect(within(panel).getByLabelText("Only when")).toHaveValue("result.action == 'approve'");
    expect(within(panel).queryByText(/About “/)).toBeNull(); // a plain branch has no extra settings
  });
});

describe("data flow (selection driven)", () => {
  const focus = () => (document.querySelector(".canvas-page") as HTMLElement).dataset.flowFocus!;

  it("is fully static by default: nothing selected, nothing moves", async () => {
    setup("process/todo.workflow");
    await screen.findByText("review", { selector: ".cv-titles strong" });
    expect(focus()).toBe("");
    expect(document.querySelector("animateMotion")).toBeNull();
    expect(document.querySelector(".cv-dots")).toBeNull();
  });

  it("selecting a step focuses only the connections touching it, incoming and outgoing", async () => {
    const { store } = setup("process/todo.workflow");
    await screen.findByText("review", { selector: ".cv-titles strong" });
    await select(store, FILE, "process/todo.workflow/step/approval");
    const ids = focus().split(",").sort();
    // approval: in from mark_pending_approval; out to done and rejected. Nothing else in the workflow.
    expect(ids).toEqual([
      "approval_approved:approval->done",
      "approval_rejected:approval->rejected",
      "to_approval:mark_pending_approval->approval",
    ]);
  });

  it("switching to another step moves the focus with it", async () => {
    const { store } = setup("process/todo.workflow");
    await screen.findByText("review", { selector: ".cv-titles strong" });
    await select(store, FILE, "process/todo.workflow/step/revise");
    expect(focus().split(",").sort()).toEqual(["review_changes:review->revise", "revise_cancel:revise->cancelled", "revise_resubmit:revise->review"]);
    await select(store, FILE, "process/todo.workflow/step/done");
    expect(focus()).toBe("approval_approved:approval->done");
  });

  it("deselecting stops it: clicking empty canvas, or Escape", async () => {
    const { store } = setup("process/todo.workflow");
    await screen.findByText("review", { selector: ".cv-titles strong" });
    await select(store, FILE, "process/todo.workflow/step/approval");
    expect(focus()).not.toBe("");
    await act(async () => { fireEvent.keyDown(window, { key: "Escape" }); });
    expect(focus()).toBe("");
    await select(store, FILE, "process/todo.workflow/step/approval");
    expect(focus()).not.toBe("");
    await act(async () => { fireEvent.click(document.querySelector(".react-flow__pane")!); });
    expect(focus()).toBe("");
  });

  it("an edge, or a step, that is merely hovered does not animate", async () => {
    setup("process/todo.workflow");
    await screen.findByText("review", { selector: ".cv-titles strong" });
    fireEvent.mouseEnter(document.querySelector('[data-id="approval"]')!);
    fireEvent.mouseOver(document.querySelector('[data-id="approval"]')!);
    expect(focus()).toBe("");
  });

  it("'Show data flow' off keeps everything still even with a step selected, and is remembered", async () => {
    const { store } = setup("process/todo.workflow");
    await screen.findByText("review", { selector: ".cv-titles strong" });
    const toggle = screen.getByLabelText("Show data flow");
    expect(toggle).toHaveAttribute("aria-pressed", "true");
    await select(store, FILE, "process/todo.workflow/step/approval");
    expect(focus()).not.toBe("");
    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-pressed", "false");
    expect(focus()).toBe("");
    expect(JSON.parse(localStorage.getItem("studio.canvas.flow.v1")!)).toEqual({ on: false, speed: "normal" });
    fireEvent.click(toggle);
    expect(focus()).not.toBe("");
  });

  it("remembers the speed, and only offers it while the flow is on", async () => {
    setup("process/todo.workflow");
    await screen.findByText("review", { selector: ".cv-titles strong" });
    const speed = screen.getByLabelText("Speed of the data flow") as HTMLSelectElement;
    expect(speed.value).toBe("normal");
    fireEvent.change(speed, { target: { value: "fast" } });
    expect(JSON.parse(localStorage.getItem("studio.canvas.flow.v1")!)).toEqual({ on: true, speed: "fast" });
    fireEvent.click(screen.getByLabelText("Show data flow"));
    expect(screen.queryByLabelText("Speed of the data flow")).toBeNull();
  });

  it("starts from what was saved", async () => {
    localStorage.setItem("studio.canvas.flow.v1", JSON.stringify({ on: false, speed: "slow" }));
    const { store } = setup("process/todo.workflow");
    await screen.findByText("review", { selector: ".cv-titles strong" });
    expect(screen.getByLabelText("Show data flow")).toHaveAttribute("aria-pressed", "false");
    await select(store, FILE, "process/todo.workflow/step/approval");
    expect(focus()).toBe("");
  });

  it("with animation off (reduced motion, or under test) the connected edges are still highlighted, but no dots travel", async () => {
    const { store } = setup("process/todo.workflow");
    await screen.findByText("review", { selector: ".cv-titles strong" });
    await select(store, FILE, "process/todo.workflow/step/approval");
    expect(document.querySelector(".canvas-page")).toHaveClass("cv-static"); // the shared animation flag
    expect(focus()).not.toBe(""); // static highlight of the connected edges
    expect(document.querySelector("animateMotion")).toBeNull();
    expect(document.querySelector(".cv-dots")).toBeNull();
  });
});

describe("CanvasPage: pipeline", () => {
  it("reorders a stage with the move buttons", async () => {
    const store = createStudioStore(fakeApi());
    const edit = vi.fn<(ops: Op[]) => void>();
    store.setState({
      meta: meta(["editor"]), schemas, catalog, draft: detail({ id: "d1", files: ["passport.bcl"] }), trees: { "passport.bcl": trees["passport.bcl"]! },
      nav: {}, diagnostics: [], edit,
    });
    render(
      <StudioProvider store={store}>
        <MemoryRouter initialEntries={[`/canvas?file=passport.bcl&path=${encodeURIComponent("pipeline/passport")}`]}>
          <Routes><Route path="/canvas" element={<CanvasPage />} /></Routes>
        </MemoryRouter>
      </StudioProvider>,
    );
    await screen.findByText("verification", { selector: ".cv-titles strong" });
    expect(screen.getByLabelText("Move application earlier")).toBeDisabled();
    expect(screen.getByLabelText("Move issuance later")).toBeDisabled();
    fireEvent.click(screen.getByLabelText("Move verification later"));
    await act(async () => {});
    const op = edit.mock.calls[0]![0][0] as Extract<Op, { op: "moveBlock" }>;
    expect(op).toMatchObject({ op: "moveBlock", path: "pipeline/passport/stage/verification" });
    const kids = find("passport.bcl", "pipeline", "passport").children!;
    expect(op.index).toBe(kids.findIndex((c) => c.path.endsWith("/stage/biometrics")) + 1);
  });
});
