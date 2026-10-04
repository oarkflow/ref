package platform

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Sealing: authenticated encryption (AES-256-GCM) of a value with a key from
// configuration, for secrets an application stores itself (a provider's
// credentials in its own table). The stored form is "v1." + base64(nonce ||
// ciphertext). The key is hashed to 32 bytes, so any string of 16 characters
// or more will do; keep it out of version control (`env("NAME", ...)`).

func registerSealActions(r *Registry) {
	mustAction(r, "crypto.seal", sealAction, ActionInfo{
		Family: "crypto", Summary: "Encrypt a value (text, or any structure as JSON) with a configured key",
		Provides: "The sealed text", Kind: "pure",
		Config: []ConfigField{
			{Name: "key", Type: "string", Required: true, Summary: "Secret, 16 or more characters"},
			{Name: "value_fact", Type: "fact", Default: "input", Summary: "What to seal"},
		},
	})
	mustAction(r, "crypto.unseal", unsealAction, ActionInfo{
		Family: "crypto", Summary: "Decrypt text sealed by crypto.seal; an empty or missing value gives nothing",
		Provides: "The text, or the structure when json is set", Kind: "pure",
		Config: []ConfigField{
			{Name: "key", Type: "string", Required: true},
			{Name: "value_fact", Type: "fact", Default: "input"},
			{Name: "json", Type: "bool", Default: "false", Summary: "Parse the opened text as JSON"},
		},
	})
}

func sealGCM(key string) (cipher.AEAD, error) {
	if len(key) < 16 {
		return nil, fmt.Errorf("the sealing key must be at least 16 characters")
	}
	sum := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal encrypts text with key.
func Seal(key, text string) (string, error) {
	gcm, err := sealGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return "v1." + base64.RawStdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(text), nil)), nil
}

// Unseal decrypts what Seal produced.
func Unseal(key, sealed string) (string, error) {
	gcm, err := sealGCM(key)
	if err != nil {
		return "", err
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(sealed, "v1."))
	if err != nil || !strings.HasPrefix(sealed, "v1.") || len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("not a sealed value")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("the value was not sealed with this key")
	}
	return string(plain), nil
}

var sealAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	key := configString(spec.Config, "key", "")
	if _, err := sealGCM(key); err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	fact := configString(spec.Config, "value_fact", "input")
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		value, _ := resolvePath(ctx.Inputs, fact)
		text, ok := value.(string)
		if !ok {
			raw, err := json.Marshal(value)
			if err != nil {
				return ActionResult{}, invalidInput("the value cannot be sealed: %v", err)
			}
			text = string(raw)
		}
		out, err := Seal(key, text)
		if err != nil {
			return ActionResult{}, err
		}
		return singleOutput(spec, out), nil
	}), nil
})

var unsealAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	key := configString(spec.Config, "key", "")
	if _, err := sealGCM(key); err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	fact := configString(spec.Config, "value_fact", "input")
	asJSON := configBool(spec.Config, "json", false)
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		value, _ := resolvePath(ctx.Inputs, fact)
		sealed := Stringify(value)
		if sealed == "" {
			if asJSON {
				return singleOutput(spec, map[string]any{}), nil
			}
			return singleOutput(spec, ""), nil
		}
		plain, err := Unseal(key, sealed)
		if err != nil {
			return ActionResult{}, err
		}
		if !asJSON {
			return singleOutput(spec, plain), nil
		}
		var out any
		if err := json.Unmarshal([]byte(plain), &out); err != nil {
			return ActionResult{}, fmt.Errorf("the sealed value is not JSON: %w", err)
		}
		return singleOutput(spec, out), nil
	}), nil
})
