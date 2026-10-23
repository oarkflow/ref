package respond

import (
	"context"
	"errors"
	"net/http"
	"sync"
)

// Exit codes a supervisor can act on when the controller ends the process.
const (
	// ExitRegenerate asks the supervisor to start a fresh copy from the sealed
	// package (systemd Restart=on-failure, a container orchestrator, ...).
	ExitRegenerate = 75
	// ExitSafeMode asks the supervisor NOT to restart without a human.
	ExitSafeMode = 76
)

// LocalEnforcer enforces actions on the process it runs in, for `aegis run`.
//
// It is honest about what in-process enforcement is worth: the Gate, key
// wiping and process exit all live in the same trust domain as the
// application, so an attacker with code execution inside the process can
// bypass them. They stop request-level abuse and give a debugger or injected
// library no quiet foothold; they are not a substitute for an OS sandbox or
// a sidecar enforcer. It therefore declares only what it can really do
// (see Supports) and the controller logs the rest as unenforceable.
type LocalEnforcer struct {
	// RevokeCreds runs on ActRevokeCreds: drop sessions, tokens and keys the
	// process still holds. It must leave the process able to resume, because
	// the posture can relax back from Isolated. Optional.
	RevokeCreds []func()
	// Wipe runs only when the process is being ended (ActRegenerate,
	// ActSafeMode): zero the in-memory filesystem and anything left. Optional.
	Wipe []func()
	// Terminate ends the process with code. In `aegis run` it cancels the
	// server context and the exit code is returned to main. Required for the
	// Regenerate and SafeMode levels to take effect.
	Terminate func(code int)

	mu          sync.RWMutex
	readOnly    bool
	isolated    bool
	credsWiped  bool
	terminating int
}

// Supports reports the one capability a request gate can really remove:
// state changes. Egress, spawn and transfer need an OS-level enforcer.
func (e *LocalEnforcer) Supports() Capability { return CapWrite }

// Gate is for serve.Options.Gate. It returns 0 to allow a request.
func (e *LocalEnforcer) Gate(method, path string) int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	switch {
	case e.terminating != 0:
		return http.StatusServiceUnavailable
	case e.isolated:
		if path == "/livez" || path == "/ping" {
			return 0 // let the supervisor still see the process
		}
		return http.StatusServiceUnavailable
	case e.readOnly:
		switch method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			return 0
		}
		return http.StatusForbidden
	}
	return 0
}

func (e *LocalEnforcer) Apply(_ context.Context, a Action) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch a.Kind {
	case ActRevokeCaps:
		if a.Caps&CapWrite != 0 {
			e.readOnly = true
		}
	case ActRestore:
		if a.Caps&CapWrite != 0 {
			e.readOnly = false
		}
	case ActRevokeCreds:
		for _, f := range e.RevokeCreds {
			f()
		}
		e.credsWiped = true
	case ActIsolate:
		e.isolated = true
	case ActRelease:
		e.isolated = false
	case ActRegenerate, ActSafeMode:
		code := ExitRegenerate
		if a.Kind == ActSafeMode {
			code = ExitSafeMode
		}
		if e.Terminate == nil {
			return errors.New("respond: no Terminate hook; cannot end the process")
		}
		e.isolated = true
		e.terminating = code
		for _, w := range e.Wipe {
			w()
		}
		e.Terminate(code)
	default:
		return errors.New("respond: unknown action " + string(a.Kind))
	}
	return nil
}

// Verify re-reads the gate, so it checks the effect and not just that Apply
// returned.
func (e *LocalEnforcer) Verify(_ context.Context, a Action) error {
	switch a.Kind {
	case ActRevokeCaps:
		if a.Caps&CapWrite != 0 && e.Gate(http.MethodPost, "/") == 0 {
			return errors.New("writes still permitted")
		}
	case ActRestore:
		if a.Caps&CapWrite != 0 && e.Gate(http.MethodPost, "/") != 0 {
			return errors.New("writes still blocked")
		}
	case ActIsolate:
		if e.Gate(http.MethodGet, "/") == 0 {
			return errors.New("requests still served")
		}
	case ActRelease:
		if e.Gate(http.MethodGet, "/") != 0 {
			return errors.New("requests still blocked")
		}
	case ActRevokeCreds:
		e.mu.RLock()
		defer e.mu.RUnlock()
		if !e.credsWiped {
			return errors.New("credentials not wiped")
		}
	case ActRegenerate, ActSafeMode:
		e.mu.RLock()
		defer e.mu.RUnlock()
		if e.terminating == 0 {
			return errors.New("termination not requested")
		}
	}
	return nil
}
