package platform

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateOpensNothing(t *testing.T) {
	dir := t.TempDir()
	src, err := os.ReadFile("../examples/passport/app.bcl")
	if err != nil {
		t.Fatal(err)
	}
	opts := DefaultLoadOptions()
	dbPath := filepath.Join(dir, "never.db")
	opts.Env = func(name string) (string, bool) {
		if name == "PASSPORT_DSN" {
			return "file:" + dbPath, true
		}
		return "", false // secrets unset here
	}
	r := Validate(context.Background(), src, "../examples/passport", opts)
	if !r.Valid {
		t.Fatalf("passport should validate: %v", r.Errors)
	}
	if len(r.Warnings) == 0 || !strings.Contains(strings.Join(r.Warnings, " "), "PASSPORT_JWT_SECRET") {
		t.Fatalf("expected warnings about unset secrets: %v", r.Warnings)
	}
	if len(r.Summary.Pipelines) != 1 || len(r.Summary.Routes) < 10 {
		t.Fatalf("summary: %+v", r.Summary)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatal("validation opened the database")
	}

	// Broken: a route to a missing intent, a pipeline node naming an unknown
	// certificate, an unknown resource kind. (bcl v0.0.33 accepts incomplete
	// expressions such as "a ==", so expression typos cannot be caught here.)
	bad := strings.Replace(string(src), `intent "passport.verify"
  allow_anonymous true`, `intent "passport.nope"
  allow_anonymous true`, 1)
	bad = strings.Replace(bad, `certificate "passport_approval"
      auto true`, `certificate "no_such_certificate"
      auto true`, 1)
	bad = strings.Replace(bad, `kind "ratelimit.memory"`, `kind "ratelimit.imaginary"`, 1)
	r2 := Validate(context.Background(), []byte(bad), "../examples/passport", opts)
	joined := strings.Join(r2.Errors, "\n")
	if r2.Valid || !strings.Contains(joined, "passport.nope") || !strings.Contains(joined, "ratelimit.imaginary") || !strings.Contains(joined, "no_such_certificate") {
		t.Fatalf("errors: %v", r2.Errors)
	}

	// Diff: what a reviewer sees.
	changed := strings.Replace(string(src), `label "Pages"`, `label "Number of pages"`, 1)
	changed += "\nflag \"beta\" {\n  default false\n}\n"
	r3 := Validate(context.Background(), []byte(changed), "../examples/passport", opts)
	diff := DiffDocuments(r.Document, r3.Document)
	got := map[string]string{}
	for _, d := range diff {
		got[d.Kind+":"+d.Name] = d.Change
	}
	if got["pipeline:passport"] != "changed" || got["flag:beta"] != "added" || len(diff) != 2 {
		t.Fatalf("diff: %+v", diff)
	}
}

// A malformed expression makes the document invalid, so a deploy proposal
// with a typo in a guard is refused instead of failing on its first request.
func TestValidateReportsMalformedExpression(t *testing.T) {
	src := `
name "guarded"
intent "check" {
  response "ok"
  node "ok" {
    uses "decision.expression"
    requires [input]
    provides [ok]
    config { expression "(input.amount > 100" }
  }
}
route "check" { method POST path "/check" intent "check" }
`
	r := Validate(context.Background(), []byte(src), ".", DefaultLoadOptions())
	if joined := strings.Join(r.Errors, "\n"); !strings.Contains(joined, "expected ')'") {
		t.Fatalf("malformed expression not reported: %v", r.Errors)
	}
	typo := strings.Replace(src, `"(input.amount > 100"`, `"abs_val(input.amount) > 100"`, 1)
	if r := Validate(context.Background(), []byte(typo), ".", DefaultLoadOptions()); !strings.Contains(strings.Join(r.Errors, "\n"), `unknown function "abs_val"`) {
		t.Fatalf("unknown function not reported: %v", r.Errors)
	}
	fixed := strings.Replace(src, `"(input.amount > 100"`, `"input.amount > 100"`, 1)
	if r := Validate(context.Background(), []byte(fixed), ".", DefaultLoadOptions()); len(r.Errors) != 0 {
		t.Fatalf("valid document rejected: %v", r.Errors)
	}
}

// Every example application validates: a broken expression, an undeclared
// resource or an unknown function in an example fails here, not in the hands
// of whoever copies it.
func TestExamplesValidate(t *testing.T) {
	files, err := filepath.Glob("../examples/*/app.bcl")
	if err != nil || len(files) == 0 {
		t.Fatalf("no examples found: %v", err)
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if r := Validate(context.Background(), src, filepath.Dir(f), DefaultLoadOptions()); len(r.Errors) != 0 {
			t.Errorf("%s: %v", f, r.Errors)
		}
	}
}
