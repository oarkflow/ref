//go:build !linux

package main

import (
	"errors"
	"runtime"
)

// run reports a clear refusal on every platform but Linux. memfd_create and
// execveat(..., AT_EMPTY_PATH) (fexecve) are Linux syscalls with no
// equivalent on Windows or macOS — see docs/aegis-protection-plan.md, phase
// 6, which keeps the generic runtime binary on disk for those two
// platforms by design rather than pretending otherwise.
func run(_ []string) error {
	return errors.New("aegis-launch only runs on linux (got " + runtime.GOOS + "); see docs/aegis-protection-plan.md, phase 6")
}
