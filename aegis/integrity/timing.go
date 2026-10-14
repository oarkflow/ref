package integrity

import (
	"context"
	"fmt"
	"time"
)

const (
	// DefaultTimingIterations is the default loop count for the single-step calibration loop.
	DefaultTimingIterations = 100_000
	// DefaultTimingThreshold is the default maximum duration allowed before flagging an anomaly.
	// In typical execution, 100,000 tight iterations take < 1ms; 50ms provides ample headroom
	// against OS thread scheduling noise while detecting interactive or automated single-stepping.
	DefaultTimingThreshold = 50 * time.Millisecond
)

// computeSink prevents compiler dead-code elimination of the tight calibration loop.
var computeSink uint64

// TimingMeasureFunc defines a function measuring the duration of a loop of the given iterations.
type TimingMeasureFunc func(iterations int) time.Duration

// TimingConfig provides tunable configuration parameters for TimingCheck.
type TimingConfig struct {
	// Iterations is the number of loops to execute. Defaults to DefaultTimingIterations if <= 0.
	Iterations int
	// Threshold is the upper duration limit. Defaults to DefaultTimingThreshold if <= 0.
	Threshold time.Duration
	// MeasureFunc overrides the loop measurement routine (useful for deterministic tests).
	MeasureFunc TimingMeasureFunc
}

// TimingCheck detects human or automated single-stepping and debugger-induced execution delays
// by measuring high-resolution elapsed time across a deterministic computational sequence.
type TimingCheck struct {
	iterations  int
	threshold   time.Duration
	measureFunc TimingMeasureFunc
}

// NewTimingCheck constructs a TimingCheck with default iterations and threshold values.
func NewTimingCheck() *TimingCheck {
	return NewTimingCheckWithConfig(TimingConfig{
		Iterations: DefaultTimingIterations,
		Threshold:  DefaultTimingThreshold,
	})
}

// NewTimingCheckWithConfig constructs a TimingCheck with user-defined parameters.
func NewTimingCheckWithConfig(config TimingConfig) *TimingCheck {
	iterations := config.Iterations
	if iterations <= 0 {
		iterations = DefaultTimingIterations
	}

	threshold := config.Threshold
	if threshold <= 0 {
		threshold = DefaultTimingThreshold
	}

	return &TimingCheck{
		iterations:  iterations,
		threshold:   threshold,
		measureFunc: config.MeasureFunc,
	}
}

// Name returns the identifier for this integrity check.
func (c *TimingCheck) Name() string {
	return "timing"
}

// Run executes the timing probe and evaluates whether execution speed deviates anomalously.
func (c *TimingCheck) Run(ctx context.Context) CheckResult {
	if err := ctx.Err(); err != nil {
		return CheckResult{
			Check:    c.Name(),
			Passed:   false,
			Evidence: fmt.Sprintf("integrity: timing check aborted: %v", err),
			Severity: SeverityCritical,
		}
	}

	var elapsed time.Duration
	if c.measureFunc != nil {
		elapsed = c.measureFunc(c.iterations)
	} else {
		startTime := time.Now()
		runTightLoop(c.iterations)
		elapsed = time.Since(startTime)
	}

	if elapsed > c.threshold {
		return CheckResult{
			Check:    c.Name(),
			Passed:   false,
			Evidence: fmt.Sprintf("integrity: timing anomaly detected: %d iterations took %v (threshold: %v); potential single-stepping or execution stall", c.iterations, elapsed, c.threshold),
			Severity: SeverityHigh,
		}
	}

	return CheckResult{
		Check:    c.Name(),
		Passed:   true,
		Evidence: fmt.Sprintf("timing nominal: %d iterations completed in %v (threshold: %v)", c.iterations, elapsed, c.threshold),
		Severity: SeverityInfo,
	}
}

// runTightLoop executes a tight deterministic computational loop designed to resist
// optimization and branch prediction shortcuts.
func runTightLoop(iterations int) uint64 {
	var accumulator uint64 = 0x243f6a8885a308d3
	for i := 0; i < iterations; i++ {
		accumulator = (accumulator ^ uint64(i)) * 6364136223846793005 + 1442695040888963407
	}
	computeSink = accumulator
	return accumulator
}
