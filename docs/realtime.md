# Realtime: WebSocket, SSE, topics and presence

`realtime.hub` serves WebSocket and SSE clients from one hub. It runs on a single node by default; `backplane "redis"` makes several nodes behave as one.

```bcl
resource "live" {
  kind "realtime.hub"
  config {
    authenticator "session_auth"          # required: there is no anonymous mode
    session "sessions"                    # lets cookie-authenticated browsers connect
    backplane "redis"                     # omit for a single node
    redis_addr env.required("REDIS_ADDR")
    callable ["chat.send"]                # intents a client may invoke with the call op
    rules { "admin:" { subscribe ["admin"] publish ["admin"] } }
  }
}
```

| Endpoint | Purpose |
|---|---|
| `GET /realtime/ws?topics=a,b` | WebSocket |
| `GET /realtime/events?topics=a,b` | SSE (honours `Last-Event-ID`) |
| `POST /realtime/send?client=<id>` | frames from an SSE client; must be the stream's own user |

## Client frames

```json
{"id":"1","op":"subscribe","topic":"room:7","track":true,"meta":{"name":"Ada"}}
{"id":"2","op":"publish","topic":"room:7","event":"chat","data":{"text":"hi"}}
{"op":"presence","topic":"room:7"}
{"op":"call","name":"chat.send","data":{}}
{"op":"subscribe","topic":"room:7","after":"<last message id>"}   // resume
```

Server frames are `ready`, `message`, `presence` (`join` / `leave`), `reply` (answers `id`) and `pong`.

## Rules

- Every connection is authenticated by the named authenticator. A browser `Origin` that is not the server's own or listed in `allowed_origins` is refused.
- Each signed-in user is subscribed to `user:<id>`, nobody else may subscribe or publish there. `realtime.publish` with `user_fact` reaches every device of a user on every node.
- `rules` match by longest topic prefix. A topic no rule covers is open to any signed-in user.
- A client more than `send_buffer` frames behind is dropped, so one slow reader cannot stall a topic.
- `call` runs only the intents listed in `callable`, as the connected user.
- Presence lists a user once however many devices they have. With a backplane each node re-announces its table every `presence_ttl / 3`; a node silent for `presence_ttl` is forgotten.

## Nodes

`realtime.publish` (effect: `topic` / `topic_fact` / `user_fact`, `event`, `data_fact`), `realtime.presence` (read), `realtime.stats` (read).

`realtime.publish` and `realtime.presence` act **as the signed-in caller**: the hub's topic rules apply to them, so an intent that takes a topic from user input cannot be used to post into, or list the members of, a topic the caller cannot join. Set `as_server true` on a node whose topic the author, not the user, chose. (`user_fact` always reaches that user's private topic; the author fixed the event name, so it is not checked.)

## Helpers

Usable in any expression or SPL/BCL template: `rt_topic("room", id)` → `room:<id>`, `rt_user(id)` → `user:<id>`, `rt_tenant_topic(tenant, name)` → `t:<tenant>:<name>`, `chat_topic(channel)` → `chat:<channel>`, `chat_dm(a, b)` → the DM id for two users, the same in either order.

## Limits

- Resume (`Last-Event-ID` / `after`) replays from the connected node's own history. Message ids are per node, so a client that reconnects to a different node gets the retained history of that node (`resumed:false`).
- Fan-out is at-most-once. Use a queue or outbox node when a message must not be lost.
