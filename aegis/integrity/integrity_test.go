package integrity

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestCheckResultJSON(t *testing.T) {
	result := CheckResult{
		Check:    "debugger",
		Passed:   true,
		Evidence: "nominal",
		Severity: SeverityInfo,
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}

	var decoded CheckResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}

	if decoded.Check != result.Check || decoded.Passed != result.Passed || decoded.Severity != result.Severity {
		t.Fatalf("decoded result mismatch: got %+v, want %+v", decoded, result)
	}
}

func TestDefaultCheckerNormalExecution(t *testing.T) {
	checker := DefaultChecker()
	if checker == nil {
		t.Fatal("DefaultChecker returned nil")
	}

	checks := checker.Checks()
	if len(checks) != 3 {
		t.Fatalf("expected 3 default checks, got %d", len(checks))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	results := checker.Run(ctx)
	if len(results) != len(checks) {
		t.Fatalf("expected %d results, got %d", len(checks), len(results))
	}

	for _, result := range results {
		if !result.Passed {
			t.Errorf("check %s failed unexpectedly under nominal environment: %s", result.Check, result.Evidence)
		}
	}

	passed, report := checker.Summary(results)
	if !passed {
		t.Errorf("checker.Summary reported passed=false, report:\n%s", report)
	}
	if !strings.Contains(report, "PASSED") {
		t.Errorf("expected report to contain 'PASSED', got: %s", report)
	}
}

func TestDebuggerCheck(t *testing.T) {
	t.Run("NormalExecution", func(t *testing.T) {
		check := NewDebuggerCheck()
		if check.Name() != "debugger" {
			t.Errorf("expected name 'debugger', got %s", check.Name())
		}
		result := check.Run(context.Background())
		if !result.Passed {
			t.Errorf("expected debugger check to pass under normal execution: %s", result.Evidence)
		}
	})

	t.Run("MockDetectedDebugger", func(t *testing.T) {
		mockCheck := NewDebuggerCheckWithDetector(func(ctx context.Context) (bool, string, error) {
			return true, "tracer process attached (mock)", nil
		})

		result := mockCheck.Run(context.Background())
		if result.Passed {
			t.Errorf("expected check to fail when debugger is detected")
		}
		if result.Severity != SeverityCritical {
			t.Errorf("expected SeverityCritical, got %s", result.Severity)
		}
		if !strings.Contains(result.Evidence, "tracer process attached") {
			t.Errorf("unexpected evidence: %s", result.Evidence)
		}
	})

	t.Run("MockDetectorError", func(t *testing.T) {
		mockCheck := NewDebuggerCheckWithDetector(func(ctx context.Context) (bool, string, error) {
			return false, "", errors.New("simulated probe io error")
		})

		result := mockCheck.Run(context.Background())
		if result.Passed {
			t.Errorf("expected check to fail when detector returns an error")
		}
		if result.Severity != SeverityHigh {
			t.Errorf("expected SeverityHigh, got %s", result.Severity)
		}
		if !strings.Contains(result.Evidence, "simulated probe io error") {
			t.Errorf("unexpected evidence: %s", result.Evidence)
		}
	})

	t.Run("LinuxTracerPidParsing", func(t *testing.T) {
		testCases := []struct {
			name        string
			input       string
			expectedPID int
			expectFound bool
			expectErr   bool
		}{
			{
				name:        "clean TracerPid zero",
				input:       "Name:\taegis\nState:\tS (sleeping)\nTracerPid:\t0\nUid:\t1000",
				expectedPID: 0,
				expectFound: true,
				expectErr:   false,
			},
			{
				name:        "tracer attached non-zero",
				input:       "Name:\taegis\nTracerPid:\t12345\nUid:\t1000",
				expectedPID: 12345,
				expectFound: true,
				expectErr:   false,
			},
			{
				name:        "tracer with spacing",
				input:       "TracerPid:   9876  \n",
				expectedPID: 9876,
				expectFound: true,
				expectErr:   false,
			},
			{
				name:        "tracer not present",
				input:       "Name:\taegis\nUid:\t1000",
				expectedPID: 0,
				expectFound: false,
				expectErr:   false,
			},
			{
				name:        "malformed tracer value",
				input:       "TracerPid:\tnot_a_number\n",
				expectedPID: 0,
				expectFound: true,
				expectErr:   true,
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				pid, found, err := parseLinuxTracerPid([]byte(tc.input))
				if tc.expectErr && err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !tc.expectErr && err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if found != tc.expectFound {
					t.Errorf("expected found=%v, got %v", tc.expectFound, found)
				}
				if pid != tc.expectedPID {
					t.Errorf("expected pid=%d, got %d", tc.expectedPID, pid)
				}
			})
		}
	})
}

func TestTimingCheck(t *testing.T) {
	t.Run("NormalExecution", func(t *testing.T) {
		check := NewTimingCheck()
		if check.Name() != "timing" {
			t.Errorf("expected name 'timing', got %s", check.Name())
		}

		result := check.Run(context.Background())
		if !result.Passed {
			t.Errorf("expected timing check to pass normally: %s", result.Evidence)
		}
	})

	t.Run("TimingAnomalyDetectedSimulated", func(t *testing.T) {
		check := NewTimingCheckWithConfig(TimingConfig{
			Iterations: 1000,
			Threshold:  10 * time.Millisecond,
			MeasureFunc: func(iterations int) time.Duration {
				return 50 * time.Millisecond
			},
		})

		result := check.Run(context.Background())
		if result.Passed {
			t.Errorf("expected timing check to fail when measured duration exceeds threshold")
		}
		if result.Severity != SeverityHigh {
			t.Errorf("expected SeverityHigh, got %s", result.Severity)
		}
		if !strings.Contains(result.Evidence, "timing anomaly detected") {
			t.Errorf("unexpected evidence message: %s", result.Evidence)
		}
	})

	t.Run("TimingAnomalyThresholdSubNanosecond", func(t *testing.T) {
		check := NewTimingCheckWithConfig(TimingConfig{
			Iterations: 50_000,
			Threshold:  1 * time.Nanosecond,
		})

		result := check.Run(context.Background())
		if result.Passed {
			t.Errorf("expected timing check to fail with 1ns threshold")
		}
	})

	t.Run("ContextCancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		check := NewTimingCheck()
		result := check.Run(ctx)
		if result.Passed {
			t.Errorf("expected check to fail when context cancelled")
		}
		if result.Severity != SeverityCritical {
			t.Errorf("expected SeverityCritical for cancelled context, got %s", result.Severity)
		}
	})
}

// dummyConn satisfies net.Conn for mock socket listener testing.
type dummyConn struct {
	net.Conn
	closed bool
}

func (c *dummyConn) Close() error {
	c.closed = true
	return nil
}

func TestEnvironmentCheck(t *testing.T) {
	t.Run("NormalExecution", func(t *testing.T) {
		check := NewEnvironmentCheck()
		if check.Name() != "environment" {
			t.Errorf("expected name 'environment', got %s", check.Name())
		}

		result := check.Run(context.Background())
		if !result.Passed {
			t.Errorf("expected environment check to pass under nominal test runner: %s", result.Evidence)
		}
	})

	t.Run("InjectedLDPreload", func(t *testing.T) {
		check := NewEnvironmentCheckWithConfig(EnvironmentConfig{
			LookupEnv: func(key string) string {
				if key == "LD_PRELOAD" {
					return "/lib/mock_hook.so"
				}
				return ""
			},
			Environ: func() []string {
				return []string{"USER=test"}
			},
			CheckSockets: false,
		})

		result := check.Run(context.Background())
		if result.Passed {
			t.Errorf("expected environment check to fail when LD_PRELOAD is present")
		}
		if result.Severity != SeverityCritical {
			t.Errorf("expected SeverityCritical, got %s", result.Severity)
		}
		if !strings.Contains(result.Evidence, "LD_PRELOAD=/lib/mock_hook.so") {
			t.Errorf("expected evidence to identify LD_PRELOAD, got: %s", result.Evidence)
		}
	})

	t.Run("InjectedDYLDInsertLibraries", func(t *testing.T) {
		check := NewEnvironmentCheckWithConfig(EnvironmentConfig{
			LookupEnv: func(key string) string {
				if key == "DYLD_INSERT_LIBRARIES" {
					return "/Library/Hook.dylib"
				}
				return ""
			},
			Environ: func() []string {
				return []string{"PATH=/usr/bin"}
			},
			CheckSockets: false,
		})

		result := check.Run(context.Background())
		if result.Passed {
			t.Errorf("expected environment check to fail when DYLD_INSERT_LIBRARIES is present")
		}
		if !strings.Contains(result.Evidence, "DYLD_INSERT_LIBRARIES=/Library/Hook.dylib") {
			t.Errorf("unexpected evidence: %s", result.Evidence)
		}
	})

	t.Run("InjectedFridaVars", func(t *testing.T) {
		check := NewEnvironmentCheckWithConfig(EnvironmentConfig{
			LookupEnv: func(key string) string {
				if key == "FRIDA_AGENT" {
					return "12.0.0"
				}
				return ""
			},
			Environ: func() []string {
				return []string{"FRIDA_AGENT=12.0.0"}
			},
			CheckSockets: false,
		})

		result := check.Run(context.Background())
		if result.Passed {
			t.Errorf("expected check to fail when FRIDA_AGENT is set")
		}
		if !strings.Contains(result.Evidence, "FRIDA_AGENT=12.0.0") {
			t.Errorf("unexpected evidence: %s", result.Evidence)
		}
	})

	t.Run("InjectedSubstringVariable", func(t *testing.T) {
		check := NewEnvironmentCheckWithConfig(EnvironmentConfig{
			LookupEnv: func(key string) string {
				return ""
			},
			Environ: func() []string {
				return []string{
					"HOME=/Users/test",
					"CUSTOM_FRIDA_GADGET=/path/gadget.dylib",
				}
			},
			CheckSockets: false,
		})

		result := check.Run(context.Background())
		if result.Passed {
			t.Errorf("expected check to fail for custom Frida variable")
		}
		if !strings.Contains(result.Evidence, "CUSTOM_FRIDA_GADGET") {
			t.Errorf("unexpected evidence: %s", result.Evidence)
		}
	})

	t.Run("ActiveSocketDetection", func(t *testing.T) {
		mockConn := &dummyConn{}
		check := NewEnvironmentCheckWithConfig(EnvironmentConfig{
			LookupEnv: func(key string) string {
				return ""
			},
			Environ: func() []string {
				return []string{"PATH=/usr/bin"}
			},
			CheckSockets: true,
			SocketPorts:  []int{27042},
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				if address == "127.0.0.1:27042" {
					return mockConn, nil
				}
				return nil, errors.New("connection refused")
			},
		})

		result := check.Run(context.Background())
		if result.Passed {
			t.Errorf("expected check to fail when mock Frida socket is open")
		}
		if !strings.Contains(result.Evidence, "active instrumentation listener open on 127.0.0.1:27042") {
			t.Errorf("unexpected evidence: %s", result.Evidence)
		}
		if !mockConn.closed {
			t.Errorf("expected probe connection to be closed")
		}
	})

	t.Run("ContextCancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		check := NewEnvironmentCheck()
		result := check.Run(ctx)
		if result.Passed {
			t.Errorf("expected check to fail on canceled context")
		}
	})
}

func TestCheckerSummaryAndReporting(t *testing.T) {
	t.Run("AllPassed", func(t *testing.T) {
		results := []CheckResult{
			{Check: "debugger", Passed: true, Evidence: "clean", Severity: SeverityInfo},
			{Check: "timing", Passed: true, Evidence: "nominal", Severity: SeverityInfo},
			{Check: "environment", Passed: true, Evidence: "nominal", Severity: SeverityInfo},
		}

		passed, report := Summarize(results)
		if !passed {
			t.Errorf("expected passed=true")
		}
		if !strings.Contains(report, "PASSED (3/3 checks passed)") {
			t.Errorf("unexpected report header: %s", report)
		}
		if !strings.Contains(report, "[PASS] debugger [info]: clean") {
			t.Errorf("unexpected report body: %s", report)
		}
	})

	t.Run("MixedResults", func(t *testing.T) {
		results := []CheckResult{
			{Check: "debugger", Passed: true, Evidence: "clean", Severity: SeverityInfo},
			{Check: "environment", Passed: false, Evidence: "LD_PRELOAD found", Severity: SeverityCritical},
		}

		passed, report := Summarize(results)
		if passed {
			t.Errorf("expected passed=false")
		}
		if !strings.Contains(report, "FAILED (1/2 checks passed)") {
			t.Errorf("unexpected report header: %s", report)
		}
		if !strings.Contains(report, "[FAIL] environment [critical]: LD_PRELOAD found") {
			t.Errorf("unexpected report body: %s", report)
		}
	})

	t.Run("EmptyResults", func(t *testing.T) {
		passed, report := Summarize(nil)
		if !passed {
			t.Errorf("expected passed=true for empty results")
		}
		if !strings.Contains(report, "no checks executed") {
			t.Errorf("unexpected empty report: %s", report)
		}
	})

	t.Run("CheckerAddAndChecks", func(t *testing.T) {
		checker := NewChecker()
		if len(checker.Checks()) != 0 {
			t.Fatalf("expected 0 checks, got %d", len(checker.Checks()))
		}

		checker.AddCheck(nil) // should be ignored
		if len(checker.Checks()) != 0 {
			t.Fatalf("expected nil check to be ignored")
		}

		timing := NewTimingCheck()
		checker.AddCheck(timing)
		if len(checker.Checks()) != 1 {
			t.Fatalf("expected 1 check, got %d", len(checker.Checks()))
		}

		// Verify defensive copying
		checks := checker.Checks()
		checks[0] = nil
		if checker.Checks()[0] == nil {
			t.Fatalf("Checks() did not return a defensive copy")
		}
	})

	t.Run("CheckerRunContextCancellation", func(t *testing.T) {
		checker := DefaultChecker()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		results := checker.Run(ctx)
		if len(results) == 0 {
			t.Fatalf("expected at least one result recording abort")
		}
		if results[0].Passed {
			t.Errorf("expected aborted result to not be passed")
		}
	})
}

func TestFallbackSignalProbe(t *testing.T) {
	attached, evidence, err := fallbackSignalTest(errors.New("simulated sysctl failure"))
	if err != nil {
		t.Fatalf("fallbackSignalTest failed: %v", err)
	}
	if attached {
		t.Errorf("expected attached=false")
	}
	if !strings.Contains(evidence, "fallback") {
		t.Errorf("expected fallback evidence, got: %s", evidence)
	}
}
