package respond

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/oarkflow/ref/aegis/integrity"
)

func localRig(t *testing.T) (*Controller, *LocalEnforcer, *int, *time.Time) {
	t.Helper()
	code := new(int)
	enf := &LocalEnforcer{Terminate: func(c int) { *code = c }}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c, err := New(DefaultPolicy(), enf, nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return c, enf, code, &now
}

func TestSignalsFromResults(t *testing.T) {
	got := SignalsFromResults([]integrity.CheckResult{
		{Check: "debugger", Passed: true, Severity: integrity.SeverityInfo},
		{Check: "debugger", Passed: false, Severity: integrity.SeverityCritical, Evidence: "TracerPid=42"},
		{Check: "timing", Passed: false, Severity: integrity.SeverityHigh},
		{Check: "newprobe", Passed: false, Severity: integrity.SeverityMedium},
	})
	if len(got) != 3 {
		t.Fatalf("signals = %d, want 3 (passed checks are not evidence)", len(got))
	}
	if got[0].Source != "integrity/debugger" || got[0].Kind != KindTamper || got[0].Confidence < DefaultPolicy().SoloConfidence {
		t.Fatalf("debugger signal = %+v", got[0])
	}
	if got[1].Confidence >= DefaultPolicy().SoloConfidence || got[1].Kind != KindAnomaly {
		t.Fatalf("timing must not be able to isolate alone: %+v", got[1])
	}
	if got[2].Confidence == 0 {
		t.Fatalf("unknown probe dropped: %+v", got[2])
	}
}

func TestLocalEnforcerRestrictsWritesOnly(t *testing.T) {
	c, enf, _, _ := localRig(t)
	d, err := c.Observe(context.Background(), Signal{Source: "integrity/environment", Kind: KindTamper, Severity: 1, Confidence: 0.85})
	if err != nil {
		t.Fatal(err)
	}
	if c.Level() < Restricted {
		t.Fatalf("level = %s", c.Level())
	}
	if enf.Gate(http.MethodPost, "/x") != http.StatusForbidden || enf.Gate(http.MethodGet, "/x") != 0 {
		t.Fatal("restricted must block writes and allow reads")
	}
	for _, a := range d.Actions {
		if a.Kind == ActRevokeCaps && a.Caps&^CapWrite != 0 {
			t.Fatalf("revoked a capability the enforcer cannot enforce: %+v", a)
		}
	}
	found := false
	for _, e := range c.Log().Entries() {
		found = found || e.Event == "unenforceable"
	}
	if !found {
		t.Fatal("unenforceable capabilities were not logged")
	}
}

func TestLocalEnforcerDebuggerEscalatesToRestart(t *testing.T) {
	c, enf, code, now := localRig(t)
	ctx := context.Background()
	// A debugger stays attached: the same near-certain signal keeps arriving.
	for i := 0; i < 30 && *code == 0; i++ {
		*now = now.Add(15 * time.Second)
		if _, err := c.Observe(ctx, Signal{Source: "integrity/debugger", Kind: KindTamper, Severity: 1, Confidence: 0.97}); err != nil {
			t.Fatal(err)
		}
	}
	if *code != ExitRegenerate {
		t.Fatalf("exit code = %d, want %d", *code, ExitRegenerate)
	}
	if enf.Gate(http.MethodGet, "/") != http.StatusServiceUnavailable {
		t.Fatal("process still serving while terminating")
	}
	if err := Verify(c.Log().Entries()); err != nil {
		t.Fatal(err)
	}
}

func TestLocalEnforcerIsolateKeepsLivenessAndRelaxes(t *testing.T) {
	c, enf, _, now := localRig(t)
	ctx := context.Background()
	c.Observe(ctx, Signal{Source: "integrity/debugger", Kind: KindTamper, Severity: 1, Confidence: 0.97})
	c.Observe(ctx, Signal{Source: "integrity/debugger", Kind: KindTamper, Severity: 1, Confidence: 0.97})
	if c.Level() < Isolated {
		t.Fatalf("level = %s", c.Level())
	}
	if enf.Gate(http.MethodGet, "/livez") != 0 || enf.Gate(http.MethodGet, "/") != http.StatusServiceUnavailable {
		t.Fatal("isolated must serve only liveness probes")
	}
	for i := 0; i < 8 && c.Level() > Normal; i++ {
		*now = now.Add(c.pol.QuietPeriod + time.Second)
		c.Tick(ctx)
	}
	if enf.Gate(http.MethodPost, "/") != 0 {
		t.Fatalf("level %s: gate did not reopen after quiet periods", c.Level())
	}
}

func TestLocalEnforcerNeedsTerminateHook(t *testing.T) {
	enf := &LocalEnforcer{}
	if err := enf.Apply(context.Background(), Action{Kind: ActRegenerate}); err == nil {
		t.Fatal("regenerate succeeded with no way to end the process")
	}
}
