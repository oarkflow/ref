package platform

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Multi-factor sign-in building blocks. They are small on purpose: the flow
// (when a code is asked for, where the secret is kept, what happens after too
// many wrong codes) is the application's, written in BCL.

func registerTOTPActions(r *Registry) {
	mustAction(r, "auth.totp_generate", totpGenerateAction, ActionInfo{
		Family:   "auth",
		Summary:  "Make a new TOTP shared secret and the otpauth:// URI an authenticator app imports",
		Provides: "{secret, uri}",
		Kind:     "pure",
		Config: []ConfigField{
			{Name: "issuer", Type: "string", Required: true, Summary: "Shown in the authenticator app"},
			{Name: "account_fact", Type: "fact", Required: true, Summary: "The person's account name (an email)"},
		},
	})
	mustAction(r, "auth.totp_check", totpCheckAction, ActionInfo{
		Family:   "auth",
		Summary:  "Check a TOTP code without failing: publishes {ok, step}. A code is accepted once: steps at or before last_step_fact are refused, so a code that was seen cannot be replayed",
		Provides: "{ok, step}",
		Kind:     "pure",
		Config: []ConfigField{
			{Name: "secret_fact", Type: "fact", Required: true, Summary: "Base32 secret"},
			{Name: "code_fact", Type: "fact", Required: true},
			{Name: "last_step_fact", Type: "fact", Summary: "The last time step already used"},
			{Name: "skew", Type: "int", Default: "1"},
		},
	})
	mustAction(r, "auth.recovery_codes", recoveryCodesAction, ActionInfo{
		Family:   "auth",
		Summary:  "Make single-use recovery codes: publishes {codes, packed}, where packed is the codes' hashes as \",h1,h2,\" for storing",
		Provides: "{codes, packed}",
		Kind:     "pure",
		Config:   []ConfigField{{Name: "count", Type: "int", Default: "8"}},
	})
	mustAction(r, "auth.recovery_hash", recoveryHashAction, ActionInfo{
		Family:   "auth",
		Summary:  "Hash a recovery code the way auth.recovery_codes does (case and dashes ignored)",
		Provides: "The hash",
		Kind:     "pure",
		Config:   []ConfigField{{Name: "value_fact", Type: "fact", Required: true}},
	})
}

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

var totpGenerateAction = ActionFactoryFunc(func(_ BuildContext, spec NodeSpec) (Action, error) {
	issuer, err := requiredString(spec.Config, "issuer")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	account, err := requiredString(spec.Config, "account_fact")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		who, err := factString(ctx.Inputs, account)
		if err != nil {
			return ActionResult{}, invalidInput("an account name is required")
		}
		raw := make([]byte, 20)
		if _, err := rand.Read(raw); err != nil {
			return ActionResult{}, err
		}
		secret := b32.EncodeToString(raw)
		label := url.PathEscape(issuer + ":" + who)
		uri := "otpauth://totp/" + label + "?secret=" + secret + "&issuer=" + url.QueryEscape(issuer) + "&algorithm=SHA1&digits=6&period=30"
		return singleOutput(spec, map[string]any{"secret": secret, "uri": uri}), nil
	}), nil
})

var totpCheckAction = ActionFactoryFunc(func(_ BuildContext, spec NodeSpec) (Action, error) {
	secretFact, err := requiredString(spec.Config, "secret_fact")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	codeFact, err := requiredString(spec.Config, "code_fact")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	lastFact := configString(spec.Config, "last_step_fact", "")
	skew, err := configInt(spec.Config, "skew", 1)
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		no := singleOutput(spec, map[string]any{"ok": false, "step": int64(0)})
		secret, err := factString(ctx.Inputs, secretFact)
		if err != nil || secret == "" {
			return no, nil
		}
		code, err := factString(ctx.Inputs, codeFact)
		if err != nil {
			return no, nil
		}
		code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
		key, err := b32.DecodeString(strings.ToUpper(strings.ReplaceAll(secret, " ", "")))
		if err != nil || len(code) != 6 {
			return no, nil
		}
		var last int64
		if lastFact != "" {
			if v, ok := resolvePath(ctx.Inputs, lastFact); ok && v != nil {
				last = int64(toFloat(v))
			}
		}
		now := ctx.Now
		if now.IsZero() {
			now = time.Now()
		}
		step := now.Unix() / 30
		for offset := -skew; offset <= skew; offset++ {
			s := step + int64(offset)
			if s > last && constantTimeEqual(totpCode(key, s, 6), code) {
				return singleOutput(spec, map[string]any{"ok": true, "step": s}), nil
			}
		}
		return no, nil
	}), nil
})

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var d byte
	for i := 0; i < len(a); i++ {
		d |= a[i] ^ b[i]
	}
	return d == 0
}

func normalizeRecovery(code string) string {
	return strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(code)))
}

func recoveryHash(code string) string {
	sum := sha256.Sum256([]byte("recovery:" + normalizeRecovery(code)))
	return hex.EncodeToString(sum[:])
}

var recoveryCodesAction = ActionFactoryFunc(func(_ BuildContext, spec NodeSpec) (Action, error) {
	count, err := configInt(spec.Config, "count", 8)
	if err != nil || count < 1 || count > 20 {
		return nil, fmt.Errorf("node %q: count must be between 1 and 20", spec.Name)
	}
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		const alphabet = "abcdefghjkmnpqrstuvwxyz23456789" // no look-alikes
		codes := make([]any, 0, count)
		var packed strings.Builder
		packed.WriteString(",")
		for i := 0; i < count; i++ {
			raw := make([]byte, 10)
			if _, err := rand.Read(raw); err != nil {
				return ActionResult{}, err
			}
			for j := range raw {
				raw[j] = alphabet[int(raw[j])%len(alphabet)]
			}
			code := string(raw[:5]) + "-" + string(raw[5:])
			codes = append(codes, code)
			packed.WriteString(recoveryHash(code) + ",")
		}
		return singleOutput(spec, map[string]any{"codes": codes, "packed": packed.String()}), nil
	}), nil
})

var recoveryHashAction = ActionFactoryFunc(func(_ BuildContext, spec NodeSpec) (Action, error) {
	fact, err := requiredString(spec.Config, "value_fact")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		v, err := factString(ctx.Inputs, fact)
		if err != nil {
			return singleOutput(spec, ""), nil
		}
		return singleOutput(spec, recoveryHash(v)), nil
	}), nil
})
