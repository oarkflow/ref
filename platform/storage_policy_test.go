package platform

import (
	"context"
	"strings"
	"testing"
)

func TestLooksInMemory(t *testing.T) {
	cases := map[string]bool{
		"file:test.db":                          false,
		"/var/lib/app/app.db":                   false,
		"file::memory:?cache=shared":            true,
		":memory:":                              true,
		"file:routing?mode=memory&cache=shared": true,
		"file:test?mode=MEMORY":                 true,
	}
	for dsn, want := range cases {
		if got := looksInMemory(dsn); got != want {
			t.Errorf("looksInMemory(%q) = %v, want %v", dsn, got, want)
		}
	}
}

func TestCheckStoragePolicyFindsEachDiskKind(t *testing.T) {
	doc := Document{Resources: []ResourceSpec{
		{Name: "a", Kind: "cache.file", Config: map[string]any{"dir": "/tmp/a"}},
		{Name: "b", Kind: "queue.file", Config: map[string]any{"dir": "/tmp/b"}},
		{Name: "c", Kind: "queue.broker", Config: map[string]any{"dir": "/tmp/c"}},
		{Name: "d", Kind: "session.file", Config: map[string]any{"dir": "/tmp/d"}},
		{Name: "e", Kind: "storage.fs", Config: map[string]any{"dir": "/tmp/e"}},
		{Name: "f", Kind: "database.sql", Config: map[string]any{"driver": "sqlite", "dsn": "file:f.db"}},
		// Not violations:
		{Name: "g", Kind: "cache.memory"},
		{Name: "h", Kind: "database.sql", Config: map[string]any{"driver": "sqlite", "dsn": "file::memory:?cache=shared"}},
		{Name: "i", Kind: "database.sql", Config: map[string]any{"driver": "postgres", "dsn": "postgres://host/db"}},
	}}
	violations := checkStoragePolicy(doc)
	if len(violations) != 6 {
		names := make([]string, len(violations))
		for i, v := range violations {
			names[i] = v.resource
		}
		t.Fatalf("expected 6 violations (a-f), got %d: %v", len(violations), names)
	}
	seen := map[string]bool{}
	for _, v := range violations {
		seen[v.resource] = true
	}
	for _, want := range []string{"a", "b", "c", "d", "e", "f"} {
		if !seen[want] {
			t.Errorf("expected a violation for resource %q", want)
		}
	}
}

func TestEnforceStoragePolicyModes(t *testing.T) {
	violatingDoc := Document{Resources: []ResourceSpec{
		{Name: "cache", Kind: "cache.file", Config: map[string]any{"dir": "/tmp/cache"}},
	}}
	cleanDoc := Document{Resources: []ResourceSpec{
		{Name: "cache", Kind: "cache.memory"},
	}}

	t.Run("no policy set never blocks", func(t *testing.T) {
		violatingDoc.StoragePolicy = ""
		if _, err := enforceStoragePolicy(violatingDoc); err != nil {
			t.Fatalf("unexpected error with no policy set: %v", err)
		}
	})

	t.Run("ephemeral refuses a violation", func(t *testing.T) {
		d := violatingDoc
		d.StoragePolicy = storagePolicyEphemeral
		_, err := enforceStoragePolicy(d)
		if err == nil {
			t.Fatal("expected ephemeral to refuse a disk-writing resource")
		}
		if !strings.Contains(err.Error(), "cache") {
			t.Errorf("error should name the offending resource: %v", err)
		}
	})

	t.Run("ephemeral allows a clean document", func(t *testing.T) {
		d := cleanDoc
		d.StoragePolicy = storagePolicyEphemeral
		if _, err := enforceStoragePolicy(d); err != nil {
			t.Fatalf("a document with no disk resources should pass: %v", err)
		}
	})

	t.Run("ephemeral_warn reports but does not block", func(t *testing.T) {
		d := violatingDoc
		d.StoragePolicy = storagePolicyEphemeralWarn
		warnings, err := enforceStoragePolicy(d)
		if err != nil {
			t.Fatalf("ephemeral_warn must not fail the load: %v", err)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], "cache") {
			t.Fatalf("expected one warning naming the resource, got %v", warnings)
		}
	})

	t.Run("an unknown mode is rejected", func(t *testing.T) {
		d := cleanDoc
		d.StoragePolicy = "something_else"
		if _, err := enforceStoragePolicy(d); err == nil {
			t.Fatal("expected an unknown storage_policy value to be rejected")
		}
	})
}

const storagePolicyApp = `
name "storage-policy-app"
storage_policy "ephemeral"
resource "db" {
  kind "database.sql"
  config { driver "sqlite" dsn env.required("SPOLICY_DSN") }
}
intent "ping" { response "out" node "out" { uses "collect" requires [input] provides [out] config { unwrap true } } }
`

func TestStoragePolicyEphemeralRefusesCompile(t *testing.T) {
	t.Setenv("SPOLICY_DSN", "file:"+t.TempDir()+"/spolicy.db")
	_, err := Compile(context.Background(), []byte(storagePolicyApp), "", DefaultLoadOptions())
	if err == nil {
		t.Fatal("expected storage_policy ephemeral to refuse a sqlite file DSN")
	}
	if !strings.Contains(err.Error(), "storage_policy") {
		t.Errorf("error should mention storage_policy: %v", err)
	}
}

func TestStoragePolicyEphemeralAllowsInMemorySQLite(t *testing.T) {
	src := strings.Replace(storagePolicyApp, `dsn env.required("SPOLICY_DSN")`,
		`dsn "file::memory:?cache=shared&_pragma=busy_timeout(5000)"`, 1)
	p, err := Compile(context.Background(), []byte(src), "", DefaultLoadOptions())
	if err != nil {
		t.Fatalf("an in-memory sqlite DSN should be allowed under ephemeral: %v", err)
	}
	defer p.Close()
}

func TestStoragePolicyWarnModeLoadsWithWarningLogged(t *testing.T) {
	src := strings.Replace(storagePolicyApp, `storage_policy "ephemeral"`, `storage_policy "ephemeral_warn"`, 1)
	t.Setenv("SPOLICY_DSN", "file:"+t.TempDir()+"/spolicy_warn.db")
	p, err := Compile(context.Background(), []byte(src), "", DefaultLoadOptions())
	if err != nil {
		t.Fatalf("ephemeral_warn must not block the load: %v", err)
	}
	defer p.Close()
}

func TestStoragePolicyReportedByValidate(t *testing.T) {
	t.Setenv("SPOLICY_DSN", "file:"+t.TempDir()+"/spolicy_validate.db")
	report := Validate(context.Background(), []byte(storagePolicyApp), "", DefaultLoadOptions())
	found := false
	for _, e := range report.Errors {
		if strings.Contains(e, "storage_policy") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected Validate to report the storage_policy violation as an error, got: %+v", report)
	}
}
