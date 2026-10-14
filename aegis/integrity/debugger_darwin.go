//go:build darwin

package integrity

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const (
	darwinCtlKern       = 1          // CTL_KERN
	darwinKernProc      = 14         // KERN_PROC
	darwinKernProcPID   = 1          // KERN_PROC_PID
	darwinPTracedFlag   = 0x00000800 // P_TRACED bit flag in kp_proc.p_flag
	darwinMinKinfoBytes = 36         // Required buffer size to encompass p_flag at offset 32
)

// detectSystemDebugger executes Darwin-specific sysctl KERN_PROC_PID check to query
// the P_TRACED flag on the current process.
func detectSystemDebugger() (attached bool, evidence string, err error) {
	currentPID := os.Getpid()
	mib := [4]int32{darwinCtlKern, darwinKernProc, darwinKernProcPID, int32(currentPID)}

	// kinfo_proc is 648 bytes on 64-bit Darwin. We allocate a safe 1024-byte buffer.
	var kinfo [1024]byte
	kinfoLen := uintptr(len(kinfo))

	_, _, errno := syscall.Syscall6(
		syscall.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])),
		4,
		uintptr(unsafe.Pointer(&kinfo[0])),
		uintptr(unsafe.Pointer(&kinfoLen)),
		0,
		0,
	)

	if errno != 0 {
		// If sysctl fails or is restricted by sandboxing, attempt fallback signal test.
		return fallbackSignalTest(errno)
	}

	if kinfoLen < darwinMinKinfoBytes {
		return fallbackSignalTest(fmt.Errorf("integrity: kinfo_proc buffer returned %d bytes, need at least %d", kinfoLen, darwinMinKinfoBytes))
	}

	// kp_proc.p_flag is an int32 at byte offset 32.
	pFlag := *(*int32)(unsafe.Pointer(&kinfo[32]))
	if (pFlag & darwinPTracedFlag) != 0 {
		return true, fmt.Sprintf("darwin sysctl: debugger attached (pid=%d, p_flag=0x%08x, P_TRACED active)", currentPID, pFlag), nil
	}

	return false, fmt.Sprintf("darwin sysctl: clean (pid=%d, p_flag=0x%08x, P_TRACED not set)", currentPID, pFlag), nil
}
