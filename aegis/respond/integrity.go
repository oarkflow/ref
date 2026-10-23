package respond

import (
	"context"
	"fmt"
	"time"

	"github.com/oarkflow/ref/aegis/integrity"
)

// profile says how much to believe a probe and which attack path it implies.
type profile struct {
	kind       Kind
	confidence float64
}

// profiles reflect each probe's false-positive rate. A debugger or injected
// library is a near-certain sign of tampering; timing jitter is common on a
// busy host, so it never reaches SoloConfidence alone and needs a second
// source before the controller isolates anything.
var profiles = map[string]profile{
	"debugger":    {KindTamper, 0.97},
	"environment": {KindTamper, 0.85},
	"timing":      {KindAnomaly, 0.5},
}

var severities = map[string]float64{
	integrity.SeverityInfo:     0,
	integrity.SeverityLow:      0.25,
	integrity.SeverityMedium:   0.5,
	integrity.SeverityHigh:     0.75,
	integrity.SeverityCritical: 1,
}

// SignalsFromResults turns failed integrity checks into signals. Passed
// checks produce nothing: clean runs are not evidence, and the controller
// relaxes on its own when signals stop. Unknown probes are still reported,
// at moderate confidence, so a newly added check is never silently ignored.
func SignalsFromResults(results []integrity.CheckResult) []Signal {
	var out []Signal
	for _, r := range results {
		if r.Passed {
			continue
		}
		p, ok := profiles[r.Check]
		if !ok {
			p = profile{KindAnomaly, 0.6}
		}
		out = append(out, Signal{
			Source:     "integrity/" + r.Check,
			Kind:       p.kind,
			Severity:   severities[r.Severity],
			Confidence: p.confidence,
			Detail:     r.Evidence,
		})
	}
	return out
}

// Monitor runs the checker every interval and feeds failures to the controller
// until ctx ends. It also ticks the controller so quiet periods relax the
// posture. Errors from enforcement are passed to onErr (may be nil); the
// controller has already failed safe by then.
func Monitor(ctx context.Context, c *Controller, ch *integrity.Checker, interval time.Duration, onErr func(error)) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	report := func(err error) {
		if err != nil && onErr != nil {
			onErr(err)
		}
	}
	for {
		for _, s := range SignalsFromResults(ch.Run(ctx)) {
			_, err := c.Observe(ctx, s)
			report(err)
		}
		_, err := c.Tick(ctx)
		report(err)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Describe is a one-line summary for startup banners.
func Describe(p Policy) string {
	return fmt.Sprintf("watch>=%.1f restricted>=%.1f isolated>=%.1f regenerate>=%.1f, regenerations %d/%s",
		p.Thresholds[0], p.Thresholds[1], p.Thresholds[2], p.Thresholds[3], p.MaxRegenerations, p.RegenWindow)
}
