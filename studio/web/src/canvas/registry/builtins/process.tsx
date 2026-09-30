// Steps that involve people and waiting. As a step of a process these are the
// heart of the workflow; as a node of a flow (where they cannot wait) the same
// types complete, list or reassign tasks. Sections are scoped accordingly.
import { createElement } from "react";
import { cfg, define, f, onStep, pick, section } from "./dsl";
import type { FieldDef } from "../types";

const note = (id: string, text: string): FieldDef => f.custom(id, "", () => createElement("p", { className: "cv-note" }, text), { at: ["config", id] });
const T = (key: string): readonly [string, string] => ["task", key];

const TASK = (defaultChoices: string) => [
  f.text("title", "Title", { at: T("title"), placeholder: "e.g. Review request #{{ run.input.id }}" }),
  f.long("instructions", "What they need to do", { at: T("instructions"), rows: 3 }),
  f.text("role", "Anyone with the role", { at: T("role"), placeholder: "e.g. reviewer" }),
  f.text("assignee", "…or this person", { at: T("assignee"), placeholder: "{{ run.input.owner_id }}" }),
  f.strs("actions", "Their choices", { at: T("actions"), help: `Each one can lead to a different next step. Usually: ${defaultChoices}.`, addLabel: "Add a choice" }),
  f.dur("due", "Due after", { at: T("due"), placeholder: "2d" }),
  f.text("form_schema", "Form to fill in", { at: T("form_schema"), placeholder: "name of a form" }),
];

const rowsForTask = (s: { text(at: readonly [string, string]): string | undefined }) => {
  const who = s.text(["task", "role"]) ?? s.text(["task", "assignee"]);
  return [{ label: "Waits for", value: who ? `a person (${who})` : "a person" }];
};

define(["approval"], {
  label: "Approval", icon: "user", category: "process",
  blurb: "Waits for a person to approve, reject or ask for changes.",
  sections: [
    section("task", "Task for a person", TASK("approve, reject"), { scope: "process" }),
    section("complete", "Which task", [
      f.fact("task_id_fact", "The task to complete", { required: true }), f.fact("action_fact", "Their choice"), f.fact("payload_fact", "Attach", { advanced: true }),
      f.select("process", "Of the process", { from: "processes" }, { bare: false, advanced: true }),
    ], { scope: "flow" }),
  ],
  summaryRows: (s) => rowsForTask({ text: (a) => s.text(a) }),
});

define(["human_task"], {
  label: "Task for a person", icon: "user", category: "process",
  blurb: "Gives a person something to do and waits until they finish.",
  sections: [
    section("task", "Task for a person", TASK("done, cannot do"), { scope: "process" }),
    section("list", "Which tasks", [
      f.select("process", "Of the process", { from: "processes" }, { bare: false }),
      f.text("scope", "Whose", { placeholder: "e.g. mine, team" }), f.text("status", "That are", { placeholder: "e.g. open" }),
      f.text("queue", "In the queue"), f.bool("overdue", "Only overdue ones"), f.int("limit", "At most", { placeholder: "e.g. 50" }),
    ], { scope: "flow" }),
  ],
  summaryRows: (s) => rowsForTask({ text: (a) => s.text(a) }),
});

define(["manual_review"], {
  label: "Manual review", icon: "user", category: "process",
  blurb: "Holds the work until a person has looked it over and decided.",
  sections: [
    section("task", "Review by a person", TASK("approve, reject, request changes"), { scope: "process" }),
    section("list", "Which reviews", [
      f.select("process", "Of the process", { from: "processes" }, { bare: false }),
      f.text("scope", "Whose"), f.text("status", "That are"), f.text("queue", "In the queue"), f.bool("overdue", "Only overdue ones"), f.int("limit", "At most"),
    ], { scope: "flow" }),
  ],
  summaryRows: (s) => rowsForTask({ text: (a) => s.text(a) }),
});

define(["form"], {
  label: "Collect information", icon: "doc", category: "process",
  blurb: "Asks a person to fill in a form, and waits for them.",
  sections: [
    section("task", "The form", [
      f.text("title", "Title", { at: T("title"), placeholder: "e.g. Tell us about your order" }),
      f.text("form_schema", "Form to fill in", { at: T("form_schema"), required: true, placeholder: "name of a form" }),
      f.text("role", "Anyone with the role", { at: T("role") }), f.text("assignee", "…or this person", { at: T("assignee") }),
      f.dur("due", "Due after", { at: T("due"), placeholder: "2d" }),
      f.long("instructions", "What they need to know", { at: T("instructions"), rows: 3, advanced: true }),
      f.strs("actions", "Their choices", { at: T("actions"), advanced: true }),
    ], { scope: "process" }),
    section("complete", "Which task", [
      f.fact("task_id_fact", "The task to complete", { required: true }), f.fact("action_fact", "Their choice"), f.fact("payload_fact", "The answers"),
    ], { scope: "flow" }),
  ],
  summaryRows: (s) => {
    const form = s.text(["task", "form_schema"]);
    return [{ label: "Waits for", value: "a person" }, ...(form ? [{ label: "Form", value: form, code: true }] : [])];
  },
});

define(["escalation"], {
  label: "Escalate", icon: "bell", category: "process",
  blurb: "Passes work to someone else when it has waited too long.",
  sections: [
    section("task", "Who it goes to", [
      f.text("role", "Anyone with the role", { at: T("role"), placeholder: "e.g. manager" }), f.text("assignee", "…or this person", { at: T("assignee") }),
      f.text("title", "Title", { at: T("title") }), f.long("instructions", "What they need to do", { at: T("instructions"), rows: 3 }),
      f.dur("due", "Due after", { at: T("due") }),
    ], { scope: "process" }),
    section("reassign", "Which task", [
      f.fact("task_id_fact", "The task", { required: true }), f.fact("assignee_fact", "Give it to", { required: true }),
      f.select("process", "Of the process", { from: "processes" }, { bare: false, advanced: true }),
    ], { scope: "flow" }),
  ],
  summaryRows: (s) => {
    const who = s.text(["task", "role"]) ?? s.text(["task", "assignee"]);
    return who ? [{ label: "Goes to", value: who }] : [];
  },
});

// -- waiting (the wait itself is set on the connection that leaves the step) -----------

const WAIT = (label: string, blurb: string, hint: string) => ({
  label, icon: "clock", category: "process", blurb, scope: "process" as const,
  sections: [section("wait", "How long it waits", [note("wait-note", hint), f.dur("timeout", "Give up after", { at: onStep("timeout"), placeholder: "e.g. 7d", help: "The longest this step may take before it counts as failed.", advanced: true })], { scope: "process" as const })],
  summaryRows: () => [{ label: "Waits", value: "until it is time" }],
  actionConfig: false,
});

define(["wait"], WAIT("Wait", "Pauses the run until something happens.", "Set what it waits for on the connection that leaves this step (a time, an event or a decision)."));
define(["delay"], WAIT("Wait for a while", "Pauses the run for a set time.", "Set the length of the wait on the connection that leaves this step (“After a delay”)."));
define(["timer"], WAIT("Wait until a time", "Pauses the run until a moment in time.", "Set the time on the connection that leaves this step (“After a delay”)."));
define(["wait_event"], WAIT("Wait for an event", "Pauses the run until something outside happens.", "Set the event to wait for, and how to recognise it, on the connection that leaves this step (“When an event arrives”)."));
define(["external_task"], WAIT("Wait for an outside system", "Hands work to another system and pauses until it reports back.", "Set what it waits for on the connection that leaves this step, and give the other system the id to report back with."));

define(["compensation"], {
  label: "Undo an earlier step", icon: "undo", category: "process", scope: "process",
  blurb: "Takes back the effect of an earlier step when something later goes wrong.",
  sections: [
    section("undo", "What undoes it", [
      f.intent("compensate", "Undo with this flow", { at: onStep("compensate"), required: true, help: "Runs if a later step fails for good." }),
    ], { scope: "process" }),
  ],
  summaryRows: (s) => pick(s, ["Undoes with", onStep("compensate"), { code: true }]),
  actionConfig: false,
});

define(["subprocess"], {
  label: "Run a sub-process", icon: "play", category: "process", scope: "process",
  blurb: "Starts another process as part of this one and waits for it to finish.",
  sections: [section("sub", "The other process", [f.select("process", "Run this process", { from: "processes" }, { at: onStep("process"), bare: false, required: true })], { scope: "process" })],
  summaryRows: (s) => pick(s, ["Runs", onStep("process"), { code: true }]),
  actionConfig: false,
});

define(["process"], {
  label: "Start a process", icon: "play", category: "process",
  blurb: "Starts a process and carries on, or waits for it.",
  sections: [
    section("start", "The process", [
      f.select("process", "Start this process", { from: "processes" }, { bare: false, required: true }),
      f.fact("input_fact", "Give it", {}), f.bool("wait", "Wait for it to finish"),
      f.fact("idempotency_fact", "Do not start twice for", { advanced: true }), f.fact("correlation_fact", "Correlate with", { advanced: true }),
    ], { scope: "flow" }),
    section("sub", "The other process", [f.select("process", "Run this process", { from: "processes" }, { at: onStep("process"), bare: false, required: true })], { scope: "process" }),
  ],
  summaryRows: (s) => pick(s, ["Starts", cfg("process"), { code: true }], ["Starts", onStep("process"), { code: true }]),
});

define(["workflow"], {
  label: "Start a workflow", icon: "play", category: "process",
  blurb: "Starts a named workflow.",
  sections: [
    section("start", "The workflow", [
      f.long("workflow", "Workflow", { required: true, rows: 1, help: "Its name. Can include values." }),
      f.fact("input_fact", "Give it"), f.long("idempotency_key", "Do not start twice for", { rows: 1, advanced: true }),
    ], { scope: "flow" }),
    section("sub", "The other process", [f.select("process", "Run this process", { from: "processes" }, { at: onStep("process"), bare: false })], { scope: "process" }),
  ],
  summaryRows: (s) => pick(s, ["Starts", cfg("workflow"), { code: true }], ["Starts", onStep("process"), { code: true }]),
});
