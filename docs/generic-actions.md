# Rules, templates, SQL transactions and the application server

Generic building blocks added for applications that keep **every decision in BCL** and no logic in Go. The SMS gateway (`examples/smsgateway`) is built only from these.

## Runtime rules (`rules.engine`)

`rules.decide` and `rules.rank` resolve the definition on every call, so publishing a new version takes effect on the next evaluation. These actions let an operator manage definitions while the application runs:

| Action | |
|---|---|
| `rules.catalog` | Every definition: name, active version, whether it was edited at runtime, whether it has a file to reset to, its decisions and rankings. |
| `rules.source` | The source of a definition's active version. |
| `rules.save` | Check and publish edited source. An invalid source is refused (`422`, with the engine's diagnostics) and the active version is unchanged. |
| `rules.reset` | Publish the definition's file again and forget the edit. |
| `rules.try` | Evaluate a decision against given facts and return the result with its trace. Changes nothing. |

`rules.source` also lists the facts a definition reads, found in its `when` blocks, with a kind (text, number, bool) and a sample inferred from the comparisons, so a console can offer a form instead of a JSON box.

Each takes its inputs from facts (`name_fact`, `source_fact`, `decision_fact`, `facts_fact`; defaults `input.name`, `input.source`, `input.decision`, `input.facts`). Edits are kept in memory unless the engine names a database: `overrides "db"` (and optionally `overrides_table`, default `rule_overrides`) stores them and publishes them again over the files whenever the engine opens; one that no longer compiles is logged and skipped so the engine still starts. Guard these routes with an authorization role.

## Bulk data: `data.parse_csv`, `data.augment`, `data.concat`, `text.render`, `phone.validate`

* `data.parse_csv` turns CSV text into a list of objects keyed by the header (or `column1`, `column2`, ...). The delimiter is detected (comma, semicolon, tab), quotes are honoured, blank lines are dropped, `max_rows` bounds it.
* `data.augment` adds computed fields to every element of a list: `fields { name "expression" }`, each seeing `item`, `index` and the node's facts, evaluated in name order. A scalar element becomes an object holding only the new fields; an object result is stored as a copy. Use it to number rows, stamp a campaign id on each, or wrap a list of strings.
* `data.concat` joins several lists (a missing fact counts as empty), so a request may bring its rows in any of several forms.
* `text.render` renders text that holds `{{ placeholder }}`s taken from a fact over another fact's fields; an unknown placeholder renders empty.
* `phone.validate` (in `contrib/messaging`, using `github.com/oarkflow/phone`) validates numbers: `value_fact` gives one result, `values_fact` a list, and `rows_fact` takes rows and returns each with `n`, `phone`, `phone_e164`, `phone_region`, `phone_type`, `state` (`pending` or `skipped`), `reason` and `fields` (a copy of the original row). `default_region` (or `region_fact`) is the country for numbers written without one; `allow_types` limits the number types accepted.

## `rules.check`, `eligibility_extra` and `validate.regex`

* `rules.check` validates source without publishing it and answers `{ valid, diagnostics: [{ severity, message, code, line, column }] }`, so an editor can mark the problem; `fail_on_invalid true` fails the intent (422) with the first diagnostic instead.
* `rules.rank` takes `eligibility_extra ["definition/decision", …]`: more per-candidate decisions, run after `eligibility`, usually in a definition generated at runtime. A deny rejects the candidate; an allow that matched a rule (not the default) merges its attributes (`priority`, `exclusive`, `tier`) into the candidate's facts. A decision name may hold `{id}`, the candidate's id: `"custom/for_{id}"` runs the decision written for that candidate alone (a candidate without one is not affected), so a large rule set is searched per candidate rather than in full (5,000 rules over 20 providers: about 4 ms a route, against about 40 ms in one decision).
* **Ranking scores.** A `ranking` orders candidates by `priority_path` plus, for each `score` block, `weight × metric`. A block that carries bounds, `score "cost" { metric "provider.cost" weight -500 normalize [0, 0.2] }` (a list; the rules library reads `normalize` but does not apply it), contributes `weight × clamp((metric − min) / (max − min), 0, 1)`: the weight is what the metric is worth at its best, whatever its unit. Each candidate is scored once and the chain is the stable order by score, so equal scores keep the order the candidates arrived in (order them in the query, for example `ORDER BY id`).
* `validate.expression` takes `message_expression`, an expression for the failure message that can name what failed (evaluated only on failure; `message` is the fallback).
* `validate.regex` fails (422) unless each listed fact is a valid regular expression; an empty value is skipped. Use it before a pattern is stored: the rules engine does not check patterns when it publishes a definition, so an invalid one would fail on the first evaluation.

## `crypto.seal` and `crypto.unseal`

Authenticated encryption (AES-256-GCM) of a value with a key from configuration, for secrets an application stores itself, such as a provider's credentials in its own table. `crypto.seal` takes any value (text, or a structure as JSON) from `value_fact` and publishes `v1.<base64>`; `crypto.unseal` opens it (`json true` parses the result; an empty value gives an empty result). `key` is any string of 16 or more characters; keep it in `env("NAME", …)`. A different key cannot open a sealed value. There is no key rotation: re-seal under the new key.

## `smpp.submit` accounts (contrib/messaging)

`smpp.submit` takes optional `system_id` and `password` templates. When `system_id` renders non-empty, the message goes out on a bind made with those credentials; the `service.smpp` resource keeps one session per account in use and rebinds an account's session after a fault. Empty uses the resource's own credentials.

## `rules.decide` and `rules.rank` (resource `rules.engine`)

A `rules.engine` resource loads every definition under a directory (`dir`) and evaluates the compiled decision tables directly.

```bcl
resource "policy" { kind "rules.engine" config { dir "rules" environment "production" strict_validation true } }

node "valid" {
  uses "rules.decide" resource "policy" kind read requires [message, user] provides [valid]
  config { definition "validate" decision "validate" fail_on ["deny"] }
}
```

* **Facts.** Every required fact is visible to the rules under its own name: `message.to`, `user.status`. Use `input_data { extract { digits "recipient" } pick ["digits"] }` to hand a rule a differently named fact.
* **Result.** `{ effect, reason, rule, … }` plus the row's outcome `attributes` flattened in (`code`, `status`, anything you declare). A table with `default allow` always answers.
* **`fail_on ["deny"]`.** A `deny` stops the intent. The failure carries the row's `code`, `status` and `reason`, so the rule alone chooses the HTTP answer.
* **`rules.rank`.** Takes candidates (`candidates_fact`, a list of plain rows, for example a SQL result), runs `eligibility` (a decision table, run once per candidate with the row as the fact `provider`) and scores the eligible ones with the `ranking` named by `ranking` (or `ranking_fact`, a fact holding the name). It returns `{ chain, rejected }`: the ordered winners, and every rejected candidate with the rule and reason that rejected it.

## `template.render`

Renders a named template through the host's template engine (`LoadOptions.Templates`; `serve` supplies the SPL engine over `templates/`).

```bcl
node "rendered" {
  uses "template.render" kind pure requires [template, input] provides [rendered]
  config { template_fact "template.file" vars_fact "input.vars" capture true }
}
```

`capture true` publishes `{ ok:false, error }` instead of failing the intent, so a rule can decide what a missing template means. Message bodies are SPL files: `Your ${brand} code is ${code}.`

## `database.transaction`

Several statements in one transaction. Each statement:

| | |
|---|---|
| `name` | Its result is the number of rows affected (exec) or the rows (query), visible to later statements and to `when`. A name must not equal a fact name. |
| `when` | An expression over facts and earlier statement results. A skipped statement's result is `nil`, so test `x != nil and x > 0`. |
| `require_affected` / `require_rows` | Roll the transaction back and fail with `failure { code status message }` if nothing changed or nothing was found. |

This is the compare-and-set pattern the gateway uses: `UPDATE … WHERE state = 'x' AND seq = $n` as the first statement; every later statement runs `when "first > 0"`. A stale or duplicate job changes nothing.

## `service.http` `capture`

`capture true` turns a non-2xx answer, a timeout or a connection failure into `{ ok:false, status, body, error { kind, status, message, retry_after_s } }` instead of a failed node. Rules then classify the failure.

## `queue.publish` / `queue.publish_delayed`

`job_type` is a template (`sms.dispatch.{{ input.provider }}`), choosing the queue per message. `delay_fact` names a fact holding a delay in seconds or a duration.

## `auth.api_key_sql`

Authenticates `Authorization: Bearer <key>` against a SQL `query` that receives the SHA-256 of the key and returns the principal (`id`, `tenant`, `name`). `auth.api_key` accepts a `header` option for another header, such as `X-Webhook-Secret`.

## `serve` and `cmd/ref`

`serve.Start`/`serve.Run` run an application **directory**: `config/*.bcl` (loaded by name; `bcl/*.bcl` or a single `app.bcl` also work), `rules/`, `templates/` (SPL pages, layouts, components), `static/`. The command is `cmd/ref` (its own module, with every driver in the repository): `make run dir=./examples/smsgateway` from the root, or `ref <dir>`.

## Extending intents

See [extending-intents.md](extending-intents.md).
