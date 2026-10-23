# Dynamic definitions: node types, helpers and handlers in BCL + SPL

Add a node, a function or an endpoint without Go and without a rebuild. Definitions are checked and expanded when the application loads, before validation, so a bad one stops the start-up and never reaches a request.

All scripts run in the SPL sandbox, which has every host capability denied (files, network, exec, env, database). A definition computes over its inputs and returns a value; anything that reaches outside is a normal node.

## Node type

```bcl
node_type "risk.score" {
  description "Flag an order above a threshold"
  param "threshold" { type "number" default 100 }
  authz { permissions ["risk:score"] authorizer "rbac" }
  file "scripts/risk_score.spl"
}

intent "check" {
  response "out"
  node "risk" { uses "risk.score" requires [input] provides [out] config { threshold 50 } }
}
```

`scripts/risk_score.spl` sees the node's facts, `params`, `principal` and `tenant`:

```
{"high": input.total > params.threshold}
```

* `config` is checked against the `param` blocks: an unknown key, a wrong type or a missing required value fails the load.
* `authz` on the type is enforced on every node that uses it; a node cannot declare its own.
* A type cannot reuse the name of a built-in action. `kind` is `pure` (default) or `decision`.

## Helper

A pure function written as a BCL expression (`expr`) or as an SPL `file`.

```bcl
helper "vat" { params [amount, rate] expr "amount * rate / 100" }

node "tax" { uses "helper.call" requires [order] provides [tax] config { helper "vat" args { amount "order.total" rate "13" } } }
```

An argument is a BCL expression. A parameter without an entry is read from the fact of the same name.

In any expression a node evaluates, `helper("name", a, b)` calls a helper with positional arguments, in the order of its `params`:

```bcl
config { data { extract { total_with_tax "input.total + helper('vat', input.total, 13)" } } }
```

## Handler

An endpoint implemented by a script or a helper. It expands into one intent (`handler.<name>`) and one route.

```bcl
handler "quote" {
  method POST
  path "/api/quote"
  auth "bearer"
  authz { permissions ["quote:create"] authorizer "rbac" }
  rate_limit { limit 60 window "1m" }
  audit "audit_log"
  file "scripts/quote.spl"
}
```

A handler is closed by default. It must name an authenticator and an authz rule, or say `public true`. The body is capped at 1 MiB unless `max_body_bytes` says otherwise.

## Governing who may publish

```bcl
definition_policy {
  require_signature true
  publish_permission "definition:publish"   # default; "definition:publish:handler" limits a publisher to one kind
  max_timeout "2s"
  max_steps 1000000
  allow_public false
}

publisher "platform_team" {
  public_key "-----BEGIN PUBLIC KEY-----..."   # Ed25519 PEM
  roles ["definition_admin"]                    # permissions come from the role model
}
```

* A definition names its `publisher`. The publisher must be enabled and hold the publish permission through its permissions, roles or `*`.
* With `require_signature`, every definition needs a `signature`: the publisher's signature over the whole declaration (kind, path, authz, limits and the script's content, with a `file` read and signed by content). Changing any of it fails the load.
* Sign with `platform.SignDefinition(key, spec, fsys)`.
* Ceilings in the policy reject a definition that asks for more, and give one that asks for nothing the ceiling.
* Each loaded definition is logged with its publisher, signature state and source hash. `Platform.Definitions()` lists them with their description and parameters.

A working application is in [`examples/dynamic-definitions`](../examples/dynamic-definitions).
