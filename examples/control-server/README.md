# Control server: key release

Phase 4 of [`docs/aegis-protection-plan.md`](../../docs/aegis-protection-plan.md): a package is sealed once with `aegis package` and distributed to as many machines as need it; each one fetches the same decryption key from a control server at start-up instead of carrying a local key file, and that access can be revoked centrally at any time.

```bash
./check.sh
```

## Walkthrough

```bash
aegis keygen -out k
aegis compile -src math/main.go -out math.air
aegis package -module math.air -key k.key -enckey k.enc -out math.apkg

# A real control server, with a store that survives a restart.
aegis serve -addr :8080 -store control.db -key k.key &

# Tell it this package's key (read locally, from the same k.enc the package
# was sealed with — proved to actually open the package before it is sent).
aegis register -pkg math.apkg -enckey k.enc -server http://127.0.0.1:8080

# Any machine with math.apkg and network access to the server — no local key.
aegis run -pkg math.apkg -grant-server http://127.0.0.1:8080 -pubkey k.pub -func Add -args 10,20
# Result: 30

# Revoke it centrally. Every machine's next run is refused immediately.
aegis revoke -package-id <id from `aegis inspect -pkg math.apkg`> -reason "key compromised" -server http://127.0.0.1:8080
aegis run -pkg math.apkg -grant-server http://127.0.0.1:8080 -pubkey k.pub -func Add -args 10,20
# aegis: run: fetch grant from http://127.0.0.1:8080: control: package ... is revoked: key compromised
```

## What `check.sh` proves

* `register` → `run -grant-server` actually fetches the key and runs correctly, for more than one exported function.
* `-key` and `-grant-server` together are refused: they are two ways to get the same thing, not two inputs to combine.
* `revoke` takes effect on the very next run — no caching, no delay.
* **The control server survives a restart.** It is stopped, restarted against the same `-store` file on a different port, and the revocation from before the restart is still enforced; a new package registered after the restart still works. `MemoryStore` (the default with no `-store`) would have forgotten all of this.
* **The rate limiter actually limits**, and does so without starving liveness checks: a server given a deliberately tiny burst is driven past it from the same IP and gets `429`s, on its own port with no other traffic — proving the limit itself works without the earlier, more realistic traffic in this same script tripping it. `/healthz` is then hit the same number of times immediately after exhausting that same burst and is never limited, because a load balancer's liveness probe polling it must never share a quota with the sensitive endpoints that actually hand out keys.

## What this does not do

* **No authentication on `register` or `revoke`.** Anyone who can reach the control server can register or revoke any package. This needs a real answer (an admin token, mTLS, or both) before the server is exposed beyond a trusted network — a different problem from key release itself, and deliberately not solved here.
* **No TLS of its own.** Run the control server behind a reverse proxy or your own TLS termination; `aegis serve` speaks plain HTTP.
* **No hardware attestation.** `GrantService`'s attestation hook exists (`control.WithAttestRegistry`) and this example does not configure it — opt in when you need it.
* **Two different key-release models exist, and this example uses one of them.** The default (`RequestGrant` with no extra option) derives a fresh key per session with HKDF, which only decrypts a package built fresh for that exact, predetermined session — see `TestRuntimeIntegration_EndToEnd` in `aegis/control/control_test.go` for that workflow. `aegis serve` instead defaults to `WithRawMasterKeyGrants()` (hands out the package's exact key, matching `aegis package`/`aegis register`), which is what this example — and every other packaging command in this repository — assumes. Pass `-session-keys` to `aegis serve` if you actually want the per-session-build workflow instead.
