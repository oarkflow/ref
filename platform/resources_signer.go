package platform

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/oarkflow/ref/intent"
	"github.com/oarkflow/ref/signing"
)

// Asymmetric signing keys.
//
// A crypto.signer resource holds one active signing key and any number of
// verification keys, so a key can be rotated without invalidating what the
// previous key signed: the new key becomes active, the old one stays listed
// for verification until everything it signed has expired.
//
// The same key set serves every signing use in an application: auth.jwt
// tokens (EdDSA, RS256, PS256), pipeline certificates, and payloads signed
// with crypto.sign. Its public half is published as a JWKS document
// (crypto.jwks), which is all an outside party needs to verify any of them
// offline.

// Signer is the crypto.signer resource.
type Signer struct {
	name string
	keys *signing.KeySet
}

// KeySet exposes the keys for host code (and deploy revision signing).
func (s *Signer) KeySet() *signing.KeySet { return s.keys }

// JWKS renders the public keys.
func (s *Signer) JWKS() signing.JWKS { return s.keys.JWKS() }

func registerSignerResources(r *Registry) {
	mustResource(r, "crypto.signer", ResourceFactoryFunc(openSigner), ResourceKindInfo{
		Family:   "crypto",
		Summary:  "Asymmetric signing keys (Ed25519, RSA-PSS or RSA-PKCS1v15 with SHA-256) with key ids, rotation and a JWKS export",
		Provides: []string{"Signer", "Verifier", "JWKS"},
		Config: []ConfigField{
			{Name: "algorithm", Type: "string", Default: "EdDSA", Summary: "EdDSA, RS256 or PS256 (an Ed25519 key is always EdDSA)"},
			{Name: "private_key", Type: "string", Summary: "PEM (PKCS#8 or PKCS#1) of the active signing key, e.g. env.required(\"SIGNING_KEY\")"},
			{Name: "private_key_file", Type: "string", Summary: "Path to the PEM of the active signing key"},
			{Name: "key_id", Type: "string", Summary: "kid of the active key (default: its RFC 7638 thumbprint)"},
			{Name: "keys", Type: "[]object", Summary: `Extra verification keys for rotation: keys [ { key_id … public_key|public_key_file|private_key|private_key_file … algorithm … } … ]`},
			{Name: "ephemeral", Type: "bool", Default: "false", Summary: "Development only: generate an Ed25519 key at startup when no private key is configured"},
		},
	})
}

func openSigner(_ context.Context, spec ResourceSpec) (Resource, io.Closer, error) {
	if err := rejectUnknownConfig("crypto.signer", spec.Config,
		"algorithm", "private_key", "private_key_file", "key_id", "keys", "ephemeral"); err != nil {
		return nil, nil, err
	}
	algorithm, err := signing.ParseAlgorithm(configString(spec.Config, "algorithm", ""))
	if err != nil {
		return nil, nil, fmt.Errorf("crypto.signer %q: %w", spec.Name, err)
	}
	var active *signing.Key
	material, err := readKeyMaterial(spec.Config, "private_key")
	if err != nil {
		return nil, nil, fmt.Errorf("crypto.signer %q: %w", spec.Name, err)
	}
	switch {
	case len(material) > 0:
		active, err = signing.ParsePrivateKeyPEM(configString(spec.Config, "key_id", ""), algorithm, material)
		if err != nil {
			return nil, nil, fmt.Errorf("crypto.signer %q: %w", spec.Name, err)
		}
	case configBool(spec.Config, "ephemeral", false):
		active, err = signing.GenerateEd25519(configString(spec.Config, "key_id", ""))
		if err != nil {
			return nil, nil, fmt.Errorf("crypto.signer %q: %w", spec.Name, err)
		}
		slog.Warn("crypto.signer uses an ephemeral key: signatures will not verify after a restart", "resource", spec.Name, "kid", active.ID)
	}

	var verification []*signing.Key
	for i, block := range configBlocks(spec.Config, "keys", "key") {
		blockAlg, err := signing.ParseAlgorithm(configString(block, "algorithm", string(algorithm)))
		if err != nil {
			return nil, nil, fmt.Errorf("crypto.signer %q: keys[%d]: %w", spec.Name, i, err)
		}
		id := configString(block, "key_id", configString(block, "kid", ""))
		var k *signing.Key
		if private, err := readKeyMaterial(block, "private_key"); err != nil {
			return nil, nil, fmt.Errorf("crypto.signer %q: keys[%d]: %w", spec.Name, i, err)
		} else if len(private) > 0 {
			parsed, err := signing.ParsePrivateKeyPEM(id, blockAlg, private)
			if err != nil {
				return nil, nil, fmt.Errorf("crypto.signer %q: keys[%d]: %w", spec.Name, i, err)
			}
			// A retired key only verifies: dropping its private half means a
			// leak of this configuration cannot sign with it.
			k = parsed.PublicOnly()
		} else {
			public, err := readKeyMaterial(block, "public_key")
			if err != nil {
				return nil, nil, fmt.Errorf("crypto.signer %q: keys[%d]: %w", spec.Name, i, err)
			}
			if len(public) == 0 {
				return nil, nil, fmt.Errorf("crypto.signer %q: keys[%d] needs public_key, public_key_file, private_key or private_key_file", spec.Name, i)
			}
			k, err = signing.ParsePublicKeyPEM(id, blockAlg, public)
			if err != nil {
				return nil, nil, fmt.Errorf("crypto.signer %q: keys[%d]: %w", spec.Name, i, err)
			}
		}
		verification = append(verification, k)
	}
	if active == nil && len(verification) == 0 {
		return nil, nil, fmt.Errorf("crypto.signer %q: configure private_key (or private_key_file), verification keys, or ephemeral true for development", spec.Name)
	}
	set, err := signing.NewKeySet(active, verification...)
	if err != nil {
		return nil, nil, fmt.Errorf("crypto.signer %q: %w", spec.Name, err)
	}
	return &Signer{name: spec.Name, keys: set}, nil, nil
}

// jwksProvider is anything that can publish public keys: a crypto.signer, or
// an auth.jwt resource backed by one.
type jwksProvider interface {
	JWKS() signing.JWKS
}

func registerSignerActions(r *Registry) {
	mustAction(r, "crypto.sign", ActionFactoryFunc(buildCryptoSign), ActionInfo{
		Family:       "crypto",
		Summary:      "Sign a value's canonical JSON with the active key of a crypto.signer",
		ResourceKind: "crypto",
		Provides:     "The signature: { alg, kid, sig }",
		Kind:         "pure",
		Config: []ConfigField{
			{Name: "value_fact", Type: "fact", Default: "input", Summary: "The value to sign (canonical JSON: sorted keys, no whitespace)"},
		},
	})
	mustAction(r, "crypto.verify", ActionFactoryFunc(buildCryptoVerify), ActionInfo{
		Family:       "crypto",
		Summary:      "Verify a signature over a value's canonical JSON with any key of a crypto.signer",
		ResourceKind: "crypto",
		Provides:     "{ valid, kid, alg }",
		Kind:         "pure",
		Config: []ConfigField{
			{Name: "value_fact", Type: "fact", Default: "input.payload"},
			{Name: "signature_fact", Type: "fact", Default: "input.signature", Summary: "An object { alg, kid, sig }"},
			{Name: "require_valid", Type: "bool", Default: "false", Summary: "Fail with INVALID_SIGNATURE instead of publishing valid=false"},
		},
	})
	mustAction(r, "crypto.jwks", ActionFactoryFunc(buildCryptoJWKS), ActionInfo{
		Family:       "crypto",
		Summary:      "Publish the public keys of a crypto.signer (or an auth.jwt backed by one) as a JWKS document",
		ResourceKind: "crypto",
		Provides:     "{ keys: [...] }",
		Kind:         "pure",
	})
}

var errInvalidSignature = intent.Failure{
	Code:     "INVALID_SIGNATURE",
	Category: intent.CategoryInvalidInput,
	Message:  "the signature is not valid",
}

func buildCryptoSign(build BuildContext, spec NodeSpec) (Action, error) {
	signer, err := requireResource[*Signer](build, spec, "a crypto.signer resource")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknownConfig("crypto.sign", spec.Config, "value_fact"); err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	if signer.keys.Active() == nil {
		return nil, fmt.Errorf("node %q: crypto.signer %q has no active private key, so it cannot sign", spec.Name, spec.Resource)
	}
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	valueFact := configString(spec.Config, "value_fact", "input")
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		value, found := resolvePath(ctx.Inputs, valueFact)
		if !found {
			return ActionResult{}, invalidInput("nothing to sign at %q", valueFact)
		}
		sig, err := signer.keys.SignJSON(value)
		if err != nil {
			return ActionResult{}, invalidInput("the value cannot be signed: %v", err)
		}
		return singleOutput(spec, signatureMap(sig)), nil
	}), nil
}

func buildCryptoVerify(build BuildContext, spec NodeSpec) (Action, error) {
	signer, err := requireResource[*Signer](build, spec, "a crypto.signer resource")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknownConfig("crypto.verify", spec.Config, "value_fact", "signature_fact", "require_valid"); err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	valueFact := configString(spec.Config, "value_fact", "input.payload")
	signatureFact := configString(spec.Config, "signature_fact", "input.signature")
	requireValid := configBool(spec.Config, "require_valid", false)
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		value, found := resolvePath(ctx.Inputs, valueFact)
		if !found {
			return ActionResult{}, invalidInput("nothing to verify at %q", valueFact)
		}
		raw, _ := resolvePath(ctx.Inputs, signatureFact)
		sig, ok := signatureFrom(raw)
		if !ok {
			return ActionResult{}, invalidInput("a signature object { alg, kid, sig } is required at %q", signatureFact)
		}
		err := signer.keys.VerifyJSON(value, sig)
		if err != nil && requireValid {
			return ActionResult{}, errInvalidSignature
		}
		out := map[string]any{"valid": err == nil}
		if err == nil {
			out["kid"], out["alg"] = sig.KeyID, sig.Algorithm
		}
		return acknowledgement(spec, out), nil
	}), nil
}

func buildCryptoJWKS(build BuildContext, spec NodeSpec) (Action, error) {
	if spec.Resource == "" {
		return nil, fmt.Errorf("node %q: crypto.jwks needs a resource (a crypto.signer or an auth.jwt with a signer)", spec.Name)
	}
	resolved, ok := build.Resource(spec.Resource)
	if !ok {
		return nil, fmt.Errorf("node %q: resource %q is not declared", spec.Name, spec.Resource)
	}
	provider, ok := resolved.(jwksProvider)
	if !ok {
		return nil, fmt.Errorf("node %q: resource %q publishes no public keys (use a crypto.signer, or an auth.jwt with config.signer)", spec.Name, spec.Resource)
	}
	if jwt, isJWT := resolved.(*jwtAuth); isJWT && jwt.signer == nil {
		return nil, fmt.Errorf("node %q: auth.jwt %q has no config.signer, so it has no public keys to publish", spec.Name, spec.Resource)
	}
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	return ActionFunc(func(*ActionContext) (ActionResult, error) {
		jwks := provider.JWKS()
		keys := make([]any, 0, len(jwks.Keys))
		for _, k := range jwks.Keys {
			entry := map[string]any{"kty": k.Kty, "kid": k.Kid, "alg": k.Alg, "use": k.Use}
			if k.Crv != "" {
				entry["crv"], entry["x"] = k.Crv, k.X
			} else {
				entry["n"], entry["e"] = k.N, k.E
			}
			keys = append(keys, entry)
		}
		return singleOutput(spec, map[string]any{"keys": keys}), nil
	}), nil
}

func signatureMap(sig signing.Signature) map[string]any {
	return map[string]any{"alg": sig.Algorithm, "kid": sig.KeyID, "sig": sig.Value}
}

func signatureFrom(value any) (signing.Signature, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		return signing.Signature{}, false
	}
	sig := signing.Signature{
		Algorithm: Stringify(object["alg"]),
		KeyID:     Stringify(object["kid"]),
		Value:     Stringify(object["sig"]),
	}
	return sig, sig.Algorithm != "" && sig.Value != ""
}

// requireSigner resolves a config key naming a crypto.signer resource.
func requireSigner(spec ResourceSpec, key string) (*Signer, error) {
	name := configString(spec.Config, key, "")
	if name == "" {
		return nil, nil
	}
	resolved, ok := spec.resolved[name]
	if !ok {
		return nil, fmt.Errorf("%s %q: config.%s names unknown resource %q", spec.Kind, spec.Name, key, name)
	}
	signer, ok := resolved.(*Signer)
	if !ok {
		return nil, fmt.Errorf("%s %q: resource %q is not a crypto.signer", spec.Kind, spec.Name, name)
	}
	return signer, nil
}
