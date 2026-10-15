//go:build darwin

package harden

import "syscall"

const ptDenyAttach = 31 // PT_DENY_ATTACH

func platformApply(o Options, r *Report) {
	if err := syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{Cur: 0, Max: 0}); err != nil {
		r.skipErr("no core dumps", err)
	} else {
		r.ok("no core dumps")
	}
	if o.AllowDebug {
		r.skip("debugger attach denied", "AEGIS_ALLOW_DEBUG=1")
	} else if _, _, e := syscall.Syscall6(syscall.SYS_PTRACE, ptDenyAttach, 0, 0, 0, 0, 0); e != 0 {
		r.skipErr("debugger attach denied", e)
	} else {
		r.ok("debugger attach denied")
	}
	// macOS has no mlockall; swap is encrypted by the OS by default.
	r.skip("memory locked out of swap", "not available on darwin (system swap is encrypted)")
}
