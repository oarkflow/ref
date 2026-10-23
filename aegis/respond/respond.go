// Package respond is Aegis's autonomous escalation controller.
//
// It turns detection signals into bounded, verified defensive actions:
//
//	detect -> corroborate -> contain -> restrict -> regenerate -> verify -> decay
//
// The controller does not detect anything itself and does not patch anything.
// Detectors (integrity checks, runtime monitors, attestation) feed it Signals;
// an Enforcer, which must run outside the protected application's trust
// domain, carries out the actions. What the controller adds is the policy:
// which level to move to, which evidence is enough, and when to stop.
//
// Limits are deliberate. Destructive levels need corroboration so one noisy
// source cannot force an outage; each source is rate limited; regeneration is
// budgeted, and exhausting the budget ends in SafeMode instead of looping.
// SafeMode never clears on its own, only a signed acknowledgement resets it.
package respond

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

// Level is the controller's current security posture, ordered by severity.
type Level int

const (
	Normal     Level = iota // full capabilities
	Watch                   // same capabilities, extra evidence collection
	Restricted              // dangerous capabilities revoked
	Isolated                // workload cut off, credentials revoked
	Regenerate              // workload replaced from a trusted image
	SafeMode                // controlled shutdown, human reset required
)

var levelNames = [...]string{"normal", "watch", "restricted", "isolated", "regenerate", "safe-mode"}

func (l Level) String() string {
	if l < 0 || int(l) >= len(levelNames) {
		return fmt.Sprintf("level(%d)", int(l))
	}
	return levelNames[l]
}

// Capability is one permission the enforcer can revoke independently.
type Capability uint8

const (
	CapRead Capability = 1 << iota
	CapWrite
	CapTransfer // move value out: payments, exports
	CapEgress   // outbound network connections
	CapSpawn    // start processes
)

// Dangerous is every capability that can move data or code out of the system.
const Dangerous = CapWrite | CapTransfer | CapEgress | CapSpawn

// Kind classifies an attack path so the response can cut that path, not
// just add a generic layer.
type Kind string

const (
	KindAnomaly         Kind = "anomaly"          // unexplained behaviour
	KindCredentialTheft Kind = "credential-theft" // a secret left its owner
	KindCodeExec        Kind = "code-exec"        // attacker-controlled code runs
	KindTamper          Kind = "tamper"           // debugger, hook or modified image
)

// Signal is one piece of evidence from an independent source.
type Signal struct {
	Source     string  // detector identity; corroboration counts distinct sources
	Kind       Kind    // attack path
	Severity   float64 // 0..1 impact if true
	Confidence float64 // 0..1 detector's belief that it is true
	Detail     string
}

// ActionKind is one bounded step the Enforcer can take.
type ActionKind string

const (
	ActRevokeCaps  ActionKind = "revoke-capabilities"
	ActRevokeCreds ActionKind = "revoke-credentials"
	ActIsolate     ActionKind = "isolate-workload"
	ActRegenerate  ActionKind = "regenerate-workload"
	ActSafeMode    ActionKind = "safe-mode"
	ActRestore     ActionKind = "restore-capabilities"
	ActRelease     ActionKind = "release-isolation"
)

// Action is a request to the Enforcer.
type Action struct {
	Kind   ActionKind
	Caps   Capability // for ActRevokeCaps and ActRestore
	Reason string
}

// Enforcer applies actions and proves they took effect. It is the only
// component with authority over the workload and lives in a separate trust
// domain (sidecar, host agent, orchestrator), never inside the application.
type Enforcer interface {
	Apply(ctx context.Context, a Action) error
	// Verify independently confirms the action's effect (for example, by
	// attempting the revoked operation and expecting a denial).
	Verify(ctx context.Context, a Action) error
}

// Capable is optionally implemented by an Enforcer to declare which
// capabilities it can really revoke. The controller revokes only those and
// logs the rest as unenforceable, rather than claiming a cut it cannot make.
type Capable interface{ Supports() Capability }

// Decision reports what one Observe call did.
type Decision struct {
	From, To  Level
	Score     float64
	Sources   int
	Actions   []Action
	Throttled bool   // signal dropped by the per-source rate limit
	Ignored   string // non-empty when the signal was dropped, and why
}

// Controller owns the posture. It is safe for concurrent use.
type Controller struct {
	mu       sync.Mutex
	pol      Policy
	enf      Enforcer
	ack      AckVerifier
	now      func() time.Time
	log      *Log
	level    Level
	events   []event
	regens   []time.Time
	last     time.Time // last accepted signal
	caps     Capability
	credsCut bool
}

type event struct {
	source string
	weight float64
	at     time.Time
}

// New builds a controller. now may be nil to use the wall clock.
func New(p Policy, enf Enforcer, ack AckVerifier, now func() time.Time) (*Controller, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	if enf == nil {
		return nil, errors.New("respond: enforcer is required")
	}
	if now == nil {
		now = time.Now
	}
	c := &Controller{pol: p, enf: enf, ack: ack, now: now, log: NewLog()}
	c.log.Append(now(), "start", "policy loaded")
	return c, nil
}

// Level returns the current posture.
func (c *Controller) Level() Level {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.level
}

// Log returns the tamper-evident evidence log.
func (c *Controller) Log() *Log { return c.log }

// Observe folds one signal into the posture and escalates if the evidence
// warrants it. Escalation applies each intermediate level in order, verifying
// each, so a failed verification pushes the posture further instead of
// leaving a silent gap.
func (c *Controller) Observe(ctx context.Context, s Signal) (Decision, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	d := Decision{From: c.level, To: c.level}

	if c.level == SafeMode {
		d.Ignored = "safe-mode: waiting for signed reset"
		return d, nil
	}
	if s.Source == "" || s.Severity < 0 || s.Severity > 1 || s.Confidence < 0 || s.Confidence > 1 {
		d.Ignored = "malformed signal"
		c.log.Append(now, "drop", d.Ignored)
		return d, nil
	}
	if s.Confidence < c.pol.MinConfidence {
		d.Ignored = "below minimum confidence"
		c.log.Append(now, "drop", fmt.Sprintf("%s: %s (%.2f)", s.Source, d.Ignored, s.Confidence))
		return d, nil
	}
	if c.recentFrom(s.Source, now) >= c.pol.SourceRatePerMinute {
		d.Throttled, d.Ignored = true, "source rate limit"
		c.log.Append(now, "throttle", s.Source)
		return d, nil
	}

	c.events = append(c.events, event{s.Source, s.Severity * s.Confidence, now})
	c.last = now
	c.log.Append(now, "signal", fmt.Sprintf("%s %s sev=%.2f conf=%.2f %s", s.Source, s.Kind, s.Severity, s.Confidence, s.Detail))
	d.Score, d.Sources = c.scoreLocked(now)

	// Path interruption first: cut only the path this evidence points at.
	if a, ok := pathAction(s); ok {
		if a, ok = c.enforceable(now, a); ok && !c.cutAlready(a) {
			if err := c.run(ctx, now, a, &d); err != nil {
				return d, c.failSafe(ctx, now, &d, err)
			}
			c.markCut(a)
		}
	}

	target := c.pol.levelFor(d.Score)
	// Destructive levels need independent agreement, or one near-certain source.
	if target >= Isolated && d.Sources < c.pol.CorroborateSources && s.Confidence < c.pol.SoloConfidence {
		target = Restricted
	}
	for next := c.level + 1; next <= target; next++ {
		if err := c.step(ctx, now, next, s, &d); err != nil {
			return d, c.failSafe(ctx, now, &d, err)
		}
		if c.level == SafeMode {
			break
		}
	}
	d.To = c.level
	return d, nil
}

// Tick decays old evidence and relaxes the posture one level per quiet
// period, so a false alarm does not leave the system degraded forever.
// Regenerate and SafeMode never relax here.
func (c *Controller) Tick(ctx context.Context) (Level, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.level >= Regenerate || now.Sub(c.last) < c.pol.QuietPeriod || (c.level == Normal && c.caps == 0) {
		return c.level, nil
	}
	if c.level == Isolated {
		a := Action{Kind: ActRelease, Reason: "quiet period elapsed"}
		if err := c.enf.Apply(ctx, a); err != nil {
			c.log.Append(now, "action-failed", fmt.Sprintf("%s: %v", a.Kind, err))
			return c.level, err
		}
		if err := c.enf.Verify(ctx, a); err != nil {
			c.log.Append(now, "verify-failed", fmt.Sprintf("%s: %v", a.Kind, err))
			return c.level, err
		}
		c.log.Append(now, "action", string(a.Kind))
	}
	if c.level > Normal {
		c.level--
	}
	// Capabilities come back once the posture is below Restricted. Revoked
	// credentials are not restored: regeneration issues new ones.
	if c.level < Restricted && c.caps != 0 {
		a := Action{Kind: ActRestore, Caps: c.caps, Reason: "quiet period elapsed"}
		if err := c.enf.Apply(ctx, a); err != nil {
			c.log.Append(now, "action-failed", fmt.Sprintf("%s: %v", a.Kind, err))
			return c.level, err
		}
		c.log.Append(now, "action", string(a.Kind))
		c.caps = 0
	}
	c.last = now
	c.log.Append(now, "relax", c.level.String())
	return c.level, nil
}

// Reset leaves SafeMode. The acknowledgement must verify against the key the
// controller was built with, so only the holder of the offline key can do it.
func (c *Controller) Reset(ack []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.level != SafeMode {
		return errors.New("respond: not in safe-mode")
	}
	if c.ack == nil || !c.ack.Verify(ack) {
		c.log.Append(c.now(), "reset-denied", "bad acknowledgement")
		return errors.New("respond: invalid reset acknowledgement")
	}
	c.level, c.events, c.regens, c.caps, c.credsCut = Normal, nil, nil, 0, false
	c.log.Append(c.now(), "reset", "signed acknowledgement accepted")
	return nil
}

func (c *Controller) step(ctx context.Context, now time.Time, next Level, s Signal, d *Decision) error {
	reason := fmt.Sprintf("%s via %s", next, s.Source)
	switch next {
	case Watch:
		// Posture only: detectors and the log raise their verbosity.
	case Restricted:
		a, ok := c.enforceable(now, Action{Kind: ActRevokeCaps, Caps: Dangerous, Reason: reason})
		if ok && c.caps&a.Caps != a.Caps {
			if err := c.run(ctx, now, a, d); err != nil {
				return err
			}
			c.caps |= a.Caps
		}
	case Isolated:
		if !c.credsCut {
			a := Action{Kind: ActRevokeCreds, Reason: reason}
			if err := c.run(ctx, now, a, d); err != nil {
				return err
			}
			c.credsCut = true
		}
		if err := c.run(ctx, now, Action{Kind: ActIsolate, Reason: reason}, d); err != nil {
			return err
		}
	case Regenerate:
		if c.regenBudgetSpent(now) {
			c.log.Append(now, "budget", "regeneration budget spent")
			return c.enterSafeMode(ctx, now, d, "regeneration budget spent")
		}
		a := Action{Kind: ActRegenerate, Reason: reason}
		if err := c.run(ctx, now, a, d); err != nil {
			return err
		}
		// Verified clean replacement: new workload, old evidence is moot.
		c.regens = append(c.regens, now)
		c.events, c.caps, c.credsCut = nil, 0, false
		c.level = Watch
		c.log.Append(now, "recovered", "workload replaced, watching")
		return nil
	case SafeMode:
		return c.enterSafeMode(ctx, now, d, reason)
	}
	c.level = next
	c.log.Append(now, "level", next.String())
	return nil
}

// run applies and verifies one action, recording both outcomes.
func (c *Controller) run(ctx context.Context, now time.Time, a Action, d *Decision) error {
	if err := c.enf.Apply(ctx, a); err != nil {
		c.log.Append(now, "action-failed", fmt.Sprintf("%s: %v", a.Kind, err))
		return fmt.Errorf("apply %s: %w", a.Kind, err)
	}
	if err := c.enf.Verify(ctx, a); err != nil {
		c.log.Append(now, "verify-failed", fmt.Sprintf("%s: %v", a.Kind, err))
		return fmt.Errorf("verify %s: %w", a.Kind, err)
	}
	d.Actions = append(d.Actions, a)
	c.log.Append(now, "action", fmt.Sprintf("%s %s", a.Kind, a.Reason))
	return nil
}

func (c *Controller) enterSafeMode(ctx context.Context, now time.Time, d *Decision, why string) error {
	a := Action{Kind: ActSafeMode, Reason: why}
	// Safe mode is the end of the line: record it even if the enforcer errs.
	err := c.enf.Apply(ctx, a)
	c.level = SafeMode
	d.Actions = append(d.Actions, a)
	c.log.Append(now, "level", "safe-mode: "+why)
	return err
}

// failSafe handles an action that could not be applied or verified. An
// unenforceable defence is no defence, so jump to SafeMode.
func (c *Controller) failSafe(ctx context.Context, now time.Time, d *Decision, cause error) error {
	if c.level != SafeMode {
		_ = c.enterSafeMode(ctx, now, d, "enforcement failed: "+cause.Error())
	}
	d.To = c.level
	return cause
}

func (c *Controller) recentFrom(source string, now time.Time) int {
	n := 0
	for _, e := range c.events {
		if e.source == source && now.Sub(e.at) < time.Minute {
			n++
		}
	}
	return n
}

func (c *Controller) regenBudgetSpent(now time.Time) bool {
	n := 0
	for _, t := range c.regens {
		if now.Sub(t) < c.pol.RegenWindow {
			n++
		}
	}
	return n >= c.pol.MaxRegenerations
}

// scoreLocked sums exponentially decayed evidence and counts the sources that
// still carry meaningful weight.
func (c *Controller) scoreLocked(now time.Time) (float64, int) {
	var total float64
	live := map[string]float64{}
	kept := c.events[:0]
	for _, e := range c.events {
		w := e.weight * math.Exp2(-float64(now.Sub(e.at))/float64(c.pol.HalfLife))
		if w < 0.01 {
			continue // fully decayed
		}
		kept = append(kept, e)
		total += w
		live[e.source] += w
	}
	c.events = kept
	n := 0
	for _, w := range live {
		if w >= 0.1 {
			n++
		}
	}
	return total, n
}

// pathAction maps an attack path to the smallest immediate cut.
func pathAction(s Signal) (Action, bool) {
	switch s.Kind {
	case KindCredentialTheft:
		return Action{Kind: ActRevokeCreds, Reason: "credential theft via " + s.Source}, s.Confidence >= 0.7
	case KindCodeExec:
		return Action{Kind: ActRevokeCaps, Caps: CapEgress | CapSpawn, Reason: "code execution via " + s.Source}, s.Confidence >= 0.7
	case KindTamper:
		return Action{Kind: ActRevokeCaps, Caps: CapTransfer, Reason: "tamper via " + s.Source}, s.Confidence >= 0.7
	}
	return Action{}, false
}

func (c *Controller) cutAlready(a Action) bool {
	if a.Kind == ActRevokeCreds {
		return c.credsCut
	}
	return c.caps&a.Caps == a.Caps
}

func (c *Controller) markCut(a Action) {
	if a.Kind == ActRevokeCreds {
		c.credsCut = true
		return
	}
	c.caps |= a.Caps
}

// enforceable narrows a capability revocation to what the enforcer supports.
// It reports false when nothing is left to revoke.
func (c *Controller) enforceable(now time.Time, a Action) (Action, bool) {
	if a.Kind != ActRevokeCaps {
		return a, true
	}
	cap, ok := c.enf.(Capable)
	if !ok {
		return a, true
	}
	if missing := a.Caps &^ cap.Supports(); missing != 0 {
		c.log.Append(now, "unenforceable", fmt.Sprintf("cannot revoke capabilities %#b here; needs an OS or sidecar enforcer", missing))
	}
	a.Caps &= cap.Supports()
	return a, a.Caps != 0
}
