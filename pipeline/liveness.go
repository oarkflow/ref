package pipeline

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// NodeLiveness is an identity-verification node (KYC): the person proves they
// are live in front of the camera by performing a random, single-use,
// short-lived challenge, and the evidence is scored by a pluggable verifier.
//
// Operations: "challenge" issues the challenge; "verify" submits the
// evidence (result.nonce plus whatever the verifier needs, e.g. captured
// frame file references). The engine owns freshness, single use, actor
// binding, attempt limits, thresholds and the audit trail. It does not
// contain a biometric model: the host supplies one as Engine.Liveness.
const NodeLiveness = "liveness"

// Liveness defaults.
const (
	DefaultLivenessChallenges = 2
	DefaultLivenessMinScore   = 0.85
	DefaultLivenessAttempts   = 3
	DefaultLivenessTTL        = 2 * time.Minute
)

// livenessActions are the gestures a challenge can ask for.
var livenessActions = []string{"blink", "turn_left", "turn_right", "smile", "nod", "open_mouth"}

// LivenessChallenge is the pending challenge of a liveness node.
type LivenessChallenge struct {
	Nonce     string    `json:"nonce"`
	Actions   []string  `json:"actions"`
	Actor     string    `json:"actor"`
	ExpiresAt time.Time `json:"expires_at"`
}

// LivenessRequest is what a verifier is asked to judge.
type LivenessRequest struct {
	CaseID    string
	Node      string
	Actor     string
	Actions   []string
	Evidence  map[string]any // result of the verify call, minus the nonce
	Reference any            // value at the node's match_with path, if any
}

// LivenessResult is a verifier's judgement.
type LivenessResult struct {
	Live       bool
	Score      float64  // liveness confidence, 0..1
	MatchScore *float64 // face match against Reference, 0..1, when compared
	Reasons    []string
}

// LivenessVerifier scores liveness evidence.
type LivenessVerifier interface {
	VerifyLiveness(ctx context.Context, req LivenessRequest) (LivenessResult, error)
}

func (e *Engine) livenessChallenge(n *Node, ns *NodeState, actor Actor, now time.Time) (*LivenessChallenge, error) {
	count := n.Challenges
	if count <= 0 {
		count = DefaultLivenessChallenges
	}
	count = min(count, len(livenessActions))
	pool := append([]string(nil), livenessActions...)
	actions := make([]string, 0, count)
	for range count {
		i, err := rand.Int(rand.Reader, big.NewInt(int64(len(pool))))
		if err != nil {
			return nil, err
		}
		actions = append(actions, pool[i.Int64()])
		pool = append(pool[:i.Int64()], pool[i.Int64()+1:]...)
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	ttl := DefaultLivenessTTL
	if n.ChallengeTTL != "" {
		d, err := time.ParseDuration(n.ChallengeTTL)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("liveness node %q: bad challenge_ttl %q", n.Name, n.ChallengeTTL)
		}
		ttl = d
	}
	return &LivenessChallenge{Nonce: hex.EncodeToString(raw), Actions: actions, Actor: actor.ID, ExpiresAt: now.Add(ttl)}, nil
}

// livenessOp runs the "challenge" and "verify" operations.
func (e *Engine) livenessOp(ctx context.Context, c *Case, stage string, n *Node, ns *NodeState, actor Actor, verb string, input NodeInput, now time.Time, entry *Entry) error {
	if ns.Status == NodePassed {
		return badState("liveness check %q is already passed", n.Name)
	}
	if ns.Status == NodeFailed && ns.Attempts >= livenessAttempts(n) {
		return badState("liveness check %q failed too many times; a reviewer must waive or reset it", n.Name)
	}
	switch verb {
	case "challenge":
		ch, err := e.livenessChallenge(n, ns, actor, now)
		if err != nil {
			return err
		}
		ns.Challenge = ch
		ns.Status = NodeInProgress
		// The nonce rides in the result so views, which show a node's
		// result, hand it to the client. It only works for the actor it was
		// issued to.
		ns.Result = map[string]any{"nonce": ch.Nonce, "actions": ch.Actions, "expires_at": ch.ExpiresAt, "attempts_left": livenessAttempts(n) - ns.Attempts}
		entry.To = "challenge issued"
	case "verify":
		return e.livenessVerify(ctx, c, stage, n, ns, actor, input, now, entry)
	}
	return nil
}

func livenessAttempts(n *Node) int {
	if n.MaxAttempts > 0 {
		return n.MaxAttempts
	}
	return DefaultLivenessAttempts
}

func (e *Engine) livenessVerify(ctx context.Context, c *Case, stage string, n *Node, ns *NodeState, actor Actor, input NodeInput, now time.Time, entry *Entry) error {
	if e.Liveness == nil {
		return badState("no liveness verifier is configured")
	}
	ch := ns.Challenge
	nonce, _ := input.Result["nonce"].(string)
	switch {
	case ch == nil:
		return badState("request a challenge for %q first", n.Name)
	case ch.Actor != actor.ID:
		return forbidden("this challenge was issued to someone else")
	case now.After(ch.ExpiresAt):
		ns.Challenge = nil
		return badState("the challenge expired; request a new one")
	case subtle.ConstantTimeCompare([]byte(nonce), []byte(ch.Nonce)) != 1:
		return &ValidationError{Message: "the challenge does not match", Fields: []FieldError{{Path: "result.nonce", Rule: "challenge", Message: "send the nonce of the current challenge"}}}
	}
	// Single use: whatever the verdict, this challenge is spent.
	ns.Challenge = nil
	ns.Attempts++

	evidence := make(map[string]any, len(input.Result))
	for k, v := range input.Result {
		if k != "nonce" {
			evidence[k] = v
		}
	}
	req := LivenessRequest{CaseID: c.ID, Node: n.Name, Actor: actor.ID, Actions: ch.Actions, Evidence: evidence}
	if n.MatchWith != "" {
		req.Reference, _ = c.Get(n.MatchWith)
	}
	res, err := e.Liveness.VerifyLiveness(ctx, req)
	if err != nil {
		// A verifier outage is not the person's failure: refund the attempt.
		ns.Attempts--
		return fmt.Errorf("liveness verifier: %w", err)
	}
	minScore := n.MinScore
	if minScore <= 0 {
		minScore = DefaultLivenessMinScore
	}
	var reasons []string
	if !res.Live {
		reasons = append(reasons, "liveness not established")
	}
	if res.Score < minScore {
		reasons = append(reasons, fmt.Sprintf("liveness score %.2f below %.2f", res.Score, minScore))
	}
	if n.MatchWith != "" {
		minMatch := n.MinMatch
		if minMatch <= 0 {
			minMatch = DefaultLivenessMinScore
		}
		switch {
		case res.MatchScore == nil:
			reasons = append(reasons, "face was not compared with the reference")
		case *res.MatchScore < minMatch:
			reasons = append(reasons, fmt.Sprintf("face match %.2f below %.2f", *res.MatchScore, minMatch))
		}
	}
	ns.Result = map[string]any{"score": res.Score, "attempts": ns.Attempts, "passed": len(reasons) == 0}
	if res.MatchScore != nil {
		ns.Result["match_score"] = *res.MatchScore
	}
	if len(reasons) == 0 {
		ns.Status = NodePassed
		entry.To = fmt.Sprintf("passed (%.2f)", res.Score)
		c.emit("identity.liveness_passed", stage, actor.ID, now, map[string]any{"node": n.Name, "score": res.Score})
		return nil
	}
	reasons = append(reasons, res.Reasons...)
	ns.Result["reasons"] = reasons
	ns.Result["attempts_left"] = max(0, livenessAttempts(n)-ns.Attempts)
	ns.Status = NodeInProgress
	if ns.Attempts >= livenessAttempts(n) {
		ns.Status = NodeFailed
	}
	entry.To = "not passed: " + strings.Join(reasons, "; ")
	c.emit("identity.liveness_failed", stage, actor.ID, now, map[string]any{"node": n.Name, "attempts": ns.Attempts})
	return nil
}
