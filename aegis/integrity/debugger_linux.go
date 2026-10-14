//go:build linux

package integrity

import (
	"fmt"
	"os"
)

// linuxProcStatusPath is the procfs path to the process status file.
var linuxProcStatusPath = "/proc/self/status"

// detectSystemDebugger executes Linux-specific /proc/self/status inspection
// to check if TracerPid is non-zero.
func detectSystemDebugger() (attached bool, evidence string, err error) {
	data, err := os.ReadFile(linuxProcStatusPath)
	if err != nil {
		if os.IsNotExist(err) || os.IsPermission(err) {
			// In restricted containers or stripped procfs, fallback to signal test.
			return fallbackSignalTest(err)
		}
		return false, "", fmt.Errorf("integrity: failed to read %s: %w", linuxProcStatusPath, err)
	}

	tracerPID, found, parseErr := parseLinuxTracerPid(data)
	if parseErr != nil {
		return false, "", parseErr
	}
	if !found {
		return fallbackSignalTest(fmt.Errorf("integrity: TracerPid field not found in %s", linuxProcStatusPath))
	}

	if tracerPID > 0 {
		return true, fmt.Sprintf("linux procfs: tracer process attached (TracerPid=%d)", tracerPID), nil
	}

	return false, "linux procfs: clean (TracerPid=0, no tracer attached)", nil
}
