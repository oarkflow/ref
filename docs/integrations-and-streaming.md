# Node families, integrations and streaming

Every node type in the catalog (`GET` the platform catalog, or `platform/catalog_builtins.go`) is directly usable. A node may declare only its semantic **family** and it runs that family's default action:

```bcl
node "notify_partner" {
  family webhook              # runs service.webhook_send
  resource "partner_api"
  requires [claim]
  provides [delivery]
  config { url "/hooks/claims" event_type "claim.submitted" payload_fact "claim" secret env.required("PARTNER_HOOK_SECRET") }
}
```

An explicit `uses` always wins. Durable families have no request-tier action, because a request cannot wait days. Using one in an intent is a compile error. Each maps to a process construct that parks the run durably:

| Durable family | Process construct |
|---|---|
| `wait`, `wait_event`, `external_task` | an edge `kind wait_event` with `event`, `correlation` and `timeout`/`on_timeout`; an external worker reports by signalling that event |
| `timer`, `delay` | an edge `kind delayed` with `timeout` |
| `subprocess` | a step with `process "<child>"` instead of `intent`; the parent parks until the child is terminal |
| `compensation` | `compensate "<intent>"` on a step, with `kind compensate` edges; completed steps are undone in reverse order on failure |
| `human_task`, `approval`, `form`, `manual_review` | a step with a `task { … }` block (assignee, role or queue, form schema, actions, due date, escalation) |
| `escalation` | a task's `due` + `escalate`, or an edge `kind escalation` |

Set `family` on the step as well, so editors and generated docs show what it means. A test (`TestNodeTypeDefaultActionsAreRegistered`) guarantees that every default action a family names is actually registered.

## gRPC (`family grpc` → `service.grpc_json`)

This action makes a unary call over JSON using the Connect protocol, through a `service.http` resource. The resource's allowlist, private-network refusal, timeout, response cap and retries all apply. A grpc-gateway style endpoint works too: set `url` to its path.

```bcl
node "submit" {
  family grpc
  resource "billing_api"
  requires [claim]
  provides [reply]
  config { service "billing.v1.ClaimService" method "SubmitClaim" request_fact "claim" }
}
```

Connect error codes map onto platform failures:

| gRPC code | HTTP status |
|---|---|
| `invalid_argument`, `failed_precondition`, `out_of_range` | 422 |
| `not_found` | 404 |
| `already_exists`, `aborted` | 409 |
| `permission_denied` | 403 |
| `resource_exhausted` | 429 |
| `unavailable`, `unauthenticated` | 503 |
| `deadline_exceeded` | 504 |

The request's remaining deadline is sent as `Connect-Timeout-Ms`.

## Webhooks (`family webhook` → `service.webhook_send`)

Deliveries are signed the Standard Webhooks way:

- `webhook-id`: from `id_fact`, which is stable and serves as the receiver's idempotency key; random when unset.
- `webhook-timestamp`.
- `webhook-signature: v1,<base64 HMAC-SHA256(secret, id.timestamp.body)>`.

The secret is either `whsec_<base64>` or a raw string of at least 24 characters. With `event_type` set, the body is `{type, timestamp, data}`.

## Retrieval (`family rag` → `service.rag_retrieve`)

This action queries a `search.*` resource and assembles the hits into a citation-ready context using `ai.BuildGroundedPrompt`, which applies the same budget and citation rules as the rest of the platform. It publishes `{context, documents, count, instruction}`. Feed `context` to a `service.llm_chat` node as its `system` prompt.

## External orchestrators (`family workflow` → `workflow.start`)

`workflow.start`, `workflow.signal` and `workflow.status` drive any resource that implements `WorkflowService`. The built-in `workflow.http` adapter uses a small REST convention through a `service.http` resource:

| Operation | Default path |
|---|---|
| start | `POST /workflows/{workflow}/runs` with body `{input}`, plus `Idempotency-Key` and `X-Tenant-ID` headers |
| signal | `POST /runs/{run_id}/signals/{signal}` with body `{payload}` |
| status | `GET /runs/{run_id}` |

All three paths can be changed in the resource config. For REF's own durable engine, use `process.*`.

## Incremental streaming (`family stream` → `stream.emit`)

On a `mode "stream"` route, `stream.emit` pushes a server-sent event while the intent keeps running. The final value still arrives as `event: result`.

```bcl
intent "import.run" {
  response "summary"
  node "begin" { family stream requires [input] provides [begin] config { event "progress" data "importing {{ input.file }}" } }
  node "rows"  { uses "data.parse_csv" requires [begin] provides [rows] config { … } }
  node "count" { family stream requires [rows] provides [count] config { event "progress" data_fact "rows" } }
  node "summary" { uses "collect" requires [count, rows] provides [summary] }
}
route "import.run" { method POST path "/imports" intent "import.run" mode "stream" }
```

Behaviour:

- **Bounded:** at most 64 events are buffered. A slow client applies backpressure to the emitting node, and memory never grows with the stream.
- **Cancellation:** a client that disconnects cancels the dispatch, which unblocks any emitting node, so no goroutine outlives the request.
- **Compatible:** if nothing is emitted, the route behaves exactly as before. A failure still returns its HTTP status (422, 403, …). Once streaming has started, a failure is sent as `event: error` with the usual `{error:{code,message,details}}` body.
- **Transport-neutral:** outside a stream route `stream.emit` is a no-op that publishes `{emitted:false}`, so the same intent also serves JSON and queue routes.
