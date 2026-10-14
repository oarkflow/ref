package aegis_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/oarkflow/ref/aegis/compiler"
	"github.com/oarkflow/ref/aegis/pkg"
	contribaegis "github.com/oarkflow/ref/contrib/aegis"
	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/ref/signing"
)

func TestAegisActionDriver(t *testing.T) {
	contribaegis.Register()

	// 1. Compile sample business logic
	src := `
package main

// CalculateSMSCost computes cost based on segments, tier discount, and country base rate
func CalculateSMSCost(segments int32, tier int32, baseRate int32) int32 {
	cost := segments * baseRate
	if tier == 1 { // VIP: 20% discount
		discount := (cost * 20) / 100
		return cost - discount
	} else if tier == 2 { // Enterprise: 10% discount
		discount := (cost * 10) / 100
		return cost - discount
	}
	return cost
}
`
	comp := compiler.New(compiler.Config{})
	res, err := comp.CompileSource(src, []string{"CalculateSMSCost"})
	if err != nil {
		t.Fatalf("CompileSource failed: %v", err)
	}

	// 2. Package module into .apkg
	key, err := signing.GenerateEd25519("")
	if err != nil {
		t.Fatalf("GenerateEd25519 failed: %v", err)
	}
	ks, err := signing.NewKeySet(key)
	if err != nil {
		t.Fatalf("NewKeySet failed: %v", err)
	}

	encKey := make([]byte, 32)
	if _, err := rand.Read(encKey); err != nil {
		t.Fatalf("rand.Read failed: %v", err)
	}
	encKeyHex := hex.EncodeToString(encKey)

	builder, err := pkg.NewBuilder("pkg-sms-pricing", "1.0.0", ks, encKey)
	if err != nil {
		t.Fatalf("NewBuilder failed: %v", err)
	}

	sealedPkg, err := builder.Build(res.Module)
	if err != nil {
		t.Fatalf("builder.Build failed: %v", err)
	}

	pkgBytes, err := pkg.Marshal(sealedPkg)
	if err != nil {
		t.Fatalf("pkg.Marshal failed: %v", err)
	}

	tmpDir := t.TempDir()
	pkgPath := filepath.Join(tmpDir, "sms_pricing.apkg")
	if err := os.WriteFile(pkgPath, pkgBytes, 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// 3. Build REF Action via BuildAegisExec
	action, err := contribaegis.BuildAegisExec(platform.BuildContext{}, platform.NodeSpec{
		Name:     "pricing_node",
		Provides: []string{"pricing_result"},
		Config: map[string]any{
			"package":  pkgPath,
			"function": "CalculateSMSCost",
			"key":      encKeyHex,
			"args":     []any{"segments", "tier", "baseRate"},
		},
	})
	if err != nil {
		t.Fatalf("BuildAegisExec failed: %v", err)
	}

	// 4. Execute with VIP Tier 1: 10 segments * 100 baseRate = 1000 - 200 (20%) = 800
	ctx := &platform.ActionContext{
		Context: context.Background(),
		Inputs: map[string]any{
			"segments": 10,
			"tier":     1,
			"baseRate": 100,
		},
	}

	actRes, err := action.Run(ctx)
	if err != nil {
		t.Fatalf("action.Run failed: %v", err)
	}

	outFact, ok := actRes.Outputs["pricing_result"].(map[string]any)
	if !ok {
		t.Fatalf("expected output fact 'pricing_result', got %v", actRes.Outputs)
	}

	if outFact["result"] != uint64(800) {
		t.Fatalf("expected result 800, got %v", outFact["result"])
	}
	t.Logf("Success! Aegis executed in REF Action, result: %v, fuel used: %v", outFact["result"], outFact["fuel_used"])
}
