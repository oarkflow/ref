import { cfg, define, f, pick, section } from "./dsl";

define(["response"], {
  label: "Send the answer", icon: "send", category: "terminal",
  blurb: "Gathers what the earlier steps produced into the answer the caller receives.",
  sections: [section("response", "The answer", [f.bool("unwrap", "When there is only one result, send it as it is", { help: "Otherwise the answer is a set of named results." })])],
  summaryRows: () => [],
});

define(["terminal"], {
  label: "Stop here", icon: "stop", category: "terminal",
  blurb: "Ends the flow at this point, optionally with a value.",
  sections: [
    section("stop", "Stopping", [
      f.cond("condition", "Stop only when", { lead: "Stop only when", help: "Leave empty to always stop." }),
      f.fact("value_fact", "End with"),
      f.text("value", "…or with this value", { advanced: true }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["When", cfg("condition"), { max: 34 }], ["Ends with", cfg("value_fact"), { code: true }]),
});

define(["noop"], {
  label: "Do nothing", icon: "flag", category: "terminal",
  blurb: "A step that does nothing. Useful as a placeholder while you build.",
  sections: [],
  summaryRows: () => [],
  actionConfig: false,
});
