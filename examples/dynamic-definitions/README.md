# Dynamic definitions

A node type, two helpers and three handlers, all written in BCL and SPL. There is no Go in this directory. Reference: [`docs/dynamic-definitions.md`](../../docs/dynamic-definitions.md).

```bash
make          # runs through Aegis on http://localhost:8080
```

| File | What it shows |
|---|---|
| `config/00_app.bcl` | Roles, an RBAC authorizer, one authenticator with two API keys (clerk, viewer), a rate limiter |
| `config/10_definitions.bcl` | `definition_policy`, `node_type "risk.score"`, `helper "vat"` (a BCL expression), `helper "shipping"` (a script), and the handlers `quote`, `shipping`, `live` |
| `config/20_flows.bcl` | A hand-written intent using the node type, `helper.call`, and `helper('vat', ...)` inside an expression |
| `scripts/*.spl` | The SPL bodies: plain scripts that end in the value they return |
| `example_test.go` | The calls below over real HTTP, and signed definitions: a valid signature loads, a script changed after signing does not |

```bash
CLERK=dev-clerk-key-change-me-0123456789
VIEWER=dev-viewer-key-change-me-012345678

curl localhost:8080/live                                         # open on purpose: {"ok":true}

curl -XPOST localhost:8080/quote -H "X-API-Key: $CLERK" -H 'content-type: application/json' \
     -d '{"lines":[{"qty":2,"price":30},{"qty":1,"price":55}]}'  # subtotal 115, quoted_by clerk-1

curl -XPOST localhost:8080/quote -H "X-API-Key: $VIEWER" ...     # 403: authenticated, lacks quote:create
curl -XPOST localhost:8080/quote -d '{}'                         # 401: no key

curl -XPOST localhost:8080/shipping -H "X-API-Key: $CLERK" -H 'content-type: application/json' -d '{"kg":4}'   # 12

curl -XPOST localhost:8080/orders/review -H "X-API-Key: $CLERK" -H 'content-type: application/json' \
     -d '{"total":800}'    # {"level":"high","tax":104,"total_with_tax":904}
```

Things to try:

* Change `threshold 500` in `20_flows.bcl` to `1000` and the same order is `low`. The type's `threshold` param is checked at load: `thresold 500` stops the start-up.
* Remove `authz` and `auth` from `handler "quote"`: the load fails, because a handler is closed unless it says `public true`.
* Raise `max_steps` on a definition above the policy's 200000: refused at load.
* For production, add `require_signature true`, and give each definition a `publisher` and `signature`. `TestSignedDefinitionsOnly` shows the whole flow.
