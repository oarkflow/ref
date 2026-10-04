package platform

import (
	"context"
	"testing"
	"time"
)

func TestParseDelay(t *testing.T) {
	for _, c := range []struct {
		in   any
		want time.Duration
		bad  bool
	}{
		{"1500ms", 1500 * time.Millisecond, false},
		{"2.5", 2500 * time.Millisecond, false},
		{3, 3 * time.Second, false},
		{0.5, 500 * time.Millisecond, false},
		{-1, 0, true},
		{"soon", 0, true},
		{[]string{"x"}, 0, true},
	} {
		got, err := parseDelay(c.in)
		if (err != nil) != c.bad || (!c.bad && got != c.want) {
			t.Errorf("parseDelay(%#v) = %v, %v", c.in, got, err)
		}
	}
}

func TestAPIKeyAuthReadsItsConfiguredHeader(t *testing.T) {
	res, _, err := openAPIKeyAuth(context.Background(), ResourceSpec{Name: "hook", Config: map[string]any{
		"key": "0123456789abcdef-secret", "principal_id": "provider", "header": "X-Webhook-Secret",
	}})
	if err != nil {
		t.Fatal(err)
	}
	auth := res.(*apiKeyAuth)
	ok := func(c Credentials) bool { _, err := auth.Authenticate(context.Background(), c); return err == nil }
	if !ok(Credentials{Headers: map[string]string{"x-webhook-secret": "0123456789abcdef-secret"}}) {
		t.Error("the configured header, in any case, must authenticate")
	}
	if ok(Credentials{Headers: map[string]string{"X-Webhook-Secret": "wrong-wrong-wrong-wrong"}}) {
		t.Error("a wrong secret must not authenticate")
	}
	if !ok(Credentials{APIKey: "0123456789abcdef-secret"}) || !ok(Credentials{BearerToken: "0123456789abcdef-secret"}) {
		t.Error("X-API-Key and bearer must keep working")
	}
	if ok(Credentials{Headers: map[string]string{"Other": "0123456789abcdef-secret"}}) {
		t.Error("an unrelated header must not authenticate")
	}
}

func TestFactHints(t *testing.T) {
	src := `row "a" { when { all { message.text_len > 0
 user.status == "active"
 message.optout == true
 charge.total >= 0.5
 sent.error.status == 429
 message.text matches "x.y" } } then { outcome { decision deny } } }`
	got := map[string][2]string{}
	for _, h := range factHints(src) {
		m := h.(map[string]any)
		got[m["path"].(string)] = [2]string{m["kind"].(string), m["sample"].(string)}
	}
	want := map[string][2]string{
		"message.text_len": {"number", "0"}, "user.status": {"text", "active"}, "message.optout": {"bool", "false"},
		"charge.total": {"number", "0"}, "sent.error.status": {"number", "429"}, "message.text": {"text", ""},
	}
	for path, kind := range want {
		if got[path] != kind {
			t.Errorf("%s = %v, want %v", path, got[path], kind)
		}
	}
	if len(got) != len(want) {
		t.Errorf("found %v", got)
	}
}

func TestSplitDiagnostic(t *testing.T) {
	msg, line, col := splitDiagnostic("error: unexpected end of file, expected '}'\n --> <input>:186:4\n\nerror: again")
	if msg != "unexpected end of file, expected '}'" || line != 186 || col != 4 {
		t.Fatalf("got %q %d:%d", msg, line, col)
	}
	if _, line, col := splitDiagnostic("no position here"); line != 1 || col != 1 {
		t.Fatalf("without a position: %d:%d", line, col)
	}
}

func TestParseCSV(t *testing.T) {
	action, err := dataParseCSVAction(BuildContext{}, NodeSpec{Name: "n", Provides: []string{"rows"}, Config: map[string]any{"source_fact": "text"}})
	if err != nil {
		t.Fatal(err)
	}
	run := func(text string) []any {
		res, err := action.Run(&ActionContext{Inputs: map[string]any{"text": text}})
		if err != nil {
			t.Fatal(err)
		}
		return res.Outputs["rows"].([]any)
	}
	rows := run("phone,name,amount\n9856034617, Asha ,\"1,250\"\n\n9801111111,Bimal,5\n")
	if len(rows) != 2 || rows[0].(map[string]any)["name"] != "Asha" || rows[0].(map[string]any)["amount"] != "1,250" || rows[1].(map[string]any)["phone"] != "9801111111" {
		t.Fatalf("rows = %v", rows)
	}
	if semi := run("phone;name\n1;a\n"); semi[0].(map[string]any)["name"] != "a" {
		t.Fatalf("semicolon = %v", semi)
	}
	if short := run("a,b,c\n1,2\n"); short[0].(map[string]any)["c"] != "" {
		t.Fatalf("a short row = %v", short)
	}
	if len(run("")) != 0 {
		t.Fatal("empty text must give no rows")
	}
}

func TestAugmentConcatAndTextRender(t *testing.T) {
	ctx := &ActionContext{Context: context.Background(), Inputs: map[string]any{
		"rows":  []any{map[string]any{"phone": "98", "name": "Asha"}, "plain"},
		"extra": []any{"z"},
		"valid": []any{true, false},
		"cid":   "cmp_1",
		"text":  "Hello {{ name }}, your code is {{ code }}.",
		"vars":  map[string]any{"name": "Asha"},
	}}
	aug, err := dataAugmentAction(BuildContext{}, NodeSpec{Name: "n", Provides: []string{"out"}, Config: map[string]any{"source_fact": "rows", "fields": map[string]any{
		"campaign_id": "cid", "n": "index", "state": "valid[index] ? 'pending' : 'skipped'", "fields": "item"}}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := aug.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := res.Outputs["out"].([]any)
	first, second := out[0].(map[string]any), out[1].(map[string]any)
	if first["campaign_id"] != "cmp_1" || first["n"] != 0 || first["state"] != "pending" || first["phone"] != "98" || second["state"] != "skipped" || second["n"] != 1 {
		t.Fatalf("augment: %v %v", first, second)
	}
	cat, _ := dataConcatAction(BuildContext{}, NodeSpec{Name: "c", Provides: []string{"out"}, Config: map[string]any{"sources": []any{"rows", "missing", "extra"}}})
	if res, _ := cat.Run(ctx); len(res.Outputs["out"].([]any)) != 3 {
		t.Fatalf("concat: %v", res.Outputs)
	}
	render, _ := textRenderAction(BuildContext{}, NodeSpec{Name: "r", Provides: []string{"out"}, Config: map[string]any{"text_fact": "text", "vars_fact": "vars"}})
	if res, err := render.Run(ctx); err != nil || res.Outputs["out"] != "Hello Asha, your code is ." {
		t.Fatalf("render: %v %v", res.Outputs, err)
	}
}
