//go:build !windows

package harden

// lockBuffer is a no-op on platforms where Apply's process-wide mlockall (or
// the OS's own encrypted swap, on macOS) already covers this buffer.
func lockBuffer(_ []byte) bool { return true }

func unlockBuffer(_ []byte) {}
