//go:build windows

package integrity

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procIsDebuggerPresent          = kernel32.NewProc("IsDebuggerPresent")
	procCheckRemoteDebuggerPresent = kernel32.NewProc("CheckRemoteDebuggerPresent")
	procGetCurrentProcess          = kernel32.NewProc("GetCurrentProcess")
)

// detectSystemDebugger checks IsDebuggerPresent (a debugger that attached
// before or is attached now) and CheckRemoteDebuggerPresent (a debugger
// attached from outside the process, which IsDebuggerPresent alone can
// miss). Neither catches a kernel-level debugger or a hypervisor, the same
// limit ptrace/sysctl detection has on Linux and macOS.
func detectSystemDebugger() (attached bool, evidence string, err error) {
	r1, _, _ := procIsDebuggerPresent.Call()
	if r1 != 0 {
		return true, "windows IsDebuggerPresent: a debugger is attached to this process", nil
	}

	h, _, _ := procGetCurrentProcess.Call() // a pseudo-handle; this never fails
	var remote int32
	r1, _, callErr := procCheckRemoteDebuggerPresent.Call(h, uintptr(unsafe.Pointer(&remote)))
	if r1 == 0 {
		return false, "", fmt.Errorf("integrity: CheckRemoteDebuggerPresent failed: %w", callErr)
	}
	if remote != 0 {
		return true, "windows CheckRemoteDebuggerPresent: a remote debugger is attached", nil
	}
	return false, "windows: clean (IsDebuggerPresent and CheckRemoteDebuggerPresent both negative)", nil
}
