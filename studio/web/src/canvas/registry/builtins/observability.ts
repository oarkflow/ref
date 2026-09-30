import { cfg, define, f, pick, section } from "./dsl";

const RECORD = (what: string) => [
  f.long("action", what, { required: true, rows: 1, help: "Can include values, e.g. deleted {{ item.name }}." }),
  f.long("subject", "About", { rows: 1, help: "Who or what it happened to." }),
  f.long("outcome", "How it went", { rows: 1, placeholder: "e.g. success" }),
  f.fact("detail_fact", "Attach", { help: "A fact with more detail." }),
  f.long("stream", "Filed under", { rows: 1, advanced: true }),
  f.text("table", "Kept in table", { advanced: true }),
  f.bool("migrate", "Create the table when it is missing", { advanced: true }),
  f.strs("redact", "Remove these values", { advanced: true }),
  f.strs("mask", "Hide part of these values", { advanced: true }),
];

define(["audit"], {
  label: "Record an audit entry", icon: "eye", category: "observability",
  blurb: "Writes a permanent note of who did what, for later review.",
  sections: [section("audit", "The entry", RECORD("What happened"))],
  summaryRows: (s) => pick(s, ["What", cfg("action"), { max: 34 }], ["About", cfg("subject"), { max: 28 }]),
});

define(["log"], {
  label: "Write a log line", icon: "eye", category: "observability",
  blurb: "Leaves a line in the log, for whoever is looking into a problem.",
  sections: [
    section("log", "The line", [
      f.long("action", "Message", { required: true, rows: 2, help: "Can include values, e.g. checked out {{ cart.id }}." }),
      f.long("subject", "About", { rows: 1 }),
      f.fact("detail_fact", "Attach", {}),
      f.strs("redact", "Remove these values", { advanced: true }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["Says", cfg("action"), { max: 38 }]),
});

define(["metric"], {
  label: "Record a metric", icon: "gauge", category: "observability",
  blurb: "Counts or measures something, to show on a dashboard.",
  sections: [
    section("metric", "The measurement", [
      f.text("name", "Name", { required: true, placeholder: "e.g. orders_created" }),
      f.select("kind", "What kind", [
        { value: "counter", label: "A count", hint: "Goes up each time." }, { value: "gauge", label: "A level", hint: "Goes up and down." },
        { value: "histogram", label: "A spread of values", hint: "For times and sizes." },
      ], { bare: false }),
      f.fact("value_fact", "Value", { help: "Leave empty to add one." }),
      f.kv("labels", "Labels", { keyLabel: "Label", valueLabel: "Value", help: "Split the measurement by these." }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["Measures", cfg("name"), { code: true }], ["As", cfg("kind")]),
});

define(["trace"], {
  label: "Trace", icon: "eye", category: "observability",
  blurb: "Marks a stretch of work so it shows up in traces.",
  sections: [
    section("trace", "The span", [
      f.text("name", "Name", { required: true, placeholder: "e.g. checkout" }),
      f.select("level", "Detail", [{ value: "info", label: "Normal" }, { value: "debug", label: "Detailed" }], { bare: false }),
      f.kv("attributes", "Notes", { keyLabel: "Name", valueLabel: "Value" }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["Span", cfg("name"), { code: true }]),
});
