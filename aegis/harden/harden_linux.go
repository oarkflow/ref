//go:build linux

package harden

import "syscall"

const (
	prSetDumpable = 4 // PR_SET_DUMPABLE
	mclCurrent    = 1
	mclFuture     = 2
)

func platformApply(o Options, r *Report) {
	if err := syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{Cur: 0, Max: 0}); err != nil {
		r.skipErr("no core dumps", err)
	} else {
		r.ok("no core dumps")
	}
	if !o.AllowDebug {
		// Non-dumpable also makes /proc/<pid>/{mem,environ} and ptrace
		// unavailable to other processes of the same user.
		if _, _, e := syscall.RawSyscall(syscall.SYS_PRCTL, prSetDumpable, 0, 0); e != 0 {
			r.skipErr("non-dumpable (blocks ptrace and /proc/pid/mem)", e)
		} else {
			r.ok("non-dumpable (blocks ptrace and /proc/pid/mem)")
		}
	} else {
		r.skip("non-dumpable", "AEGIS_ALLOW_DEBUG=1")
	}
	if o.NoMlock {
		r.skip("memory locked out of swap", "AEGIS_NO_MLOCK=1")
	} else if _, _, e := syscall.RawSyscall(syscall.SYS_MLOCKALL, mclCurrent|mclFuture, 0, 0); e != 0 {
		r.skipErr("memory locked out of swap", e)
	} else {
		r.ok("memory locked out of swap")
	}
}
