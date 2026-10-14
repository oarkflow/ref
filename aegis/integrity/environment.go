package integrity

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

var (
	// DefaultExactSuspiciousVars lists dynamic linker preloads and known instrumentation variables.
	DefaultExactSuspiciousVars = []string{
		"LD_PRELOAD",
		"LD_AUDIT",
		"DYLD_INSERT_LIBRARIES",
		"DYLD_FORCE_FLAT_NAMESPACE",
		"FRIDA_VERSION",
		"FRIDA_SERVER",
		"FRIDA_AGENT",
		"LIBFRIDA_CORE",
	}

	// DefaultSuspiciousSubstrings lists substrings within variable names that signal instrumentation frameworks.
	DefaultSuspiciousSubstrings = []string{
		"FRIDA",
		"XPOSED",
		"SUBSTRATE",
	}

	// DefaultInstrumentationPorts contains standard localhost ports for Frida server/agent listeners.
	DefaultInstrumentationPorts = []int{27042, 27043}

	// DefaultSocketProbeTimeout is the dial timeout for localhost instrumentation socket probes.
	DefaultSocketProbeTimeout = 25 * time.Millisecond
)

// DialContextFunc matches the net.Dialer.DialContext signature.
type DialContextFunc func(ctx context.Context, network, address string) (net.Conn, error)

// EnvironmentConfig defines the configuration options for the EnvironmentCheck.
type EnvironmentConfig struct {
	// ExactVars is the list of exact environment variable names that trigger detection.
	ExactVars []string
	// SuspiciousSubstrings flags any environment variable name containing one of these substrings.
	SuspiciousSubstrings []string
	// CheckSockets enables scanning localhost for active instrumentation daemon listeners.
	CheckSockets bool
	// SocketPorts defines the port list to probe if CheckSockets is enabled.
	SocketPorts []int
	// SocketTimeout specifies the timeout for probing each socket.
	SocketTimeout time.Duration
	// LookupEnv overrides the environment variable lookup routine (for testing).
	LookupEnv func(key string) string
	// Environ overrides the full environment slice provider (for testing).
	Environ func() []string
	// DialContext overrides the network dialer for socket probing (for testing).
	DialContext DialContextFunc
}

// EnvironmentCheck inspects the process environment for dynamic linker injections,
// hooking libraries, and active instrumentation agent sockets.
type EnvironmentCheck struct {
	exactVars            []string
	suspiciousSubstrings []string
	checkSockets         bool
	socketPorts          []int
	socketTimeout        time.Duration
	lookupEnv            func(key string) string
	environ              func() []string
	dialContext          DialContextFunc
}

// NewEnvironmentCheck constructs an EnvironmentCheck with standard detection rules.
func NewEnvironmentCheck() *EnvironmentCheck {
	return NewEnvironmentCheckWithConfig(EnvironmentConfig{
		ExactVars:            DefaultExactSuspiciousVars,
		SuspiciousSubstrings: DefaultSuspiciousSubstrings,
		CheckSockets:         true,
		SocketPorts:          DefaultInstrumentationPorts,
		SocketTimeout:        DefaultSocketProbeTimeout,
	})
}

// NewEnvironmentCheckWithConfig constructs an EnvironmentCheck with custom parameters.
func NewEnvironmentCheckWithConfig(config EnvironmentConfig) *EnvironmentCheck {
	exactVars := config.ExactVars
	if exactVars == nil {
		exactVars = DefaultExactSuspiciousVars
	}

	substrings := config.SuspiciousSubstrings
	if substrings == nil {
		substrings = DefaultSuspiciousSubstrings
	}

	socketPorts := config.SocketPorts
	if socketPorts == nil {
		socketPorts = DefaultInstrumentationPorts
	}

	socketTimeout := config.SocketTimeout
	if socketTimeout <= 0 {
		socketTimeout = DefaultSocketProbeTimeout
	}

	lookupEnv := config.LookupEnv
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}

	environ := config.Environ
	if environ == nil {
		environ = os.Environ
	}

	dialContext := config.DialContext
	if dialContext == nil {
		dialer := &net.Dialer{}
		dialContext = dialer.DialContext
	}

	return &EnvironmentCheck{
		exactVars:            exactVars,
		suspiciousSubstrings: substrings,
		checkSockets:         config.CheckSockets,
		socketPorts:          socketPorts,
		socketTimeout:        socketTimeout,
		lookupEnv:            lookupEnv,
		environ:              environ,
		dialContext:          dialContext,
	}
}

// Name returns the identifier for this integrity check.
func (c *EnvironmentCheck) Name() string {
	return "environment"
}

// Run executes the environment sanity check and returns the evaluation outcome.
func (c *EnvironmentCheck) Run(ctx context.Context) CheckResult {
	if err := ctx.Err(); err != nil {
		return CheckResult{
			Check:    c.Name(),
			Passed:   false,
			Evidence: fmt.Sprintf("integrity: environment check aborted: %v", err),
			Severity: SeverityCritical,
		}
	}

	var anomalies []string
	foundExact := make(map[string]bool)

	// 1. Check designated high-risk exact environment variables
	for _, varName := range c.exactVars {
		if val := c.lookupEnv(varName); val != "" {
			anomalies = append(anomalies, fmt.Sprintf("%s=%s", varName, val))
			foundExact[varName] = true
		}
	}

	// 2. Scan all environment entries for suspicious substrings
	allVars := c.environ()
	for _, entry := range allVars {
		pair := strings.SplitN(entry, "=", 2)
		key := pair[0]
		if foundExact[key] {
			continue
		}
		upperKey := strings.ToUpper(key)
		for _, substr := range c.suspiciousSubstrings {
			if strings.Contains(upperKey, substr) {
				val := ""
				if len(pair) > 1 {
					val = pair[1]
				}
				anomalies = append(anomalies, fmt.Sprintf("%s=%s (matched keyword %s)", key, val, substr))
				break
			}
		}
	}

	// 3. Probe for active instrumentation agent sockets if configured
	if c.checkSockets {
		for _, port := range c.socketPorts {
			if ctx.Err() != nil {
				break
			}
			address := fmt.Sprintf("127.0.0.1:%d", port)
			probeCtx, cancel := context.WithTimeout(ctx, c.socketTimeout)
			conn, dialErr := c.dialContext(probeCtx, "tcp", address)
			cancel()
			if dialErr == nil && conn != nil {
				_ = conn.Close()
				anomalies = append(anomalies, fmt.Sprintf("active instrumentation listener open on %s (possible Frida daemon)", address))
			}
		}
	}

	if len(anomalies) > 0 {
		return CheckResult{
			Check:    c.Name(),
			Passed:   false,
			Evidence: fmt.Sprintf("integrity: suspicious environment / dynamic hooks detected: %s", strings.Join(anomalies, "; ")),
			Severity: SeverityCritical,
		}
	}

	return CheckResult{
		Check:    c.Name(),
		Passed:   true,
		Evidence: "environment nominal: no dynamic preloads or instrumentation hooks detected",
		Severity: SeverityInfo,
	}
}
