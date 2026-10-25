# Audio and video calls

Calls are run by **rtcd**, a standalone service in its own repository, [oarkflow/rtcd](https://github.com/oarkflow/rtcd): call state, signaling, the Pion SFU, recording and end-to-end encryption. It knows nothing about REF; any backend can use it. REF talks to it through `contrib/rtc` (`rtc.Register()`), which adds one resource and the `call.*` nodes, and depends on it as an ordinary Go module (`github.com/oarkflow/rtcd`).

```
 browser ──token──▶ rtcd ◀──service key── your backend (REF app)
    │                 │
    └── events, SFU signaling, key exchange, media go straight to rtcd
```

Your application signs users in and gives each one a short-lived token (`call.token`), with the scopes you choose. The browser then keeps a WebSocket to rtcd and does everything over it, so none of the event, signaling or media traffic passes through the application. If you want your application to control who may call whom, mint tokens without the `calls` scope and place calls from the backend with the `call.start` node instead.

```bcl
resource "calls" {
  kind "rtc.service"
  config {
    url     env("RTCD_URL", "http://127.0.0.1:8480")
    api_key env.required("RTCD_API_KEY")        # this application's service key
  }
}
```

## Nodes

`call.start` (`to`, `kind` audio|video, `mode` p2p|sfu) · `call.answer` · `call.decline` · `call.end` · `call.get` · `call.signal` · `call.e2ee` · `call.ice_servers` · `call.token`. They act as the signed-in user; a node with no principal is refused. rtcd's tenant is the one the service key belongs to; the application's own tenant is not sent. A browser normally needs only `call.token`: with the token it does the rest itself.

## Running it

The service is a separate program with its own repository. Build it from a checkout (not `go install`: its `go.mod` replaces `pion/ice` with a fork, and Go refuses to install a module that has a `replace`):

```
git clone git@github.com:oarkflow/rtcd.git && cd rtcd
go build -o rtcd ./cmd/rtcd
./rtcd -config rtcd.bcl        # see the config in the rtcd README
```

`examples/realtime-platform` runs both halves (`make run`; it expects the checkout next to this repository, or `RTCD_DIR=/path/to/rtcd`) and has a complete `rtcd.bcl`. It is also a working browser client: `static/index.html` keeps one WebSocket to the service and does everything over it.

## More

Everything about how a call works is documented with the service, in the rtcd repository:

- [README](https://github.com/oarkflow/rtcd): the API (operations and events), groups, the audit log, performance, configuration, ICE restarts.
- [docs/calls.md](https://github.com/oarkflow/rtcd/blob/main/docs/calls.md): the call model, group calls and their operations, simulcast, network changes, operating notes, and the end-to-end encryption design and its limits.
