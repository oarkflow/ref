package platform

import (
	"strings"
	"testing"
)

// RFC 6238 test vector: secret "12345678901234567890", time 59 s, step 1, SHA-1, 8 digits 94287082 (6 digits 287082).
func TestTOTPCodeMatchesTheRFCVector(t *testing.T) {
	if got := totpCode([]byte("12345678901234567890"), 1, 6); got != "287082" {
		t.Fatalf("code = %s", got)
	}
	if got := totpCode([]byte("12345678901234567890"), 1, 8); got != "94287082" {
		t.Fatalf("8-digit code = %s", got)
	}
}

func TestRecoveryCodesHashIgnoringCaseAndDashesAndPackForSearching(t *testing.T) {
	if recoveryHash("ABCDE-fghjk") != recoveryHash(" abcde fghjk ") || recoveryHash("abcde-fghjk") == recoveryHash("abcde-fghjm") {
		t.Fatal("hashing must ignore case and separators and still tell codes apart")
	}
	if len(recoveryHash("abcde-fghjk")) != 64 || strings.Contains(recoveryHash("abcde-fghjk"), "abcde") {
		t.Fatal("a hash is not the code")
	}
}
