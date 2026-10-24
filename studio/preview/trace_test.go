package preview

import "testing"

func TestSplitNode(t *testing.T) {
	for _, c := range []struct{ in, intent, node string }{
		{"platform.orders.create.validate", "orders.create", "validate"},
		{"platform.health.ping.__authz", "health.ping", "__authz"},
		{"platform.solo", "solo", ""},
		{"somethingelse", "", "somethingelse"},
	} {
		if i, n := splitNode(c.in); i != c.intent || n != c.node {
			t.Errorf("splitNode(%q) = %q, %q; want %q, %q", c.in, i, n, c.intent, c.node)
		}
	}
}
