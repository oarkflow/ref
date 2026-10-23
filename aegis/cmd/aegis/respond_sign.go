package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/oarkflow/ref/aegis/respond"
)

// runRespondKeygen writes an Ed25519 key pair for signing respond policies.
// Keep it separate from the package signing key: whoever can sign a policy
// can loosen the controller, so that authority should not ride along with
// the ability to publish packages.
func runRespondKeygen(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("respond-keygen", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var out string
	flags.StringVar(&out, "out", "respond", "Output file prefix: writes <prefix>.key (private) and <prefix>.pub (public)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if out == "" {
		return errors.New("aegis: respond-keygen: -out cannot be empty")
	}
	if dir := filepath.Dir(out); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("aegis: respond-keygen: create %q: %w", dir, err)
		}
	}
	privFile, pubFile := out+".key", out+".pub"
	for _, f := range []string{privFile, pubFile} {
		if _, err := os.Stat(f); err == nil {
			return fmt.Errorf("aegis: respond-keygen: %s already exists; refusing to overwrite a policy key", f)
		}
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("aegis: respond-keygen: %w", err)
	}
	// O_EXCL so a concurrent writer can never be silently replaced.
	if err := writeNew(privFile, []byte(hex.EncodeToString(priv)+"\n"), 0o600); err != nil {
		return err
	}
	pubHex := hex.EncodeToString(pub)
	if err := writeNew(pubFile, []byte(pubHex+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Generated respond policy key:\n")
	fmt.Fprintf(stdout, "  Private Key: %s (keep offline; never ship with the application)\n", privFile)
	fmt.Fprintf(stdout, "  Public Key:  %s\n", pubFile)
	fmt.Fprintf(stdout, "  Public (hex, for -respond-pubkey): %s\n", pubHex)
	return nil
}

func writeNew(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("aegis: respond: write %s: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("aegis: respond: write %s: %w", path, err)
	}
	return f.Close()
}

// runRespondSign signs an escalation policy with the key from respond-keygen.
// Unset flags keep respond.DefaultPolicy values; the policy is validated
// before signing, so a bad value fails here instead of at `aegis run`.
func runRespondSign(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("respond-sign", flag.ContinueOnError)
	flags.SetOutput(stderr)
	def := respond.DefaultPolicy()
	var (
		keyFile    string
		out        string
		thresholds string
	)
	pol := def
	flags.StringVar(&keyFile, "key", "", "Private key file from respond-keygen (required)")
	flags.StringVar(&out, "out", "respond-policy.json", "Where to write the signed policy")
	flags.StringVar(&thresholds, "thresholds", "", "Four increasing scores that enter watch,restricted,isolated,regenerate (default "+joinFloats(def.Thresholds[:])+")")
	flags.DurationVar(&pol.HalfLife, "half-life", def.HalfLife, "Time for evidence to lose half its weight")
	flags.DurationVar(&pol.QuietPeriod, "quiet-period", def.QuietPeriod, "Quiet time before the posture relaxes one level")
	flags.Float64Var(&pol.MinConfidence, "min-confidence", def.MinConfidence, "Signals below this confidence are dropped")
	flags.IntVar(&pol.SourceRatePerMinute, "source-rate", def.SourceRatePerMinute, "Max accepted signals per source per minute")
	flags.IntVar(&pol.CorroborateSources, "corroborate", def.CorroborateSources, "Independent sources needed to isolate or regenerate")
	flags.Float64Var(&pol.SoloConfidence, "solo-confidence", def.SoloConfidence, "One signal this confident may act without corroboration")
	flags.IntVar(&pol.MaxRegenerations, "max-regenerations", def.MaxRegenerations, "Regenerations allowed per window before safe-mode")
	flags.DurationVar(&pol.RegenWindow, "regen-window", def.RegenWindow, "Window for -max-regenerations")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if keyFile == "" {
		return errors.New("aegis: respond-sign: -key is required (generate one with `aegis respond-keygen`)")
	}
	if thresholds != "" {
		t, err := parseFloats(thresholds, len(pol.Thresholds))
		if err != nil {
			return fmt.Errorf("aegis: respond-sign: -thresholds: %w", err)
		}
		copy(pol.Thresholds[:], t)
	}
	raw, err := os.ReadFile(keyFile)
	if err != nil {
		return fmt.Errorf("aegis: respond-sign: read key: %w", err)
	}
	seed, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(seed) != ed25519.PrivateKeySize {
		return fmt.Errorf("aegis: respond-sign: %s is not a respond-keygen private key", keyFile)
	}
	priv := ed25519.PrivateKey(seed)
	sp, err := respond.SignPolicy(priv, pol)
	if err != nil {
		return fmt.Errorf("aegis: respond-sign: %w", err)
	}
	doc, err := json.MarshalIndent(sp, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, append(doc, '\n'), 0o644); err != nil {
		return fmt.Errorf("aegis: respond-sign: write policy: %w", err)
	}
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	fmt.Fprintf(stdout, "Signed respond policy: %s\n", out)
	fmt.Fprintf(stdout, "  %s\n", respond.Describe(pol))
	fmt.Fprintf(stdout, "  Run with: aegis run <app.apkg> -respond=on -respond-policy %s -respond-pubkey %s ...\n", out, pubHex)
	return nil
}

func joinFloats(v []float64) string {
	parts := make([]string, len(v))
	for i, f := range v {
		parts[i] = strconv.FormatFloat(f, 'g', -1, 64)
	}
	return strings.Join(parts, ",")
}

func parseFloats(s string, n int) ([]float64, error) {
	parts := strings.Split(s, ",")
	if len(parts) != n {
		return nil, fmt.Errorf("want %d comma-separated values, got %d", n, len(parts))
	}
	out := make([]float64, n)
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return nil, err
		}
		out[i] = f
	}
	return out, nil
}
