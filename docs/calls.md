# Audio and video calls

`contrib/rtc` adds calls to the `ref` command (`rtc.Register()`), for **peer-to-peer** calls between two people and **group** calls through a Pion **SFU**.

```bcl
resource "calls" {
  kind "rtc.calls"
  config {
    database "db"  hub "live"
    stun_urls ["stun:stun.l.google.com:19302"]
    turn_urls ["turn:turn.example.com:3478"]
    turn_secret env.required("TURN_SECRET")     # coturn use-auth-secret
  }
}
resource "sfu" { kind "rtc.sfu"  config { hub "live" calls "calls" udp_port_min 50000 udp_port_max 50100 } }
```

Call state lives in SQL, so any node can answer for any call. A signed-in user only: every node needs a principal.

## Nodes

`call.start` (`to`, `kind` audio|video, `mode` p2p|sfu) · `call.answer` · `call.decline` · `call.end` · `call.get` · `call.signal` · `call.ice_servers`.

Starting a call rings each invitee with a `call.incoming` event on their private topic. A call that rings longer than `ring_timeout` (45 s) is *missed*; it expires when next touched, and `Store.Sweep` ends stale ones for callers who never look again.

## p2p (mode `p2p`, exactly two people)

Media flows directly between the browsers. The server only relays signaling: `call.signal` with `type` offer, answer, candidate, renegotiate, mute or unmute. The sender is stamped by the server, the payload (≤ 16 KiB) is opaque, and only participants can send to participants. The caller may send an offer while the call still rings; the callee cannot signal before answering. A browser client is in `examples/realtime-platform/static/index.html`; it does both p2p (the *Video call* button in a DM) and group calls (the *Group call* button in a channel).

## Group calls (mode `sfu`)

Each participant sends their media to the server once; the server forwards it to the others. After `call.answer`, a participant talks to the SFU over HTTP:

| | |
|---|---|
| `POST /rtc/sfu/{call}/join` `{sdp}` | the first offer → `{type:"answer", sdp}` |
| `POST /rtc/sfu/{call}/answer` `{sdp}` | answer to a server offer |
| `POST /rtc/sfu/{call}/candidate` | a client ICE candidate |
| `POST /rtc/sfu/{call}/leave` | leave the room |

The server answers back as hub events on the user's private topic: `sfu.offer` `{call_id, type, sdp}` (the room's tracks changed: answer it) and `sfu.candidate`. The demo page shows one tile per remote stream. A forwarded track's stream id is the publisher's user id, set by the server, so a publisher cannot pose as someone else.

Authentication is the hub's (the same cookie or key as the sockets); requests need `Content-Type: application/json`, which keeps another site from driving a signed-in browser into a call.

## Operating it

- **One room lives on one node.** Media cannot be split across nodes without cascading SFUs. In a cluster, route every request for a call to one node (hash the call id at the load balancer). Call state and signaling are shared via SQL and the hub backplane regardless.
- Open the UDP range (`udp_port_min`–`max`) in the firewall; behind static NAT set `public_ips`.
- **TURN:** `rtc.calls` refuses `turn_urls` without a `turn_secret`. Clients get a credential that expires (`turn_ttl`, default 1 h), derived by HMAC from the secret; the secret is never sent.
- Limits: `max_peers` (50) per room, `max_rooms` (1000), `max_invitees` (20). Bandwidth estimation, simulcast and recording are not implemented: it forwards what it receives.
