//go:build windows

package harden

import "testing"

// TestLockBufferActuallyLocksOnWindows is stronger than the cross-platform
// TestLockBufferRealBuffer: on Windows, LockBuffer must report true for an
// ordinary small buffer (VirtualLock succeeding is the whole point of
// having this function here rather than relying on Apply's process-wide
// claim, which Windows cannot make).
func TestLockBufferActuallyLocksOnWindows(t *testing.T) {
	b := make([]byte, 32)
	if !LockBuffer(b) {
		t.Fatal("LockBuffer should succeed for an ordinary 32-byte buffer on Windows")
	}
	UnlockBuffer(b)
}

func TestPlatformApplyAppliesWindowsMitigations(t *testing.T) {
	r := Apply(Options{})
	foundErrorMode := false
	for _, a := range r.Applied {
		if a == "no crash dialogs (SEM_FAILCRITICALERRORS | SEM_NOGPFAULTERRORBOX)" {
			foundErrorMode = true
		}
	}
	if !foundErrorMode {
		t.Errorf("expected the SetErrorMode protection to apply (it cannot fail), got Applied=%v", r.Applied)
	}
	// Debugger-deny and process-wide memory locking are documented, honest
	// skips on Windows (see harden_windows.go); make sure they are reported
	// as skips with a reason rather than silently vanishing from the report.
	sawDebugSkip, sawMlockSkip := false, false
	for _, s := range r.Skipped {
		if hasPrefix(s, "debugger attach denied") {
			sawDebugSkip = true
		}
		if hasPrefix(s, "memory locked out of swap") {
			sawMlockSkip = true
		}
	}
	if !sawDebugSkip || !sawMlockSkip {
		t.Errorf("expected honest skips for debugger-deny and process-wide mlock, got Skipped=%v", r.Skipped)
	}
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
