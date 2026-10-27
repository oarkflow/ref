package serve

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResourcesApplicationUsesResourcesAsWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"resources/config", "resources/templates", "resources/scripts"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "resources/config/app.bcl"), []byte(`name "test"`), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := Options{Dir: root}
	if err := opts.defaults(); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "resources"); opts.Dir != want {
		t.Fatalf("working directory = %q, want %q", opts.Dir, want)
	}
	if opts.ConfigDir != "config" || opts.TemplatesDir != filepath.Join(root, "resources/templates") {
		t.Fatalf("application paths = %q, %q", opts.ConfigDir, opts.TemplatesDir)
	}
}

func TestRuntimeEnvironmentLoadedAfterDirectoryDefaults(t *testing.T) {
	t.Setenv("REF_ADDR", "")
	t.Setenv("PORT", "39123")
	t.Setenv("APP_ENV", "production")
	opts := Options{Addr: ":8080", Env: "development"}
	if err := opts.applyRuntimeEnv(false, false, false, false); err != nil {
		t.Fatal(err)
	}
	if opts.Addr != ":39123" || opts.Env != "production" {
		t.Fatalf("runtime defaults = %q, %q", opts.Addr, opts.Env)
	}
	explicit := Options{Addr: ":9000", Env: "testing"}
	if err := explicit.applyRuntimeEnv(true, true, false, false); err != nil {
		t.Fatal(err)
	}
	if explicit.Addr != ":9000" || explicit.Env != "testing" {
		t.Fatalf("explicit options were overridden: %q, %q", explicit.Addr, explicit.Env)
	}
}
