//go:build windows

package harden

import (
	"syscall"
	"unsafe"
)

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procSetErrorMode               = kernel32.NewProc("SetErrorMode")
	procSetProcessMitigationPolicy = kernel32.NewProc("SetProcessMitigationPolicy")
)

const (
	// SetErrorMode flags (winbase.h).
	semFailCriticalErrors = 0x0001
	semNoGPFaultErrorBox  = 0x0002

	// PROCESS_MITIGATION_POLICY values this file uses (winnt.h). The two
	// skipped here (a true anti-debug policy and a process-wide memory lock)
	// have no Windows equivalent available to an ordinary process; see the
	// Skipped reasons below and LockBuffer for the narrower alternative the
	// plan asks for ("VirtualLock on key buffers").
	processExtensionPointDisablePolicy = 6
	processImageLoadPolicy             = 10

	// PROCESS_MITIGATION_IMAGE_LOAD_POLICY bit flags.
	imageLoadNoRemoteImages            = 1 << 0
	imageLoadNoLowMandatoryLabelImages = 1 << 1
)

func platformApply(o Options, r *Report) {
	// SEM_FAILCRITICALERRORS also stops the OS routing a crash through the
	// "Windows has stopped working" dialog and WER's interactive prompt,
	// which otherwise sits on screen (and can sit there for an attacker) with
	// decrypted application state already in the process's memory.
	ret, _, _ := procSetErrorMode.Call(uintptr(semFailCriticalErrors | semNoGPFaultErrorBox))
	_ = ret // SetErrorMode returns the previous mode; it cannot itself fail.
	r.ok("no crash dialogs (SEM_FAILCRITICALERRORS | SEM_NOGPFAULTERRORBOX)")

	if setMitigationPolicy(processExtensionPointDisablePolicy, 1) {
		r.ok("legacy DLL injection blocked (AppInit_DLLs and other global hooks)")
	} else {
		r.skip("legacy DLL injection blocked", "SetProcessMitigationPolicy(ExtensionPointDisable) failed or is unavailable on this Windows version")
	}

	if setMitigationPolicy(processImageLoadPolicy, imageLoadNoRemoteImages|imageLoadNoLowMandatoryLabelImages) {
		r.ok("DLL loading restricted (no images from a network path or a low-integrity location)")
	} else {
		r.skip("DLL loading restricted", "SetProcessMitigationPolicy(ImageLoad) failed or is unavailable on this Windows version")
	}

	if !o.AllowDebug {
		// Unlike PT_DENY_ATTACH on Darwin or non-dumpable on Linux, Windows
		// gives an ordinary process no API to refuse a debugger that a
		// privileged account chooses to attach — that needs a kernel driver.
		// integrity.DebuggerCheck (IsDebuggerPresent,
		// CheckRemoteDebuggerPresent) detects one; it cannot prevent one.
		r.skip("debugger attach denied", "windows has no per-process self-protection from a privileged debugger without a kernel driver; detection only, see the integrity package")
	} else {
		r.skip("debugger attach denied", "AEGIS_ALLOW_DEBUG=1")
	}

	if o.NoMlock {
		r.skip("memory locked out of swap", "AEGIS_NO_MLOCK=1")
	} else {
		// mlockall's whole-process guarantee has no Windows equivalent an
		// ordinary process can request: VirtualLock requires the caller to
		// name the exact buffer, and locking everything would need the
		// rarely granted SeLockMemoryPrivilege. Call LockBuffer on the
		// specific key material instead (see run.go).
		r.skip("memory locked out of swap (process-wide)", "windows has no mlockall equivalent available without SeLockMemoryPrivilege; call harden.LockBuffer on specific secrets instead")
	}
}

// setMitigationPolicy calls SetProcessMitigationPolicy with a single DWORD
// flags value, the shape every PROCESS_MITIGATION_*_POLICY structure this
// file uses actually has.
func setMitigationPolicy(policy uintptr, flags uint32) bool {
	ret, _, _ := procSetProcessMitigationPolicy.Call(policy, uintptr(unsafe.Pointer(&flags)), unsafe.Sizeof(flags))
	return ret != 0
}
