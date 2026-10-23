package respond_test

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"time"

	"github.com/oarkflow/ref/aegis/respond"
)

// logEnforcer stands in for the real enforcer (host agent, sidecar, cloud
// API). In production it runs outside the application's trust domain.
type logEnforcer struct{}

func (logEnforcer) Apply(_ context.Context, a respond.Action) error  { return nil }
func (logEnforcer) Verify(_ context.Context, a respond.Action) error { return nil }

// Example walks one incident end to end: a policy signed offline, a
// credential theft cut immediately, corroborated escalation to isolation and
// regeneration, then verified recovery and a tamper-evident log.
func Example() {
	// Offline: the policy owner signs the policy and keeps the reset key.
	policyPub, policyPriv, _ := ed25519.GenerateKey(nil)
	resetPub, _, _ := ed25519.GenerateKey(nil)
	signed, _ := respond.SignPolicy(policyPriv, respond.DefaultPolicy())

	// Online: the controller refuses anything not signed by that key.
	pol, err := respond.LoadPolicy(policyPub, signed)
	if err != nil {
		panic(err)
	}
	clock := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ctl, _ := respond.New(pol, logEnforcer{}, respond.Ed25519Ack{Pub: resetPub}, func() time.Time { return clock })
	ctx := context.Background()

	observe := func(s respond.Signal) {
		clock = clock.Add(time.Second)
		d, _ := ctl.Observe(ctx, s)
		fmt.Printf("%-18s %-16s -> %s\n", s.Source, s.Kind, d.To)
	}

	// 1. One weak anomaly only moves to Watch.
	observe(respond.Signal{Source: "timing", Kind: respond.KindAnomaly, Severity: 0.5, Confidence: 0.7})
	// 2. A confident credential-theft report revokes that credential at once.
	observe(respond.Signal{Source: "vault-audit", Kind: respond.KindCredentialTheft, Severity: 0.9, Confidence: 0.9})
	// 3. A second, independent source corroborates: isolation is allowed.
	observe(respond.Signal{Source: "runtime-monitor", Kind: respond.KindCodeExec, Severity: 1, Confidence: 0.95})
	// 4. More corroborated evidence crosses the last threshold: the workload
	//    is replaced from a trusted image and the posture resets to Watch.
	observe(respond.Signal{Source: "vault-audit", Kind: respond.KindCredentialTheft, Severity: 1, Confidence: 0.9})
	// 5. The same exploit hits the fresh workload. Regeneration removed the
	//    attacker's foothold, not the vulnerability, so it is cut again.
	observe(respond.Signal{Source: "runtime-monitor", Kind: respond.KindCodeExec, Severity: 1, Confidence: 0.95})

	fmt.Println("final level:", ctl.Level())
	fmt.Println("evidence log intact:", respond.Verify(ctl.Log().Entries()) == nil)

	// Output:
	// timing             anomaly          -> watch
	// vault-audit        credential-theft -> restricted
	// runtime-monitor    code-exec        -> isolated
	// vault-audit        credential-theft -> watch
	// runtime-monitor    code-exec        -> restricted
	// final level: restricted
	// evidence log intact: true
}
