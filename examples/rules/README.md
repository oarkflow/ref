# Routing rules, end to end

One message goes in; the chain of providers it would be sent through comes out, with
the reason for every provider that was left out. There is no Go in this directory and
nothing is sent: the whole decision is the rules in `rules/`, and `config/` only gathers
the facts they read. It is the routing of [`../smsgateway`](../smsgateway) on its own,
small enough to read in one sitting.

```
make run dir=./examples/rules          # http://localhost:8080
```

```
POST /route   { "account": "bob", "to": "+977 985-123-4567", "text": "hello" }
```

```json
{ "to": "9779851234567", "country": "NP", "segments": 1, "account": "bob", "tenant": "acme",
  "objective": "route_balanced", "first": "np_premium",
  "order": ["np_premium", "np_telecom", "global_fallback"],
  "chain": [ { "id": "np_premium", "tier": "tenant", "priority": 70000, "score": 70xxx, "...": "..." } ],
  "rejected": [ { "id": "np_old", "rule": "provider-not-active", "reason": "the provider is not active" }, ... ] }
```

## What is where

| File | What it decides |
|---|---|
| `rules/numbering.bcl` | How a number was written (`9841234567`, `+977 984-123-4567`, `009779841234567` are one recipient: `9779841234567`) and which country it belongs to. |
| `rules/routing.bcl` | Which providers **may** carry the message (state, circuit, capabilities, message type, OTP quality floor, assignment, ownership) and the **tier** each belongs to; what the account is routed **for** (cost, delivery, balanced); the **rankings**. |
| `rules/overrides.bcl` | Rules an operator adds on top: per provider, by account, tenant, country, recipient, text, sender, template, size. |
| `config/01_resources.bcl` | The catalogue (eleven providers with prices and health, five accounts) and the rules engine. |
| `config/10_route.bcl` | The pipeline: number → country, account, message facts, candidates, objective, `rules.rank`. |
| `config/20_admin.bcl` | What an operator changes while it runs: provider state and health, and the rules themselves. |

## How a message is routed

1. **Eligibility** (`route_eligibility`, first match wins). For each provider in turn: the
   reasons it can *never* carry this message come first (paused, circuit open, cannot send
   Unicode, no delivery receipts, cannot send several segments, does not carry this message
   type, an OTP needs quality 85), then the **tier** it belongs to:

   | Tier | Priority | A provider is in it when |
   |---|---|---|
   | rule | 200000 + 1000 × the rule's priority | an operator's rule names it for this message (`overrides.bcl`) |
   | user | 100000, **exclusive** | the account owns it: it is the only one the account uses |
   | user | 90000 | it is assigned to the account, for this destination |
   | tenant | 70000 | it is assigned to the account's tenant, for this destination |
   | operator | 60000 | a number range it was bought for matches the recipient |
   | country | 50000 | it serves the destination |
   | platform | 10000 | it serves everything (a last resort) |

   A provider nobody assigned and no rule names is *not available* here, and the response
   says so.
2. **Operator rules** (`overrides.bcl`, evaluated before eligibility for the candidate they
   name). A rule can **use** a provider first, **reserve** it (`exclusive`, everything below
   is dropped), or **deny** it. A rule that uses or reserves a provider also *admits* it when
   only assignment kept it out: that is how one account gets a route nobody else may have.
   Its own limits still apply.
3. **Ranking.** Candidates are ordered by score: the tier's priority plus, per ranking, a
   weighted sum of delivery rate, quality, price and recent failures, each scaled to 0–1
   between the bounds in the rule (`normalize [0, 0.2]`), so a weight is what the metric is
   worth at its best. Tiers are 10000 apart and the metrics add at most a few hundred, so a
   metric only orders providers **within** a tier. Equal scores keep the order of the
   candidates, which is their id.
4. **What it is routed for** (`route_objective`): the account's own objective, else
   delivery for one-time codes, price for promotions, balance for the rest.

## Try it

Providers: `np_telecom` (primary Nepal), `np_budget` (cheap: plain short text, no receipts),
`np_mobile_range` (bought for 980/981/982 numbers), `in_shared`, `global_fallback`
(everywhere), `otp_gateway` (one-time codes only), `np_old` (paused), and four private ones:
`np_premium` (tenant `acme`), `in_vendor` (account `alice`), `boss_own` (owned by `acme_boss`),
`np_reserved` (assigned to nobody). Accounts: `alice`, `bob` and `acme_boss` (tenant `acme`),
`thrifty` (wants the lowest price), `careful` (wants the best delivery).

Every row below is a test (`contrib/messaging/e2e/rules_example_test.go`).

| Ask | First | Why |
|---|---|---|
| alice → `9841234567` | `np_telecom` | country tier; `global_fallback` stays behind it |
| alice → `009779801234567` | `np_mobile_range` | operator tier: the range matches 980… |
| bob → `9861234567` | `np_premium` | tenant tier: assigned to `acme` |
| alice → `+919876543210` | `in_vendor` | account tier: assigned to alice for India |
| acme_boss → anything | `boss_own` only | an owned route is exclusive |
| alice → `+14155552671` | `global_fallback` only | no country route: the catch-all |
| alice → `9841234567`, `"type": "otp"` | `np_telecom`, then `otp_gateway` | ranked for delivery; the gateway is a platform-tier fall-back |
| alice → `9841234567`, Devanagari text | no `np_budget` | `no-unicode` |
| alice → a 300-character text, `"dlr": false` | no `np_budget` | `no-long-messages` |
| thrifty → `9841234567`, `"dlr": false` | `np_budget` | `route_cost`: price outweighs quality inside a tier |
| alice → "Play CASINO tonight" | no `np_telecom` | rule `no-gambling` (content) |
| bob → `+977 984 123 4567` | `np_reserved` | rule `bob-vip-number` (account + recipient) admits a route nobody has |
| bob → `9841234568` | no `np_reserved` | same account, other number |
| alice → "SALE", `promotional`, `"dlr": false` | `np_budget` | rule `promotions-over-the-cheap-route` (type + country) |
| bob → `9851234567` | `np_premium` | rule `acme-985-range` (tenant + range) |
| bob → `+919876543210` | no `in_shared` | rule `acme-not-on-shared-india` (tenant, deny) |
| alice → a 600-character text | `np_mobile_range` | rule `long-messages` (size ≥ 3 segments) |
| alice → `template: "legal_notice"` | `global_fallback` only | rule `legal-notices-only-here` (template, exclusive) |
| alice → "Your code is 481516", from `BANKNP`, `otp` | `otp_gateway` | rule `bank-codes` (sender + content) |

## Change it while it runs

The operator routes need the key (`X-API-Key: dev-admin-key-change-me-0123`, or
`ROUTING_ADMIN_KEY`). Each applies to the very next message.

```
PUT /providers/np_telecom/state    { "state": "paused" }            # the next route takes over
PUT /providers/np_telecom/health   { "circuit_open": true }         # out until it is closed again
PUT /providers/np_telecom/health   { "consecutive_failures": 10 }   # lower in its tier, not out
GET /rules/overrides                                                 # the rules, as text
PUT /rules/overrides               { "source": "bcl { version \"1.0\" } ..." }   # checked first
DELETE /rules/overrides                                              # back to the file
```

A change to the rules is compiled by the engine before it is kept; a mistake is refused
with its line and nothing changes. Saved rules are stored in the database and published
again at the next start.

## Add a rule

In `rules/overrides.bcl`, find (or add) the decision `for_<provider>` and add a row:

```
row "acme-sms-to-india" {
  priority 1075                                   # 1000 + the rule's priority: 75
  when {
    all {
      provider.id == "in_vendor"
      user.tenant == "acme"
      message.country matches "^(IN)$"
    }
  }
  then { outcome { decision allow reason "acme's India traffic" attributes { tier "rule" priority 275000 exclusive false rule_id "acme-sms-to-india" } } }
}
```

* `priority` (the row's) picks between rules that name the **same provider** for the same
  message: the highest wins. The `priority` in `attributes` ranks providers **against each
  other**; keep it `200000 + 1000 ×` the rule's priority so a rule outranks every tier.
* `decision allow` uses the provider first; `exclusive true` uses only it; `decision deny`
  never uses it. A rule needs `provider.id == "…"` as its first condition.
* Conditions are `message.*`, `user.*` and `provider.*`. `matches` is a Go regular expression
  (linear time, no catastrophic backtracking); anchor it, `^( … )$`, to match a whole value.
  `message.to` is international digits.
* A new provider needs its own `for_<id>` decision only if it has rules.

[`../smsgateway`](../smsgateway) manages the same rules from a console: they are rows in a
table, checked on every save (the recipient is read by the phone library, a word is matched
as plain text, a pattern must compile) and rendered into this file's form.

## How correct and how fast

* `platform/rules_rank_test.go`: scoring (bounds, clamping, missing metrics) and the claim
  that scoring each candidate once orders them exactly as choosing the best of the rest again
  and again does, ties included, over 300 random candidate sets.
* `contrib/messaging/e2e/routing_model_test.go`: random rule sets (every kind of condition,
  all three effects, ties) against a reference model written from the specification; the
  router must give the same chain, provider for provider, for 1,200 messages.
* `contrib/messaging/e2e/routing_edge_test.go`: ties, quotes and angle brackets in rules,
  regular-expression characters in words, every written form of a number, bad input refused.
* `platform/rules_rank_perf_test.go`: with 20 providers a message is routed in about 1 ms
  against 500 rules and 4 ms against 5,000, because only the rules of a candidate are
  evaluated (about 10× fewer than one decision holding them all).
