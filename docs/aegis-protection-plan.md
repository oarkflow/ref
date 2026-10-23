# Plan: protecting the build and code with Aegis on Windows, Linux and macOS

## Goal

No proprietary code or logic is persisted on disk in readable form, on any of the three platforms, and what is persisted cannot be turned into the application with a decompiler or an AI.

## Principle

A native executable has to exist as a file to be started, on every OS. Linux can start one from memory (`memfd_create`); Windows and macOS cannot do it without malware techniques (reflective loading, process hollowing) that EDR and notarization reject. So the design does not try to hide a native binary. It makes the native binary **contain nothing worth stealing**, and delivers everything valuable encrypted, into memory only:

| Layer | What it is | On disk |
|---|---|---|
| **A. Generic runtime** | The `ref`/`aegis` executable: stripped, path-free, signed, identical for every customer, no application logic | Yes, and that is acceptable |
| **B. Sealed bundle** | BCL, SPL, templates, static files in an encrypted, signed `.apkg` (exists today) | Ciphertext only; plaintext only in memory |
| **C. Key release** | The bundle key is released at start-up by a control server, after attestation, with a short lease | Never |
| **D. Proprietary Go logic** | Either removed (BCL/SPL cover it), compiled to Aegis bytecode and shipped inside B, or kept on your servers | Not as native code |

Linux gets an optional stricter mode where even layer A is started from memory (phase 6).

## Original audit (now addressed; see Progress below for what changed)

* Works: AES-256-GCM sealing, tamper and wrong-key rejection, signature check with `-pubkey`, zero files written by a sealed run (tested), core-dump/ptrace/swap protection on Linux, `PT_DENY_ATTACH` on macOS. Still true, unchanged.
* ~~Windows does not build.~~ Fixed in phase 0.
* ~~The binary carries 263 source paths and about 2,100 symbols (no `-trimpath`, no strip, no obfuscation).~~ Fixed in phase 2 (`make release`); garble (`make release-obfuscated`) additionally removes the runtime function-name strings that stripping alone cannot reach.
* ~~`aegis run <dir>` prints "ENFORCED" with no protection. Signature checking is optional. Passphrase keys are one unsalted SHA-256. The packager would seal a `.env` or key file.~~ Fixed in phase 1.
* `make secure` (the example Makefiles) still stores the decryption key beside the package on the same machine — unchanged, and out of scope until phase 4 (key release) exists to do better.
* Obfuscation passes run only in `aegis lab` (unchanged) and separately in `make release-obfuscated` via garble (phase 2) — two different mechanisms for the same goal; `lab`'s own transform passes are not yet wired into `aegis compile`'s bytecode path (phase 7, not started).
* The control server and attestation are not used to start an app, and the control server stores state in memory only (unchanged; phase 4, not started).
* The Go-to-bytecode compiler supports numeric, pointer-free Go only (unchanged; phase 7 deliberately does not extend this).

## Phases

Each phase ends with a check that fails when the phase is not done. Phases 1, 2 and 3 are independent of one another and can run in parallel after phase 0.

### Phase 0: all three platforms build and are tested (blocker)
* Fix the Windows build: patch or replace `interpreter/pkg/repl` (build-tag the signal use upstream, or a `replace` to a fixed fork). Confirm nothing in the module imports the REPL at run time.
* Add a CI matrix: `{linux, macos, windows} x {amd64, arm64}`, building `aegis`, `ref` and running `go vet` and the tests.
* **Done when:** `GOOS=windows go build ./...` passes and the matrix is green.

### Phase 1: honest and safe by default
* `aegis run <dir>` prints "UNPROTECTED (development)" and drops the isolation claims; the "ENFORCED" text appears only for a sealed run, and lists only the protections that were actually applied.
* A sealed run requires `-pubkey` (or a pinned key compiled into the runtime) and refuses an unsigned or wrongly signed package. A flag `-insecure-skip-verify` exists for development and is loud.
* Keys: derive passphrase keys with Argon2id (already in the repo) with a stored salt and cost; read keys from stdin, an OS secret store (Keychain, DPAPI/Credential Manager, Secret Service) or the control server; never from argv.
* Packager: a deny-list (`.env*`, `*.key`, `*.pem`, `*.p12`, `.git`, `.data`, `*.apkg`) that fails the build instead of skipping; a manifest of everything sealed.
* **Done when:** tests cover each refusal (unsigned, forged, secret file in the directory, weak passphrase).

### Phase 2: hardened build pipeline
* `make release` builds each target with `-trimpath -ldflags="-s -w -buildid="`, then `garble -literals -tiny` (decide in phase 2 whether garble's cost in size, speed and reflection-heavy code is acceptable; BCL decoding uses reflection, so test it).
* Sign: Authenticode (Windows), `codesign` + notarization with the hardened runtime and no `get-task-allow` (macOS), detached signature/cosign (Linux). The runtime verifies its own bundle's publisher key.
* `scripts/verify-build.sh`: fails if the binary contains source paths, package names of the proprietary code, dev default keys, or exported symbols beyond an allow-list.
* **Done when:** the verify script passes for all six targets in CI.

### Phase 3: process hardening on every platform
Replace the "not implemented" stubs and keep the report honest (what applied, what was skipped, why).

| Protection | Linux | macOS | Windows |
|---|---|---|---|
| No crash dumps | `RLIMIT_CORE=0` (exists) | exists | disable WER dumps for the process, `SetErrorMode`, no minidump on crash |
| Block debugger/memory read | `PR_SET_DUMPABLE=0` (exists); optional seccomp | `PT_DENY_ATTACH` (exists); hardened runtime | restrictive process DACL, `SetProcessMitigationPolicy` (block non-Microsoft DLLs, strict handle checks); run as a protected-light process where available |
| Keep secrets out of swap | `mlockall` (exists) | `mlock` on key buffers | `VirtualLock` on key buffers |
| Debugger detection | exists | exists | `IsDebuggerPresent`, `NtQueryInformationProcess(DebugPort)`, remote-debugger flag |
| Injection detection | `LD_PRELOAD` and similar (exists) | `DYLD_*` (exists) | loaded-module allow-list, known hooking DLLs |
| Wipe key and plaintext after use | exists | exists | exists (portable) |

Make the integrity checks run **before** decryption and refuse to start on failure (a policy setting: `enforce` or `warn`).
* **Done when:** a test per OS asserts the report lists what is expected for that OS.

### Phase 4: key release and attestation
* `aegis run <app.apkg>` obtains its key from the control server through `HTTPGrantProvider`: mutual TLS, a request bound to the package id, the machine, and an attestation, and a short lease. The key is held in locked memory and zeroed when the lease ends.
* Attestation providers per platform: TPM 2.0 (Windows and Linux), Secure Enclave-backed key (macOS), with a fallback of a license token plus mTLS for machines without hardware support, recorded as a weaker tier.
* Control server: persistent store (replace `MemoryStore`), revocation, audit log, rate limits, key rotation. Grants are denied for revoked customers, versions or machines.
* Offline mode (policy-controlled): a time-limited grant cached encrypted under a TPM/Secure Enclave key, never in plaintext.
* **Done when:** start-up fails with a revoked grant, a wrong machine, an expired lease and a forged attestation, and succeeds otherwise.

### Phase 5: zero-disk at run time
The sealed run already writes nothing. Make that a guarantee rather than an accident:
* A `secure` storage mode: databases, queues, caches, sessions, uploads and logs go to memory, or to an encrypted store under a per-run key that is not persisted. Config that points at a file path in this mode is a load error.
* A test harness that runs a sealed app on each OS under a file-system watcher and fails on any file creation outside an allow-list.
* **Done when:** the harness passes on all three OSes for the example apps.

### Phase 6: strict launcher (Linux only, optional)
* A small launcher (the only native file) fetches the encrypted runtime and key, decrypts into `memfd_create`, seals the memfd and starts it with `fexecve`. Nothing but the launcher is on disk.
* Windows and macOS keep layer A on disk by design; the documentation states this plainly instead of claiming otherwise.

### Phase 7: proprietary Go logic
Pick per piece of logic, in this order of preference:
1. Express it in BCL/SPL (travels inside the sealed bundle, already protected).
2. Compile to Aegis bytecode (numeric, pointer-free Go), ship it sealed, and wire the obfuscation passes (`transform`) and strip/diversify into `aegis compile` behind flags; the `lab` benchmark becomes a regression gate.
3. Keep it behind your own API.
Do not extend the bytecode compiler to full Go; the cost is high and the benefit small compared with 1 and 3.

### Phase 8: verification and release gate
`scripts/protect-check.sh`, in the spirit of `examples/dynamic-definitions/check.sh`, runs on every OS in CI: build, verify-build, package, tamper, forged signature, wrong key, revoked grant, zero-disk watcher, and a scan of the sealed file for plaintext markers. A release is blocked if any step fails.

## Per-platform summary

| | Linux | macOS | Windows |
|---|---|---|---|
| Native runtime on disk | optional memory-only (phase 6) | yes, generic and signed | yes, generic and signed |
| Bundle plaintext on disk | never | never | never |
| Key release | TPM attestation | Secure Enclave attestation | TPM attestation |
| Process hardening | full | full | full after phase 3 |
| Build target today | works | works | **fails (phase 0)** |

## Limits that remain

Root (or Administrator) on the machine, a hypervisor, or a cold-boot attack can read process memory while the application runs; hardware-backed isolation (SGX/SEV/TDX, `aegis/platform/confidential`) is the only answer to that and is outside this plan. Everything here raises the cost and makes theft traceable and revocable; it does not make it impossible.

## Decisions made

1. **Garble**: acceptable if it works correctly and isn't overhead. Measured on this machine, building `./aegis/cmd/aegis`: a cold build with `-trimpath -ldflags="-s -w -buildid="` alone takes 16.5s and produces a 30.9MB binary; the same build through `garble -literals -tiny` takes 70.9s (4.3x) and produces a 103.7MB binary (3.4x). No runtime startup-time difference was measurable. Correctness: a garbled binary ran the full `dynamic-definitions` example (node types, helpers, handlers, authz, signature verification) with no observed difference in behavior. Given the measured build-time and size cost, `make release` defaults to the plain build; `make release-obfuscated` runs garble, documented with these numbers so the tradeoff is explicit rather than assumed. **Important finding that justifies garble despite its cost:** `-s -w` strips only the symbol table `go tool nm` reads. Go's runtime keeps function-name strings for its own panic and stack-trace machinery independent of that table — the plain binary had roughly 9,700 occurrences of `github.com/oarkflow/` as plain, `strings`-readable text; `go tool nm` reported zero either way. Garble is the only one of the two that actually removes this (verified: 0 occurrences after garble). `scripts/verify-build.sh` checks for exactly this and fails a plain build on it by default (`--allow-package-paths` acknowledges the known gap).
2. **Control server**: both self-hosted and customer-hosted, for any kind of operation. Not yet built (phase 4); the `HTTPGrantProvider` and `GrantService` scaffolding already support either deployment shape.
3. **Offline operation**: not decided yet; revisit when phase 4 starts.
4. **Hardware attestation**: opt-in, later. Phase 4 is deferred; when it starts, attestation is a policy an application can turn on, not a requirement every deployment must meet.
5. **Linux strict launcher (phase 6)**: deferred.

## Progress

* **Phase 0 (all platforms build): done.** Two upstream fixes were needed and are now published: `github.com/oarkflow/interpreter` v0.0.15 (the SPL REPL and the untrusted-script worker used `syscall.SIGTSTP`/`syscall.SysProcAttr{Setpgid}`/`syscall.Kill`, none of which exist on Windows) and `github.com/oarkflow/zlog` v0.0.4 (`platform.go` imported `log/syslog` unconditionally, itself a Unix/Plan9/JS-only package). No local `replace` directives remain anywhere in this repo. `aegis`, `serve`, `platform` and `cmd/ref` all build clean for `{windows,linux,darwin} x {amd64,arm64}`.
* **Phase 1 (honest and safe by default): done.**
  * `aegis run <dir>` prints "UNPROTECTED (development)" and makes no isolation claim; it does not run `harden.Apply` at all, deliberately, so a developer's own debugger still attaches.
  * A `.apkg` run refuses without `-pubkey` unless `-insecure-skip-verify` is passed, which prints a loud stderr warning every time it is used.
  * The host-integrity check (debugger, injected library, timing anomaly) now runs **before** decryption, not after — it used to run against the already-decrypted plaintext, which only proved the check itself ran under whatever was attached. `-integrity-policy` is `enforce` (default, refuses to start) or `warn` (logs and continues).
  * A bare passphrase is derived with Argon2id and a random salt (`aegis/pkg/kdf.go`); the salt and cost parameters travel in the manifest (not secret) so `aegis run` reproduces the exact key from the same passphrase. A key file or 64-character hex string is unaffected. The manifest's `key_derivation` field now says what actually happened (`none` or `argon2id`) instead of unconditionally claiming `HKDF-SHA256`, which never actually ran.
  * The packager refuses outright (not a silent skip) on `.env*`, `*.pem`, `*.key`, `*.p12`, `*.pfx`, `id_rsa*`, `id_ed25519*` found in the application directory, and prints the manifest of every file it did seal.
* **Phase 2 (hardened build pipeline): done**, garble made opt-in per decision 1 above. `make release` and `make release-obfuscated` build `aegis` and `ref` for all six targets; `make verify-release` runs `scripts/verify-build.sh` against every binary produced. Not done: code signing (Authenticode/codesign/cosign) — this needs real signing identities/certificates this environment does not have; the Makefile targets and this doc describe where it plugs in, for whoever holds those credentials to wire up.
* **Phase 3 (process hardening on every platform): done.** Windows had only "not implemented" stubs before this; it now gets `SetErrorMode` (no crash dialogs), `SetProcessMitigationPolicy(ExtensionPointDisable)` (blocks legacy `AppInit_DLLs`-style injection) and `SetProcessMitigationPolicy(ImageLoad)` (refuses to load a DLL from a network path or a low-integrity location), plus real debugger detection (`IsDebuggerPresent`, `CheckRemoteDebuggerPresent`) replacing Windows's previous behavior of unconditionally reporting "no debugger attached" regardless of whether one was. Two gaps the plan's table named are genuinely unavailable to an ordinary Windows process and are reported as honest skips rather than faked: denying a privileged debugger's attach (no API without a kernel driver) and locking the whole process out of swap (`mlockall` has no per-process Windows equivalent without `SeLockMemoryPrivilege`, which is rarely granted). `harden.LockBuffer`/`UnlockBuffer` cover the narrower, achievable version — `VirtualLock` on the specific key buffer — and `aegis run` calls it on the resolved encryption key.
* **Phase 5 (zero-disk guarantee as policy): done.** `storage_policy "ephemeral"` (`platform/storage_policy.go`) refuses to load a document whose resources would write to a real disk path — `cache.file`, `queue.file`, `queue.broker`, `session.file`, `storage.fs`, or `database.sql` with an on-disk sqlite DSN (a network driver is never flagged: the point is catching a *second*, local copy, not the application's own database). `"ephemeral_warn"` logs the same violations without blocking, for migrating an existing application. Wired into both `Compile` and the config-only `Validate` path. `examples/dynamic-definitions` now declares it for real, and `check.sh` has a fifth broken-copy case proving a disk resource added under the policy is refused.
* **Phase 6 (Linux strict launcher): done, verified for real in Docker, not just built.** `aegis/cmd/aegis-launch` is the only native file a deployment keeps on disk: `aegis package -binary <elf>` seals the real `aegis` executable the same way an application bundle is sealed, and the launcher decrypts it directly into a sealed `memfd` (`MFD_ALLOW_SEALING`, then `F_ADD_SEALS` with `SEAL|SHRINK|GROW|WRITE` before exec) and replaces itself with `execveat(fd, "", argv, envp, AT_EMPTY_PATH)` — `fexecve`, which Linux has no separate syscall for. Verified end to end: built both binaries for linux/arm64, packaged the runtime and an application inside a Debian container, **deleted the 43.5MB runtime binary from disk**, launched it anyway, and confirmed by `ps` that the launcher's own PID now runs as `aegis run ...` (true process replacement, not a child process), that the application served a real authenticated request (`{"lines":1,"quoted_by":"clerk-1","subtotal":9}`), and that both layers (the launcher's own image and the exec'd runtime's own application package) verified their signature independently. The launcher itself is 6.2MB against the runtime's 43.5MB. `aegis/cmd/aegis-launch/check.sh` reproduces all of this; `aegis/cmd/aegis-launch/README.md` has the walkthrough. Windows and macOS are unaffected by design (no equivalent syscalls; see the launcher's README).
  * This needed two small refactors to avoid duplicating security-relevant code across the three binaries that now need it: `aegis/pkg/keyflag.go` (key-file/hex/passphrase resolution, previously private to `cmd/aegis`) and `aegis/pkg/open.go` (`pkg.Open`: verify payload hash, decrypt, verify plaintext hash — previously inlined in `run.go`). `cmd/aegis/run.go` now calls `pkg.Open` too, so it is exercised by the full existing `cmd/aegis` test suite, not just the launcher's own.
* **Phase 4 (key release): the "seal once, distribute, many machines fetch the same key" path is done and verified for real; hardware attestation stays opt-in and unconfigured, per the decision above.**
  * **Persistent store.** `control/store_sql.go` (`SQLStore`) implements the existing `Store` interface (previously only `MemoryStore` did) against plain `database/sql`, with `OpenSQLite` as the convenience for the common case. A shared conformance test (`store_conformance_test.go`) runs the same checks against both implementations, and `TestSQLStoreSurvivesRestart` closes the file and reopens it as a fresh `*SQLStore` to prove a revocation and a master key are still there. `aegis serve -store <path>` uses it; without `-store` it still warns that everything will be forgotten on exit.
  * **A real, pre-existing design gap, found and fixed.** `GrantService` already existed and already does real work (device/package revocation checks, an attestation hook, audit events) — but it only ever handed out an HKDF-derived *per-session* key, which only decrypts a package that was itself built fresh for that exact session ID, chosen in advance. The one test that exercised it (`TestRuntimeIntegration_EndToEnd`, not written by this work) builds its package that way. That is incompatible with how every other command here works: `aegis package` seals one `.apkg` once, with one fixed key, meant to run on many machines. `WithRawMasterKeyGrants()` (`control/grant.go`) is the fix: a `GrantService` option that hands out the package's exact master key instead, leaving the default (and that existing test) untouched. `aegis serve` uses it by default; `-session-keys` opts back into the original per-session-build behavior for whoever actually wants that workflow.
  * **`aegis register`** (new subcommand) posts a sealed package's identity and its exact key to a control server's `/v1/package/register`, after opening the package locally first to prove the key is the right one before sending it anywhere.
  * **`aegis run -grant-server <url> [-device-id <id>]`** is now a full alternative to `-key`: it fetches the key from the control server (`control.NewHTTPGrantProvider`, which already existed) instead, honouring every check `GrantService` already enforces.
  * **Rate limiting** (`control/ratelimit.go`): a dependency-free per-IP token bucket, `WithServerRateLimit`, applied by default in `aegis serve` (120/min, burst 20) — a grant endpoint hands out decryption keys, so nothing should be able to hammer it for free.
  * **Verified for real, at the binary level, not just in tests**: built `bin/aegis`, started `aegis serve` with a real SQLite file, registered a real package, ran it via `-grant-server` (`Result: 30`), **killed the server, restarted it pointed at the same file**, and ran the same package again (`Result: 300`) — proving the registration survived the restart, not just that the code compiles.
  * **`aegis revoke`** (new subcommand, the CLI counterpart to `register`) wraps `POST /v1/package/revoke`, previously reachable only by hand-building the HTTP request.
  * **Example and check script**: [`examples/control-server`](../examples/control-server) — package once, register, run via `-grant-server` for two different functions, both refusals (`-key` with `-grant-server` together; an unknown function), revoke and confirm the very next run is refused, kill and restart the server against the same `-store` file and confirm the revocation and a newly-registered package both survived, and the rate limiter actually rejecting a request in isolation. 19/19 checks pass.
  * **A second real bug this surfaced and fixed**: the rate limiter, as first wired, covered `/healthz` along with everything else. A liveness probe is polled far more often than any real client calls the sensitive endpoints, so sharing one per-IP quota between the two let routine health checks starve real traffic from that same IP — exactly backwards from what a rate limiter is for. `Server.ServeHTTP` now exempts `/healthz` unconditionally; `TestHealthzIsNeverRateLimited` (`control/ratelimit_test.go`) checks it stays that way.
  * **Known gaps, deliberately not closed here**: the registration and revocation endpoints have no authentication at all (anyone who can reach the control server can register or revoke any package) — this needs a real answer (an admin token, mTLS, or both) before this is exposed beyond a trusted network, and is a different problem from key release itself. mTLS between a client and the control server (named in the original plan) is not implemented; the connection should be run behind TLS by the operator. Hardware attestation (TPM/Secure Enclave) remains unconfigured by default, per the "opt-in, later" decision — `GrantService`'s attestation hook already exists and is ready for it.
* **Phase 7 (bytecode for proprietary logic): not started.**
