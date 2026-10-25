# Chat

`chat.store` keeps channels, direct messages, history and read receipts in a SQL database (`database.sql`; SQLite, Postgres and MySQL) and announces them over a `realtime.hub`.

```bcl
resource "chat" { kind "chat.store"  config { database "db" hub "live" } }
```

The tables (`chat_channels`, `chat_members`, `chat_messages`) are created on start. The store is the source of truth: a message is committed first and announced second, so a client that missed the push finds it in history.

## Nodes

| Node | Does |
|---|---|
| `chat.channel_create` | group channel; the caller owns it. `name`, `members_fact` |
| `chat.dm` | open or create the DM with `user`. One per pair, whichever way round |
| `chat.send` | `channel`, `body` (≤ 8 KiB), optional `meta_fact`. Seq numbers are gapless per channel |
| `chat.history` | oldest first; page back with `before_seq_fact`, `limit_fact` (≤ 200) |
| `chat.since` | messages after `after_seq_fact`: what a reconnecting client missed |
| `chat.channels` | the caller's channels with `unread` and `last_seq` |
| `chat.members`, `chat.add_member`, `chat.remove_member` | owner adds and removes; anyone may leave; the owner cannot abandon the channel |
| `chat.mark_read`, `chat.receipts` | read position per member (never moves backwards) |
| `chat.typing` | ephemeral, not stored |

Every argument is either a literal (`channel "ch_1"`) or a fact (`channel_fact "input.channel"`).

## Events

A client subscribes to `chat:<channel>` and receives `message`, `typing`, `receipt`, `member.joined` and `member.left`. Subscribing needs membership in the channel's tenant. **Nobody can publish to a chat topic directly**, so a message always carries its sender's real identity and goes through `chat.send`.

## Rules worth knowing

- Every operation checks membership. A stranger and an unknown channel id get the same answer, so ids cannot be probed.
- Users from another tenant are never members, whatever the table says.
- Within one process senders to a channel are serialized; across processes the `(channel, seq)` key and a retry keep the numbering gapless.
- Moderation, blocking and retention are yours to add; put them in an intent in front of `chat.send` (`extend` can insert a node without editing the flow).
