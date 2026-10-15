//go:build !linux && !darwin

package harden

func platformApply(o Options, r *Report) {
	r.skip("core dump / debugger / swap protection", "not implemented on this platform")
}
