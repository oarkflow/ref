package preview

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oarkflow/ref/platform"
)

func res(name, kind string, cfg map[string]any) platform.ResourceSpec {
	return platform.ResourceSpec{Name: name, Kind: kind, Config: cfg}
}

func TestSandboxMapping(t *testing.T) {
	root := t.TempDir()
	doc := &platform.Document{
		Resources: []platform.ResourceSpec{
			res("db", "database.sql", map[string]any{"driver": "pgx", "dsn": "postgres://prod/x", "read_replica_dsn": "postgres://replica/x", "max_open_connections": 9}),
			res("cf", "cache.file", map[string]any{"dir": "/var/cache/app"}),
			res("qf", "queue.file", map[string]any{"dir": "/var/queue", "workers": 3}),
			res("fs", "storage.fs", map[string]any{"dir": "/srv/uploads"}),
			res("sf", "session.file", map[string]any{"dir": "/var/sessions"}),
			res("env", "secret.env", map[string]any{"prefix": "APP_"}),
			res("oidc", "auth.oidc", map[string]any{"issuer": "https://idp.example", "audience": "api", "roles_claim": "groups", "refresh_interval": "1h"}),
			res("cm", "cache.memory", nil),
			res("http", "service.http", map[string]any{"allowed_hosts": []any{"api.example"}}),
		},
		Workers:   []platform.WorkerSpec{{Name: "w"}},
		Schedules: []platform.ScheduleSpec{{Name: "s"}},
		Triggers:  []platform.TriggerSpec{{Name: "t"}},
	}
	changes, err := Sandbox(doc, SandboxOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]platform.ResourceSpec{}
	for _, r := range doc.Resources {
		by[r.Name] = r
	}
	db := by["db"].Config
	if db["driver"] != "sqlite" || !strings.Contains(db["dsn"].(string), root) || db["read_replica_dsn"] != nil || db["max_open_connections"] != 9 {
		t.Fatalf("database not rewritten: %v", db)
	}
	for _, n := range []string{"cf", "qf", "fs", "sf"} {
		dir, _ := by[n].Config["dir"].(string)
		if !strings.HasPrefix(dir, root+string(filepath.Separator)) {
			t.Errorf("%s dir %q escapes the root", n, dir)
		}
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			t.Errorf("%s dir not created: %v", n, err)
		}
	}
	if by["qf"].Config["workers"] != 3 {
		t.Error("unrelated config dropped")
	}
	if by["env"].Kind != "secret.file" {
		t.Errorf("secret.env kept: %s", by["env"].Kind)
	}
	oidc := by["oidc"]
	if oidc.Kind != "auth.jwt" || oidc.Config["algorithm"] != "HS256" || oidc.Config["roles_claim"] != "groups" || oidc.Config["audience"] != "api" {
		t.Errorf("oidc not converted: %+v", oidc)
	}
	if _, has := oidc.Config["refresh_interval"]; has {
		t.Error("oidc network settings survived")
	}
	if len(by["oidc"].Config["secret"].(string)) < 32 {
		t.Error("placeholder secret too short for HS256")
	}
	if by["http"].Kind != "service.http" || by["cm"].Kind != "cache.memory" {
		t.Error("kept kinds changed")
	}
	if doc.Workers != nil || doc.Schedules != nil || doc.Triggers != nil {
		t.Error("background blocks not removed")
	}
	got := map[string]Action{}
	for _, c := range changes {
		got[c.Block] = c.Action
	}
	for block, want := range map[string]Action{
		"resource/db": ActionRewrite, "resource/env": ActionReplace, "resource/oidc": ActionReplace,
		"resource/http": ActionOffline, "resource/cm": ActionKeep, "worker/w": ActionDisable,
		"schedule/s": ActionDisable, "trigger/t": ActionDisable,
	} {
		if got[block] != want {
			t.Errorf("%s: got %q, want %q", block, got[block], want)
		}
	}
}

func TestSandboxRefusesUnknownKind(t *testing.T) {
	doc := &platform.Document{Resources: []platform.ResourceSpec{res("r", "cache.redis", nil), res("k", "queue.kafka", nil)}}
	_, err := Sandbox(doc, SandboxOptions{Root: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "cache.redis") || !strings.Contains(err.Error(), "queue.kafka") {
		t.Fatalf("want error naming both kinds, got %v", err)
	}
	if _, err := Sandbox(doc, SandboxOptions{Root: t.TempDir(), ExtraKinds: []string{"cache.redis", "queue.kafka"}}); err != nil {
		t.Fatalf("vouched kinds should pass: %v", err)
	}
	if _, err := Sandbox(doc, SandboxOptions{}); err == nil {
		t.Fatal("empty Root accepted")
	}
}

// Every built-in resource kind must have a preview rule. A new kind added to
// the platform without one fails here, not at a user's first preview.
func TestKindPolicyCoversCatalog(t *testing.T) {
	for _, k := range platform.NewRegistry().Catalog().ResourceKinds {
		if _, ok := KindPolicy(k.Name); !ok {
			t.Errorf("resource kind %q has no preview rule; add it to kindPolicy in sandbox.go", k.Name)
		}
	}
	for kind := range kindPolicy {
		found := false
		for _, k := range platform.NewRegistry().Catalog().ResourceKinds {
			if k.Name == kind {
				found = true
			}
		}
		if !found {
			t.Errorf("kindPolicy names %q, which is not a registered kind", kind)
		}
	}
}

func TestRequiredEnvPlaceholders(t *testing.T) {
	src := []byte(`x env.required("SESSION_SECRET")
secret "k" { env "API_KEY" required true }
y env("OPTIONAL", "dflt")`)
	env := previewEnv(src, map[string]string{"EXPLICIT": "v"})
	if v, ok := env("SESSION_SECRET"); !ok || len(v) < 32 {
		t.Errorf("required var: %q %v", v, ok)
	}
	if _, ok := env("API_KEY"); !ok {
		t.Error("secret env not covered")
	}
	if _, ok := env("OPTIONAL"); ok {
		t.Error("optional var must stay unset so its default applies")
	}
	if v, _ := env("EXPLICIT"); v != "v" {
		t.Error("explicit env ignored")
	}
	t.Setenv("HOME_LEAK_CHECK", "secret")
	if _, ok := env("HOME_LEAK_CHECK"); ok {
		t.Error("host environment leaked")
	}
}

func TestSandboxGuard(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "pack.bcl"), []byte("# endpoint in a comment is fine\nrule \"r\" {\n  action {\n    type webhook\n  }\n}\n"), 0o644)
	mk := func() *platform.Document {
		return &platform.Document{Resources: []platform.ResourceSpec{res("guard", "security.tcpguard", map[string]any{"path": dir, "mode": "enforce"})}}
	}
	doc := mk()
	if _, err := Sandbox(doc, SandboxOptions{Root: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if doc.Resources[0].Config["geoip"] != false || doc.Resources[0].Config["mode"] != "enforce" {
		t.Errorf("config: %v", doc.Resources[0].Config)
	}
	os.WriteFile(filepath.Join(dir, "hook.bcl"), []byte("action \"notify\" {\n  endpoint \"https://hooks.example/x\"\n}\n"), 0o644)
	if _, err := Sandbox(mk(), SandboxOptions{Root: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "hook.bcl") {
		t.Fatalf("policy with an HTTP endpoint accepted: %v", err)
	}
	if _, err := Sandbox(mk(), SandboxOptions{Root: t.TempDir(), AllowGuardEndpoints: true}); err != nil {
		t.Fatalf("explicit allow refused: %v", err)
	}
}
