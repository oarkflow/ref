# Step-type registry

A *descriptor* says what one kind of step looks like on the canvas and what its
settings form contains. One generic renderer (`SettingsPanel`) draws every
descriptor, so a new kind of step is data, not UI code.

## Add a step type (about 20 lines)

```ts
import { registerNodeType } from "./registry";   // from inside the canvas folder
import { f, section } from "./registry/builtins/dsl";

registerNodeType({
  id: "type:ship",                     // unique; registering the same id again replaces it
  label: "Ship an order", icon: "send", category: "messaging",   // category = colour family
  blurb: "Hands an order to a carrier.",
  priority: 100,                       // built-in types use 100, action overrides 150, the generic fallback 0
  match: (typeName, actionName) => typeName === "ship" || actionName === "acme.ship",
  sections: [
    section("ship", "Shipping", [
      f.text("carrier", "Carrier", { required: true }),       // config.carrier
      f.dur("wait", "Wait up to"),
      f.cond("condition", "Only when", { lead: "Ship only when" }),
      f.kv("labels", "Labels"),
    ]),
  ],
  summaryRows: (s) => [{ label: "Carrier", value: s.text(["config", "carrier"]) ?? "" }],   // shown on the card
});
```

That is all: the card, the form, the "to do" list of missing required settings,
and the raw fallback for settings the form does not know come for free.

## Fields

`f.text` `f.long` `f.int` `f.num` `f.bool` `f.dur` `f.expr` (offers the facts the step reads) `f.code`
`f.select` (static options, or `{ from: "nodeTypes" | "actions" | "intents" | "processes" | "shapes" | "edgeTypes" | "resources" }`)
`f.kv` (map) `f.list` `f.strs` `f.fact` / `f.facts` `f.resource` `f.intent` / `f.intents` `f.route` `f.secret`
`f.cases` (rows of `{ ... }`) `f.cond` (visual condition builder) `f.custom` (your own React component).

Every field takes `help`, `placeholder`, `default`, `required`, `advanced` (folds under *More settings*),
`visibleWhen(values)` and `validate(raw, values)` (return a plain-language problem or null).
`at` says where the value lives: `["config", "url"]` is the default, `["timeout"]` a field of the step itself.

## Where descriptors come from

1. **Registered** descriptors, highest `priority` first (later wins a tie). `defineAction([...])` in
   `builtins/dsl.ts` registers one for whatever step runs a given action, whatever its type.
2. **The generic fallback**, for a type nobody described. Its form is generated from the catalog's
   `ActionInfo.config` (name, type, required, default, summary), so an action the backend adds today has a
   usable form and card tomorrow with no frontend change. A registered descriptor overrides it.
3. After a descriptor's own sections, any config key of the step's action that the descriptor did not list is
   added under *Settings*, and anything the catalog does not know at all is shown as written
   (*More settings*). Nothing present in the file is ever hidden or dropped.

## Conditions

`f.cond` edits a condition as checks ("total is over 1000, and role is admin"). `conditions.ts` parses a formula
into checks and compiles them back; a formula it cannot represent (calls, brackets, mixed `&&` and `||`) stays a
formula and is never rewritten. `look.ts#friendlyCondition` phrases conditions for labels ("If approved").

## Tests

`registry.test.ts` (matching, priority, every catalog type and action covered, generic fallback, card rows),
`conditions.test.ts` (round trips), `settings.test.tsx` (every field kind produces the right BCL).
