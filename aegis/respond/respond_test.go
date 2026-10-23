package respond

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"
)

type fakeEnforcer struct {
	applied   []Action
	failApply ActionKind
	failCheck ActionKind
}

func (f *fakeEnforcer) Apply(_ context.Context, a Action) error {
	f.applied = append(f.applied, a)
	if a.Kind == f.failApply {
		return errors.New("boom")
	}
	return nil
}

func (f *fakeEnforcer) Verify(_ context.Context, a Action) error {
	if a.Kind == f.failCheck {
		return errors.New("still permitted")
	}
	return nil
}

func (f *fakeEnforcer) count(k ActionKind) int {
	n := 0
	for _, a := range f.applied {
		if a.Kind == k {
			n++
		}
	}
	return n
}

type rig struct {
	c   *Controller
	enf *fakeEnforcer
	now time.Time
}

func newRig(t *testing.T, mutate func(*Policy)) *rig {
	t.Helper()
	r := &rig{enf: &fakeEnforcer{}, now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	p := DefaultPolicy()
	if mutate != nil {
		mutate(&p)
	}
	c, err := New(p, r.enf, nil, func() time.Time { return r.now })
	if err != nil {
		t.Fatal(err)
	}
	r.c = c
	return r
}

func (r *rig) observe(t *testing.T, s Signal) Decision {
	t.Helper()
	r.now = r.now.Add(time.Second)
	d, err := r.c.Observe(context.Background(), s)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	return d
}

func sig(src string, k Kind, sev, conf float64) Signal {
	return Signal{Source: src, Kind: k, Severity: sev, Confidence: conf}
}

func TestSingleNoisySourceCannotIsolate(t *testing.T) {
	r := newRig(t, func(p *Policy) { p.SourceRatePerMinute = 100 })
	for i := 0; i < 20; i++ {
		r.observe(t, sig("noisy", KindAnomaly, 1, 0.9))
	}
	if got := r.c.Level(); got != Restricted {
		t.Fatalf("level = %s, want restricted", got)
	}
	if r.enf.count(ActIsolate) != 0 {
		t.Fatal("isolated on one source")
	}
}

func TestRateLimitStopsFlood(t *testing.T) {
	r := newRig(t, nil)
	var throttled int
	for i := 0; i < 30; i++ {
		if r.observe(t, sig("flood", KindAnomaly, 1, 0.9)).Throttled {
			throttled++
		}
	}
	if throttled == 0 {
		t.Fatal("flood never throttled")
	}
}

func TestCredentialTheftRevokesWithoutEscalating(t *testing.T) {
	r := newRig(t, nil)
	r.observe(t, sig("vault", KindCredentialTheft, 0.5, 0.8))
	if r.enf.count(ActRevokeCreds) != 1 || r.enf.count(ActRevokeCaps) != 0 {
		t.Fatalf("actions = %+v", r.enf.applied)
	}
}

func TestLowConfidenceDropped(t *testing.T) {
	r := newRig(t, nil)
	d := r.observe(t, sig("weak", KindCodeExec, 1, 0.1))
	if d.Ignored == "" || r.c.Level() != Normal || len(r.enf.applied) != 0 {
		t.Fatalf("weak signal acted on: %+v", d)
	}
}

func TestRegenerationBudgetEndsInSafeModeAndNeedsSignedReset(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	r := newRig(t, func(p *Policy) { p.MaxRegenerations = 2; p.SourceRatePerMinute = 100 })
	r.c.ack = Ed25519Ack{Pub: pub}
	for i := 0; i < 40 && r.c.Level() != SafeMode; i++ {
		r.observe(t, sig("a", KindCodeExec, 1, 0.99))
		r.observe(t, sig("b", KindTamper, 1, 0.99))
	}
	if r.c.Level() != SafeMode {
		t.Fatalf("level = %s, want safe-mode", r.c.Level())
	}
	if got := r.enf.count(ActRegenerate); got != 2 {
		t.Fatalf("regenerations = %d, want 2", got)
	}
	if d := r.observe(t, sig("a", KindCodeExec, 1, 1)); d.Ignored == "" {
		t.Fatal("safe-mode accepted a signal")
	}
	if err := r.c.Reset([]byte("nope")); err == nil {
		t.Fatal("reset accepted a bad acknowledgement")
	}
	if err := r.c.Reset(ed25519.Sign(priv, ResetMessage)); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if r.c.Level() != Normal {
		t.Fatalf("level after reset = %s", r.c.Level())
	}
}

func TestFailedVerificationFailsSafe(t *testing.T) {
	r := newRig(t, nil)
	r.enf.failCheck = ActRevokeCaps
	r.now = r.now.Add(time.Second)
	_, err := r.c.Observe(context.Background(), sig("rt", KindCodeExec, 1, 0.9))
	if err == nil || r.c.Level() != SafeMode {
		t.Fatalf("err=%v level=%s, want safe-mode on unverifiable defence", err, r.c.Level())
	}
}

func TestQuietPeriodRelaxesAndRestores(t *testing.T) {
	r := newRig(t, nil)
	r.observe(t, sig("rt", KindCodeExec, 1, 0.9))
	if r.c.Level() < Restricted {
		t.Fatalf("level = %s", r.c.Level())
	}
	ctx := context.Background()
	for i := 0; i < 6 && (r.c.Level() > Normal || r.c.caps != 0); i++ {
		r.now = r.now.Add(r.c.pol.QuietPeriod + time.Second)
		if _, err := r.c.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if r.c.Level() != Normal || r.c.caps != 0 || r.enf.count(ActRestore) == 0 {
		t.Fatalf("level=%s caps=%d restores=%d", r.c.Level(), r.c.caps, r.enf.count(ActRestore))
	}
}

func TestPolicySignatureRequired(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	sp, err := SignPolicy(priv, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicy(pub, sp); err != nil {
		t.Fatal(err)
	}
	sp.Body = append([]byte(nil), sp.Body...)
	sp.Body[len(sp.Body)-2] ^= 1
	if _, err := LoadPolicy(pub, sp); err == nil {
		t.Fatal("tampered policy accepted")
	}
	otherPub, _, _ := ed25519.GenerateKey(nil)
	good, _ := SignPolicy(priv, DefaultPolicy())
	if _, err := LoadPolicy(otherPub, good); err == nil {
		t.Fatal("policy accepted under the wrong key")
	}
}

func TestPolicyValidation(t *testing.T) {
	p := DefaultPolicy()
	p.Thresholds = [4]float64{1, 1, 2, 3}
	if _, err := New(p, &fakeEnforcer{}, nil, nil); err == nil {
		t.Fatal("non-increasing thresholds accepted")
	}
}

func TestLogDetectsTampering(t *testing.T) {
	r := newRig(t, nil)
	r.observe(t, sig("rt", KindCodeExec, 1, 0.9))
	es := r.c.Log().Entries()
	if err := Verify(es); err != nil {
		t.Fatal(err)
	}
	es[1].Detail = "nothing happened"
	if Verify(es) == nil {
		t.Fatal("edited entry not detected")
	}
	if Verify(append(es[:1:1], es[2:]...)) == nil {
		t.Fatal("dropped entry not detected")
	}
}
