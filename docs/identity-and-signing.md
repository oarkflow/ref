# Identity and signing

This page covers two built-in pieces that work together:
- **`identity.users`**: a SQL-backed directory of users, their tenant memberships, invitations and an audit trail. Its `identity.login` action verifies a password and issues a JWT through an `auth.jwt` resource.
- **`crypto.signer`**: asymmetric signing keys (Ed25519, RSA-PSS, RSA-PKCS1v15, all with SHA-256) with key ids, rotation and a JWKS export. One key set signs JWTs, pipeline certificates, deploy revisions and arbitrary payloads.

The worked example is [examples/identity](../examples/identity/app.bcl). Its end-to-end test runs on SQLite, and on PostgreSQL when `TEST_POSTGRES_DSN` is set.

## Signing keys: `crypto.signer`

```bcl
resource "keys" {
  kind "crypto.signer"
  config {
    private_key env.required("SIGNING_KEY")   # PKCS#8 or PKCS#1 PEM; or private_key_file "/run/keys/signing.pem"
    key_id "2026-01"                          # default: the key's RFC 7638 thumbprint
    algorithm "EdDSA"                         # EdDSA (default), RS256 or PS256; an Ed25519 key is always EdDSA
    keys [                                    # verification-only keys, for rotation
      { key_id "2025-01" public_key_file "/run/keys/2025-01.pub.pem" algorithm "EdDSA" }
    ]
  }
}
```

- **Key material.**
  - `private_key` or `private_key_file` holds the active signing key. Inline PEM may use literal `\n` for newlines, which is convenient in environment variables.
  - Generate an Ed25519 key with `openssl genpkey -algorithm ed25519 -out signing.pem`.
  - RSA keys must have at least 2048 bits.
- **Rotation.** Make the new key active and move the old one into `keys`. Anything the old key signed keeps verifying: tokens, certificates and payloads carry the `kid` of the key that signed them. A retired key configured with its private half is loaded as public-only, so it can't sign.
- **Algorithm pinning.** The algorithm belongs to the key. A signature that names another algorithm than its key's is refused.
- **Development.** `ephemeral true` generates an Ed25519 key at startup when no private key is configured. Signatures made with it don't survive a restart.

| Action | Config | Result |
|---|---|---|
| `crypto.sign` | `value_fact` (default `input`) | `{alg, kid, sig}`: a signature over the value's canonical JSON |
| `crypto.verify` | `value_fact` (`input.payload`), `signature_fact` (`input.signature`), `require_valid` | `{valid, kid, alg}`; with `require_valid true`, an invalid signature fails with `INVALID_SIGNATURE` (422) |
| `crypto.jwks` | — | `{keys: [...]}`: the public keys, active first. It also works on an `auth.jwt` resource that has a `signer`. |

**Canonical JSON.**
- Object keys are sorted, there is no insignificant whitespace, HTML is not escaped, and numbers keep their decimal text.
- Two payloads that are equal as JSON produce the same bytes, whatever their key order.
- It follows RFC 8785, except that numbers are not re-serialised as IEEE doubles.
- In Go, `signing.Canonical(v)` produces it, and `KeySet.SignJSON` / `VerifyJSON` wrap it.

**Publishing the keys.**

```bcl
intent "jwks" {
  response "jwks"
  node "jwks" { uses "crypto.jwks" resource "keys" provides [jwks] }
}
route "jwks" { method GET path "/.well-known/jwks.json" intent "jwks" cache_control "public, max-age=300" }
```

Ed25519 keys appear as `{"kty":"OKP","crv":"Ed25519","x":…}` (RFC 8037) and RSA keys as `{"kty":"RSA","n":…,"e":…}`; each has `kid`, `alg` and `use: "sig"`. Offline, `signing.ParseJWKS(doc)` returns a verification-only `*signing.KeySet`.

### Where the keys are used

| Use | Configuration | Verified by |
|---|---|---|
| JWTs | `auth.jwt { config { signer "keys" } }` | the same resource, by `kid`; external services through the JWKS |
| Pipeline certificates | `pipeline.cases { config { signer "keys" } }` | `pipeline.verify`; offline with `pipeline.VerifyCertificateSignature(cert, keys)` |
| Deploy revisions | `deploy.Manager{Signer: keySet}` | `Manager.Verify` (see [deploy.md](deploy.md#signing-revisions)) |
| Arbitrary payloads | `crypto.sign` / `crypto.verify` | `crypto.verify`; offline with `KeySet.VerifyJSON` |

## JWTs with EdDSA, RS256 and PS256

`auth.jwt` has always verified HS256/384/512, RS256/384/512 and ES256/384/512 against a configured secret or PEM key. It now also supports:
- `EdDSA` (Ed25519) and `PS256/384/512` (RSA-PSS), both with `public_key`/`private_key` PEM configuration and in OIDC JWKS documents (`OKP` keys).
- `signer "keys"`: the key set replaces `secret`/`public_key`/`private_key`, and setting both is an error.
  - The active key signs every token the resource issues, and its `kid` goes in the header.
  - Every key in the set verifies tokens that carry its `kid`, so tokens issued before a rotation stay valid until they expire.
  - The algorithm is taken from each key, never from the token header. An `HS256` token forged with a public key as the secret is refused.

```bcl
resource "jwt" {
  kind "auth.jwt"
  config {
    signer "keys"
    issuer "https://app.example.com"
    audience "app"
    ttl "1h"
  }
}
```

## The user directory: `identity.users`

```bcl
resource "users" {
  kind "identity.users"
  config {
    database "db"                    # database.sql: SQLite, PostgreSQL or MySQL
    table_prefix "identity_"         # identity_users, _memberships, _invitations, _audit
    token_issuer "jwt"               # auth.jwt that identity.login signs with
    admin_roles ["admin"]            # membership roles that administer a tenant
    global_roles ["platform_admin"]  # principal roles that administer every tenant
    invite_ttl "72h"
    password_min_length 12
    max_failed_logins 10             # then a silent 15m lock (lockout "15m"); 0 disables
    argon2_memory 65536              # KiB; argon2_iterations 3, argon2_parallelism 4
    bootstrap {                      # created once, while the directory has no users
      email env.required("ADMIN_EMAIL")
      password env.required("ADMIN_PASSWORD")   # or password_hash "$argon2id$…"
      tenant "acme"
      roles ["admin"]
    }
  }
}
```

The tables are created at startup (`migrate false` to skip), with the same DDL on every dialect. Statements use `$n` placeholders, rebound for MySQL. Timestamps are stored as Unix milliseconds and returned as RFC 3339.

| Table | Holds |
|---|---|
| `users` | id, email (unique, lower-cased), name, argon2id `password_hash`, `status` (`active`, `suspended`, `invited`), `mfa_enabled`/`mfa_secret`, failed-login counter and lock, `created_at`, `updated_at`, `last_login` |
| `memberships` | tenant × user → roles (JSON array) and an optional `org_unit` |
| `invitations` | SHA-256 of the token, email, user, tenant, roles, org unit, inviter, expiry, `accepted_at` |
| `audit` | who did what to whom in which tenant, with a JSON detail |

### Sign-in

```bcl
intent "auth.login" {
  response "session"
  node "session" {
    uses "identity.login"
    resource "users"
    kind effect
    requires [input]
    provides [session]
    # email_fact "input.email"  password_fact "input.password"
    # tenant_fact "input.tenant_id"  code_fact "input.code"  ttl "15m"
  }
}
route "auth.login" {
  method POST
  path "/api/auth/login"
  intent "auth.login"
  cache_control "no-store"
  rate_limit { limiter "limits" limit 20 window 1m }
}
```

- **Response.** It returns `{token, token_type, expires_at, user, tenant_id, roles, org_unit}`.
- **Token claims.** The token carries:
  - `sub`, `email` and `preferred_username`;
  - `tenant_id` and `roles` from the membership in the chosen tenant;
  - `name`;
  - `org_units: [org_unit]` when the membership has one. That is `org.hierarchy`'s default assignment claim, so the user is scoped to that unit with no further wiring.
- **Choosing a tenant.** A user with one membership needs no `tenant_id`. A user with several must choose one, or gets `TENANT_REQUIRED` listing them. Asking for a tenant the user doesn't belong to is `PERMISSION_DENIED`.
- **Failures are uniform.** Unknown email, wrong password, a pending invitation and a locked account all return `INVALID_CREDENTIALS` (401). Each performs one argon2id verification with the directory's own parameters, so neither the response nor its timing reveals whether an account exists.
- **Rate limiting.** Because every failure is the same response, a route `rate_limit` keyed on the remote address can do the brute-force defence without leaking anything.
- **Lockout.** After `max_failed_logins` consecutive failures, the account is locked for `lockout`. The lock is silent: it looks like any other failure.
- **Suspended accounts** can't sign in. Only after the password has been verified is the answer `ACCOUNT_SUSPENDED` (403), so the status is never revealed to someone who doesn't know the password.
- **MFA-ready.** When `mfa_enabled` is set on a user, sign-in also requires a TOTP `code` (RFC 6238, 30-second steps, ±1 step), and returns `MFA_REQUIRED` without one. Enrolment isn't built in yet: set `mfa_secret` (base32) and `mfa_enabled` from your own flow.

### Invitations

- **Inviting.** `identity.admin operation "invite"` takes `{email, name, roles, org_unit}`.
  - If the email is unknown, it creates the account with status `invited` and no password.
  - It returns `{token, email, user_id, tenant_id, roles, org_unit, expires_at}`.
  - The plaintext token appears only in this response, meant for an email node; only its SHA-256 is stored.
  - Inviting an existing member is a conflict.
- **Accepting.** `identity.accept_invite` takes `{token, password, name}`.
  - A new account sets its password (at least `password_min_length`, otherwise `WEAK_PASSWORD`) and becomes `active`.
  - An existing account must give its current password instead, so an invitation can add a membership but can never take an account over.
  - A failed attempt does not consume the invitation.
- **Single use.** The token is consumed by a conditional `UPDATE … WHERE accepted_at = 0 AND expires_at > now`. Of two racing requests, exactly one succeeds. Expired, used and unknown tokens all fail with the same `INVALID_INVITATION` (422).

### Administration: `identity.admin`

One action with an `operation`. Every operation first checks that the caller administers the target tenant. The caller must either:
- hold one of `global_roles`, or
- hold one of `admin_roles` in their membership of that tenant, as **read from the database**, while their account is active.

The roles in the token are not trusted for this. Demoting or suspending an admin takes effect on their next request, not when their token expires. Guard the routes with `authz { roles [...] }` as well, as a first filter.

The target tenant is `tenant_fact`, else `input.tenant_id`, else the request's tenant (the token's `tenant_id`). The target user is `user_fact`, else a `:id` or `:user_id` path parameter, else `input.user_id`.

| Operation | Input | Result |
|---|---|---|
| `invite` | `{email, name, roles, org_unit}` | the invitation, with its token |
| `list_users` | — | the tenant's members, with roles and org units |
| `list_invitations` | — | pending invitations (never their tokens) |
| `get_user` | user | the member (a global admin also sees every membership) |
| `suspend`, `reactivate` | user | the member |
| `add_membership` | `{user_id or email, roles, org_unit}` | the member |
| `change_membership` | user, `{roles?, org_unit?}` | the member |
| `remove_membership` | user | `{removed: true}` |
| `audit` | `?limit=` (default 100, max 500) | the tenant's audit entries, newest first |

Rules the directory enforces, inside the transaction that makes the change:
- **A tenant keeps an administrator.** Removing, demoting or suspending the last active admin of a tenant fails with `LAST_ADMIN` (409). The check reads every membership of the tenant with `FOR UPDATE` on PostgreSQL and MySQL, so two concurrent demotions serialise. On SQLite, a process-wide lock serialises them.
- **Nobody changes their own account status.**
- **Suspension is account-wide.** A tenant admin may only suspend someone who belongs to no other tenant; otherwise it takes a global role.
- **Every change is audited.** Changes, invitations, acceptances, password changes and sign-ins append an audit entry in the same transaction.

### Passwords

`identity.change_password` needs a signed-in principal and takes `{current_password, new_password}`. It verifies the current password, applies the minimum length, hashes the new one with argon2id and clears any lock. Hashes use the directory's argon2id parameters. `VerifyPassword` also accepts bcrypt hashes, so users imported from an older system keep working.

## Limitations

- MySQL is supported by the DDL and statements but is not exercised by the test suite. SQLite and PostgreSQL are.
- There is no built-in MFA enrolment or recovery-code flow; the columns and the sign-in check are in place.
- Password reset by email is not part of `identity.users` yet. Compose it from `auth.reset_token` and your own table for now.
- Revoking a signing key means removing it from the set. Anything it signed stops verifying immediately, which is usually the point.
