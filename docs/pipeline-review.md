# Review modes and notifications

This page covers two features of [pipelines](pipelines.md):

- **Review modes**: human-in-the-loop policies on a stage (diff, gate, triage and sampling).
- **Notifications**: `notify` rules, and each person's preferences for how and when notifications reach them.

The rules live in the [`pipeline`](../pipeline) package (`review.go`, `notify.go`). The `pipeline.cases` resource stores the notifications and delivers them. Both features are exercised end to end in [`platform/pipeline_review_e2e_test.go`](../platform/pipeline_review_e2e_test.go).

## Review modes

A stage declares review modes with `review` blocks. Each block is named by its mode, and a stage may combine several of them:

```bcl
stage "assess" {
  roles ["officer", "senior"]
  routing { strategy least_loaded  roles ["officer"] }

  review "triage" {
    bucket "urgent" { condition "claim.amount > 1000"  priority 1  queue "urgent"  roles ["senior"] }
    bucket "normal" { priority 2  queue "standard" }
    default_queue "standard"
  }
  review "diff" { against submission }           # or: against approved
  review "gate" { approvals 2  roles ["senior"] }

  action "accept" { outcome advance }
  action "return" { outcome return  return_to "apply" }
}

stage "screen" {
  roles ["screener"]
  review "sampling" {
    percent 10
    salt "2026-q3"
    sample_if "claim.channel == 'agent'"
    always_review_if ["claim.amount > 10000", "claim.prior_fraud == true"]
  }
}
```

> **Spelling:** conditions are written `condition`; `when` is accepted as an alias (BCL v0.0.34 and later). Setting both differently is a compile error.

Staff see a stage's review state in the view, under `review`: `{modes, diff, gate, triage, sampling, reviewed_revision}`. The applicant never sees it.

### diff

A diff review shows the reviewer what changed:

- When a case enters the stage, its data is snapshotted as a **submission**.
- When a reviewer completes the stage (an `advance` or `approve` action), the data is snapshotted as **approved**.

`review.diff` compares the current submission with a baseline:

| `against` | Baseline |
|---|---|
| `submission` (default) | The previous submission to this stage. After a correction loop, the reviewer sees exactly what the applicant changed. |
| `approved` | The last version a reviewer approved at this stage. Use it when approved cases come back for amendment. |

Each entry of `changes` has the form `{path, change: added|changed|removed, before, after}`:

- Paths are field level (`claim.title`).
- Entries of repeatable forms are compared one by one (`addresses[1].city`).
- Sensitive inputs are masked unless the viewer may reveal them.

On the first submission, `first` is `true` and every field shows as added.

**Approval records the revision reviewed.** Completing the stage stores the case revision the reviewer saw in three places:

- `stages.<stage>.review.reviewed_revision`, together with `reviewed_by` and `reviewed_at`;
- the action's history entry, as `revision`;
- the approved snapshot.

Approvals, votes and gate decisions also carry the `revision` they were given on. Combine this with the client's `revision` parameter to reject stale approvals. The case keeps at most 20 snapshots, and the newest one of each stage and kind is always kept. Erasure anonymises personal data in snapshots as well.

### gate

A gate is a hard gate: the case cannot advance until `approvals` distinct reviewers approve. If `roles` is set, only reviewers holding one of those roles count.

The gate is a node (named `gate`, or `node "…"` in the review block). Reviewers operate it with `POST …/stages/:stage/nodes/gate/approve` or `…/reject` (a rejection needs a `comment`).

| Rule | |
|---|---|
| Who | Holders of `roles` (default: the stage's roles). The applicant may never decide on their own case. |
| One decision per person | A second decision by the same person is `409`. |
| Rejections | Recorded, with who, when, why and the revision. They never open or close the gate: the reviewer returns or rejects the case with a stage action. |
| Hard | `skip_nodes` actions cannot pass it, and it cannot be waived. Any `advance` or `approve` action is `409 stage "…" is gated: "gate" has 1 of 2 approvals`. |
| Claims | Like approvals and votes, the gate is exempt from claims: many people decide. |
| Re-entry | A case returned and resubmitted re-enters the stage with a fresh gate. |

Events: `review.approved`, `review.rejected`, `review.gate_opened`. The view's `review.gate` holds `{node, required, approvals, rejections, open, decisions}`.

### triage

When a case enters the stage, the buckets classify it in order, and the first bucket whose `condition` holds wins. An empty `condition` matches every case. If no bucket matches, the case gets `default_priority` (default: the lowest bucket priority plus 1) and `default_queue`.

The result is stored on the case as `triage: {stage, bucket, priority, queue, at}`. It is recorded in the history and emitted as the `triaged` event.

- **Priority**: `1` is the most urgent. Stores keep the priority and the queue in indexed columns. `Query.Order = "priority"` lists the most urgent first, oldest first within a priority, and untriaged cases last. `Query.Queues` filters by queue.
- **Work lists**: `pipeline.list` orders `scope=queue` and `scope=assigned` by priority whenever the pipeline has a triage stage. `?order=newest` restores newest first, and `?queue=urgent` filters. Rows carry `priority`, `queue` and `triage_bucket`.
- **Routing**: a bucket's `roles` route its cases to those roles (least loaded) when the stage routes automatically. The routing reason says `triage bucket urgent`.

### sampling

Only some cases need human review:

- `always_review_if` conditions always force a review.
- Otherwise, cases matching `sample_if` are reviewed.
- Otherwise, the case is reviewed if its **sampling bucket** is below `percent × 100`. The bucket is `SampleBucket(salt, pipeline, stage, case id)`: the first 8 bytes of a SHA-256, modulo 10000. It is deterministic, so anyone can reproduce and audit the decision. Change `salt` to draw a new sample.

A case that is **not sampled** passes the stage without review:

- its nodes are `skipped`;
- the stage is completed by `system`;
- the history records `not_sampled` with the reason, for example `not sampled: outside the 10% sample (bucket 4711 >= 1000)`;
- the `review.not_sampled` event is emitted.

A sampled case records `sampled` and emits `review.sampled`. Either way, the decision `{sampled, reason, bucket, percent, at}` is kept on `stages.<stage>.review.sampling`.

## Notifications

`notify` rules turn events into notifications for people:

```bcl
pipeline "claim" {
  notify "stage.entered" {
    stage "assess"
    to ["role:senior"]
    channels ["email"]
    subject "New work: {case.number}"
  }
  notify "review.rejected" {
    to ["applicant"]
    channels ["email", "sms"]
    severity critical
    subject "{case.number} was rejected by a reviewer"
    body "Reason: see your case page."
  }
  notify "note.added" {
    to ["applicant"]
    channels ["email"]
    condition "event.actor != case.created_by"
  }
}

resource "cases" {
  kind "pipeline.cases"
  config {
    database "db"
    notify_channels {
      email "send_email"      # channel -> the intent that delivers it
      sms "send_sms"
    }
  }
}
```

| Attribute | Meaning |
|---|---|
| event (the label) | An event name, a prefix pattern (`sla.*`) or `*`. See the event list in [pipelines.md](pipelines.md#events-and-hooks). |
| `to` | Any of:<ul><li>`assignee` (the event's, or the stage's)</li><li>`previous_assignee`</li><li>`applicant`</li><li>`actor`</li><li>`role:<role>` (every active directory worker holding it)</li><li>`escalation` (an SLA escalation level's `notify` list: roles, else user ids)</li><li>a user id</li></ul> |
| `channels` | The channels delivered by default. Recipients may opt out, or opt in to the resource's other channels. With no `channels`, every channel is a default. |
| `severity` | `info` (default), `warning`, `urgent` or `critical`. |
| `subject`, `body` | Templates: `{event}`, `{stage}`, `{actor}`, `{case.number}`, `{case.id}`, `{case.status}`, `{case.stage}` and `{data.<form>.<input>}`. |
| `stage`, `condition` | Filters. The condition sees the case environment plus `event {name, stage, actor, detail}`. |

Every channel a rule names must be declared in `notify_channels`, or the resource does not open. `platform.Validate` reports this too, together with channels whose intents are not declared.

### Preferences

Each person keeps their own preferences:

```json
{
  "channels": {"sms": false},
  "events": {"sla.*": {"sms": true}, "sla.warning": {"sms": false}, "*": {"push": true}},
  "timezone": "Asia/Kathmandu",
  "quiet_hours": {"start": "22:00", "end": "07:00"},
  "digest": "daily",
  "digest_at": "08:30"
}
```

- **Channels.** Whether a person receives an event on a channel is decided by the first of these that applies:
  1. the most specific `events` pattern naming the channel: the exact event name, then the longest prefix pattern, then `*`;
  2. the global `channels` switch;
  3. the rule's defaults.
- **Quiet hours** are a daily window in `timezone`. A window whose end is before its start spans midnight. A notification planned in quiet hours is deferred to the end of the window (`deferred: "quiet_hours"`). **Urgent and critical notifications bypass quiet hours** and digests: they are delivered at once.
- **Digest.** `immediate` (the default), `hourly` (windows end on the local hour) or `daily` (at `digest_at`, default 08:00 local). Every notification of one person, channel and window is delivered as **one message**. A window that closes in quiet hours waits for the quiet hours to end.

Actions:

| Action | Parameters | Result |
|---|---|---|
| `pipeline.notify_prefs` | — | `{preferences, channels, events, pending}` for the caller |
| `pipeline.notify_prefs_set` | body: the preferences (or `{preferences: …}`) | The same. Invalid preferences are `422` with `details`. |

A caller reads and writes only their own preferences. The tenant and user come from the principal, and anonymous callers are refused. `pending` lists the caller's undelivered notifications with `deliver_at`, `deferred` and `digest`.

### Delivery

The steps from an event to a delivered message are:

1. **Record.** Events that a notify rule listens to are written to the pipeline outbox in the same transaction as the case change (see [pipelines.md](pipelines.md#events-and-hooks)).
2. **Plan.** When the dispatcher delivers such an event, it plans the notifications before running any hooks. It resolves the recipients, applies each person's preferences, and stores one row per person and channel. Notification ids are derived from the outbox event id, so a retried event never stores a notification twice.
3. **Flush.** The resource's background loop delivers what is due every second:
   - Each digest group becomes one call of the channel's intent.
   - Anything else becomes one call per notification.
   - A failed delivery is retried with the outbox backoff and dead-lettered after `event_max_attempts`.

The channel intent receives this input:

```json
{"channel": "email", "user": "s1", "tenant_id": "", "digest": true, "count": 3, "severity": "info",
 "subject": "3 notifications", "body": "- New work: C-1\n- New work: C-2\n- New work: C-3",
 "items": [{"id": "ntf_…", "event": "stage.entered", "stage": "assess", "severity": "info",
            "subject": "New work: C-1", "body": "", "case": {"id": "…", "number": "C-1", "pipeline": "claim"},
            "at": "2026-05-01T09:00:00Z", "deferred": "digest:daily"}]}
```

The intent resolves the user's address, typically with a lookup and `notify.send`. A failing intent makes the delivery retry, so write intents that are idempotent. Each item's `id` is stable.

**Storage.** With a database, the resource also creates two tables. The same conformance tests run them on SQLite and PostgreSQL. Both tables use only portable SQL, so MySQL works as well.

| Table | Contents |
|---|---|
| `<prefix>notify_prefs` | One row per tenant and user |
| `<prefix>notifications` | Planned notifications, with `deliver_at`, a lease, attempts and a dead-letter flag |

Without a database, both are kept in memory.

### Using the engine directly

```go
items, _ := engine.PlanNotifications(ctx, c, eventID, ev, []string{"email", "sms"},
    func(user string) (*pipeline.NotifyPreferences, error) { return store.Preferences(ctx, c.TenantID, user) }, now)
_ = store.EnqueueNotifications(ctx, items)
sent, _ := pipeline.FlushNotifications(ctx, store, now, 10, func(ctx context.Context, b pipeline.NotificationBatch) error {
    return deliver(b.Channel, b.User, b.Subject(), b.Body())
})
```

`NotifyPreferences.Schedule(severity, now)` and `QuietUntil(t)` expose the timing rules. Both are pure, so tests can inject the time.

## Limitations

- Notifications and preferences are not covered by erasure (`pipeline.erase`): subjects may quote case numbers, and preferences name users.
- Recipients are user ids. Addresses (email, phone) are the channel intent's business.
- A digest group larger than one flush batch (500 notifications) is sent as more than one message.
