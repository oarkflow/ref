package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oarkflow/ref/aegis/respond"
)

func TestRespondOptionsValidate(t *testing.T) {
	ok := respondOptions{Mode: "off", Interval: 30 * time.Second}
	cases := []struct {
		name string
		mut  func(*respondOptions)
		want string
	}{
		{"bad mode", func(o *respondOptions) { o.Mode = "maybe" }, "must be"},
		{"policy without on", func(o *respondOptions) { o.PolicyPath = "p" }, "need -respond=on"},
		{"policy without key", func(o *respondOptions) { o.Mode = "on"; o.PolicyPath = "p" }, "together"},
		{"key without policy", func(o *respondOptions) { o.Mode = "on"; o.PubKeyHex = "ab" }, "together"},
		{"fast interval", func(o *respondOptions) { o.Mode = "on"; o.Interval = time.Millisecond }, "at least 1s"},
	}
	for _, tc := range cases {
		o := ok
		tc.mut(&o)
		if err := o.validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
	on := ok
	on.Mode = "on"
	if err := on.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRespondPolicyMustBeSigned(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	sp, _ := respond.SignPolicy(priv, respond.DefaultPolicy())
	raw, _ := json.Marshal(sp)
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	o := respondOptions{Mode: "on", PolicyPath: path, PubKeyHex: hex.EncodeToString(pub), Interval: time.Minute}
	if _, signed, err := o.policy(); err != nil || !signed {
		t.Fatalf("signed=%v err=%v", signed, err)
	}
	other, _, _ := ed25519.GenerateKey(nil)
	o.PubKeyHex = hex.EncodeToString(other)
	if _, _, err := o.policy(); err == nil {
		t.Fatal("policy accepted under the wrong key")
	}
}

func TestRunRespondRejectsAppDirectory(t *testing.T) {
	var out, errb strings.Builder
	err := runRun([]string{t.TempDir(), "-respond", "on"}, &out, &errb)
	if err == nil || !strings.Contains(err.Error(), "sealed .apkg") {
		t.Fatalf("err = %v", err)
	}
}

func TestRespondKeygenSignRoundTrip(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "k")
	var out, errb strings.Builder
	if err := Run([]string{"respond-keygen", "-out", prefix}, &out, &errb); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(prefix + ".key"); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode: %v %v", info, err)
	}
	if strings.Contains(out.String(), mustRead(t, prefix+".key")) {
		t.Fatal("private key printed to stdout")
	}
	if err := Run([]string{"respond-keygen", "-out", prefix}, &out, &errb); err == nil {
		t.Fatal("overwrote an existing policy key")
	}

	policy := filepath.Join(dir, "p.json")
	if err := Run([]string{"respond-sign", "-key", prefix + ".key", "-out", policy,
		"-thresholds", "0.2,0.5,1,2", "-max-regenerations", "1"}, &out, &errb); err != nil {
		t.Fatal(err)
	}
	o := respondOptions{Mode: "on", PolicyPath: policy, PubKeyHex: strings.TrimSpace(mustRead(t, prefix+".pub")), Interval: time.Minute}
	p, signed, err := o.policy()
	if err != nil || !signed {
		t.Fatalf("signed=%v err=%v", signed, err)
	}
	if p.Thresholds != [4]float64{0.2, 0.5, 1, 2} || p.MaxRegenerations != 1 {
		t.Fatalf("policy = %+v", p)
	}
	// A hand-edited policy must be rejected at load.
	edited := strings.Replace(mustRead(t, policy), `"sig": "`, `"sig": "AAAA`, 1)
	if err := os.WriteFile(policy, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := o.policy(); err == nil {
		t.Fatal("tampered policy accepted")
	}
}

func TestRespondSignRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "k")
	var out, errb strings.Builder
	if err := Run([]string{"respond-keygen", "-out", prefix}, &out, &errb); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"respond-sign"},
		{"respond-sign", "-key", prefix + ".key", "-thresholds", "1,1,2,3"},
		{"respond-sign", "-key", prefix + ".key", "-thresholds", "1,2,3"},
		{"respond-sign", "-key", prefix + ".pub"},
	} {
		if err := Run(args, &out, &errb); err == nil {
			t.Errorf("%v: accepted", args)
		}
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
