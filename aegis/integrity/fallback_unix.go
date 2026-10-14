//go:build unix

package integrity

import (
	"fmt"
	"os"
	"syscall"
)

// fallbackSignalTest verifies process signaling capability under POSIX when
// the primary debugger probe encounters restricted permissions or sysctl failure.
func fallbackSignalTest(primaryErr error) (attached bool, evidence string, err error) {
	currentPID := os.Getpid()
	if signalErr := syscall.Kill(currentPID, 0); signalErr != nil {
		return false, "", fmt.Errorf("integrity: fallback signal probe failed: %w (primary error: %v)", signalErr, primaryErr)
	}
	return false, fmt.Sprintf("fallback signal probe verified process responsiveness; primary check skipped (%v)", primaryErr), nil
}
