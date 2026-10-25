package platform

import (
	"strings"
	"testing"
)

func TestRealtimeHelpers(t *testing.T) {
	env := Env{"input": map[string]any{"room": 42, "peer": "bob"}, "principal": map[string]any{"id": "ada"}, "tenant": "acme"}
	cases := map[string]string{
		`rt_topic("room", input.room)`:      "room:42",
		`rt_topic("a", "b", "c")`:           "a:b:c",
		`rt_user(principal.id)`:             "user:ada",
		`rt_tenant_topic(tenant, "orders")`: "t:acme:orders",
		`chat_topic("ch_1")`:                "chat:ch_1",
	}
	for src, want := range cases {
		expr, err := CompileExpr(src)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		got, err := expr.Eval(env)
		if err != nil || Stringify(got) != want {
			t.Errorf("%s = %v, %v; want %s", src, got, err, want)
		}
	}
	a, _ := mustEval(t, `chat_dm(principal.id, input.peer)`, env)
	b, _ := mustEval(t, `chat_dm(input.peer, principal.id)`, env)
	if a == "" || a != b || !strings.HasPrefix(a, "dm_") {
		t.Fatalf("dm ids %q %q", a, b)
	}
	if _, err := mustEval(t, `rt_topic("room", "has space")`, env); err == nil {
		t.Fatal("whitespace in a topic part was accepted")
	}
	if _, err := mustEval(t, `rt_user("")`, env); err == nil {
		t.Fatal("empty user accepted")
	}
}

func mustEval(t *testing.T, src string, env Env) (string, error) {
	t.Helper()
	expr, err := CompileExpr(src)
	if err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	v, err := expr.Eval(env)
	return Stringify(v), err
}
