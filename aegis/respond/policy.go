package respond

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Policy is the controller's whole decision surface. It is data, signed
// offline, so a compromised application cannot loosen it.
type Policy struct {
	// Thresholds are the decayed evidence scores that enter Watch, Restricted,
	// Isolated and Regenerate, in that order. They must be increasing.
	Thresholds [4]float64 `json:"thresholds"`
	// HalfLife is how long evidence takes to lose half its weight.
	HalfLife time.Duration `json:"half_life"`
	// QuietPeriod without accepted signals relaxes the posture one level.
	QuietPeriod time.Duration `json:"quiet_period"`
	// MinConfidence drops weak signals outright.
	MinConfidence float64 `json:"min_confidence"`
	// SourceRatePerMinute caps one source's influence (anti-abuse).
	SourceRatePerMinute int `json:"source_rate_per_minute"`
	// CorroborateSources distinct sources are needed for Isolated and above,
	// unless one signal reaches SoloConfidence.
	CorroborateSources int     `json:"corroborate_sources"`
	SoloConfidence     float64 `json:"solo_confidence"`
	// MaxRegenerations within RegenWindow, then SafeMode instead of looping.
	MaxRegenerations int           `json:"max_regenerations"`
	RegenWindow      time.Duration `json:"regen_window"`
}

// DefaultPolicy is a starting point, not a tuned value; calibrate against
// the false-positive rate of the detectors actually wired in.
func DefaultPolicy() Policy {
	return Policy{
		Thresholds:          [4]float64{0.3, 0.8, 1.6, 2.5},
		HalfLife:            10 * time.Minute,
		QuietPeriod:         15 * time.Minute,
		MinConfidence:       0.4,
		SourceRatePerMinute: 6,
		CorroborateSources:  2,
		SoloConfidence:      0.95,
		MaxRegenerations:    3,
		RegenWindow:         time.Hour,
	}
}

func (p Policy) validate() error {
	for i := 1; i < len(p.Thresholds); i++ {
		if p.Thresholds[i] <= p.Thresholds[i-1] {
			return errors.New("respond: thresholds must be strictly increasing")
		}
	}
	switch {
	case p.Thresholds[0] <= 0:
		return errors.New("respond: thresholds must be positive")
	case p.HalfLife <= 0 || p.QuietPeriod <= 0 || p.RegenWindow <= 0:
		return errors.New("respond: durations must be positive")
	case p.SourceRatePerMinute < 1 || p.CorroborateSources < 1 || p.MaxRegenerations < 1:
		return errors.New("respond: limits must be at least 1")
	case p.MinConfidence < 0 || p.MinConfidence > 1 || p.SoloConfidence < p.MinConfidence || p.SoloConfidence > 1:
		return errors.New("respond: confidence bounds out of range")
	}
	return nil
}

// levelFor maps a score to the highest level whose threshold it reaches.
func (p Policy) levelFor(score float64) Level {
	l := Normal
	for i, t := range p.Thresholds {
		if score >= t {
			l = Level(i + 1)
		}
	}
	return l
}

// SignedPolicy is a policy plus an Ed25519 signature over its JSON bytes.
type SignedPolicy struct {
	Body []byte `json:"body"`
	Sig  []byte `json:"sig"`
}

// SignPolicy is run offline by whoever holds the policy key.
func SignPolicy(priv ed25519.PrivateKey, p Policy) (SignedPolicy, error) {
	if err := p.validate(); err != nil {
		return SignedPolicy{}, err
	}
	body, err := json.Marshal(p)
	if err != nil {
		return SignedPolicy{}, err
	}
	return SignedPolicy{Body: body, Sig: ed25519.Sign(priv, body)}, nil
}

// LoadPolicy verifies the signature before decoding, so an unsigned or
// altered policy never reaches the controller.
func LoadPolicy(pub ed25519.PublicKey, sp SignedPolicy) (Policy, error) {
	if len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, sp.Body, sp.Sig) {
		return Policy{}, errors.New("respond: policy signature invalid")
	}
	var p Policy
	if err := json.Unmarshal(sp.Body, &p); err != nil {
		return Policy{}, fmt.Errorf("respond: decode policy: %w", err)
	}
	return p, p.validate()
}

// AckVerifier checks a human's signed SafeMode reset.
type AckVerifier interface{ Verify(ack []byte) bool }

// Ed25519Ack accepts a signature over ResetMessage made with the offline key.
type Ed25519Ack struct{ Pub ed25519.PublicKey }

// ResetMessage is what the operator signs to leave SafeMode.
var ResetMessage = []byte("aegis-respond:reset-safe-mode")

func (a Ed25519Ack) Verify(ack []byte) bool {
	return len(a.Pub) == ed25519.PublicKeySize && ed25519.Verify(a.Pub, ResetMessage, ack)
}
