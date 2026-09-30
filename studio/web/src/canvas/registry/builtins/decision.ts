import { getPair, parseObjectList } from "../../objectList";
import { unquote } from "../../../lib/bcl";
import type { Row } from "../../look";
import { cfg, define, defineAction, f, pick, section } from "./dsl";

const RULES = f.cases("rules", {
  rowLabel: "Rule", addLabel: "Add a rule", seed: "name", required: true, empty: "No rules yet. Without any rule, the default outcome is used.",
  cols: [
    { key: "name", label: "Name", kind: "text", title: true },
    { key: "condition", label: "When", kind: "expr", placeholder: "e.g. order.amount > 500" },
    { key: "outcome", label: "Then the outcome is", kind: "text", placeholder: "approve" },
    { key: "reason", label: "Because", kind: "text", placeholder: "shown in the audit trail" },
    { key: "score", label: "Score", kind: "number", placeholder: "optional" },
  ],
});

const TABLE_FIELDS = [
  RULES,
  f.text("default_outcome", "If nothing matches", { placeholder: "e.g. reject", help: "Leave empty to treat “no match” as a failure." }),
  f.text("default_reason", "Because", { placeholder: "optional" }),
  f.select("mode", "How rules are used", [
    { value: "first", label: "Stop at the first match", hint: "The usual choice." },
    { value: "all", label: "Collect every match" },
    { value: "highest_score", label: "Pick the highest score" },
  ], { bare: false, allowNone: true }),
  f.strs("deny_outcomes", "These outcomes deny", { help: "Outcomes that stop the request.", advanced: true }),
];

const tableRows = (s: Parameters<NonNullable<Parameters<typeof define>[1]["summaryRows"]>>[0]): Row[] => {
  const raw = s.get(cfg("rules"));
  const n = raw ? (parseObjectList(raw)?.length ?? 0) : 0;
  const rows: Row[] = n ? [{ label: "Rules", value: `${n} rule${n === 1 ? "" : "s"}` }] : [];
  const first = raw ? parseObjectList(raw)?.[0] : undefined;
  const o = first && getPair(first, "outcome");
  if (o) rows.push({ label: "First", value: unquote(o) ?? o });
  return rows.length ? rows : pick(s, ["Default", cfg("default_outcome")]);
};

const TABLE = {
  label: "Decision table", icon: "diamond", category: "decision",
  blurb: "Goes through a table of rules and answers with the outcome of the one that matches.",
  sections: [section("decision", "The rules", TABLE_FIELDS, { hint: "The first rule that matches wins" })],
  summaryRows: tableRows,
};
define(["decision_matrix"], TABLE);
// whatever the type, a step that runs the rule-table action gets the table form
defineAction(["decision.table"], { ...TABLE, id: "action:decision.table" });

define(["rules"], {
  label: "Rules", icon: "diamond", category: "decision",
  blurb: "Checks a set of rules and reports which of them apply.",
  sections: [section("decision", "The rules", TABLE_FIELDS, { hint: "The first rule that matches wins" })],
  summaryRows: tableRows,
});

define(["decision"], {
  label: "Allow or deny", icon: "diamond", category: "decision",
  blurb: "Lets the request carry on only when a condition is true.",
  sections: [
    section("decision", "The decision", [
      f.expr("expression", "Allow when", { required: true, rows: 3, help: "Everything that follows only runs when this is true.", placeholder: "e.g. principal.role == 'admin'" }),
      f.text("message", "If denied, tell the caller", { placeholder: "You are not allowed to do this." }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["Allow if", cfg("expression"), { max: 40 }]),
});

define(["condition"], {
  label: "Check a condition", icon: "diamond", category: "decision",
  blurb: "Stops the request unless something you describe is true.",
  sections: [
    section("decision", "The check", [
      f.cond("expression", "Carry on only when", { required: true, lead: "Carry on only when" }),
      f.text("message", "Otherwise, tell the caller", { placeholder: "That is not allowed." }),
    ]),
  ],
  summaryRows: (s) => pick(s, ["Only if", cfg("expression"), { max: 40 }]),
});
