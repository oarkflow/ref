//go:build !darwin && !linux && !windows

package integrity

import (
	"fmt"
	"runtime"
)

// detectSystemDebugger provides graceful fallback on non-Darwin and non-Linux operating systems.
func detectSystemDebugger() (attached bool, evidence string, err error) {
	return false, fmt.Sprintf("debugger detection gracefully passed on unmonitored platform %s/%s", runtime.GOOS, runtime.GOARCH), nil
}
