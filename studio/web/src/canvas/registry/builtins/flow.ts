import { getPair, parseObjectList } from "../../objectList";
import { unquote } from "../../../lib/bcl";
import type { Row } from "../../look";
import type { StepFacts } from "../types";
import { cfg, define, f, onStep, pick, section } from "./dsl";

/** Case rows ("large -> orders.tier_large") read from a `[ { name ... } ]` list in the config. */
export function caseRows(s: StepFacts, key: string, labelKey: string, extra?: { defaultKey?: string }): Row[] {
  const raw = s.get(cfg(key));
  const items = raw ? parseObjectList(raw) : null;
  const rows: Row[] = [];
  for (const it of items ?? []) {
    const label = getPair(it, labelKey) ?? getPair(it, "name") ?? getPair(it, "label");
    const target = getPair(it, "intent");
    rows.push({ label: label ? (unquote(label) ?? label) : "case", value: target ? (unquote(target) ?? target) : "", code: !!target, dot: true });
  }
  const def = extra?.defaultKey ? s.text(cfg(extra.defaultKey)) : undefined;
  if (def) rows.push({ label: "otherwise", value: def, code: true, dot: true });
  return rows;
}

const RUN = { key: "intent", label: "Run this flow", kind: "intent" as const };

// -- choose a path ---------------------------------------------------------------

define(["branch"], {
  label: "Choose a path", icon: "branch", category: "flow",
  blurb: "Follows the first path whose condition is true, and runs that flow.",
  sections: [
    section("cases", "Choose a path", [
      f.cases("cases", {
        rowLabel: "Case", addLabel: "Add a case", seed: "name", required: true, empty: "No cases yet. Add one to choose between flows.",
        cols: [
          { key: "name", label: "Name", kind: "text", title: true },
          { key: "condition", label: "When", kind: "expr", placeholder: "e.g. input.amount > 500" },
          RUN,
        ],
      }),
      f.intent("default_intent", "Otherwise run"),
      f.bool("required", "Fail when nothing matches and there is no “otherwise”"),
    ]),
  ],
  summaryRows: (s) => caseRows(s, "cases", "name", { defaultKey: "default_intent" }),
});

define(["switch"], {
  label: "Match a value", icon: "branch", category: "flow",
  blurb: "Compares one value with each case and runs the flow of the case that matches.",
  sections: [
    section("cases", "Match a value", [
      f.expr("on", "Look at", { required: true, rows: 2, help: "The value that is compared with each case.", placeholder: "e.g. order.status" }),
      f.cases("cases", {
        rowLabel: "Case", addLabel: "Add a case", seed: "label", required: true, empty: "No cases yet. Add one to choose between flows.",
        cols: [{ key: "label", label: "When the value is", kind: "text", title: true }, RUN],
      }),
      f.intent("default_intent", "Otherwise run"),
      f.bool("required", "Fail when nothing matches and there is no “otherwise”"),
    ]),
  ],
  summaryRows: (s) => [...pick(s, ["Matches", cfg("on")]).map((r) => ({ ...r, value: `on ${r.value}` })), ...caseRows(s, "cases", "label", { defaultKey: "default_intent" })],
});

// -- run several at once -----------------------------------------------------------

const BRANCHES = (empty: string) =>
  f.cases("branches", { rowLabel: "Branch", addLabel: "Add a branch", seed: "name", required: true, empty, cols: [{ key: "name", label: "Name", kind: "text", title: true }, RUN] });

define(["parallel"], {
  label: "Do at the same time", icon: "loop", category: "flow",
  blurb: "Runs several flows together and waits for all of them.",
  sections: [
    section("branches", "What runs", [
      BRANCHES("No branches yet."),
      f.bool("fail_fast", "Stop the others as soon as one fails"),
      f.bool("continue_on_error", "Carry on when a branch fails", { advanced: true }),
      f.fact("input_fact", "Give every branch", { help: "The fact each flow starts from. Leave empty to pass everything.", advanced: true }),
    ]),
  ],
  summaryRows: (s) => caseRows(s, "branches", "name"),
});

define(["race"], {
  label: "First one wins", icon: "bolt", category: "flow",
  blurb: "Runs several flows together and keeps the answer of the first one to finish.",
  sections: [
    section("branches", "What runs", [
      BRANCHES("No branches yet."),
      f.bool("cancel_losers", "Cancel the branches that lose", { default: "true" }),
      f.dur("timeout", "Give up after", { placeholder: "e.g. 5s" }),
      f.fact("input_fact", "Give every branch", { advanced: true }),
    ]),
  ],
  summaryRows: (s) => [...caseRows(s, "branches", "name"), ...pick(s, ["Give up", cfg("timeout")])],
});

define(["quorum"], {
  label: "Wait for enough results", icon: "check", category: "flow",
  blurb: "Runs several flows together and carries on once enough of them have answered.",
  sections: [
    section("branches", "What runs", [
      BRANCHES("No branches yet."),
      f.int("quorum", "How many must answer", { required: true, placeholder: "e.g. 2" }),
      f.dur("timeout", "Give up after"),
      f.fact("input_fact", "Give every branch", { advanced: true }),
    ]),
  ],
  summaryRows: (s) => [...pick(s, ["Needs", cfg("quorum")]).map((r) => ({ ...r, value: `${r.value} answers` })), ...caseRows(s, "branches", "name")],
});

// -- repeat -----------------------------------------------------------------------------

const LOOP_FIELDS = (verb: string) => [
  f.fact("items_fact", "Go through the items in", { required: true, help: "One of the things this step needs." }),
  f.intent("intent", "Run this flow for each", { required: true }),
  f.int("concurrency", "At the same time", { placeholder: "1", help: "1 handles them one after another." }),
  f.int("max_items", "At most", { placeholder: "500" }),
  f.text("item_key", "Call each item", { placeholder: "item", help: "The name the flow sees the current item under.", advanced: true }),
  f.bool("continue_on_error", `Keep going when one ${verb} fails`),
];
const loopRows = (s: StepFacts): Row[] => {
  const rows = pick(s, ["For each", cfg("items_fact"), { code: true }], ["Runs", cfg("intent"), { code: true }]);
  const c = s.text(cfg("concurrency"));
  if (c && c !== "1") rows.push({ label: "At once", value: c });
  return rows;
};

define(["foreach"], {
  label: "For each item", icon: "loop", category: "flow",
  blurb: "Runs a flow once for every item in a list.",
  sections: [section("loop", "For each item", LOOP_FIELDS("item"))],
  summaryRows: loopRows,
});

define(["iterator"], {
  label: "Go through items", icon: "loop", category: "flow",
  blurb: "Walks through a list one item at a time, running a flow for each and collecting the answers.",
  sections: [section("loop", "Go through the items", LOOP_FIELDS("item"))],
  summaryRows: loopRows,
});

define(["batch"], {
  label: "Handle in batches", icon: "loop", category: "flow",
  blurb: "Works through a long list in chunks, running a flow for each.",
  sections: [section("loop", "In batches", LOOP_FIELDS("batch"))],
  summaryRows: loopRows,
});

define(["parallel_map"], {
  label: "Do for all items at once", icon: "loop", category: "flow",
  blurb: "Runs a flow for every item in a list, many at a time, and gathers the results.",
  sections: [
    section("loop", "For every item", [
      f.fact("items_fact", "Go through the items in", { required: true }),
      f.intent("intent", "Run this flow for each", { required: true }),
      f.int("concurrency", "At the same time", { placeholder: "e.g. 8", help: "How many run together." }),
      f.int("max_items", "At most", { placeholder: "500" }),
      f.bool("continue_on_error", "Keep going when one item fails"),
    ]),
  ],
  summaryRows: loopRows,
});

define(["loop"], {
  label: "Repeat until", icon: "loop", category: "flow",
  blurb: "Runs a flow again and again until a condition is true.",
  sections: [
    section("loop", "Repeat", [
      f.intent("intent", "Run this flow", { required: true }),
      f.cond("condition", "Stop when", { required: true, placeholder: "e.g. result.done == true" }),
      f.int("max_cycles", "At most", { placeholder: "e.g. 10", help: "A safety limit, so it can never repeat forever." }),
      f.dur("delay", "Wait between rounds", { placeholder: "e.g. 2s" }),
      f.fact("input_fact", "Start from", { advanced: true }),
      f.bool("required", "Fail when the limit is reached without stopping"),
    ]),
  ],
  summaryRows: (s) => pick(s, ["Runs", cfg("intent"), { code: true }], ["Until", cfg("condition")], ["At most", cfg("max_cycles")]),
});

// -- reliability wrappers -------------------------------------------------------------------

define(["retry"], {
  label: "Try again", icon: "loop", category: "flow",
  blurb: "Runs a flow, and runs it again if it fails.",
  sections: [
    section("retry", "The flow and how often", [
      f.intent("intent", "Run this flow", { required: true }),
      f.int("max_attempts", "Tries in total", { placeholder: "3" }),
      f.select("strategy", "Wait between tries", [
        { value: "fixed", label: "Same wait every time" }, { value: "linear", label: "A little longer each time" },
        { value: "exponential", label: "Doubling wait" }, { value: "exponential_jitter", label: "Doubling wait, with some randomness" },
        { value: "decorrelated_jitter", label: "Randomised wait" },
      ], { bare: false }),
      f.dur("initial_delay", "First wait", { placeholder: "200ms" }),
      f.dur("max_delay", "Longest wait", { placeholder: "5s" }),
      f.bool("jitter", "Add some randomness to the wait"),
      f.strs("retry_on", "Only try again after", {
        help: "Empty means temporary problems only.", advanced: true,
        options: ["invalid_input", "not_found", "conflict", "permission", "auth", "rate_limit", "unavailable", "timeout", "internal"].map((v) => ({ value: v, label: v })),
      }),
      f.fact("input_fact", "Start from", { advanced: true }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["Runs", cfg("intent"), { code: true }], ["Tries", cfg("max_attempts")]),
});

define(["timeout"], {
  label: "Time limit", icon: "clock", category: "flow",
  blurb: "Runs a flow, but gives up if it takes too long.",
  sections: [
    section("limit", "The time limit", [
      f.intent("intent", "Run this flow", { required: true }),
      f.dur("timeout", "Give up after", { required: true, placeholder: "e.g. 5s" }),
      f.text("fallback", "If it takes too long, use", { help: "A value to carry on with. Empty means fail.", advanced: true }),
      f.fact("input_fact", "Start from", { advanced: true }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["Runs", cfg("intent"), { code: true }], ["Within", cfg("timeout")]),
});

define(["fallback"], {
  label: "Fallback", icon: "undo", category: "flow",
  blurb: "Tries flows in order and keeps the first one that succeeds.",
  sections: [
    section("fallback", "Try in this order", [
      f.intents("intents", "Flows", { required: true, help: "The first is tried first; the next only if it fails." }),
      f.text("fallback", "If all of them fail, use", { help: "A value to carry on with. Empty means fail.", advanced: true }),
      f.fact("input_fact", "Start from", { advanced: true }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["Tries", cfg("intents")]),
});

define(["pipeline"], {
  label: "Pipeline", icon: "list", category: "flow",
  blurb: "Runs flows one after another, each starting from the last one's answer.",
  sections: [section("pipeline", "In this order", [f.intents("intents", "Flows", { required: true }), f.fact("input_fact", "Start from", { advanced: true })])],
  summaryRows: (s) => pick(s, ["Runs", cfg("intents")]),
});

define(["subflow"], {
  label: "Sub-flow", icon: "list", category: "flow",
  blurb: "Runs another flow as one step, and uses its answer.",
  sections: [
    section("subflow", "The other flow", [
      f.intent("intent", "Run this flow", { required: true }),
      f.fact("input_fact", "Give it", { help: "The fact it starts from." }),
      f.kv("input", "Or give it these values", { help: "Named values the flow receives.", keyLabel: "Name" }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["Runs", cfg("intent"), { code: true }]),
});

define(["join"], {
  label: "Join results", icon: "branch", category: "flow",
  blurb: "Waits for the steps it needs and gathers what they produced.",
  sections: [section("join", "Gathering", [f.bool("unwrap", "Pass on a single result as itself", { help: "When exactly one thing arrives, use it directly instead of wrapping it." })])],
  summaryRows: () => [],
});

export { onStep };
