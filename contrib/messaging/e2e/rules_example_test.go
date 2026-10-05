package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/ref/serve"
)

// examples/rules is a routing application of its own: one message in, the chain
// of providers out, from nothing but rules. These tests are its specification,
// and the scenarios in its README are these cases.

type exampleApp struct {
	t   *testing.T
	app *serve.App
}

// rulesDir is resolved before any application starts: starting one changes the
// working directory.
var rulesDir, _ = filepath.Abs("../../../examples/rules")

func startExample(t *testing.T) *exampleApp {
	t.Helper()
	dir := rulesDir
	env := map[string]string{"ROUTING_DSN": "file:" + filepath.Join(t.TempDir(), "routing.db") + "?_pragma=busy_timeout(5000)", "ROUTING_ADMIN_KEY": adminKey, "APP_ENV": "test"}
	a, err := serve.Start(context.Background(), serve.Options{Dir: dir, Addr: "127.0.0.1:0", Env: "test", Load: func(lo *platform.LoadOptions) {
		lo.Env = func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop(5 * time.Second) })
	return &exampleApp{t: t, app: a}
}

func (e *exampleApp) do(method, path string, key bool, body any) (int, map[string]any) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.app.URL()+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if key {
		req.Header.Set("X-API-Key", adminKey)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// route asks where a message would go.
func (e *exampleApp) route(req map[string]any) (order []string, rejected map[string]string, out map[string]any) {
	e.t.Helper()
	st, out := e.do("POST", "/route", false, req)
	if st != 200 {
		e.t.Fatalf("route %v = %d %v", req, st, out)
	}
	for _, id := range asList(out["order"]) {
		order = append(order, id.(string))
	}
	rejected = map[string]string{}
	for _, r := range asList(out["rejected"]) {
		m := asMap(r)
		rejected[m["id"].(string)] = Stringify(m["rule"])
	}
	return
}

func TestRoutingExample(t *testing.T) {
	e := startExample(t)
	type want struct {
		first    string   // the provider tried first
		order    []string // the whole chain, when the case pins it
		absent   []string // providers that must not be in the chain
		rejected map[string]string
		ranking  string
	}
	cases := []struct {
		name string
		req  map[string]any
		want want
	}{
		// ---- The tiers of routing.bcl, with no rule involved -----------------------------
		{"a Nepal message goes over the primary Nepal route first; the catch-all stays as a fall-back",
			map[string]any{"account": "alice", "to": "9841234567", "text": "hello"},
			want{first: "np_telecom", absent: []string{"np_old", "np_premium", "np_reserved", "boss_own", "in_vendor", "otp_gateway"}}},
		{"every written form of a number is the same recipient",
			map[string]any{"account": "alice", "to": "+977 984-123-4567", "text": "hello"},
			want{first: "np_telecom"}},
		{"an operator's number range outranks the country route for its numbers",
			map[string]any{"account": "alice", "to": "009779801234567", "text": "hello"},
			want{first: "np_mobile_range"}},
		{"a tenant assigned route comes first for that tenant",
			map[string]any{"account": "bob", "to": "9861234567", "text": "hello"},
			want{first: "np_premium", rejected: map[string]string{"in_vendor": "not-available-here"}}},
		{"an assigned account route serves its destination",
			map[string]any{"account": "alice", "to": "+919876543210", "text": "hello"},
			want{first: "in_vendor", absent: []string{"np_telecom"}}},
		{"a route an account owns is the only one it uses",
			map[string]any{"account": "acme_boss", "to": "9841234567", "text": "hello"},
			want{order: []string{"boss_own"}}},
		{"a destination with no country route falls to the catch-all",
			map[string]any{"account": "alice", "to": "+14155552671", "text": "hello"},
			want{order: []string{"global_fallback"}}},
		{"a one-time code is routed for delivery, and may fall back to the OTP gateway, which carries nothing else",
			map[string]any{"account": "alice", "to": "9841234567", "text": "Your code is 481516", "type": "otp"},
			want{first: "np_telecom", order: []string{"np_telecom", "otp_gateway"}, ranking: "route_delivery"}},
		{"a paused provider is never used, and says why",
			map[string]any{"account": "alice", "to": "9841234567", "text": "hello"},
			want{absent: []string{"np_old"}, rejected: map[string]string{"np_old": "provider-not-active"}}},
		{"Unicode text skips a provider that cannot send it",
			map[string]any{"account": "alice", "to": "9841234567", "text": "नमस्ते"},
			want{absent: []string{"np_budget"}, rejected: map[string]string{"np_budget": "no-unicode"}}},
		{"a long message skips a provider that cannot send several segments",
			map[string]any{"account": "alice", "to": "9841234567", "text": strings.Repeat("long ", 60), "dlr": false},
			want{absent: []string{"np_budget"}, rejected: map[string]string{"np_budget": "no-long-messages"}}},
		// A receipt is a reporting question, not a carrier-selection one: routing
		// does not read dlr, so np_budget is filtered and scored like any other and
		// the same chain comes back whichever way the request asks about receipts.
		{"asking for a receipt does not change the route",
			map[string]any{"account": "alice", "to": "9841234567", "text": "hello", "dlr": true},
			want{first: "np_telecom", order: []string{"np_telecom", "np_budget", "global_fallback"}}},

		// ---- What an account is routed for ---------------------------------------------------
		{"an account that wants the lowest price gets the cheapest route first",
			map[string]any{"account": "thrifty", "to": "9841234567", "text": "hello", "dlr": false},
			want{first: "np_budget", ranking: "route_cost"}},
		{"an account that wants the best delivery gets it, price aside",
			map[string]any{"account": "careful", "to": "9841234567", "text": "hello"},
			want{first: "np_telecom", ranking: "route_delivery"}},
		{"promotions are routed for price whatever the account",
			map[string]any{"account": "alice", "to": "+919876543210", "text": "SALE", "type": "promotional", "dlr": false},
			want{ranking: "route_cost"}},

		// ---- The operator's rules in overrides.bcl --------------------------------------------
		{"content: a word in the text keeps a message off a route",
			map[string]any{"account": "alice", "to": "9841234567", "text": "Play CASINO tonight"},
			want{absent: []string{"np_telecom"}, rejected: map[string]string{"np_telecom": "no-gambling"}}},
		{"account + recipient: a rule can admit a reserved route nobody else may use",
			map[string]any{"account": "bob", "to": "+977 984 123 4567", "text": "hello"},
			want{first: "np_reserved"}},
		{"...but only for that account and that number",
			map[string]any{"account": "alice", "to": "9841234567", "text": "hello"},
			want{absent: []string{"np_reserved"}}},
		{"...and not for another number of the same account",
			map[string]any{"account": "bob", "to": "9841234568", "text": "hello"},
			want{absent: []string{"np_reserved"}}},
		{"type + country: promotions use the cheap route",
			map[string]any{"account": "alice", "to": "9841234567", "text": "SALE", "type": "promotional", "dlr": false},
			want{first: "np_budget"}},
		{"...whose own limits still apply: a Unicode promotion cannot use it",
			map[string]any{"account": "alice", "to": "9841234567", "text": "सेल", "type": "promotional"},
			want{absent: []string{"np_budget"}}},
		{"tenant + number range",
			map[string]any{"account": "bob", "to": "9851234567", "text": "hello"},
			want{first: "np_premium"}},
		{"tenant: acme never uses the shared India route",
			map[string]any{"account": "bob", "to": "+919876543210", "text": "hello"},
			want{absent: []string{"in_shared"}, rejected: map[string]string{"in_shared": "acme-not-on-shared-india"}}},
		{"another tenant still does",
			map[string]any{"account": "thrifty", "to": "+919876543210", "text": "hello", "dlr": false},
			want{first: "in_shared"}},
		{"size: three segments or more use the range route",
			map[string]any{"account": "alice", "to": "9841234567", "text": strings.Repeat("long ", 120)},
			want{first: "np_mobile_range"}},
		{"template + exclusive: a legal notice has one route and no fall-back",
			map[string]any{"account": "alice", "to": "9841234567", "text": "notice", "template": "legal_notice"},
			want{order: []string{"global_fallback"}}},
		{"sender + content: a bank's code goes first over the OTP gateway",
			map[string]any{"account": "alice", "to": "9841234567", "text": "Your code is 481516", "from": "BANKNP", "type": "otp"},
			want{first: "otp_gateway"}},
	}
	for _, c := range cases {
		order, rejected, out := e.route(c.req)
		if c.want.first != "" && (len(order) == 0 || order[0] != c.want.first) {
			t.Errorf("%s: first = %v, want %s (chain %v)", c.name, order, c.want.first, order)
		}
		if c.want.order != nil && !slices.Equal(order, c.want.order) {
			t.Errorf("%s: chain %v, want %v", c.name, order, c.want.order)
		}
		for _, id := range c.want.absent {
			if slices.Contains(order, id) {
				t.Errorf("%s: %s must not be in the chain %v", c.name, id, order)
			}
		}
		for id, rule := range c.want.rejected {
			if rejected[id] != rule {
				t.Errorf("%s: %s rejected by %q, want %q (all: %v)", c.name, id, rejected[id], rule, rejected)
			}
		}
		if c.want.ranking != "" && out["objective"] != c.want.ranking {
			t.Errorf("%s: ranked for %v, want %s", c.name, out["objective"], c.want.ranking)
		}
	}
}

// What an operator changes at run time applies to the very next message.
func TestRoutingExampleRuntimeChanges(t *testing.T) {
	e := startExample(t)
	msg := map[string]any{"account": "alice", "to": "9841234567", "text": "hello"}
	if order, _, _ := e.route(msg); order[0] != "np_telecom" {
		t.Fatalf("start: %v", order)
	}
	// Pause the primary route: the next one takes over, and the chain says why.
	if st, out := e.do("PUT", "/providers/np_telecom/state", true, map[string]any{"state": "paused"}); st != 200 {
		t.Fatalf("pause = %d %v", st, out)
	}
	order, rejected, _ := e.route(msg)
	if slices.Contains(order, "np_telecom") || rejected["np_telecom"] != "provider-not-active" || order[0] == "" {
		t.Fatalf("paused: %v %v", order, rejected)
	}
	e.do("PUT", "/providers/np_telecom/state", true, map[string]any{"state": "active"})
	// A circuit that is open takes a provider out; failures in a row only lower its score.
	e.do("PUT", "/providers/np_telecom/health", true, map[string]any{"circuit_open": true})
	if order, rejected, _ := e.route(msg); slices.Contains(order, "np_telecom") || rejected["np_telecom"] != "circuit-open" {
		t.Fatalf("open circuit: %v %v", order, rejected)
	}
	// Failures in a row only reorder providers of the same tier: with np_budget (same
	// tier, no receipts) in play, ten failures put np_telecom behind it, still ahead of
	// the catch-all.
	e.do("PUT", "/providers/np_telecom/health", true, map[string]any{})
	quiet := map[string]any{"account": "alice", "to": "9841234567", "text": "hello", "dlr": false}
	if order, _, _ := e.route(quiet); !slices.Equal(order, []string{"np_telecom", "np_budget", "global_fallback"}) {
		t.Fatalf("before the failures: %v", order)
	}
	e.do("PUT", "/providers/np_telecom/health", true, map[string]any{"consecutive_failures": 10})
	if order, _, _ := e.route(quiet); !slices.Equal(order, []string{"np_budget", "np_telecom", "global_fallback"}) {
		t.Fatalf("failures should lower the score within a tier: %v", order)
	}
	e.do("PUT", "/providers/np_telecom/health", true, map[string]any{})
	if order, _, _ := e.route(msg); order[0] != "np_telecom" {
		t.Fatalf("recovered: %v", order)
	}

	// Change the rules: a new decision for np_budget that always wins for alice. Checked first.
	st, rule := e.do("GET", "/rules/overrides", true, nil)
	if st != 200 {
		t.Fatalf("read rules = %d %v", st, rule)
	}
	source := str(rule, "source")
	if st, _ := e.do("PUT", "/rules/overrides", true, map[string]any{"source": source + "\n  stray {{\n"}); st < 400 || st >= 500 {
		t.Fatalf("a broken rule file was accepted: %d", st)
	}
	if order, _, _ := e.route(msg); order[0] != "np_telecom" {
		t.Fatalf("a refused change altered routing: %v", order)
	}
	edited := strings.Replace(source, `provider.id == "np_telecom"
          message.text matches "(?i)(casino|lottery|betting)"`, `provider.id == "np_telecom"
          message.text matches "(?i)(casino|lottery|betting|hello)"`, 1)
	if edited == source {
		t.Fatal("the example rule changed; update this test")
	}
	if st, out := e.do("PUT", "/rules/overrides", true, map[string]any{"source": edited}); st != 200 {
		t.Fatalf("save rules = %d %v", st, out)
	}
	if order, rejected, _ := e.route(msg); slices.Contains(order, "np_telecom") || rejected["np_telecom"] != "no-gambling" {
		t.Fatalf("the edited rule should apply at once: %v %v", order, rejected)
	}
	// Reset goes back to the file.
	if st, out := e.do("DELETE", "/rules/overrides", true, nil); st != 200 {
		t.Fatalf("reset = %d %v", st, out)
	}
	if order, _, _ := e.route(msg); order[0] != "np_telecom" {
		t.Fatalf("after reset: %v", order)
	}
	// The operator routes need the key.
	if st, _ := e.do("PUT", "/providers/np_telecom/state", false, map[string]any{"state": "paused"}); st != 401 {
		t.Fatalf("without the key = %d", st)
	}
}

// A request that cannot be routed is refused with a reason, not an empty chain.
func TestRoutingExampleRefusals(t *testing.T) {
	e := startExample(t)
	for _, c := range []struct {
		name string
		req  map[string]any
		code int
		note string
	}{
		{"unknown account", map[string]any{"account": "nobody", "to": "9841234567"}, 404, "no such account"},
		{"not a number", map[string]any{"account": "alice", "to": "call me"}, 422, ""},
		{"a destination not served", map[string]any{"account": "alice", "to": "+8613800138000"}, 422, "not served"},
		{"missing recipient", map[string]any{"account": "alice"}, 422, ""},
	} {
		st, out := e.do("POST", "/route", false, c.req)
		msg := fmt.Sprint(get(out, "error", "message"))
		if st != c.code || !strings.Contains(msg, c.note) {
			t.Errorf("%s: %d %q, want %d %q", c.name, st, msg, c.code, c.note)
		}
	}
}
