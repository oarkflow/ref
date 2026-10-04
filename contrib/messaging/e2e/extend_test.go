package e2e

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	messaging "github.com/oarkflow/ref/contrib/messaging"
	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/ref/serve"
)

// tryStart boots the application in app and reports the error, stopping it if it did start.
func tryStart(t *testing.T, app string) error {
	t.Helper()
	messaging.Register()
	dir := t.TempDir()
	env := map[string]string{
		"APP_ENV": "test", "SMS_ADMIN_KEY": adminKey,
		"SMS_DSN":        "file:" + filepath.Join(dir, "sms.db") + "?_pragma=busy_timeout(5000)",
		"SMS_BROKER_DIR": filepath.Join(dir, "broker"), "SMS_RULES_DIR": filepath.Join(app, "rules"),
	}
	a, err := serve.Start(context.Background(), serve.Options{Dir: app, Addr: "127.0.0.1:0", Env: "test",
		Load: func(lo *platform.LoadOptions) {
			lo.Env = func(k string) (string, bool) { v, ok := env[k]; return v, ok }
		}})
	if err == nil {
		_ = a.Stop(2 * time.Second)
	}
	return err
}

// copyApp copies the example application to a temporary directory, so a test
// can add files to it the way a deployment would.
func copyApp(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.Walk(appDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(appDir, path)
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), raw, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func add(t *testing.T, app, rel, content string) {
	t.Helper()
	path := filepath.Join(app, rel)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A business rule is added to the send flow with two new files and no edit to
// an existing one.
func TestExtensionAddsABusinessRule(t *testing.T) {
	app := copyApp(t)
	add(t, app, "rules/blocklist.bcl", `bcl { version "1.0" }
module "blocklist" {
  version "1"
  environment "production"
  decision_schema "blocklist" { effects [allow, deny] default allow strategy first_match }
  decision_table "blocklist" {
    default allow
    hit_policy first
    row "blocked-range" {
      priority 100
      when { message.to matches "^9779800" }
      then { outcome { decision deny reason "this number range is blocked" attributes { code "NUMBER_BLOCKED" status 422 } } }
    }
  }
}
`)
	add(t, app, "config/90_blocklist.bcl", `extend "blocklist" {
  intent "sms.prepare"
  node "blocklist" {
    uses "rules.decide" resource "policy" kind read requires [message] provides [blocklist]
    config { definition "blocklist" decision "blocklist" fail_on ["deny"] }
  }
  feed ["prepared"]
}
`)
	s := start(t, opts{app: app})
	status, out := s.send("demo", map[string]any{"to": "+9779800000001", "text": "hi"})
	if status != 422 || str(out, "error", "code") != "NUMBER_BLOCKED" {
		t.Fatalf("blocked number = %d %v", status, out)
	}
	if status, out = s.send("demo", map[string]any{"to": "+9779841234567", "text": "hi"}); status != 202 {
		t.Fatalf("other number = %d %v", status, out)
	}
}

// A node is removed from a flow by a new file. The validation node holds the daily limit:
// with the node there, a capped account's second message is refused.
func TestExtensionRemovesANode(t *testing.T) {
	capped := func(t *testing.T, s *stack) (int, int) {
		s.admin("PUT", "/v1/admin/users/capped", map[string]any{"name": "Capped", "daily_limit": 1})
		s.admin("POST", "/v1/admin/users/capped/topup", map[string]any{"amount": 5, "reference": "t"})
		a, _ := s.send("capped", map[string]any{"to": "+9779841234567", "text": "one"})
		b, _ := s.send("capped", map[string]any{"to": "+9779841234567", "text": "two"})
		return a, b
	}
	if a, b := capped(t, start(t)); a != 202 || b == 202 {
		t.Fatalf("with the validation node: %d, %d", a, b)
	}
	app := copyApp(t)
	add(t, app, "config/90_relax.bcl", `extend "relax" {
  intent "sms.prepare"
  remove ["valid"]
}
`)
	if a, b := capped(t, start(t, opts{app: app})); a != 202 || b != 202 {
		t.Fatalf("without the validation node: %d, %d", a, b)
	}
}

func TestExtensionMistakesFailAtLoad(t *testing.T) {
	for name, ext := range map[string]string{
		"unknown intent": `extend "x" { intent "nope" }`,
		"existing node":  `extend "x" { intent "sms.prepare" node "limits" { uses "constant" kind pure provides [limits] config { value { a 1 } } } }`,
		"missing remove": `extend "x" { intent "sms.prepare" remove ["ghost"] }`,
		"missing feed":   `extend "x" { intent "sms.prepare" feed ["ghost"] }`,
	} {
		app := copyApp(t)
		add(t, app, "config/90_bad.bcl", ext)
		if err := tryStart(t, app); err == nil {
			t.Errorf("%s: the application loaded", name)
		}
	}
}
