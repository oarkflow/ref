import { cfg, define, f, pick, section } from "./dsl";

define(["action"], {
  label: "Runs an action", icon: "bolt", category: "compute",
  blurb: "Runs one of the platform's actions. Pick the action above; its settings appear below.",
  sections: [],
  summaryRows: (s) => (s.uses ? [{ label: "Action", value: s.uses, code: true }] : []),
});

define(["custom"], {
  label: "Custom action", icon: "wrench", category: "compute",
  blurb: "An action your own code registered. Its settings come from what that action declares.",
  sections: [],
  summaryRows: (s) => (s.uses ? [{ label: "Action", value: s.uses, code: true }] : []),
});

define(["constant"], {
  label: "Fixed value", icon: "flag", category: "compute",
  blurb: "Publishes a value that never changes.",
  sections: [section("value", "The value", [f.code("value", "Value", { required: true, language: "text", rows: 3, help: "Written as text, a number, true/false or a list." })])],
  summaryRows: (s) => pick(s, ["Value", cfg("value"), { code: true, max: 34 }]),
});

define(["script"], {
  label: "Calculate", icon: "code", category: "compute",
  blurb: "Works something out from the facts this step needs.",
  sections: [
    section("script", "The calculation", [
      f.expr("expression", "Work out", { required: true, rows: 4, placeholder: "e.g. order.amount * order.quantity", help: "Uses the facts this step needs. The result becomes what it produces." }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["Formula", cfg("expression"), { code: true, max: 36 }]),
});

define(["template"], {
  label: "Fill in a template", icon: "doc", category: "compute",
  blurb: "Builds text by putting values into a template.",
  sections: [section("template", "The template", [f.long("template", "Template", { required: true, rows: 5, help: "Write {{ name }} where a value should go." })])],
  summaryRows: (s) => pick(s, ["Template", cfg("template"), { max: 38 }]),
});

define(["transform"], {
  label: "Reshape data", icon: "code", category: "compute",
  blurb: "Builds a new shape out of the facts this step needs.",
  sections: [
    section("transform", "The new shape", [
      f.fact("source_fact", "Start from", { help: "Leave empty to use everything this step needs." }),
      f.kv("data", "Fields", { required: true, keyLabel: "Field", valueLabel: "Value", help: "Each field is worked out from the facts, e.g. total = order.amount * 2." }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["From", cfg("source_fact"), { code: true }]),
});

define(["validate"], {
  label: "Check the data", icon: "shield", category: "compute",
  blurb: "Checks that the data has the right shape before it goes any further.",
  sections: [
    section("validate", "What to check", [
      f.select("shape", "It must match the shape", { from: "shapes" }, { required: true, bare: false, help: "One of your data shapes." }),
      f.fact("source_fact", "Check", { help: "Leave empty to check the request." }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["Shape", cfg("shape"), { code: true }], ["Of", cfg("source_fact"), { code: true }]),
});
