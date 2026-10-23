//go:build windows

package harden

import "unsafe"

var (
	procVirtualLock   = kernel32.NewProc("VirtualLock")
	procVirtualUnlock = kernel32.NewProc("VirtualUnlock")
)

func lockBuffer(b []byte) bool {
	ret, _, _ := procVirtualLock.Call(uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
	return ret != 0
}

func unlockBuffer(b []byte) {
	_, _, _ = procVirtualUnlock.Call(uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
}
