# Extending intents: add, replace and remove nodes

An intent is a graph of nodes wired by **facts**: a node `requires` facts and `provides` facts, and runs once everything it requires exists. Nothing is wired by position, which is why a flow can be changed from outside.

An `extend` block changes an intent that is declared somewhere else. Use it to add a validation rule, an audit log, a metric or a notification to a flow you do not own, and to take one out again by deleting one file. Extensions are applied when the application loads, before validation, so a mistake fails the load and never reaches a request.

```bcl
extend "audit_sends" {
  intent "sms.prepare"                       # the intent to change

  node "audit" {                             # added
    uses "trace.span" kind pure requires [message] provides [audit]
    config { name "sms.prepared" }
  }
  feed ["prepared"]                          # "prepared" waits for "audit"
}
```

| Attribute | Meaning |
|---|---|
| `intent` | Required. The intent to change. |
| `node "name" { … }` | Adds a node. It is an error if the intent already has a node of that name, unless it is listed in `replace`. |
| `replace ["name"]` | The extension's node of that name takes the place of the existing one. Give it the same `provides`. |
| `remove ["name"]` | Deletes an existing node. Its facts are also taken out of other nodes' `requires`, so a node that only waited for it carries on. A node that **reads** a removed fact fails the load, because nothing provides it. |
| `feed ["name"]` | Existing nodes that must wait for every fact the added nodes provide. This is how an added node is placed **in front of** an existing one. Without `feed`, an added node still runs when something requires its facts, or when it is an effect. |
| `response "fact"` | Replaces the intent's response fact. |
| `disabled true` | Switches the extension off without deleting it. |

## Placing a node

"In between A and B" means: **require what A provides, and `feed` B.**

```
 message ──► [your node] ──► prepared
 (A)                          (B)
```

To change the request's outcome, make the node fail. `rules.decide` with `fail_on ["deny"]` stops the intent with the rule's `code`, `status` and `reason`. To observe only, use `trace.span`, `metric.emit` or `audit.record`, which never fail the flow.

## Rules

* Extensions from every file are applied in file order (`config/` files load by name, so use a numeric prefix: `90_…`). Each `extend` block needs a unique name.
* Several extensions may change one intent. Each sees the result of the ones before it.
* An extension can only name an intent, node or fact that exists. Unknown names, duplicate nodes and unmet requirements are load errors with the extension's name in the message.
* `extend` changes graph structure only. Settings of the existing nodes (config, resources) change by `replace`.

## Worked examples

### A business rule on validation

`rules/blocklist.bcl` holds the rule, `config/90_blocklist.bcl` wires it in. Neither existing file changes.

```bcl
extend "blocklist" {
  intent "sms.prepare"
  node "blocklist" {
    uses "rules.decide" resource "policy" kind read requires [message] provides [blocklist]
    config { definition "blocklist" decision "blocklist" fail_on ["deny"] }
  }
  feed ["prepared"]
}
```

### Logging and metrics after a message is accepted

```bcl
extend "accepted_metrics" {
  intent "settle.accept"
  node "count" {
    uses "metric.emit" kind pure requires [input] provides [counted]
    config { name "sms_accepted_total" labels { provider "{{ input.provider }}" } }
  }
  feed ["settled"]
}
```

### Notify on failure

`alerts` is any channel resource (mail, HTTP, queue) declared in `config/`.

```bcl
extend "notify_failed" {
  intent "settle.fail"
  node "tell" {
    uses "notify.send" kind effect requires [input, applied] provides [told]
    config { channel "alerts" target "{{ input.user_id }}" subject "SMS failed" body "message {{ input.id }} failed: {{ input.code }}" }
  }
  feed ["settled"]
}
```

### Replace or remove

```bcl
extend "no_daily_limit" { intent "sms.prepare" remove ["valid"] }

extend "stricter_price" {
  intent "sms.prepare"
  replace ["rate"]
  node "rate" {
    uses "rules.decide" resource "policy" kind read requires [message, user] provides [rate]
    config { definition "pricing_strict" decision "price" fail_on ["deny"] }
  }
}
```

The tests in `contrib/messaging/e2e/extend_test.go` do exactly these things to a copy of the SMS gateway.
