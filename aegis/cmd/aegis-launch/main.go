// Command aegis-launch is the strict, Linux-only launcher described in
// docs/aegis-protection-plan.md, phase 6: the only native file this
// deployment keeps on disk.
//
// aegis run already decrypts an application's BCL, SPL and templates into
// memory and writes nothing to disk — tested in that doc. The one thing
// that is still a plaintext file on disk under that scheme is the `aegis`
// executable itself: a generic, identical-for-every-customer binary, but a
// binary nonetheless. aegis-launch removes even that: it decrypts the real
// `aegis` executable (sealed with `aegis package -binary`) directly into an
// anonymous, sealed memfd and execs it there, so the only thing a directory
// listing or a disk image on this one machine ever shows is this launcher
// and an encrypted blob it cannot read without the key.
//
// It deliberately does not import ref/platform, ref/serve or anything else
// the real runtime needs: the whole point is for this binary to stay small,
// so there is as little to say about it as possible.
//
//	aegis-launch -image runtime.apkg -key <key> -pubkey trusted.pub -- run app.apkg -pubkey app.pub
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "aegis-launch:", err)
		os.Exit(1)
	}
}
