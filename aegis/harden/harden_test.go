package harden

import (
	"os"
	"testing"
)

func TestApplyReportsSomethingOnEveryPlatform(t *testing.T) {
	r := Apply(Options{})
	if len(r.Applied) == 0 && len(r.Skipped) == 0 {
		t.Fatal("Apply reported nothing at all; it should at least report the terse-traceback protection")
	}
	// Every Skipped entry must say why, not just that it was skipped: a
	// caller (and an operator reading the printed report) needs the reason
	// to judge whether it matters on their platform.
	for _, s := range r.Skipped {
		if !containsColon(s) {
			t.Errorf("skipped entry has no reason attached: %q", s)
		}
	}
}

func containsColon(s string) bool {
	for _, c := range s {
		if c == ':' {
			return true
		}
	}
	return false
}

func TestWipeZeroesTheBuffer(t *testing.T) {
	b := []byte("secret-key-material")
	Wipe(b)
	for i, c := range b {
		if c != 0 {
			t.Fatalf("byte %d is %q after Wipe, want 0", i, c)
		}
	}
}

func TestLockBufferEmptySliceIsFine(t *testing.T) {
	if !LockBuffer(nil) {
		t.Error("LockBuffer(nil) should report true (nothing to lock, trivially satisfied)")
	}
	UnlockBuffer(nil) // must not panic
}

func TestLockBufferRealBuffer(t *testing.T) {
	b := make([]byte, 32)
	// LockBuffer's return value is platform-dependent (Windows's VirtualLock
	// can fail under a tight per-process working-set quota), but it must
	// never panic, and Unlock must be safe to call whether or not Lock
	// reported success.
	_ = LockBuffer(b)
	UnlockBuffer(b)
}

func TestScrubEnvRemovesTheVariable(t *testing.T) {
	t.Setenv("AEGIS_TEST_SCRUB_ME", "x")
	ScrubEnv("AEGIS_TEST_SCRUB_ME")
	if v, ok := os.LookupEnv("AEGIS_TEST_SCRUB_ME"); ok {
		t.Fatalf("variable still set after ScrubEnv: %q", v)
	}
}

func TestScrubEnvPrefixRemovesMatching(t *testing.T) {
	t.Setenv("AEGIS_TEST_PFX_A", "1")
	t.Setenv("AEGIS_TEST_PFX_B", "2")
	t.Setenv("AEGIS_TEST_KEEP", "3")
	ScrubEnvPrefix("AEGIS_TEST_PFX_")
	if _, ok := os.LookupEnv("AEGIS_TEST_PFX_A"); ok {
		t.Error("AEGIS_TEST_PFX_A should have been removed")
	}
	if _, ok := os.LookupEnv("AEGIS_TEST_PFX_B"); ok {
		t.Error("AEGIS_TEST_PFX_B should have been removed")
	}
	if v, ok := os.LookupEnv("AEGIS_TEST_KEEP"); !ok || v != "3" {
		t.Error("AEGIS_TEST_KEEP should have been left alone")
	}
}
