# studio/preview

Runs a Studio draft as a live, sandboxed application and serves it at
`/preview/{draft id}/`. `*Service` implements `studio.PreviewManager`
(docs/studio-api.md) and adds `EnsureStatus`, `RequestLog`, `Outbound`,
`Status` and `Events` for hosts that want the detail.

```go
pv := preview.New(preview.Options{
    NewApp: newApp,   // same hooks as deploy.Supervisor: template engine, middleware…
    Mount:  mount,    // must call p.Mount(app)
    BaseDir: "resources/config",
    Registry: myRegistry, // host actions/drivers (default platform.NewRegistry)
})
defer pv.Close()
srv := server.New(server.Config{Preview: pv, ...})
```

## Lifecycle

- One generation per draft id. `Ensure` rebuilds only when `Draft.Version()`
  changed; concurrent `Ensure` calls for one draft build once.
- A rebuild that fails leaves the previous generation serving; the status says
  which version is being served (`Serving`) and carries diagnostics with file
  and line (from `platform.CompileBundle`, else `ValidateBundle`).
- A replaced generation keeps serving for `Grace` (1s), drains in-flight
  requests for at most `Drain` (10s), then closes and deletes its temp dir.
- Unused previews stop after `Idle` (10 min). `Stop(id)` stops one now.
- Mount and after-build panics (fh panics on a missing static root) become
  failed builds, never a crashed Studio.
- Generations are served over in-process pipes; no port is opened.

## The sandbox

`Sandbox(doc, opts)` is installed as `platform.LoadOptions.Mutate`, so the BCL
source is untouched. Outbound providers are stubbed through
`platform.WithOffline` on the compile context (scoped to that build).

| Block / kind | Preview treatment |
|---|---|
| `database.sql` | driver becomes `sqlite`, DSN a file in the generation's temp dir; `read_replica_dsn` dropped; **starts empty** (use `Options.AfterBuild` to migrate/seed) |
| `cache.file`, `queue.file`, `session.file`, `storage.fs` | `dir` moved into the temp dir |
| `secret.file` | reads an empty temp dir |
| `secret.env` | replaced by an empty `secret.file` (would read the host environment) |
| `auth.oidc` | replaced by `auth.jwt` HS256 with a throwaway key (claim mapping, issuer, audience kept); real IdP tokens are rejected |
| `service.http`, `service.llm` | config parsed and allowlist enforced, but requests are answered by a stub (`200 {"preview":true,"stubbed":true}`) and recorded; no DNS, no dial, no client-cert read |
| `service.smtp` | mail is recorded, never sent |
| `security.tcpguard` | `geoip false` (its database download and `~/.ipdata` cache are outside the sandbox); refused if the policy pack declares an HTTP `endpoint` unless `AllowGuardEndpoints` |
| `worker`, `schedule`, `trigger` | removed |
| every other built-in kind (`cache.memory/sql`, `queue.sql`, `session.memory/sql`, `store.*`, `storage.sql`, `search.sql`, `lock.*`, `ratelimit.*`, `circuit_breaker.store`, `auth.*` (rest), `authz.*`, `rules.engine`, `crypto.signer`, `org.hierarchy`, `identity.users`, `pipeline.cases`, `workflow.http`, `outbox.memory`) | kept; they only touch the sandboxed database or memory |
| unknown kind (host driver) | **build fails** unless listed in `Options.ExtraKinds` |

`TestKindPolicyCoversCatalog` fails when a built-in kind is added without a
rule here.

The environment is never read from the host: variables the source requires
(`env.required(...)`, `secret { env ... }`) get deterministic placeholders,
everything else is unset so `env("X","default")` yields its default;
`Options.Env` overrides.

The `preview` BCL profile is selected, so a document can carry
`profile "preview" { override ... }`.

## Serving under a prefix

Responses are rewritten so a page works at `/preview/{id}/`: `Location`, cookie
`Path`, root-relative `href/src/action/formaction/poster/data-src` in HTML (up
to 8 MiB), `X-Frame-Options: DENY` and CSP `frame-ancestors 'none'` are relaxed
for the same-origin iframe. Requests carry `X-Forwarded-Prefix` and
`X-Studio-Preview: 1`.

## Recording

Last 200 requests (method, path, status, duration, matched route, intent,
draft version) and last 200 stubbed outbound calls (channel, target, truncated
payload). `Requests(id)` returns both merged, oldest first, as
`studio.RecordedRequest`. The matched route is derived from method + path
against the document's routes; fh does not expose it.

## What preview cannot faithfully show

- **It is a guard against accidents, not a security boundary.** A trusted
  editor's document can still read files the process can read (`import`s are
  off, but `rules.engine dir`, `crypto.signer *_file`, `tcpguard path`,
  templates and static roots are read from the host).
- Data: the database starts empty; behaviour that depends on production data
  needs `AfterBuild` seeding.
- Time and events: no workers, schedules or inbound webhooks; async jobs are
  enqueued but never consumed.
- Outbound: every HTTP call gets the same canned 200 response; flows that
  branch on a real response body will take the default path.
- tcpguard: rules keyed on country see no location; policy packs with HTTP
  endpoints are refused rather than intercepted (tcpguard uses
  `http.DefaultClient`).
- Templates and static files come from the host filesystem, not from the draft.
- Links assembled by JavaScript at run time (`fetch("/api")`) are not
  rewritten; use `X-Forwarded-Prefix`.
- `document.environment "production"` still applies (e.g. an ephemeral
  `crypto.signer` is refused).
- Session cookies are `Secure` only if the document says so; over plain HTTP
  set `secure false` for preview.
- Two known pitfalls outside this package: `platform.Validate` warns about
  process-local resources when a replica id is set (preview sets none), and the
  root module pins an older `oarkflow/template` than `examples/starter`, whose
  SPL rejects the starter's login page in secure mode.
