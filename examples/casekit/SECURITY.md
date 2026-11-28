# Casekit production security

Run Casekit with `-demo` only on loopback for local demonstrations. The demo
mode creates sample bearer tokens and uses a stub liveness service; startup now
refuses to expose it on a non-loopback address.

Production startup requires explicit values for `CASEKIT_ADMIN_EMAIL`,
`CASEKIT_ADMIN_PASSWORD`, `CASEKIT_SESSION_SECRET`, `CASEKIT_DB_DRIVER`,
`CASEKIT_DSN`, `CASEKIT_LIVENESS_URL`, and `CASEKIT_LIVENESS_TOKEN`. Use
`CASEKIT_DB_DRIVER=pgx` or `mysql`; SQLite is reserved for local use. Set
`CASEKIT_COOKIE_SECURE=true` and use HTTPS from the browser through the trusted
reverse proxy. The liveness endpoint must use HTTPS. Keep all secrets in a
secret manager, use independent random 32-byte-or-longer values for signing and
session secrets, and never put them in source control or logs.

Browser password sign-in uses a server-side SQL session and an HttpOnly,
Secure, SameSite cookie. Bearer tokens remain available for service clients
and the demo, but the browser keeps them in memory only. Sessions and rate
limits use the configured SQL database so replicas share their state. Keep that
database on a private network; use `sslmode=verify-full` for pgx or `tls=true`
for MySQL. Restrict its credentials to this application, encrypt storage and
backups, and regularly rehearse restoring a backup.

Casekit handles identity and health information. The data owner must define
retention and deletion periods for cases, liveness artifacts, inbox messages,
and audit records before production use. Apply and verify that policy in the
database and backup lifecycle; do not infer a retention period from the sample
workflow. Review access roles and workspace assignments whenever staff change,
and rotate secrets after suspected exposure.
