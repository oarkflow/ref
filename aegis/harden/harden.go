// Package harden keeps a decrypted Aegis application in process memory only.
//
// Decrypted configs, templates and keys live in the Go heap. Without
// precautions the OS can still copy that heap to disk: a core dump after a
// crash, swap under memory pressure, or a debugger/ptrace read. Apply closes
// those paths on a best-effort basis and reports what it could and could not
// do, so a caller never claims protection it does not have.
package harden

import (
	"os"
	"runtime/debug"
	"strings"
)

// Report lists each protection and whether it took effect.
type Report struct {
	Applied []string
	Skipped []string // "<name>: <reason>"
}

func (r *Report) ok(name string)                 { r.Applied = append(r.Applied, name) }
func (r *Report) skip(name, why string)          { r.Skipped = append(r.Skipped, name+": "+why) }
func (r *Report) skipErr(name string, err error) { r.skip(name, err.Error()) }

// Options select the protections. The zero value enables everything except
// the ones that get in the way of debugging, which AEGIS_ALLOW_DEBUG opts out.
type Options struct {
	// AllowDebug leaves debugger attach enabled (development only).
	AllowDebug bool
	// NoMlock skips locking memory (containers with a low RLIMIT_MEMLOCK).
	NoMlock bool
}

// FromEnv reads AEGIS_ALLOW_DEBUG=1 and AEGIS_NO_MLOCK=1.
func FromEnv() Options {
	return Options{
		AllowDebug: os.Getenv("AEGIS_ALLOW_DEBUG") == "1",
		NoMlock:    os.Getenv("AEGIS_NO_MLOCK") == "1",
	}
}

// Apply installs the protections. Call it before decrypting anything.
func Apply(o Options) Report {
	var r Report

	// A panic trace prints goroutine state and arguments; keep it terse so a
	// crash log never carries application data.
	debug.SetTraceback("none")
	r.ok("terse crash traces")

	platformApply(o, &r)
	return r
}

// Wipe overwrites b with zeros. The compiler cannot elide it because the
// slice escapes through the caller.
func Wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// LockBuffer locks b's backing memory so the OS cannot page it to swap or
// disk, and reports whether it did. On Linux and macOS, Apply's mlockall (or
// macOS's encrypted swap) already covers every byte the process holds, so
// this is a no-op that reports true. On Windows, where there is no per-
// process equivalent of mlockall available without a rarely granted
// privilege, this calls VirtualLock on exactly this buffer — the narrower
// guarantee the plan calls "VirtualLock on key buffers". Call it on key
// material right after it is resolved, and call UnlockBuffer (or just let
// the process exit) once it is wiped.
func LockBuffer(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	return lockBuffer(b)
}

// UnlockBuffer releases a lock LockBuffer took. Call it before Wipe-ing a
// buffer that will be reused for something else; it is unnecessary before
// process exit, which releases every lock anyway.
func UnlockBuffer(b []byte) {
	if len(b) == 0 {
		return
	}
	unlockBuffer(b)
}

// ScrubEnv removes the named environment variables from the process so a
// later /proc/<pid>/environ read or child process cannot see them.
func ScrubEnv(names ...string) {
	for _, n := range names {
		_ = os.Unsetenv(n)
	}
}

// ScrubEnvPrefix removes every variable starting with prefix.
func ScrubEnvPrefix(prefix string) {
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, prefix) {
			if i := strings.IndexByte(kv, '='); i > 0 {
				_ = os.Unsetenv(kv[:i])
			}
		}
	}
}
