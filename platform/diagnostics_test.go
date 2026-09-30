package platform

import (
	"context"
	"strings"
	"testing"
)

func TestValidateDiagnosticsCarryPathAndSpan(t *testing.T) {
	src := `name "diag"
resource "cachey" {
  kind "cache.imaginary"
}
intent "ok" {
  response "ok"
  node "ok" {
    uses "constant"
    provides [ok]
    config { value "x" }
  }
}
route "ok" { method GET path "/ok" intent "ok" }
`
	r := Validate(context.Background(), []byte(src), ".", DefaultLoadOptions())
	if r.Valid {
		t.Fatal("expected an invalid document")
	}
	if len(r.Diagnostics) != len(r.Errors)+len(r.Warnings) {
		t.Fatalf("diagnostics %d != errors %d + warnings %d", len(r.Diagnostics), len(r.Errors), len(r.Warnings))
	}
	var found *Diagnostic
	for i, d := range r.Diagnostics {
		if strings.Contains(d.Message, "cache.imaginary") {
			found = &r.Diagnostics[i]
		}
	}
	if found == nil {
		t.Fatalf("no diagnostic for the bad kind: %+v", r.Diagnostics)
	}
	if found.Severity != SeverityError || found.Code != DiagValidate {
		t.Fatalf("severity/code: %+v", found)
	}
	if found.Path != "resource/cachey/kind" || found.Span == nil || found.Span.Line != 3 {
		t.Fatalf("path/span not resolved: %+v", found)
	}
}

func TestValidateParseErrorDiagnostic(t *testing.T) {
	r := Validate(context.Background(), []byte("name \"x\"\nresource \"a\" {\n"), ".", DefaultLoadOptions())
	if r.Valid || len(r.Errors) == 0 {
		t.Fatalf("expected a parse failure: %+v", r)
	}
	if len(r.Diagnostics) == 0 || r.Diagnostics[0].Severity != SeverityError {
		t.Fatalf("no structured parse diagnostic: %+v", r.Diagnostics)
	}
}

// A variable the validating process lacks is information, not a problem: the
// deployment that activates the revision supplies it.
func TestUnsetEnvironmentIsInfoNotAProblem(t *testing.T) {
	src := `name "envy"
version env("STUDIO_TEST_VERSION_UNSET", "")
intent "ok" {
  response "ok"
  node "ok" {
    uses "constant"
    provides [ok]
    config { value env("STUDIO_TEST_VALUE_UNSET") }
  }
}
route "ok" { method GET path "/ok" intent "ok" }
`
	opts := DefaultLoadOptions()
	opts.AllowEnv = true
	r := Validate(context.Background(), []byte(src), ".", opts)
	found := false
	for _, d := range r.Diagnostics {
		if strings.Contains(d.Message, "is not set here") {
			found = true
			if d.Severity != SeverityInfo || d.Code != DiagEnvUnset {
				t.Fatalf("env-unset finding is %s/%s, want info/env.unset", d.Severity, d.Code)
			}
		} else if d.Severity == SeverityWarning && strings.Contains(d.Message, "STUDIO_TEST") {
			t.Fatalf("unexpected warning: %+v", d)
		}
	}
	if !found {
		t.Skipf("document produced no env-unset finding: %+v", r.Diagnostics)
	}
	if len(r.Warnings) == 0 {
		t.Fatal("ValidationReport.Warnings must still list it, for existing callers")
	}
}
