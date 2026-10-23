# aegis-launch

Linux-only, phase 6 of [`docs/aegis-protection-plan.md`](../../../docs/aegis-protection-plan.md): the only native file a deployment keeps on disk.

`aegis run <app.apkg>` already decrypts an application's BCL, SPL and templates into memory and writes nothing to disk. The one thing still a plaintext file on that machine is the `aegis` executable itself — generic and identical for every customer, but a file. `aegis-launch` removes that too: it decrypts the real `aegis` binary straight into an anonymous, sealed `memfd`, execs it there with `execveat(..., AT_EMPTY_PATH)` (`fexecve`), and is itself replaced. Nothing between the launcher and the running application ever touches disk.

It does not import `ref/platform`, `ref/serve` or `aegis/runtime` — the whole point is to stay small. Verified: on this machine, `aegis-launch` is 6.2MB against `aegis`'s 43.5MB.

## Seal the runtime

```bash
aegis keygen -out runtime-key
aegis package -binary bin/aegis -key runtime-key.key -enckey runtime-key.enc -out runtime.apkg -appid runtime -ver 1
```

This is the same `.apkg` format, signing and AES-256-GCM sealing as an application bundle — `-binary` instead of `-dir` just says the payload is an ELF executable, checked by its magic bytes at both packaging and launch time.

## Launch it

```bash
aegis-launch -image runtime.apkg -key runtime-key.enc -pubkey runtime-key.pub -- \
  run app.apkg -key app-key.enc -pubkey app-key.pub -addr :8080
```

Everything before `--` is the launcher's own flags; everything after is passed to the real `aegis` binary's argv exactly as typed. A `.apkg` requires `-pubkey` to verify its signer, same as `aegis run`; `-insecure-skip-verify` exists for development and warns loudly every time it is used. `-key` accepts the same three forms as everywhere else in Aegis: a key file, a 64-character hex string, or (if the image's manifest says the key is a passphrase) the passphrase itself, or set `AEGIS_LAUNCH_KEY` instead of passing it on the command line.

## What this proves, and how to check it yourself

`./check.sh` builds both binaries, seals the runtime inside a Docker container, **deletes the real runtime binary from disk**, launches it anyway, and confirms:

* the exec'd process shows as `aegis run ...` in `ps`, under the launcher's own PID (true process replacement, not a child process);
* the real application serves a real authenticated request;
* both layers (the launcher's own image, and the exec'd runtime's own application package) verify their signature independently;
* a missing `-pubkey`, a wrong key, and missing runtime arguments are all refused.

It needs Docker (memfd_create and execveat are Linux syscalls) and runs on your host's native architecture, no emulation.

## What this does not do

* **Windows and macOS keep the generic runtime on disk.** Neither OS has an equivalent of `memfd_create` + `execveat` available to an ordinary process without reflective-loading techniques that EDR and notarization reject; the plan's design (layer A generic, layer B the sealed bundle) treats this as acceptable rather than pretending otherwise.
* **The key must still be delivered somehow.** This launcher resolves it the same way every other Aegis command does (file, hex, passphrase, environment variable); phase 4 (key release through a control server, with attestation) is what removes needing to hand it a key file sitting on this machine.
* **Root, or a hypervisor, can still read this process's memory** while it runs — the same limit every other phase in this plan has.
