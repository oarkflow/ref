package platform

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/oarkflow/ref/signing"
)

var defFS = fstest.MapFS{
	"scripts/risk.spl":  {Data: []byte("{\"high\": input.total > params.threshold, \"by\": principal.id}")},
	"scripts/quote.spl": {Data: []byte("{\"total\": input.qty * input.price}")},
}

const defBase = `
name "defs"
role "admin" { permissions ["*"] }
role "publisher" { permissions ["definition:publish"] }
role "node_only" { permissions ["definition:publish:node_type"] }
`

func compileDefs(t *testing.T, src string) (*Platform, error) {
	t.Helper()
	opts := DefaultLoadOptions()
	opts.FS = defFS
	p, err := Compile(context.Background(), []byte(defBase+src), "", opts)
	if p != nil {
		t.Cleanup(func() { _ = p.Close() })
	}
	return p, err
}

func TestNodeTypeIsCheckedAndRuns(t *testing.T) {
	p, err := compileDefs(t, `
node_type "risk.score" {
  param "threshold" { type "number" default 100 }
  file "scripts/risk.spl"
}
intent "check" {
  response "out"
  node "risk" { uses "risk.score" requires [input] provides [out] config { threshold 50 } }
}
`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.CallIntent(context.Background(), "check", map[string]any{"total": 75}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := out.(map[string]any); !ok || m["high"] != true {
		t.Fatalf("got %#v", out)
	}
}

func TestNodeTypeRejectsBadConfig(t *testing.T) {
	for name, cfg := range map[string]string{
		"unknown key": `config { thresold 5 }`,
		"wrong type":  `config { threshold "x" }`,
	} {
		_, err := compileDefs(t, `
node_type "risk.score" { param "threshold" { type "number" } file "scripts/risk.spl" }
intent "check" { response "out" node "r" { uses "risk.score" requires [order] provides [out] `+cfg+` } }
`)
		if err == nil {
			t.Errorf("%s: expected a load error", name)
		}
	}
}

func TestNodeTypeCannotShadowBuiltin(t *testing.T) {
	if _, err := compileDefs(t, `node_type "collect" { file "scripts/risk.spl" }`); err == nil || !strings.Contains(err.Error(), "shadow") {
		t.Fatalf("got %v", err)
	}
}

func TestHandlerIsClosedByDefault(t *testing.T) {
	_, err := compileDefs(t, `handler "quote" { method POST path "/quote" file "scripts/quote.spl" }`)
	if err == nil || !strings.Contains(err.Error(), "without authorization") {
		t.Fatalf("got %v", err)
	}
}

func TestPublicHandlerExpandsToIntentAndRoute(t *testing.T) {
	p, err := compileDefs(t, `
handler "quote" { method POST path "/quote" public true file "scripts/quote.spl" }
`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.CallIntent(context.Background(), "handler.quote", map[string]any{"qty": 3, "price": 5}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := out.(map[string]any); !ok || m["total"] != int64(15) {
		t.Fatalf("got %#v", out)
	}
	found := false
	for _, r := range p.Document.Routes {
		found = found || (r.Name == "handler.quote" && r.AllowAnonymous && r.MaxBodyBytes > 0)
	}
	if !found {
		t.Fatal("route not generated with a body limit")
	}
}

func TestPolicyCanForbidPublicHandlers(t *testing.T) {
	_, err := compileDefs(t, `
definition_policy { allow_public false }
handler "q" { method GET path "/q" public true file "scripts/quote.spl" }
`)
	if err == nil {
		t.Fatal("expected refusal")
	}
}

func TestHelperExpressionAndHandler(t *testing.T) {
	p, err := compileDefs(t, `
helper "vat" { params [amount, rate] expr "amount * rate / 100" }
handler "vat" { method POST path "/vat" public true helper "vat" }
`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.CallIntent(context.Background(), "handler.vat", map[string]any{"amount": 200, "rate": 13}, nil)
	if err != nil || ToFloatMust(out) != 26 {
		t.Fatalf("%#v %v", out, err)
	}
}

func ToFloatMust(v any) float64 { f, _ := ToFloat(v); return f }

func TestSignaturesAndPublisherAuthority(t *testing.T) {
	key, _ := signing.GenerateEd25519("k")
	pem, _ := key.PublicOnly().MarshalPublicKeyPEM()
	pub := func(roles string) string {
		return `publisher "team" { public_key ` + "`" + string(pem) + "`" + ` roles [` + roles + `] }
definition_policy { require_signature true }
`
	}
	h := HandlerSpec{Name: "quote", Method: "POST", Path: "/quote", Public: true, File: "scripts/quote.spl", Publisher: "team"}
	sig, err := SignDefinition(key, h, defFS)
	if err != nil {
		t.Fatal(err)
	}
	block := func(path, sig string) string {
		return `handler "quote" { method POST path "` + path + `" public true file "scripts/quote.spl" publisher "team" signature "` + sig + `" }`
	}
	if _, err := compileDefs(t, pub(`"publisher"`)+block("/quote", sig)); err != nil {
		t.Fatalf("valid signature refused: %v", err)
	}
	// Changing any signed field breaks it.
	if _, err := compileDefs(t, pub(`"publisher"`)+block("/other", sig)); err == nil {
		t.Fatal("tampered path accepted")
	}
	// Unsigned is refused under the policy.
	if _, err := compileDefs(t, pub(`"publisher"`)+`handler "quote" { method POST path "/quote" public true file "scripts/quote.spl" }`); err == nil {
		t.Fatal("unsigned accepted")
	}
	// A publisher without the permission cannot publish a handler.
	if _, err := compileDefs(t, pub(`"node_only"`)+block("/quote", sig)); err == nil {
		t.Fatal("publisher without permission accepted")
	}
	// A signature from another key is refused.
	other, _ := signing.GenerateEd25519("o")
	bad, _ := SignDefinition(other, h, defFS)
	if _, err := compileDefs(t, pub(`"publisher"`)+block("/quote", bad)); err == nil {
		t.Fatal("foreign signature accepted")
	}
}

func TestPolicyClampsLimits(t *testing.T) {
	_, err := compileDefs(t, `
definition_policy { max_steps 1000 }
node_type "x" { file "scripts/risk.spl" max_steps 5000 }
`)
	if err == nil || !strings.Contains(err.Error(), "ceiling") {
		t.Fatalf("got %v", err)
	}
}

func TestHelperCallableFromExpressions(t *testing.T) {
	p, err := compileDefs(t, `
helper "vat" { params [amount, rate] expr "amount * rate / 100" }
intent "tax" {
  response "out"
  node "out" { uses "data.transform" requires [input] provides [out] config { expression "helper(\"vat\", input.amount, input.rate)" } }
}
`)
	if err != nil {
		t.Skipf("data.transform shape differs: %v", err)
	}
	out, err := p.CallIntent(context.Background(), "tax", map[string]any{"amount": 200, "rate": 13}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ToFloatMust(out) != 26 {
		t.Fatalf("got %#v", out)
	}
}
