package e2e

import (
	"encoding/json"
	"strings"
	"testing"
)

// An account sends a message and is told about its message. It is not told how
// the message is carried, and these tests hold that line.
//
// Carriers are not an incidental implementation detail of this gateway: they are
// its supplier list, its contracts and its failover order. An account that could
// read which carrier took a message could reconstruct all three, would be coupled
// to every change of them, and could route around the gateway's own policy. So
// the carrier is in the messages row for the operator, and out of every
// sender-facing projection — left out of the SQL rather than blanked afterwards,
// because a field that is never read cannot leak.

// assertNoCarrierInfo checks that an account's answer says nothing about how the
// message is carried: no carrier id, no carrier channel, and none of the words
// the routing vocabulary uses.
func assertNoCarrierInfo(t *testing.T, what string, body any, ids []string, channels []string) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	text := string(raw)
	for _, id := range ids {
		if strings.Contains(text, id) {
			t.Fatalf("%s names the carrier %q: %s", what, id, text)
		}
	}
	for _, ch := range channels {
		if ch != "" && strings.Contains(text, ch) {
			t.Fatalf("%s names a carrier's channel %q: %s", what, ch, text)
		}
	}
	for _, word := range []string{"sandbox", "tier", "carrier", "reserves", "NOT_AVAILABLE", "PROVIDER_STATE"} {
		if strings.Contains(text, word) {
			t.Fatalf("%s mentions %q, which is routing detail: %s", what, word, text)
		}
	}
}

func TestAnAccountIsNotToldWhichCarrierCarriesItsMessage(t *testing.T) {
	s := start(t)
	demo := s.browser()
	demo.login("demo@example.com", "demo-pass-123")
	const nepal = "+9779841234567"

	// The carriers in play, so the checks below can name them.
	ids, channels := s.carriers()
	if len(ids) < 2 {
		t.Fatalf("expected several carriers, got %v", ids)
	}

	// 1. The answer to a send.
	status, sent := s.send("demo", map[string]any{"to": nepal, "text": "hello", "dlr": false})
	if status != 202 {
		t.Fatalf("send = %d %v", status, sent)
	}
	assertNoCarrierInfo(t, "the answer to a send", sent, ids, channels)
	if sent["status"] != "accepted" || sent["receipt"] != false {
		t.Fatalf("the account should be told it was accepted and that no receipt is coming: %v", sent)
	}
	id := str(sent, "id")
	eventually(t, "delivered", func() bool { return s.state("demo", id) == "delivered" })
	carrier := s.carrier(id)

	// 2. The account's view of the message.
	m := s.asAccount("demo", id)
	assertNoCarrierInfo(t, "GET /v1/messages/{id}", m, ids, channels)
	if m["status"] != "delivered" || str(m, "detail") == "" {
		t.Fatalf("the account should be told it was delivered, and what that means: %v", m)
	}

	// 3. The list.
	rows := s.asAccountList("demo")
	assertNoCarrierInfo(t, "GET /v1/messages", rows, ids, channels)
	if len(rows) == 0 || asMap(rows[0])["status"] == "" {
		t.Fatalf("the list should carry the account's status word: %v", rows)
	}
	// Price in the currency it cost, as on one message — not the micros the row
	// holds, and not the database's own column names.
	if asMap(rows[0])["price"] != m["price"] {
		t.Fatalf("the list and the message disagree about the price: %v vs %v", asMap(rows[0])["price"], m["price"])
	}
	for _, internal := range []string{"want_dlr", "promise", "state", "provider", "err_code", "err_text"} {
		if _, leaked := asMap(rows[0])[internal]; leaked {
			t.Fatalf("the list exposes %q: %v", internal, rows[0])
		}
	}

	// 4. The account's own dry run, which answers what it was asked and no more.
	_, dry := demo.do("POST", "/ui/route/explain", map[string]any{"to": nepal, "text": "hello"})
	assertNoCarrierInfo(t, "POST /v1/route/explain", dry, ids, channels)
	if asMap(dry)["ok"] != true || asMap(dry)["price"] == nil || asMap(dry)["to"] != "9779841234567" {
		t.Fatalf("the account's dry run should still answer its question: %v", dry)
	}

	// 5. An account cannot ask the operator's dry run, by either door.
	if st, _ := demo.do("POST", "/ui/admin/route/explain", map[string]any{"account": "demo", "to": nepal, "text": "x"}); st != 403 {
		t.Fatalf("an account asking the operator's dry run = %d, want 403", st)
	}
	if st, _ := s.browser().do("POST", "/ui/admin/route/explain", map[string]any{"account": "demo", "to": nepal, "text": "x"}); st != 401 {
		t.Fatalf("anonymous = %d, want 401", st)
	}
	if st, _ := s.do("POST", "/v1/admin/route/explain", s.key("demo"), map[string]any{"account": "demo", "to": nepal, "text": "x"}); st < 400 {
		t.Fatalf("an account with its own key asking the operator's dry run = %d", st)
	}

	// 6. And the operator is not kept in the dark. The same question, asked with
	// the operator key, answers with the carriers and what decided them.
	out := s.explainAs("demo", nepal, "hello", nil)
	chain := asList(out["route"])
	if len(chain) == 0 {
		t.Fatalf("the operator's dry run has no chain: %v", out)
	}
	first := asMap(chain[0])
	if first["id"] != carrier {
		t.Fatalf("the dry run says %v goes first, but the carrier was %q", first["id"], carrier)
	}
	if first["tier"] == nil && first["custom_rule"] == nil {
		t.Fatalf("the operator should be told what decided the order: %v", first)
	}
	if _, full := s.admin("GET", "/v1/admin/messages/"+id, nil); str(asMap(full), "message", "provider") != carrier {
		t.Fatalf("the operator's view of the message and the dry run disagree")
	}
}

// The status an account is shown is five words, and they are the account's. What
// it is told about a failure is its own, not a carrier's error string.
func TestTheAccountIsShownAStatusNotTheMachinery(t *testing.T) {
	s := start(t)
	demo := s.browser()
	demo.login("demo@example.com", "demo-pass-123")

	// The carrier refuses this destination, so the message is accepted and then
	// fails: the path where the gateway's internal code and the carrier's own
	// wording would otherwise reach the account.
	// The SMPP driver may send the destination with or without the country code,
	// so both forms are rejected.
	s.smsc.SetRejectPrefixes("9841234", "9779841234")
	defer s.smsc.SetRejectPrefixes()

	_, sent := s.send("demo", map[string]any{"to": "+9779841234567", "text": "no such number", "dlr": false})
	if sent == nil {
		t.Fatalf("send: %v", sent)
	}
	id := str(sent, "id")
	eventually(t, "failed", func() bool { return s.state("demo", id) == "failed" })

	ids, channels := s.carriers()

	m := s.asAccount("demo", id)
	assertNoCarrierInfo(t, "a failed message", m, ids, channels)
	if m["status"] != "failed" || m["failure"] != "recipient_rejected" || m["terminal"] != true {
		t.Fatalf("the account should be told it failed and why, in its own words: %v", m)
	}
	// Not the carrier's words, and not the gateway's attempt log.
	_, full := s.admin("GET", "/v1/admin/messages/"+id, nil)
	carrierText := toJSON(asMap(full))
	for _, w := range []string{"RINVDSTADR", str(asMap(full), "message", "provider")} {
		if strings.Contains(toJSON(m), w) {
			t.Fatalf("the account was shown the carrier's wording %q, which only the operator sees (%s): %v", w, carrierText, m)
		}
	}
	// The failure is terminal, so a client knows to stop asking.
	if m["terminal"] != true || str(m, "detail") == "" {
		t.Fatalf("a terminal message should say so and say why: %v", m)
	}
}

// The status word is written in two places — the CASE in the message list, so
// that listing 100 messages is not 100 decision runs, and rules/status.bcl, which
// gives each word its detail. If they ever disagree a list and a single message
// would tell an account different things about the same message, so they are
// pinned to each other here.
func TestAccountStatusMatchesTheRules(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	demo := s.browser()
	demo.login("demo@example.com", "demo-pass-123")

	// Every state the machine can be in, and the word each becomes.
	want := map[string]string{
		"queued":      "accepted",
		"dispatching": "sending",
		"submitted":   "sent",
		"delivered":   "delivered",
		"failed":      "failed",
		"nonsense":    "accepted",
	}
	for state, word := range want {
		_, out := op.do("POST", "/ui/admin/rules/status/try", map[string]any{
			"decision": "status",
			"facts":    map[string]any{"message": map[string]any{"state": state, "err_code": "", "want_dlr": 1}},
		})
		if out == nil {
			t.Fatalf("no answer for state %q", state)
		}
		if got := str(asMap(out), "status"); got != word {
			t.Fatalf("rules/status.bcl calls %q %q, want %q", state, got, word)
		}
	}

	// And end to end: the list and the single message agree.
	_, sent := s.send("demo", map[string]any{"to": "+9779841234567", "text": "one word please", "dlr": false})
	id := str(sent, "id")
	eventually(t, "delivered", func() bool { return s.state("demo", id) == "delivered" })
	rows := s.asAccountList("demo")
	if len(rows) == 0 {
		t.Fatalf("nothing listed")
	}
	if a, b := asMap(rows[0])["status"], s.asAccount("demo", id)["status"]; a != b {
		t.Fatalf("the list says %v and the message says %v", a, b)
	}
	if asMap(rows[0])["status"] != "delivered" {
		t.Fatalf("the list should say delivered, got %v", rows[0])
	}
}
