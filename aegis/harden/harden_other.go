//go:build !linux && !darwin && !windows

package harden

func platformApply(o Options, r *Report) {
	r.skip("core dump / debugger / swap protection", "not implemented on this platform")
}
