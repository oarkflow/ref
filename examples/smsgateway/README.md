# SMS gateway

A complete SMS sending application built on three pieces:

- **REF** runs the application. The pipelines, providers, routing policy, pricing and retry rules are **BCL** in [`app/`](app/); Go supplies the vocabulary (resource kinds and actions) that the BCL composes.
- **[smppflow](../../../smppflow)** is the SMPP stack and the routing engine: SMPP sessions, segmentation, delivery-receipt parsing, rule matching, the circuit breaker, provider statistics.
- **[broker](../../../broker)** is the queue: one durable queue per provider, delivery-receipt and configuration queues, a write-ahead log.

```bash
go run . --sandbox        # SMSC, vendor API and demo accounts in-process; keys are printed once
go test -race ./...       # 10k lines, engine + routing + gateways + the BCL application end to end
```

`go.mod` replaces `github.com/oarkflow/{ref,smppflow,broker}` with sibling checkouts (`../../`, `../../../smppflow`, `../../../broker`).

```bash
curl -s http://127.0.0.1:8089/v1/messages \
  -H "Authorization: Bearer $DEMO_KEY" -H 'Content-Type: application/json' \
  -d '{"to":"+977 9841234567","text":"Your code is 482913","type":"otp"}'
# 202 {"id":"msg_…","state":"queued","provider":"np_telecom","price":0.03,"route":["np_telecom"], …}

curl -s http://127.0.0.1:8089/v1/messages/msg_… -H "Authorization: Bearer $DEMO_KEY"
# {"state":"delivered","payment":"captured","attempt_log":[{"provider":"np_telecom","outcome":"accepted"}], …}
```

## How it fits together

```
 HTTP ─▶ route ─▶ intent sms.submit
                    validate_user ─▶ check_data ─▶ content ─▶ route ─▶ accept ─▶ enqueue
                                                                         │          │
                                                          hold funds + store     publish job
                                                          (one transaction)          │
                                                                                     ▼
                                   broker:  sms.dispatch.np_telecom   sms.dispatch.in_vendor   …   (queue + consumers per provider)
                                                  │ N consumers
                                                  ▼
                                   intent sms.deliver:  load_job ─▶ provider_send ─▶ settle
                                                          claim       gateway.Send    capture │ retry │ fail over │ fail + release
                                                                          │
                              smpp / http / mock plugins ─────────────────┘            receipts ─▶ sms.dlr queue ─▶ intent sms.dlr
                                                                                   (SMPP deliver_sm, vendor webhook)

 admin API ─▶ store + broadcast ─▶ sms.config.{user,provider,assignment,rate} consumers ─▶ directory + router
```

| Requirement | Where it lives |
|---|---|
| Platform-configured providers | `resource "…" { kind "sms.gateway.<plugin>" }` in [`10_providers.bcl`](app/10_providers.bcl) |
| Providers by country | `countries`, `prefixes` on the provider; the platform tier of the route |
| Providers assigned to users, by country | `assignment` blocks / `PUT /v1/admin/assignments` with `countries`, `prefixes`, `senders`; user-owned providers (`owner`) |
| Routing parameters: user, sender, recipient, delivery quality, provider support, cost | [Routing](#routing) |
| Queue: github.com/oarkflow/broker | [`internal/brokerq`](internal/brokerq): the `queue.broker` REF resource |
| Multiple consumers for providers and for user configuration | one queue and a consumer pool per provider (`concurrency`); four configuration consumers |
| Pipelines: user validation, data check, payment, dispatch | [`30_submit.bcl`](app/30_submit.bcl), [`40_deliver.bcl`](app/40_deliver.bcl) |
| Fault tolerance, configurable retry, at-least-once, single payment | [Delivery guarantees](#delivery-guarantees) |

## The BCL

| File | Contents |
|---|---|
| [`00_platform.bcl`](app/00_platform.bcl) | database, broker queue, the `sms.hub` (currency, pricing, routing policy, recovery, seed accounts), authentication |
| [`10_providers.bcl`](app/10_providers.bcl) | one resource per provider |
| [`20_schemas.bcl`](app/20_schemas.bcl) | the request contract (`shape`), enforced before any stage runs |
| [`30_submit.bcl`](app/30_submit.bcl) | the submit pipeline and `POST /v1/messages` |
| [`40_deliver.bcl`](app/40_deliver.bcl) | the delivery pipeline, the receipt pipeline, the receipt webhook |
| [`50_account.bcl`](app/50_account.bcl), [`60_admin.bcl`](app/60_admin.bcl) | self-service and operator API |

### A provider

```bcl
resource "np_telecom" {
  kind "sms.gateway.smpp"            # sms.gateway.smpp | .http | .mock | any plugin you register
  depends_on ["sms"]
  config {
    hub "sms"
    countries ["NP"]                 # platform provider for these countries; none = catch-all
    cost_per_segment 0.0110          # what it costs the platform (per-country: cost { NP 0.011 })
    quality 92  delivery_rate 0.97   # priors; observed results take over
    tps 50  concurrency 8  timeout 10s
    retry { max_attempts 3 initial 1s max 15s factor 2 jitter 0.2 }
    plugin { addr "smsc:2775" system_id "gw" password env.required("NP_SMPP_PASSWORD") }
  }
}
```

| Key | Meaning |
|---|---|
| `countries`, `prefixes` | where the provider is used. `prefixes` are E.164 digits (`"97798"`) and replace the countries' dial codes: a route bought per operator |
| `message_types` | only these types (`otp`, `transactional`, `promotional`) |
| `assigned_only` | not a platform provider: used only through assignments |
| `owner` | private to one user, exclusively unless `exclusive false` |
| `sender_countries` | numeric sender ids registered in these countries only |
| `capabilities { unicode dlr alpha_sender numeric_sender long_sms }` | all default true; false keeps messages that need it away |
| `cost_per_segment`, `cost { … }` | platform cost, drives the `lowest_cost` objective |
| `quality`, `delivery_rate`, `weight`, `priority`, `objective` | scoring inputs |
| `tps`, `concurrency`, `timeout` | rate limit, consumers on the provider's queue, send timeout |
| `retry` | attempts on this provider before failing over, and the backoff |
| `webhook_secret` | required in `X-Webhook-Secret` on the provider's receipt webhook |
| `plugin { … }` | the plugin's own settings |

Plugins: **smpp** (smppflow session and message layers: binds lazily, rebinds after a transport fault, classifies SMPP status codes), **http** (any REST vendor: body templates, auth, id extraction, error-code mapping, receipt parsing; refuses private-network targets and never follows redirects), **mock** (in-process sandbox and the test double).

### The hub

`resource "sms" { kind "sms.hub" … }` carries `currency`, `default_country`, `capture_on`, `refund_on_dlr_failure`, `routing { … }`, `pricing { … }`, `recovery { … }` and `seed { … }` (accounts, assignments and rates created on first start; existing ones are never overwritten and an opening balance is credited once). Every key is validated at load: a typo fails the start, not a request.

## Routing

For each message the planner builds an ordered **chain** of providers; the first is tried, the rest are the failover path.

**Tiers.** A lower priority number wins, and the chain is the eligible providers tier by tier:

| Priority | Tier | Source |
|---|---|---|
| 5 | user | a provider the user owns |
| 10 / 20 | user | an assignment to the user, with / without countries |
| 30 | tenant | an assignment to the user's tenant |
| 100 | country | a platform provider that lists the destination's country |
| 1000 | platform | a platform provider with no countries (catch-all) |

An `exclusive` assignment ends the chain at its tier: the account asked for nothing else.

**Parameters.**

| Parameter | How it is used |
|---|---|
| User, tenant | assignment tiers above |
| Recipient number and location | country (from the number), `prefixes` for operator blocks |
| Sender id and location | `senders` on an assignment; alphanumeric vs numeric capability; `sender_countries` for numeric ids |
| Message type | `message_types` on providers and assignments; sets the objective and the quality floor |
| Quality of delivery | `quality`, observed delivery rate, `routing.quality_floor` / `delivery_floor` per type, the circuit breaker |
| Provider support | `capabilities` (unicode, receipts, alpha sender, long messages), provider state |
| Cost | `cost_per_segment` / `cost { country }`; the `lowest_cost` objective orders by it |

**Objectives** order providers inside a tier: `highest_delivery`, `lowest_cost`, `lowest_latency`, `balanced`. A message type picks one (`routing.objectives`), and a user can override it (`objective`). `POST /v1/route/explain` shows the chain, every candidate considered and why each rejected one was.

**Pricing** is what the *user* pays, and does not depend on which provider carries the message, so failing over never changes a bill. The most specific rate wins: a user's own rate, then the one matching most of country and type, then the default.

## Pipelines

Both pipelines are BCL intents. A stage is a node; its position is what it `requires` and `provides`.

**Submit** (`sms.submit`)

| Stage | Does |
|---|---|
| `sms.validate_user` | active account, per-second rate limit, daily cap |
| `sms.check_data` | number → E.164 and country, sender, text and segments (smppflow's analysis), type, schedule, opt-outs |
| `sms.content_policy` | *example of an added stage*: deny patterns for chosen message types |
| `sms.route` | the provider chain |
| `sms.accept` | price the message, **hold** the funds and store it, in one transaction; idempotent by `reference` |
| `sms.enqueue` | publish the dispatch job to the first provider's queue |

The payment check and the reduction are one statement (`UPDATE … SET held = held + ? WHERE balance - held >= ?`), so two concurrent requests can never spend the same balance. A pre-check stage would be racy; this one cannot be.

**Deliver** (`sms.deliver`, run by the provider's consumers)

| Stage | Does |
|---|---|
| `sms.load_job` | claims the message for this job; stale and duplicate jobs stop here |
| `sms.provider_send` | circuit breaker, per-provider rate limit, timeout, `gateway.Send` |
| `sms.settle` | accepted → **capture** the hold; retryable → retry the same provider after backoff (the provider's `retry`); provider fault → fail over; permanent error or chain exhausted → fail and **release** the hold |

A delivery receipt (pushed by an SMPP bind, or posted to `/v1/webhooks/dlr/:provider`) goes through the `sms.dlr` queue and intent: delivered, or failed (refunding a charge already captured when `refund_on_dlr_failure` is set).

## Delivery guarantees

**At least one delivery; one charge.** Both come from the state machine in [`internal/sms/store.go`](internal/sms/store.go):

- Money and message state move in the **same transaction**, each as a compare-and-set: the hold (`hold → captured | released | refunded`), the claim (`queued → dispatching`, keyed by the job's sequence number), every transition after it. A redelivered job, a second worker or a repeated receipt finds the row already moved and does nothing.
- `reference` makes a request idempotent: `UNIQUE (user, reference)` returns the first message with `"duplicate": true` and holds nothing more.
- Delivery is retried until a provider accepts or the chain is exhausted; a message never ends without either an acceptance or a failure that released its funds.

| What goes wrong | What happens |
|---|---|
| Provider times out, 5xx, dropped bind | retried per the provider's `retry`, then the next provider |
| Provider throttles (429, `ESME_RTHROTTLED`) | backoff without spending an attempt (bounded at 12 sends) |
| Bad credentials, unsupported route, open circuit | immediate failover |
| Invalid destination, blocked content | message fails at once, funds released, no other provider tried |
| Every provider failed | message fails (`all_providers_failed`), funds released |
| Process dies after accepting, before publishing | the stored message is `queued` past its run time; the recovery sweep republishes it |
| Worker dies mid-send | its claim lapses; the broker redelivers and another worker re-claims |
| Receipt arrives before the submission commits | the receipt job is retried quickly |
| Whole app restarts | the broker's write-ahead log and the database carry queued work across (tested) |

**The one window.** If a provider accepts a message and the response is lost (a timeout, a crash before the commit), the message is sent again: a duplicate *delivery*, never a lost one, and still one charge. The HTTP plugin sends the message id as `Idempotency-Key` so a vendor that honours it deduplicates; SMPP has no equivalent.

## Writing plugins and stages

**A provider type** is a Go package with one `init()`:

```go
func init() { gateway.Register("acme", openAcme) }

func openAcme(ctx context.Context, name string, cfg map[string]any) (gateway.Gateway, error) { … }
// Send returns a Receipt or a *gateway.Error{Class: Retryable | Throttled | Provider | Permanent}.
// Optionally implement DLRSource (push receipts), DLRParser (webhook receipts), Pinger.
```

Its BCL resource kind, `sms.gateway.acme`, exists as soon as the package is imported. Decode `cfg` with [`cfgdec`](internal/cfgdec), which undoes REF's BCL conventions (durations, labelled blocks) and rejects unknown keys.

**A pipeline stage** is a REF action. [`internal/stages/contentpolicy.go`](internal/stages/contentpolicy.go) is the worked example: it is registered with `platform.RegisterActionDriver`, takes its patterns from the node's `config`, and joins the pipeline when [`30_submit.bcl`](app/30_submit.bcl) puts a node between `data` and `route`. The core package never mentions it. `route` and `accept` take `draft_fact` so an inserted stage need not rename its neighbours.

**The queue** is a REF resource kind (`queue.broker`): it satisfies `queue.publish`, `worker` blocks and schedules like `queue.file` and `queue.sql`, and adds per-job-type consumers (`consumer "sms.dispatch.*" { concurrency 8 }`), delayed jobs, broadcast jobs and a write-ahead log.

## API

Account endpoints take `Authorization: Bearer <account key>`; operator endpoints take the operator key (`X-API-Key` or Bearer).

| | |
|---|---|
| `POST /v1/messages` | `{to, text, from?, type?, reference?, dlr?, schedule_at?, expires_in?, meta?}` → 202 |
| `GET /v1/messages/:id`, `GET /v1/messages?state=&limit=` | status, payment state, attempt log. Another account's message is a 404 |
| `GET /v1/balance`, `GET /v1/ledger` | balance, held, available; the journal (hold, capture, release, refund, topup) |
| `POST /v1/route/explain` | the chain and every candidate, without sending or charging |
| `POST /v1/webhooks/dlr/:provider` | a provider's receipts (`X-Webhook-Secret`) |
| `/v1/admin/users[/:id]`, `…/key`, `…/topup`, `…/balance`, `…/route-explain` | accounts, key issue (shown once), idempotent top-up by `reference` |
| `/v1/admin/providers[/:id]`, `…/state` | create providers at runtime, set `down` / `maintenance` / `draining` / `suspended` |
| `/v1/admin/assignments`, `/v1/admin/rates`, `/v1/admin/optouts` | routing assignments, prices, opt-outs |
| `GET /v1/admin/stats`, `POST /v1/admin/recover`, `GET /healthz` | counters, provider health and circuits, a manual recovery sweep |

Errors are `{"error":{"code","message"}}`: 401 unknown key, 403 suspended / opted out / sender or country not allowed, 409 `INSUFFICIENT_FUNDS` or `REFERENCE_REUSED`, 422 invalid input or no route, 429 rate or daily limit, 503 no provider available right now. (REF has no 402 category, so insufficient funds is a 409.)

Configuration changes made through the admin API are stored, applied on the receiving node and broadcast; each node's configuration consumers reload the entity from the database, so duplicated or reordered events are harmless.

## Operating it

| Variable | |
|---|---|
| `SMS_ADMIN_KEY` | operator key. Required in production; in development one is generated and printed once |
| `SMS_DSN`, `SMS_BROKER_DIR`, `SMS_NODE_ID`, `SMS_LISTEN`, `APP_ENV` | storage, queue log, node id, address |
| `NP_SMPP_*`, `IN_VENDOR_*` | the demo providers' endpoints and secrets (set `IN_VENDOR_ALLOW_PRIVATE=false` outside the sandbox) |

Security: API keys are stored as SHA-256; message text is erased the moment a provider accepts the message (`keep_text true` to retain it); a runtime-created HTTP provider cannot reach private networks; receipt webhooks need a secret; the operator key is separate from account keys.

## Limits of this implementation

- **Single node.** `queue.broker` opens an embedded broker, so configuration broadcasts and queues are per process. Several nodes need a shared database (PostgreSQL) *and* a shared broker, which the plugin does not provide yet. The pipeline is already safe against several consumers (claims, sequence numbers), but that is not exercised across processes here.
- **SQLite only is tested.** The SQL is written for SQLite and PostgreSQL (`?` is rewritten, `UPSERT`/`RETURNING` are used by both), but no PostgreSQL server was available. SQLite runs with one connection and an fsync per commit, which is what bounds throughput (about 40 messages/s end to end in the burst test); PostgreSQL is the scaling path.
- Routing health (circuit breaker, observed delivery rates) and per-user rate limits live in memory and start fresh after a restart.
- One platform currency. Destinations are a built-in table of ~65 countries (all of `+1` is `US`); operators are separated by `prefixes`, not MCC/MNC.
- A failed receipt after acceptance fails the message (and can refund); it is not re-routed through another provider.
- Held funds, ledger rows and attempt logs are kept; there is no retention job.
