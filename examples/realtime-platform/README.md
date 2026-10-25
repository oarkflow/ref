# Realtime platform

Chat (channels, DMs, unread counts, read receipts, typing), presence, p2p video calls (the *Video call* button in a DM), group calls through the SFU (the *Group call* button in a channel), structured logs, and CSV/XLSX exports, in one BCL file and a static page. No application code.

```sh
make run            # http://localhost:8080  (sign in as ada, bob or cy; password: demo-password)
```

Open it in two browsers as two users. The demo password and keys in the `Makefile` are for localhost only: replace the sign-in (`auth.login`) with a real identity provider before using this for anything else.

| File | |
|---|---|
| `config/00_app.bcl` | resources, intents and routes |
| `static/index.html` | the browser client (WebSocket, `fetch`, `RTCPeerConnection`) |

Operators use the API key: `curl -H "X-API-Key: $OPS_API_KEY" localhost:8080/api/ops/stats`, and `POST /api/ops/logs {"q":"..."}`.

To run on several nodes, uncomment the Redis backplane in `config/00_app.bcl`; for calls behind strict NATs add a TURN server. See `docs/realtime.md`, `docs/chat.md`, `docs/logs-and-reports.md`, `docs/calls.md`.
