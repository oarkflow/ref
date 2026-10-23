package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/oarkflow/ref/aegis/integrity"
	"github.com/oarkflow/ref/aegis/respond"
	"github.com/oarkflow/ref/aegis/vfs"
)

// respondOptions are the `aegis run -respond*` flags.
type respondOptions struct {
	Mode       string // "off" or "on"
	PolicyPath string // JSON respond.SignedPolicy
	PubKeyHex  string // Ed25519 public key (hex) that signed the policy
	Interval   time.Duration
}

func (o respondOptions) enabled() bool { return o.Mode == "on" }

func (o respondOptions) validate() error {
	switch {
	case o.Mode != "off" && o.Mode != "on":
		return fmt.Errorf("aegis: run: -respond must be %q or %q, got %q", "off", "on", o.Mode)
	case o.Mode == "off" && (o.PolicyPath != "" || o.PubKeyHex != ""):
		return errors.New("aegis: run: -respond-policy and -respond-pubkey need -respond=on")
	case (o.PolicyPath == "") != (o.PubKeyHex == ""):
		return errors.New("aegis: run: -respond-policy and -respond-pubkey must be given together (an unsigned policy is never accepted)")
	case o.Interval < time.Second:
		return errors.New("aegis: run: -respond-interval must be at least 1s")
	}
	return nil
}

// policy returns the signed policy, or the built-in default when none is given.
func (o respondOptions) policy() (p respond.Policy, signed bool, err error) {
	if o.PolicyPath == "" {
		return respond.DefaultPolicy(), false, nil
	}
	pub, err := hex.DecodeString(strings.TrimSpace(o.PubKeyHex))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return p, false, errors.New("aegis: run: -respond-pubkey must be a 32-byte Ed25519 public key in hex")
	}
	raw, err := os.ReadFile(o.PolicyPath)
	if err != nil {
		return p, false, fmt.Errorf("aegis: run: read respond policy: %w", err)
	}
	var sp respond.SignedPolicy
	if err := json.Unmarshal(raw, &sp); err != nil {
		return p, false, fmt.Errorf("aegis: run: decode respond policy: %w", err)
	}
	p, err = respond.LoadPolicy(ed25519.PublicKey(pub), sp)
	return p, err == nil, err
}

// respondRuntime is a started controller plus the hooks the server needs.
type respondRuntime struct {
	enforcer *respond.LocalEnforcer
	ctl      *respond.Controller
	exit     func() int // exit code requested by the controller, 0 if none
	stop     func()
}

// startRespond builds the controller, starts the integrity monitor and
// returns the request gate. cancel ends the server when the controller
// decides the process must be replaced or shut down.
func startRespond(ctx context.Context, o respondOptions, fs *vfs.MemFS, cancel context.CancelFunc, stdout, stderr io.Writer) (*respondRuntime, error) {
	pol, signed, err := o.policy()
	if err != nil {
		return nil, err
	}
	var (
		codeCh = make(chan int, 1)
		enf    = &respond.LocalEnforcer{}
	)
	enf.Wipe = append(enf.Wipe, fs.Wipe)
	enf.Terminate = func(code int) {
		select {
		case codeCh <- code:
		default:
		}
		cancel()
	}
	ctl, err := respond.New(pol, enf, nil, nil)
	if err != nil {
		return nil, err
	}
	ctl.Log().SetSink(func(e respond.Entry) {
		fmt.Fprintf(stderr, "  [Respond] #%d %s %s hash=%.12s\n", e.Seq, e.Event, e.Detail, e.Hash)
	})

	mctx, stop := context.WithCancel(ctx)
	go respond.Monitor(mctx, ctl, integrity.DefaultChecker(), o.Interval, func(err error) {
		fmt.Fprintf(stderr, "  [Respond] enforcement failed: %v\n", err)
	})

	if signed {
		fmt.Fprintf(stdout, "  [Respond] ON, signed policy %s: %s\n", o.PolicyPath, respond.Describe(pol))
	} else {
		fmt.Fprintf(stdout, "  [Respond] ON, built-in unsigned policy (pass -respond-policy/-respond-pubkey to pin one): %s\n", respond.Describe(pol))
	}
	fmt.Fprintf(stdout, "  [Respond] in-process enforcement: revokes writes, isolates, wipes and exits (codes %d replace, %d do-not-restart); egress/spawn limits need an OS sandbox\n",
		respond.ExitRegenerate, respond.ExitSafeMode)

	return &respondRuntime{
		enforcer: enf,
		ctl:      ctl,
		stop:     stop,
		exit: func() int {
			select {
			case c := <-codeCh:
				return c
			default:
				return 0
			}
		},
	}, nil
}

// exitError carries a process exit code through the error return path.
type exitError struct {
	Code int
	Msg  string
}

func (e *exitError) Error() string { return e.Msg }
