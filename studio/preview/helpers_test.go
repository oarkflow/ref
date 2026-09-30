package preview

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/oarkflow/ref/platform"
)

// testDraft is a mutable studio.Draft.
type testDraft struct {
	mu      sync.Mutex
	id      string
	version int64
	bundle  platform.Bundle
}

func (d *testDraft) ID() string { return d.id }
func (d *testDraft) Version() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.version
}
func (d *testDraft) Bundle() platform.Bundle {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append(platform.Bundle(nil), d.bundle...)
}
func (d *testDraft) Subscribe() (<-chan int64, func()) { ch := make(chan int64); return ch, func() {} }

func (d *testDraft) set(t *testing.T, files ...platform.BundleFile) {
	t.Helper()
	b, err := platform.NewBundle(files)
	if err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.bundle = b
	d.version++
	d.mu.Unlock()
}

func newDraft(t *testing.T, id string, files ...platform.BundleFile) *testDraft {
	t.Helper()
	d := &testDraft{id: id}
	d.set(t, files...)
	return d
}

func starterBundle(t *testing.T) platform.Bundle {
	t.Helper()
	dir := filepath.Join("..", "..", "examples", "starter", "resources", "config")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("starter config not present: %v", err)
	}
	b, err := platform.ReadBundleDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// starterRegistry is a registry with stand-ins for the two host actions the
// starter registers from Go (internal/ops), which are not part of the BCL.
func starterRegistry() *platform.Registry {
	r := platform.NewRegistry()
	stub := platform.ActionFactoryFunc(func(_ platform.BuildContext, spec platform.NodeSpec) (platform.Action, error) {
		return platform.ActionFunc(func(*platform.ActionContext) (platform.ActionResult, error) {
			if len(spec.Provides) == 0 {
				return platform.ActionResult{}, nil
			}
			return platform.ActionResult{Outputs: map[string]any{spec.Provides[0]: false}}, nil
		}), nil
	})
	_ = r.RegisterAction("ops.maintenance_status", stub, platform.ActionInfo{Family: "ops", Kind: "pure"})
	_ = r.RegisterAction("ops.maintenance_set", stub, platform.ActionInfo{Family: "ops", Kind: "effect"})
	return r
}

func file(path, content string) platform.BundleFile {
	return platform.BundleFile{Path: path, Content: content}
}

// appBundle is a small application exercising a datastore, outbound HTTP and
// mail, a redirect, a cookie, and static HTML. host is where service.http and
// service.smtp would really go.
func appBundle(host, staticDirName string) []platform.BundleFile {
	return []platform.BundleFile{
		file("00_app.bcl", `
name "preview-app"

resource "database" {
  kind "database.sql"
  config { driver "pgx" dsn "postgres://nobody@nowhere.invalid:5432/prod" ping false }
}
resource "cache" { kind "cache.memory" }
resource "web" {
  kind "service.http"
  config { base_url "http://`+host+`" allowed_hosts ["`+hostname(host)+`"] allow_private_networks true timeout 2s }
}
resource "mailer" {
  kind "service.smtp"
  config { host "`+hostname(host)+`" port `+port(host)+` from "app@example.test" tls "none" timeout 2s }
}
`),
		file("01_logic.bcl", `
intent "hello" {
  response "pong"
  node "pong" { uses "expression" requires [input] provides [pong] config { expression "'pong'" } }
}
intent "out.notify" {
  response "response"
  node "call" { uses "service.http" resource "web" kind effect requires [input] provides [resp] config { method POST url "/hook" body_fact "input" } }
  node "mail" { uses "service.smtp" resource "mailer" kind effect requires [input] provides [sent] config { to ["someone@example.test"] subject "Hello" body "Body text" } }
  node "out" { uses "collect" requires [resp, sent] provides [response] }
}
`),
		file("02_routes.bcl", `
route "hello" { method GET path "/hello" intent "hello" }
route "notify" { method POST path "/notify" intent "out.notify" }
route "go" {
  method GET
  path "/go"
  intent "hello"
  status 302
  headers { Location "/hello" "Set-Cookie" "sid=1; Path=/; HttpOnly" }
}
static "site" { prefix "/site" root "`+staticDirName+`" }
`),
	}
}
