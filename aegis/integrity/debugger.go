package integrity

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
)

// DebuggerDetector defines the signature of a low-level debugger detection function.
type DebuggerDetector func(ctx context.Context) (attached bool, evidence string, err error)

// DebuggerCheck inspects the host environment for active debuggers, ptrace attachment,
// or tracer processes.
type DebuggerCheck struct {
	customDetector DebuggerDetector
}

// NewDebuggerCheck creates a new DebuggerCheck with default platform-specific probes.
func NewDebuggerCheck() *DebuggerCheck {
	return &DebuggerCheck{}
}

// NewDebuggerCheckWithDetector creates a DebuggerCheck with a custom detection routine,
// primarily for testing or specialized embedded profiles.
func NewDebuggerCheckWithDetector(detector DebuggerDetector) *DebuggerCheck {
	return &DebuggerCheck{
		customDetector: detector,
	}
}

// Name returns the identifier for this integrity check.
func (c *DebuggerCheck) Name() string {
	return "debugger"
}

// Run executes the debugger detection probe and returns a CheckResult.
func (c *DebuggerCheck) Run(ctx context.Context) CheckResult {
	if c.customDetector != nil {
		attached, evidence, err := c.customDetector(ctx)
		if err != nil {
			return CheckResult{
				Check:    c.Name(),
				Passed:   false,
				Evidence: fmt.Sprintf("integrity: debugger probe error: %v", err),
				Severity: SeverityHigh,
			}
		}
		if attached {
			return CheckResult{
				Check:    c.Name(),
				Passed:   false,
				Evidence: evidence,
				Severity: SeverityCritical,
			}
		}
		return CheckResult{
			Check:    c.Name(),
			Passed:   true,
			Evidence: evidence,
			Severity: SeverityInfo,
		}
	}

	attached, evidence, err := detectSystemDebugger()
	if err != nil {
		return CheckResult{
			Check:    c.Name(),
			Passed:   false,
			Evidence: fmt.Sprintf("integrity: debugger probe failure: %v", err),
			Severity: SeverityHigh,
		}
	}

	if attached {
		return CheckResult{
			Check:    c.Name(),
			Passed:   false,
			Evidence: evidence,
			Severity: SeverityCritical,
		}
	}

	return CheckResult{
		Check:    c.Name(),
		Passed:   true,
		Evidence: evidence,
		Severity: SeverityInfo,
	}
}

// parseLinuxTracerPid extracts the TracerPid field from Linux /proc/self/status content.
func parseLinuxTracerPid(data []byte) (tracerPID int, found bool, err error) {
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, []byte("TracerPid:")) {
			rawVal := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("TracerPid:")))
			pid, parseErr := strconv.Atoi(string(rawVal))
			if parseErr != nil {
				return 0, true, fmt.Errorf("integrity: malformed TracerPid %q: %w", string(rawVal), parseErr)
			}
			return pid, true, nil
		}
	}
	return 0, false, nil
}

