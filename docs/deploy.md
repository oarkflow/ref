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

> Expressions are checked during validation: an unclosed bracket, a dangling operator, leftover tokens (`(a`, `a ==`, `"USD" "NPR"`) or a call to a function that does not exist (`uper(name)`) makes the revision invalid. Errors that depend on data, such as dividing text, still appear only at run time.

## The workflow

```go
m := &deploy.Manager{
	Store:     store,                          // deploy.NewSQLStore(db, "postgres", "") or NewMemoryStore()
	App:       "passport",
	Secret:    []byte(os.Getenv("DEPLOY_SIGNING_SECRET")),
	Signer:    keys,                           // optional: a *signing.KeySet with an Ed25519 key
	Approvals: 1,                              // distinct reviewers required; 0 = no review (development)
	Validate: func(ctx context.Context, src []byte) platform.ValidationReport {
		return platform.Validate(ctx, src, baseDir, platform.DefaultLoadOptions())
	},
}
```

`deploy.NewSQLStore` takes the dialect `"postgres"`, `"mysql"` or `"sqlite"`. Its tests run on SQLite and on MariaDB 10.11 for the `mysql` dialect; MySQL 8 itself has not been run.

- **Propose** validates the document and refuses an invalid one, returning the full report. It records:
  - the source and its SHA-256 checksum;
  - an HMAC signature (with `Secret`) and an Ed25519 or RSA key signature (with `Signer`);
  - the author and message;
  - the diff against the active revision.
- **Approve** records a reviewer. The author can't approve their own revision unless `AllowSelfApproval` is set, the same reviewer can't approve twice, and `Approvals` distinct reviewers are required.
- **Reject** needs a reason.
- **Activate** applies only to an approved revision, after verifying its checksum and signature (see [Signing revisions](#signing-revisions)). A source edited directly in the store can't be activated. The previously active revision becomes `superseded`.
- **Rollback** re-activates a superseded revision: the one named, or the most recent.
- Every step is recorded in the revision's `history`.

## Signing revisions

A revision can carry two signatures, and `Verify` (which `Activate` and `Rollback` run) accepts either one.

- **HMAC** (`Secret`): HMAC-SHA256 over the app, sequence and checksum, stored in `signature`. Anyone who can check it can also forge it, so it only protects against edits by someone without the secret.
- **Key signature** (`Signer`, optionally `Verifier`): an asymmetric signature over `deploy.SigningPayload(r)` (`ref-revision/v1|app|seq|checksum`), stored in `key_signature` as `{alg, kid, sig}`. Ed25519 is the intended key; RSA (RS256, PS256) also works. The private key can live on the machine that proposes revisions, while the machines that activate them hold only the public key.

```go
key, _ := signing.ParsePrivateKeyPEM("deploy-2026", signing.EdDSA, pemBytes) // or signing.GenerateEd25519
keys, _ := signing.NewKeySet(key)                                           // add old public keys to rotate
m.Signer = keys                                                              // signs and, by default, verifies

// A host that only activates: public keys from a JWKS document.
public, _ := signing.ParseJWKS(jwksJSON)
m.Verifier = public
```

Verification rules:
- The source must always match its checksum.
- With neither `Secret` nor a verifier, only the checksum is checked.
- Otherwise, a valid key signature **or** a valid HMAC is required. Revisions signed before a key was added keep verifying through their HMAC, so a key can be introduced without re-signing history.
- A tampered source, a dropped signature or a signature by another key fails with `ErrTampered`.
- When `Verifier` is nil and `Signer` can verify (a `*signing.KeySet` can), `Signer` is used.

The key types, JWKS format and rotation are described in [identity-and-signing.md](identity-and-signing.md).

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
