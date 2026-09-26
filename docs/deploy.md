# Configuration lifecycle: revisions, review, hot swap, rollback

Every change to an application's BCL document is a **revision**. The `deploy` package takes each revision through the same steps:
1. it is validated without touching any database;
2. it is diffed against what is live;
3. it is approved by someone other than its author;
4. it is activated;
5. if needed, it is rolled back.

A **Supervisor** serves the active revision. When a new revision is activated, the swap refuses no connection. A revision that fails to build never replaces the one being served.

```
propose ─▶ pending ─▶ approved ─▶ active ─▶ superseded ─(rollback)─▶ active
              │            │          │
              ▼            ▼          ▼
          rejected     rejected    failed  (did not build; the previous revision stays live)
```

## Static validation

`platform.Validate(ctx, src, baseDir, opts)` runs every check `Compile` does before opening a resource:
- parsing, shapes, roles and entity expansion;
- document cross-references, such as routes that name unknown intents;
- that every resource kind is registered;
- every pipeline and its expressions, and every flag.

It opens nothing: no database connection, no migration, no listener. Secrets that aren't set in the validating process are reported as **warnings**, because the deployment that activates the revision may have them.

`platform.DiffDocuments(before, after)` lists every named block (resource, shape, role, intent, route, process, pipeline, entity, flag) that was added, removed or changed. Reviewers see this on each revision.

> bcl v0.0.33 accepts some incomplete expressions (`a ==`, `(a`), so a typo inside an expression can pass validation. It still fails when the revision builds, and then the running revision keeps serving.

## The workflow

```go
m := &deploy.Manager{
	Store:     store,                          // deploy.NewSQLStore(db, "postgres", "") or NewMemoryStore()
	App:       "passport",
	Secret:    []byte(os.Getenv("DEPLOY_SIGNING_SECRET")),
	Approvals: 1,                              // distinct reviewers required; 0 = no review (development)
	Validate: func(ctx context.Context, src []byte) platform.ValidationReport {
		return platform.Validate(ctx, src, baseDir, platform.DefaultLoadOptions())
	},
}
```

- **Propose** validates the document and refuses an invalid one, returning the full report. It records:
  - the source and its SHA-256 checksum;
  - an HMAC signature;
  - the author and message;
  - the diff against the active revision.
- **Approve** records a reviewer. The author can't approve their own revision unless `AllowSelfApproval` is set, the same reviewer can't approve twice, and `Approvals` distinct reviewers are required.
- **Reject** needs a reason.
- **Activate** applies only to an approved revision, after verifying its checksum and signature. A source edited directly in the store can't be activated. The previously active revision becomes `superseded`.
- **Rollback** re-activates a superseded revision: the one named, or the most recent.
- Every step is recorded in the revision's `history`.

## Serving and hot swap

```go
sup := &deploy.Supervisor{Manager: m, Build: func(ctx context.Context, src []byte) (*platform.Platform, error) {
	return platform.Compile(ctx, src, baseDir, platform.DefaultLoadOptions())
}}
ln, _ := net.Listen("tcp", ":8080")
go sup.Serve(ctx, ln)
```

The supervisor owns the listener. Each accepted connection is handed to the current generation. On activation (checked every `Poll`, or immediately via `Notify`), the swap works like this:
1. The new revision is built.
2. New connections go to it.
3. The old generation keeps serving for `Grace` (default 2s), so connections handed to it just before the swap are answered rather than closed as idle.
4. It then drains: in-flight requests finish (bounded by `Drain`, default 30s) and answer `Connection: close`, so clients reconnect to the new generation. Its resources close last.

If the build fails, the supervisor marks the revision `failed` with the error, restores the previous revision as active, and keeps serving it.

In the test suite, thousands of concurrent requests cross a swap. Requests on fresh connections never fail. As with any HTTP server that shuts down, a keep-alive client can occasionally send a request on an idle connection at the moment the old generation closes it. Standard clients retry idempotent requests in that case, and the test does the same (one retry).

## Admin API

`(&deploy.Admin{Manager: m, Supervisor: sup, Tokens: map[string]string{token: "alice", …}}).Handler()` is a `net/http` handler. Mount it on an internal port.

| Method and path | Purpose |
|---|---|
| `GET /revisions`, `GET /revisions/{id}`, `GET /active` | Read revisions; `/active` also says which revision is actually being served |
| `POST /validate {source}` | Static validation report |
| `POST /revisions {source, message}` | Propose; `422` with the report when invalid |
| `POST /revisions/{id}/approve \| reject \| activate {comment}` | Decide |
| `POST /rollback {to?, reason}` | Roll back |

Each request authenticates with `Authorization: Bearer <token>`. The token identifies the person, which is what makes the four-eyes rule enforceable. Tokens are looked up by hash, so the comparison doesn't leak timing.
