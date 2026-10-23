# Autonomous escalation with `aegis run -respond=on`

A runnable demo of the respond controller (`aegis/respond`): integrity checks
feed it signals, and it restricts, isolates and finally replaces the process
without anyone in the loop.

```bash
examples/respond/run.sh
```

It seals [`app/`](app) into a signed, encrypted package, signs a policy, runs
the app clean, then runs it again with `FRIDA_VERSION` set (a variable the
integrity `environment` check treats as instrumentation). Last verified output:

```
== Run clean
GET  /hello -> 200
POST /hello -> 200
respond events after 3s: 0

== Run under simulated instrumentation
  GET=200 POST=403 livez=200      restricted: writes blocked
  GET=503 POST=503 livez=200      isolated: only liveness answers
  GET=000 POST=000 livez=000      regenerate: process gone
process exit code: 75
```

## The pieces, by hand

```bash
# package signing + encryption keys, then seal the app
aegis keygen -out pkg
aegis package -dir examples/respond/app -key pkg.key -enckey pkg.enc -out app.apkg

# a separate key for escalation policy; keep policy.key offline
aegis respond-keygen -out policy
aegis respond-sign -key policy.key -out policy.json \
  -thresholds 0.3,0.8,1.6,2.5 -max-regenerations 3 -quiet-period 15m

# run with the controller on
aegis run app.apkg -pubkey pkg.pub -key pkg.enc \
  -respond=on -respond-policy policy.json -respond-pubkey "$(cat policy.pub)"
```

Without `-respond-policy` the built-in default policy is used, and the banner
says it is unsigned.

## What each level does in `aegis run`

| Level | Trigger (default policy) | Effect |
|---|---|---|
| watch | score 0.3 | none; evidence is logged |
| restricted | score 0.8 | non-GET/HEAD/OPTIONS requests get 403 |
| isolated | score 1.6, two sources or one at confidence 0.95 | everything 503 except `/livez`, `/ping` |
| regenerate | score 2.5 | wipe memory, exit 75 (restart me) |
| safe-mode | regeneration budget spent, or an action fails to verify | wipe, exit 76 (do not restart) |

Quiet periods relax restricted and isolated one level at a time. Regenerate
and safe-mode end the process.

## Supervisor example (systemd)

Exit 75 should restart; exit 76 should not:

```ini
[Service]
EnvironmentFile=/etc/aegis/key.env   # AEGIS_KEY=...; mode 0600, scrubbed from the process at start
ExecStart=/usr/local/bin/aegis run /srv/app.apkg -pubkey /etc/aegis/pkg.pub \
  -respond=on -respond-policy /etc/aegis/policy.json -respond-pubkey <hex>
Restart=on-failure
RestartPreventExitStatus=76
RestartSec=2
StartLimitIntervalSec=300
StartLimitBurst=5
```

`StartLimit*` matters: if the attacker is still present after a restart the
controller escalates again, and the limit stops an endless restart loop.

## Using the controller as a library

[`aegis/respond/example_test.go`](../../aegis/respond/example_test.go) is a
self-contained walk through signing a policy, feeding signals and reading the
evidence log: `go test -v -run Example ./aegis/respond`. To protect something
other than an `aegis run` process, implement `respond.Enforcer` (`Apply` and
`Verify`) against your own sidecar, host agent or cloud API and, optionally,
`respond.Capable` to declare which capabilities it can really revoke.

## Limits

- The `LocalEnforcer` used here runs inside the protected process. Code
  running in that process can bypass it. Blocking egress, process spawning
  and transfers needs an OS sandbox or a sidecar (the log shows these as
  `unenforceable`).
- `-integrity-policy=warn` is used by the script only so the process can start
  with the simulated attack already present. Keep the default (`enforce`) in
  production.
- `-solo-confidence 0.8` in the script shortens the demo. The default (0.95)
  stops this single detector at restricted until a second source agrees.
